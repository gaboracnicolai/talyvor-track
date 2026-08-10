// Package issue owns the database operations for issue records.
//
// The store is intentionally low-magic: hand-rolled SQL with positional
// args, dynamic-but-allowlisted UPDATE, and one struct-scanner helper
// that every read path reuses. No ORM, no reflection, no struct tag
// parsing at runtime — easier to debug, easier to reason about under
// concurrency.
package issue

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/talyvor/track/internal/model"
)

// pgxDB is the subset of *pgxpool.Pool the store uses. Decoupled so
// pgxmock can stand in for the real pool inside unit tests.
type pgxDB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

// fieldFetcher loads custom-field values for one or many issues.
// The issue store calls into it from GetByID / List so the issue
// JSON payload includes field_values without callers having to
// stitch the data together. It's optional — without a fetcher the
// store behaves exactly as before, returning issues with no
// FieldValues populated.
type fieldFetcher interface {
	GetValues(ctx context.Context, issueID, workspaceID string) (map[string]string, error)
	GetValuesBulk(ctx context.Context, issueIDs []string) (map[string]map[string]string, error)
}

// blockedChecker reports whether an issue has open blockers. Wired
// at boot from dependency.Store; the issue store doesn't import
// dependency directly to keep the package graph one-way.
type blockedChecker interface {
	IsBlocked(ctx context.Context, issueID string) (bool, error)
}

// timeSummariser returns total tracked seconds for an issue. Wired
// from timetracking.Store at boot — same package-graph reasoning as
// blockedChecker.
type timeSummariser interface {
	IssueTotalSec(ctx context.Context, issueID string) (int, error)
}

// scoreReader returns RICE / ICE scores for an issue. Wired from
// scoring.Store via a tiny adapter in main.go so the issue package
// stays free of a scoring import. Pointer returns: nil means
// "unscored", a non-nil zero means "scored as 0" — the two states
// are visually distinct in the UI.
type scoreReader interface {
	IssueScores(ctx context.Context, issueID string) (rice, ice *float64, err error)
}

type Store struct {
	pool    pgxDB
	fetcher fieldFetcher
	blocked blockedChecker
	timer   timeSummariser
	scorer  scoreReader
}

func NewStore(pool *pgxpool.Pool) *Store {
	var db pgxDB
	if pool != nil {
		db = pool
	}
	return newStore(db)
}

func newStore(db pgxDB) *Store { return &Store{pool: db} }

// WithFieldFetcher attaches a custom-field reader so List + GetByID
// populate the FieldValues map on every returned issue. Optional —
// callers that don't wire it get the original behaviour.
func (s *Store) WithFieldFetcher(f fieldFetcher) *Store {
	s.fetcher = f
	return s
}

// WithBlockedChecker attaches a dependency-aware blocker so GetByID
// populates Issue.IsBlocked. Skipped on List to avoid an N×1 query
// in the common list path; UIs surface the badge via the per-issue
// detail fetch.
func (s *Store) WithBlockedChecker(b blockedChecker) *Store {
	s.blocked = b
	return s
}

// WithTimeTracker attaches a time-tracking summariser so GetByID
// populates Issue.TimeTracked. Same one-shot read policy as the
// blocked checker — list reads stay cheap.
func (s *Store) WithTimeTracker(t timeSummariser) *Store {
	s.timer = t
	return s
}

// WithScorer attaches a RICE/ICE score reader so GetByID populates
// Issue.RICEScore / Issue.ICEScore. List reads stay unscored — the
// dedicated prioritised-backlog endpoint covers list-time ranking.
func (s *Store) WithScorer(sr scoreReader) *Store {
	s.scorer = sr
	return s
}

// IssueFilter drives the List query. Empty / zero fields are ignored
// (no WHERE clause emitted) — every field is independently optional.
type IssueFilter struct {
	WorkspaceID string
	TeamID      string
	ProjectID   string
	CycleID     string
	Status      string
	AssigneeID  string
	Priority    int
	Labels      []string
	Limit       int
	Offset      int
	OrderBy     string
	OrderDir    string
}

// issueColumns is the SELECT projection. Declared once so every read
// path scans the same column order — adding a new column means
// touching one constant + one scan helper.
const issueColumns = `id, workspace_id, team_id, project_id, number, identifier,
    title, description, status, priority,
    assignee_id, creator_id, cycle_id, parent_id,
    due_date, completed_at,
    lens_feature, ai_cost_usd, ai_tokens,
    labels, sort_order, created_at, updated_at`

// scanIssue reads a single row into model.Issue. The row is whatever
// the caller gets from QueryRow or rows.Next + rows.Scan.
func scanIssue(scanner interface {
	Scan(...any) error
}) (*model.Issue, error) {
	// Status is the typed alias IssueStatus; pgx won't auto-cast a
	// driver string into a custom string type, so we land it in a
	// regular string first and convert.
	var (
		i        model.Issue
		status   string
		priority int
	)
	err := scanner.Scan(
		&i.ID, &i.WorkspaceID, &i.TeamID, &i.ProjectID, &i.Number, &i.Identifier,
		&i.Title, &i.Description, &status, &priority,
		&i.AssigneeID, &i.CreatorID, &i.CycleID, &i.ParentID,
		&i.DueDate, &i.CompletedAt,
		&i.LensFeature, &i.AICostUSD, &i.AITokens,
		&i.Labels, &i.SortOrder, &i.CreatedAt, &i.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	i.Status = model.IssueStatus(status)
	i.Priority = model.IssuePriority(priority)
	return &i, nil
}

// Create allocates the next per-team issue number, formats the
// identifier (TEAM-N), and inserts the row. Three queries: look up
// the team prefix, compute the next number, INSERT … RETURNING.
//
// The (team_id, number) UNIQUE constraint catches the race between
// two concurrent Creates picking the same number. Callers can retry
// the operation on a unique-violation error.
func (s *Store) Create(ctx context.Context, issue model.Issue) (*model.Issue, error) {
	if s.pool == nil {
		return nil, errors.New("issue: store has no pool")
	}
	if issue.WorkspaceID == "" || issue.TeamID == "" || issue.Title == "" || issue.CreatorID == "" {
		return nil, errors.New("issue: WorkspaceID, TeamID, Title, and CreatorID are required")
	}
	if issue.Status == "" {
		issue.Status = model.StatusBacklog
	}
	if issue.Labels == nil {
		issue.Labels = []string{}
	}

	var teamIdentifier string
	if err := s.pool.QueryRow(ctx,
		`SELECT identifier FROM teams WHERE id = $1 AND workspace_id = $2`,
		issue.TeamID, issue.WorkspaceID,
	).Scan(&teamIdentifier); err != nil {
		return nil, fmt.Errorf("issue: team not found in workspace: %w", err)
	}

	// Object-graph integrity: optional cross-object refs must be in this workspace.
	for field, p := range map[string]*string{
		"project_id":  issue.ProjectID,
		"cycle_id":    issue.CycleID,
		"assignee_id": issue.AssigneeID,
		"parent_id":   issue.ParentID,
	} {
		if p == nil || *p == "" {
			continue
		}
		if err := s.assertRefInWorkspace(ctx, issueRefQueries[field], field, *p, issue.WorkspaceID); err != nil {
			return nil, err
		}
	}

	nextNumber, err := s.nextIssueNumber(ctx, issue.TeamID, issue.WorkspaceID, teamIdentifier)
	if err != nil {
		return nil, err
	}
	issue.Number = nextNumber
	issue.Identifier = fmt.Sprintf("%s-%d", teamIdentifier, nextNumber)

	// COMPLETION TIME AT CREATION — the second copy of a seam #74 fixed once.
	//
	// This INSERT named due_date and did NOT name completed_at at all, so a caller-supplied
	// CompletedAt was discarded in silence. That is the same defect #74 found in the importer's
	// UPSERT and fixed there; Create is the OTHER write path, and it is the one every CSV import row
	// takes for every row that carries no provider identifier — an export filtered down past its key
	// column (`Issue key` for Jira, `ID` for Linear). Both CSV transports now read that column when
	// the export carries it and reach the upsert instead, so this statement is no longer EVERY import
	// row's write path; it is the path of a row whose export was filtered.
	//
	// ⚠ IT IS GATED, NOT SIMPLY ADDED. handler.Create decodes the whole model.Issue off the request
	// body, so naming the column with no rule would newly let any client file BACKLOG work carrying a
	// completion time — a row no Track path can produce (Update stamps completed_at only on a
	// transition ONTO done and CLEARS it on any transition away) and one that analytics' resolution
	// stats count as delivered, because that query selects on `completed_at IS NOT NULL` with no
	// status predicate. So the invariant Update maintains is stated once more here, at the other
	// door: a completion time is recorded only on a row that is done.
	completedAt := issue.CompletedAt
	if issue.Status != model.StatusDone {
		completedAt = nil
	}

	// created_at: THE PROVIDER'S OPENING TIME, AND ONLY THE PROVIDER'S. Third copy of the seam #74
	// found in the importer's UPSERT (completed_at named nowhere in the SQL, so a perfectly mapped
	// value was discarded) and #78 found here for the same column. This one is worse than both,
	// because the column has `DEFAULT NOW()`: a mapper-only fix is not merely inert, it is INVISIBLE
	// — the row lands with a plausible timestamp and the loss shows up only as a NEGATIVE time to
	// resolution in analytics (measured: -2400.0 hours for an issue opened 200 days before import).
	//
	// ⚠ IT IS GATED TO THE IMPORT PATH, AND THE GATE IS LOAD-BEARING RATHER THAN TIDY. handler.Create
	// decodes the WHOLE model.Issue off the request body and CreatedAt carries a `json:"created_at"`
	// tag, so naming this column with no rule would newly let any authenticated client choose its own
	// created_at — and created_at is the WINDOW PREDICATE of every analytics report
	// (`created_at > NOW() - INTERVAL '1 day' * $2`). A client could file work that is invisible to
	// every report, or fabricate any cycle time it liked. The handler already refuses a supplied
	// creator_id (SEC-5: `in.CreatorID = actorID`, never a body field), so ImporterCreatorID is a
	// value no HTTP caller can reach — which is exactly what makes it usable as the gate.
	//
	// A zero CreatedAt means "nobody supplied one" and takes the DEFAULT, which is every native
	// path: Create, the MCP server, feature-board conversion and automation all leave it zero.
	var createdAt *time.Time
	if !issue.CreatedAt.IsZero() && issue.CreatorID == model.ImporterCreatorID {
		t := issue.CreatedAt
		createdAt = &t
	}
	// updated_at rides the SAME import-owned gate as created_at, for the same reason and with the
	// same shape: the column is DEFAULT NOW(), so an unsupplied value is not a null anybody can
	// spot but a plausible timestamp. It is load-bearing on the product's MAIN SCREEN — the issue
	// list sorts by updated_at DESC and every row renders "updated <n> ago" — so an import that
	// leaves it defaulted puts a backlog whose real median staleness is 2692 DAYS at the top of
	// today's list. Native paths (Create, MCP, feature-board conversion, automation) all leave it
	// zero and take the DEFAULT, which is correct for them: they really are being written now.
	var updatedAt *time.Time
	if !issue.UpdatedAt.IsZero() && issue.CreatorID == model.ImporterCreatorID {
		t := issue.UpdatedAt
		updatedAt = &t
	}

	const insertSQL = `INSERT INTO issues
        (workspace_id, team_id, project_id, number, identifier,
         title, description, status, priority,
         assignee_id, creator_id, cycle_id, parent_id,
         due_date, completed_at, lens_feature, labels, sort_order, created_at, updated_at)
    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18,
            COALESCE($19::timestamptz, NOW()), COALESCE($20::timestamptz, NOW()))
    RETURNING ` + issueColumns
	return scanIssue(s.pool.QueryRow(ctx, insertSQL,
		issue.WorkspaceID, issue.TeamID, issue.ProjectID, issue.Number, issue.Identifier,
		issue.Title, issue.Description, string(issue.Status), int(issue.Priority),
		issue.AssigneeID, issue.CreatorID, issue.CycleID, issue.ParentID,
		issue.DueDate, completedAt, issue.LensFeature, issue.Labels, issue.SortOrder, createdAt,
		updatedAt,
	))
}

// identifierScanBound caps how far past MAX(number)+1 the allocator will look for a number whose derived
// identifier is free. Only imported provider keys can occupy those identifiers, so the gap is the size of
// the imported key range that overlaps this team's numbering — large in theory, bounded here so a pathological
// import can never turn issue creation into an unbounded scan. Exceeding it is a loud error, not a wedge.
const identifierScanBound = 10000

// nextIssueNumber computes the next per-team issue number: COALESCE(MAX(number),0)+1, advanced past any
// number whose DERIVED identifier ("<teamIdentifier>-<number>") is already taken in the workspace. Shared by
// Create and UpsertByIdentifier so both allocate numbers identically. The (team_id, number) UNIQUE constraint
// catches the race between two concurrent allocators picking the same number — callers retry.
//
// WHY THE IDENTIFIER CHECK EXISTS. MAX(number)+1 alone is only safe while every identifier in the workspace
// was derived from a number. An API import breaks that: the row keeps the PROVIDER's key (ENG-3) but takes a
// Track-allocated number (1), so the two disagree, and the allocator — which counts numbers and never looks
// at identifiers — walks straight into the provider's key and violates UNIQUE (workspace_id, identifier).
// MEASURED before this guard existed: import ENG-3 into a team called ENG, and the SECOND native issue
// creation fails; because a failed INSERT does not advance MAX(number), the same number is retried forever
// and the team can never create another issue. Advancing past taken identifiers costs one index probe in the
// normal case (the first candidate is free) and makes that state unreachable.
func (s *Store) nextIssueNumber(ctx context.Context, teamID, workspaceID, teamIdentifier string) (int, error) {
	const nextNumberSQL = `
        WITH start AS (
            SELECT COALESCE(MAX(number), 0) + 1 AS n FROM issues WHERE team_id = $1
        )
        SELECT g FROM start, generate_series(start.n, start.n + $4::int) AS g
         WHERE NOT EXISTS (
               SELECT 1 FROM issues
                WHERE workspace_id = $2 AND identifier = $3 || '-' || g
         )
         ORDER BY g
         LIMIT 1`
	var n int
	err := s.pool.QueryRow(ctx, nextNumberSQL, teamID, workspaceID, teamIdentifier, identifierScanBound).Scan(&n)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("issue: no free identifier for team %q within %d numbers of the next issue number "+
			"(imported provider keys occupy the whole range)", teamIdentifier, identifierScanBound)
	}
	if err != nil {
		return 0, fmt.Errorf("issue: compute next number: %w", err)
	}
	return n, nil
}

// UpsertByIdentifier lands a provider-imported issue on its identifier (the provider-key, e.g. ENG-123):
// INSERT when the (workspace_id, identifier) pair is new, UPDATE its content when it already exists. Unlike
// Create — which AUTO-generates a TEAM-N identifier — the identifier here is CALLER-SUPPLIED, so the imported
// issue is addressable by its provider key and PR #30's cost attribution (WHERE identifier=$feature) resolves
// it. Returns the issue and inserted=true when this call INSERTed (xmax=0), false when it UPDATEd (so the
// C.3 runner can count created vs updated).
//
// RE-IMPORT POLICY — the locked (c) field-class split. On the UPDATE branch:
//
//	CLOBBER  (provider is source of truth):        title, description, labels   → in the UPDATE SET
//	PRESERVE (a Track user's local workflow action): status, priority           → OMITTED (deliberately kept)
//	NEVER TOUCH (money-path + attribution, locked):  ai_cost_usd, ai_tokens, lens_feature → OMITTED (untouched)
//
// Two omission classes, different reasons. number is allocated on INSERT and left alone on UPDATE — a
// re-imported issue keeps its identity.
func (s *Store) UpsertByIdentifier(ctx context.Context, issue model.Issue) (*model.Issue, bool, error) {
	if s.pool == nil {
		return nil, false, errors.New("issue: store has no pool")
	}
	if issue.WorkspaceID == "" || issue.TeamID == "" || issue.Title == "" || issue.CreatorID == "" || issue.Identifier == "" {
		return nil, false, errors.New("issue: WorkspaceID, TeamID, Title, CreatorID, and Identifier are required")
	}
	if issue.Status == "" {
		issue.Status = model.StatusBacklog
	}
	if issue.Labels == nil {
		issue.Labels = []string{}
	}

	// Team-in-workspace tenancy — same lookup Create uses; a team from another workspace is rejected.
	var teamIdentifier string
	if err := s.pool.QueryRow(ctx,
		`SELECT identifier FROM teams WHERE id = $1 AND workspace_id = $2`,
		issue.TeamID, issue.WorkspaceID,
	).Scan(&teamIdentifier); err != nil {
		return nil, false, fmt.Errorf("issue: team not found in workspace: %w", err)
	}
	// Object-graph integrity: optional cross-object refs must be in this workspace (same guard as Create —
	// also what keeps this INSERT clear of the .semgrep cross-object-tenancy lock).
	for field, p := range map[string]*string{
		"project_id":  issue.ProjectID,
		"cycle_id":    issue.CycleID,
		"assignee_id": issue.AssigneeID,
		"parent_id":   issue.ParentID,
	} {
		if p == nil || *p == "" {
			continue
		}
		if err := s.assertRefInWorkspace(ctx, issueRefQueries[field], field, *p, issue.WorkspaceID); err != nil {
			return nil, false, err
		}
	}

	// A number for the INSERT branch (shared with Create). On CONFLICT this value is discarded — the existing
	// row keeps its number.
	nextNumber, err := s.nextIssueNumber(ctx, issue.TeamID, issue.WorkspaceID, teamIdentifier)
	if err != nil {
		return nil, false, err
	}
	issue.Number = nextNumber

	// created_at: THE PROVIDER'S OPENING TIME, AND ONLY THE PROVIDER'S — the FOURTH copy of the seam
	// this package has now paid for. #74 found the importer's UPSERT omitting `completed_at`; #78
	// found the second copy in Create's INSERT for the same column; #83 found the THIRD, `created_at`
	// in Create's INSERT, and DELIBERATELY left this one alone because at that time no mapper fed it
	// ("an un-fed column is untestable and rots"). Both API mappers feed it now, so it lands here.
	//
	// ⚠ THIS IS THE STATEMENT THE API TRANSPORTS ACTUALLY REACH. A mapper-only fix here is not merely
	// inert, it is INVISIBLE: the column is TIMESTAMPTZ DEFAULT NOW(), so the row lands with a
	// plausible timestamp and the loss surfaces only as a NEGATIVE time to resolution in analytics.
	// MEASURED before this line existed, through the async runner on real Postgres, for an issue the
	// provider opened 200 days ago and finished 100 days ago: median time to resolution = -2400.0
	// hours on BOTH jira_api and linear_api. Against 100 real resolved Jira Cloud issues: 100 of 100
	// negative, true median 88.7h against a computed median of -408.3h.
	//
	// ⚠ THE GATE IS COPIED FROM Create DELIBERATELY AND IS LOAD-BEARING RATHER THAN TIDY, for exactly
	// the reason stated there: created_at is the WINDOW PREDICATE of every analytics report
	// (`created_at > NOW() - INTERVAL '1 day' * $2`), and ImporterCreatorID is a value no HTTP caller
	// can reach because handler.Create refuses a supplied creator_id outright (SEC-5). A zero
	// CreatedAt means "nobody supplied one" and takes the DEFAULT.
	//
	// ⚠ AND IT IS ON THE INSERT BRANCH ONLY, WHICH IS A DECISION RATHER THAN AN OVERSIGHT. On the
	// UPDATE branch created_at is OMITTED alongside status/priority/completed_at/due_date: a
	// re-imported issue keeps its identity, and an opening time that moved between two imports of the
	// same issue is a provider fact this package has no rule for. Stated in the queue, not invented here.
	var createdAt *time.Time
	if !issue.CreatedAt.IsZero() && issue.CreatorID == model.ImporterCreatorID {
		t := issue.CreatedAt
		createdAt = &t
	}
	// updated_at: THE PROVIDER'S LAST-TOUCHED INSTANT, on the SAME import-owned gate as created_at.
	// This is the FIFTH copy of the seam this package has paid for and the SECOND column to cross
	// it: #74 completed_at in this UPSERT, #78 completed_at in Create's INSERT, #83 created_at in
	// Create, #84 created_at HERE, and now updated_at here. #85 landed this column on Create and
	// DELIBERATELY left this statement alone because no mapper fed it ("an un-fed column is
	// untestable and rots"); importer/api_updated.go feeds it now, so it lands.
	//
	// ⚠ THE LOSS IS NOT A NUMBER ON A REPORT, IT IS THE ORDER OF THE MAIN SCREEN. #83 scoped this
	// column out with "nothing in Track reads updated_at for a report" and #84 repeated it; #85
	// measured it FALSE by enumerating READS — five consumers in two languages, the largest being
	// the issue list itself (frontend IssueList.tsx sorts by updated_at DESC, IssueRow.tsx prints
	// "updated <n> ago" on every row, Search below is ORDER BY updated_at DESC). MEASURED through
	// the async runner on real Postgres for an issue the provider last touched 200 days ago: off by
	// 4800h on BOTH jira_api and linear_api, and the stale row OUTRANKED work edited during the test
	// in the very query the product lists by. A column assertion alone cannot see it — updated_at is
	// TIMESTAMPTZ DEFAULT NOW(), so the wrong value has the same shape as the right one.
	//
	// ⚠ IT IS ON THE INSERT BRANCH ONLY, AND THE CONFLICT ARM BELOW REMAINS AN OPEN QUESTION rather
	// than an oversight — UNCHANGED BY THIS MERGE AND DELIBERATELY SO. A local edit bumps updated_at
	// to NOW(), so `updated_at = EXCLUDED.updated_at` would move the column BACKWARDS past a human's
	// edit and hide it from the recency sort: this defect's mirror image.
	// GREATEST(issues.updated_at, EXCLUDED.updated_at) is the obvious third option and is STILL a
	// rule nobody has decided, so it is NOT invented here. THE RESIDUAL IS REAL AND IS STATED: a
	// RE-import still stamps NOW() on every row it touches. Left in the queue with numbers.
	var updatedAt *time.Time
	if !issue.UpdatedAt.IsZero() && issue.CreatorID == model.ImporterCreatorID {
		t := issue.UpdatedAt
		updatedAt = &t
	}
	const upsertSQL = `INSERT INTO issues
        (workspace_id, team_id, project_id, number, identifier,
         title, description, status, priority,
         assignee_id, creator_id, cycle_id, parent_id,
         due_date, completed_at, lens_feature, labels, sort_order, created_at, updated_at)
    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18,
            COALESCE($19::timestamptz, NOW()), COALESCE($20::timestamptz, NOW()))
    ON CONFLICT (workspace_id, identifier) DO UPDATE SET
        title       = EXCLUDED.title,        -- CLOBBER: provider is source of truth
        description = EXCLUDED.description,   -- CLOBBER
        labels      = EXCLUDED.labels,       -- CLOBBER
        updated_at  = NOW()
        -- OMITTED (PRESERVE local workflow):        status, priority, completed_at, due_date, created_at
        --   completed_at travels WITH status and cannot be split from it: status is preserved here
        --   so local workflow wins, and clobbering completed_at alone could leave a locally-done
        --   issue with status "done" and no completion time — the invariant Update maintains, broken
        --   by a re-import. due_date is preserved for the same reason it is not in the CLOBBER list
        --   above: whether a provider's plan should overwrite a local one is a decision, not a
        --   default, and it is stated in the queue rather than made here. Both land on INSERT.
        -- OMITTED (NEVER TOUCH money + attribution): ai_cost_usd, ai_tokens, lens_feature
      WHERE issues.creator_id = '` + model.ImporterCreatorID + `'
        -- ^ "provider is source of truth" is true of the rows the PROVIDER put here, and of no
        --   others. Track derives a native issue's key itself (Create: "<team>-<number>") and both
        --   providers emit that same shape — Linear ENG-123, Jira PROJ-123 — so a team called ENG
        --   in Track and a team called ENG in Linear collide in this one un-namespaced column.
        --   Without this predicate the import UPDATEs a user's issue and reports it as Imported.
        AND issues.team_id = $2
        -- ^ THE SAME ARGUMENT, ONE STEP FURTHER, AND IT IS THE HALF THE LINE ABOVE COULD NOT MAKE.
        --   creator_id separates an IMPORT from a HUMAN; it does not separate one import from
        --   another, and the identifier column carries no team. So the row this statement lands on
        --   may belong to a DIFFERENT TEAM of the same workspace, and every consequence of that is
        --   silent: team_id is not in the SET (correctly — a re-imported issue keeps its identity,
        --   and number is UNIQUE per (team_id, number)), so the write goes to the other team's
        --   row and the caller counts it Imported.
        --   MEASURED through the async runner on real Postgres, two teams in one workspace:
        --   the same export imported into team B after team A reported succeeded imported=2 with
        --   ZERO issues in team B, and a Linear export whose keys collide REWROTE team A's Jira
        --   issues — title, description and labels, the three columns this arm clobbers — under
        --   succeeded again. The collision is the namespace, not a coincidence: whole-population
        --   over 305 real Jira exports (172 distinct project keys, MEDIAN LENGTH 4), NINE keys are
        --   carried by exports from two or more DISTINCT REPOSITORY OWNERS, and the list is headed
        --   by SCRUM (9 owners), KAN (3) and PROJ (2) — the keys Jira Software's own Scrum and
        --   Kanban templates hand out by default. Two unrelated Jira sites landing in one Track
        --   workspace collide on the key the provider itself chose for both of them.
        --   Refusing is the same policy the line above already implements, and it is deliberately
        --   NOT a move: carrying a row into the requested team would reallocate an issue's number
        --   under a user, and no Track path moves an issue between teams (team_id is not in
        --   updatableFields). That is a product decision, written up rather than made here.
    RETURNING (xmax = 0) AS inserted, ` + issueColumns

	var inserted bool
	out, err := scanIssue(insertedScanner{
		row:      s.pool.QueryRow(ctx, upsertSQL, issue.WorkspaceID, issue.TeamID, issue.ProjectID, issue.Number, issue.Identifier, issue.Title, issue.Description, string(issue.Status), int(issue.Priority), issue.AssigneeID, issue.CreatorID, issue.CycleID, issue.ParentID, issue.DueDate, issue.CompletedAt, issue.LensFeature, issue.Labels, issue.SortOrder, createdAt, updatedAt),
		inserted: &inserted,
	})
	if err != nil {
		// The conflicting row exists but the DO UPDATE ... WHERE matched nothing, so RETURNING produced no
		// row. Say exactly WHICH of the two predicates declined — the caller (importer.run) counts both as
		// Refused, and the difference is the sentence a human reads off the job row. One read, on the error
		// path only, because the statement cannot report a row it deliberately did not touch.
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, s.diagnoseUpsertRefusal(ctx, issue.WorkspaceID, issue.TeamID, issue.Identifier)
		}
		return nil, false, err
	}
	return out, inserted, nil
}

// diagnoseUpsertRefusal names which of the conflict arm's two predicates declined the write. It runs ONLY on
// the ErrNoRows path — a refusal — so the ordinary import costs nothing, and it reads the ONE row the
// statement just refused to touch, scoped to the same (workspace_id, identifier) the conflict fired on.
//
// ⚠ THE ORDER IS NOT ARBITRARY. A row can be BOTH human-created and in another team; "a human owns this
// key" is the stronger fact and the one #71's whole refusal exists to state, so it is answered first and
// its sentence is byte-identical to the one that shipped before the team predicate existed.
//
// ⚠ THE THIRD BRANCH IS NOT DEFENSIVE PADDING. Between the upsert and this read another connection may have
// deleted the conflicting row, and a diagnosis that assumed the row is still there would either panic or
// report a confident falsehood. It says what it knows: the write did not land and the reason could not be
// re-read.
func (s *Store) diagnoseUpsertRefusal(ctx context.Context, workspaceID, teamID, identifier string) error {
	var creatorID, holdingTeamID, holdingTeamIdentifier string
	err := s.pool.QueryRow(ctx,
		`SELECT i.creator_id, i.team_id, COALESCE(t.identifier, '')
           FROM issues i LEFT JOIN teams t ON t.id = i.team_id
          WHERE i.workspace_id = $1 AND i.identifier = $2`,
		workspaceID, identifier).Scan(&creatorID, &holdingTeamID, &holdingTeamIdentifier)
	switch {
	case err != nil:
		return fmt.Errorf(
			"issue: %q already exists in this workspace and this import did not overwrite it (the conflicting row could not be re-read: %v): %w",
			identifier, err, ErrIdentifierNotImportOwned)
	case creatorID != model.ImporterCreatorID:
		return fmt.Errorf(
			"issue: %q already exists in this workspace and was not created by an import; refusing to overwrite it: %w",
			identifier, ErrIdentifierNotImportOwned)
	case holdingTeamID != teamID:
		return fmt.Errorf(
			"issue: %q is already imported into another team of this workspace (%s); this import will not move it or overwrite it: %w",
			identifier, holdingTeamIdentifier, ErrIdentifierOwnedByAnotherTeam)
	default:
		// Neither predicate explains it: the row is this team's and is import-owned, so the conflict arm
		// should have updated it. Reporting a refusal we cannot account for is the only honest answer.
		return fmt.Errorf(
			"issue: %q exists and is owned by this team's import, yet the re-import matched no row: %w",
			identifier, ErrIdentifierNotImportOwned)
	}
}

// ErrIdentifierNotImportOwned is returned by UpsertByIdentifier when the provider key collides with an issue
// this workspace created itself. Exported so a caller can distinguish "this one row could not land" from a
// transport or tenancy failure — which importer.run now does: it counts a refusal SEPARATELY from a failure,
// because the refusal is this policy working, not breaking.
//
// ⚠ IT IS AN ALIAS, NOT A SECOND VALUE. errors.Is compares identity, so a re-declaration here would be a
// different error from the one the importer tests against and every refusal would silently score as a
// failure again. See model.ErrIdentifierNotImportOwned for the argument.
var ErrIdentifierNotImportOwned = model.ErrIdentifierNotImportOwned

// ErrIdentifierOwnedByAnotherTeam is returned by UpsertByIdentifier when the provider key resolves to an
// issue an EARLIER IMPORT put in a different team of the same workspace. Same aliasing argument as above and
// for the same reason: errors.Is compares identity, so a re-declaration here would be a different error from
// the one importer.run tests against and every cross-team refusal would score as a failure.
var ErrIdentifierOwnedByAnotherTeam = model.ErrIdentifierOwnedByAnotherTeam

// insertedScanner adapts a row whose projection is `(xmax=0) AS inserted, ` + issueColumns so scanIssue (which
// scans exactly issueColumns) can be reused unchanged: it prepends &inserted to the scan destinations.
type insertedScanner struct {
	row      pgx.Row
	inserted *bool
}

func (s insertedScanner) Scan(dest ...any) error {
	return s.row.Scan(append([]any{s.inserted}, dest...)...)
}

func (s *Store) GetByID(ctx context.Context, id string) (*model.Issue, error) {
	out, err := scanIssue(s.pool.QueryRow(ctx,
		`SELECT `+issueColumns+` FROM issues WHERE id = $1`,
		id,
	))
	if err != nil {
		return nil, err
	}
	s.attachFieldValues(ctx, out)
	s.attachBlocked(ctx, out)
	s.attachTimeTracked(ctx, out)
	s.attachScores(ctx, out)
	return out, nil
}

// workspaceID is REQUIRED. This lookup used to run `WHERE identifier = $1` with no
// tenancy filter, but migration 0022 made identifier unique PER WORKSPACE, not globally
// — two tenants each running a team called ENG both hold ENG-42, and QueryRow returned
// whichever row Postgres produced first. The GitHub webhook then WROTE through it
// (status→done plus a comment), so a single global webhook secret could close any
// tenant's issue by naming an identifier, non-deterministically. An empty workspaceID is
// an error rather than a wildcard: there is no legitimate caller that wants every tenant's
// ENG-42 at once, and a silent wildcard is how this got missed. Callers that must resolve
// an identifier WITHOUT knowing the workspace up front use WorkspaceOfIdentifier, which
// is bounded by the caller's own memberships.
func (s *Store) GetByIdentifier(ctx context.Context, identifier, workspaceID string) (*model.Issue, error) {
	if workspaceID == "" {
		return nil, errors.New("issue: GetByIdentifier requires a workspace_id (an unscoped identifier lookup crosses tenants)")
	}
	out, err := scanIssue(s.pool.QueryRow(ctx,
		`SELECT `+issueColumns+` FROM issues WHERE identifier = $1 AND workspace_id = $2`,
		identifier, workspaceID,
	))
	if err != nil {
		return nil, err
	}
	s.attachFieldValues(ctx, out)
	s.attachBlocked(ctx, out)
	s.attachTimeTracked(ctx, out)
	s.attachScores(ctx, out)
	return out, nil
}

// attachFieldValues populates FieldValues on an issue if a fetcher is
// wired. Errors from the fetcher are intentionally swallowed: a
// transient failure reading custom fields shouldn't 500 the core
// issue read. The issue still comes back, just without its
// custom-field payload.
func (s *Store) attachFieldValues(ctx context.Context, i *model.Issue) {
	if s.fetcher == nil || i == nil {
		return
	}
	vals, err := s.fetcher.GetValues(ctx, i.ID, i.WorkspaceID)
	if err != nil || len(vals) == 0 {
		return
	}
	i.FieldValues = vals
}

// attachBlocked populates IsBlocked if a checker is wired. Same
// swallow-on-error policy as attachFieldValues — the blocker badge
// is informational, not load-bearing.
func (s *Store) attachBlocked(ctx context.Context, i *model.Issue) {
	if s.blocked == nil || i == nil {
		return
	}
	blocked, err := s.blocked.IsBlocked(ctx, i.ID)
	if err != nil {
		return
	}
	i.IsBlocked = blocked
}

// attachTimeTracked populates TimeTracked if a tracker is wired.
// Same error-swallow policy as the other attach helpers — total time
// is a UX hint, not a correctness invariant.
func (s *Store) attachTimeTracked(ctx context.Context, i *model.Issue) {
	if s.timer == nil || i == nil {
		return
	}
	sec, err := s.timer.IssueTotalSec(ctx, i.ID)
	if err != nil || sec <= 0 {
		return
	}
	i.TimeTracked = sec
}

// attachScores populates RICEScore / ICEScore if a scorer is wired.
// Each is set independently — an issue scored with RICE only gets
// RICEScore filled and leaves ICEScore nil, matching the optional
// per-method storage in issue_scores.
func (s *Store) attachScores(ctx context.Context, i *model.Issue) {
	if s.scorer == nil || i == nil {
		return
	}
	rice, ice, err := s.scorer.IssueScores(ctx, i.ID)
	if err != nil {
		return
	}
	i.RICEScore = rice
	i.ICEScore = ice
}

// List composes a WHERE-clause set dynamically from the filter. Each
// filter field that's non-zero produces one $N placeholder. Ordering
// and pagination are validated against allowlists to keep the SQL
// safely composed.
func (s *Store) List(ctx context.Context, filter IssueFilter) ([]model.Issue, error) {
	if s.pool == nil {
		return nil, nil
	}
	var (
		where []string
		args  []any
		argN  int
	)
	add := func(clause string, val any) {
		argN++
		where = append(where, fmt.Sprintf(clause, argN))
		args = append(args, val)
	}
	if filter.WorkspaceID != "" {
		add("workspace_id = $%d", filter.WorkspaceID)
	}
	if filter.TeamID != "" {
		add("team_id = $%d", filter.TeamID)
	}
	if filter.ProjectID != "" {
		add("project_id = $%d", filter.ProjectID)
	}
	if filter.CycleID != "" {
		add("cycle_id = $%d", filter.CycleID)
	}
	if filter.Status != "" {
		add("status = $%d", filter.Status)
	}
	if filter.AssigneeID != "" {
		add("assignee_id = $%d", filter.AssigneeID)
	}
	if filter.Priority > 0 {
		add("priority = $%d", filter.Priority)
	}
	if len(filter.Labels) > 0 {
		add("labels && $%d", filter.Labels)
	}

	limit := filter.Limit
	switch {
	case limit <= 0:
		limit = 50
	case limit > 250:
		limit = 250
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}

	// Order column allowlist: anything else falls back to created_at DESC
	// so a malformed query never breaks pagination.
	orderBy := "created_at"
	switch filter.OrderBy {
	case "created_at", "updated_at", "priority", "sort_order":
		orderBy = filter.OrderBy
	}
	orderDir := "DESC"
	if strings.EqualFold(filter.OrderDir, "asc") {
		orderDir = "ASC"
	}

	args = append(args, limit, offset)
	limitPos := argN + 1
	offsetPos := argN + 2

	whereClause := ""
	if len(where) > 0 {
		whereClause = " WHERE " + strings.Join(where, " AND ")
	}
	sql := `SELECT ` + issueColumns + ` FROM issues` + whereClause +
		fmt.Sprintf(" ORDER BY %s %s LIMIT $%d OFFSET $%d", orderBy, orderDir, limitPos, offsetPos)

	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("issue: list: %w", err)
	}
	defer rows.Close()

	var out []model.Issue
	for rows.Next() {
		issue, err := scanIssue(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *issue)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	s.attachFieldValuesBulk(ctx, out)
	return out, nil
}

// attachFieldValuesBulk decorates every issue in the slice with its
// custom-field values using one bulk SELECT instead of N. Same
// error-swallowing policy as the per-issue variant.
func (s *Store) attachFieldValuesBulk(ctx context.Context, issues []model.Issue) {
	if s.fetcher == nil || len(issues) == 0 {
		return
	}
	ids := make([]string, 0, len(issues))
	for i := range issues {
		ids = append(ids, issues[i].ID)
	}
	byIssue, err := s.fetcher.GetValuesBulk(ctx, ids)
	if err != nil {
		return
	}
	for i := range issues {
		if v, ok := byIssue[issues[i].ID]; ok && len(v) > 0 {
			issues[i].FieldValues = v
		}
	}
}

// updatableFields is the allowlist of columns Update will touch. Any
// key in the map argument that isn't in this set is silently dropped
// — protects against SQL injection via map keys.
var updatableFields = map[string]struct{}{
	"title":        {},
	"description":  {},
	"status":       {},
	"priority":     {},
	"assignee_id":  {},
	"project_id":   {},
	"cycle_id":     {},
	"parent_id":    {},
	"due_date":     {},
	"labels":       {},
	"sort_order":   {},
	"lens_feature": {},
}

// Update applies the supplied field map and returns the materialised
// row. Status transitions to "done" stamp completed_at; transitions
// away from "done" clear it — both happen server-side so the API
// caller never has to set completed_at by hand.
// issueRefQueries maps a settable cross-object reference to a FIXED EXISTS query
// confirming the referenced object lives in a given workspace. The queries are
// literals (no dynamic table name), so there is no injection surface. team_id is
// validated separately, folded into Create's existing identifier lookup.
var issueRefQueries = map[string]string{
	"assignee_id": `SELECT EXISTS(SELECT 1 FROM members WHERE id = $1 AND workspace_id = $2)`,
	"project_id":  `SELECT EXISTS(SELECT 1 FROM projects WHERE id = $1 AND workspace_id = $2)`,
	"cycle_id":    `SELECT EXISTS(SELECT 1 FROM cycles WHERE id = $1 AND workspace_id = $2)`,
	"parent_id":   `SELECT EXISTS(SELECT 1 FROM issues WHERE id = $1 AND workspace_id = $2)`,
}

// assertRefInWorkspace refuses unless refID exists in workspaceID. query is a fixed
// literal supplied by the caller (issueRefQueries).
func (s *Store) assertRefInWorkspace(ctx context.Context, query, field, refID, workspaceID string) error {
	var ok bool
	if err := s.pool.QueryRow(ctx, query, refID, workspaceID).Scan(&ok); err != nil {
		return fmt.Errorf("issue: validate %s: %w", field, err)
	}
	if !ok {
		return fmt.Errorf("issue: %s references an object outside the issue's workspace", field)
	}
	return nil
}

// validateRefWorkspaces checks every settable cross-object reference in updates
// against the issue's own workspace. Object-graph integrity: you can't give an issue
// a parent / cycle / project / assignee from another workspace. The workspace is
// looked up only when at least one reference is actually being set, so status-only
// (and other ref-free) updates pay nothing.
func (s *Store) validateRefWorkspaces(ctx context.Context, issueID string, updates map[string]any) error {
	pending := map[string]string{}
	for field := range issueRefQueries {
		raw, ok := updates[field]
		if !ok || raw == nil {
			continue
		}
		if id, ok := raw.(string); ok && id != "" {
			pending[field] = id
		}
	}
	if len(pending) == 0 {
		return nil
	}
	var ws string
	if err := s.pool.QueryRow(ctx, `SELECT workspace_id FROM issues WHERE id = $1`, issueID).Scan(&ws); err != nil {
		return fmt.Errorf("issue: lookup workspace: %w", err)
	}
	for field, refID := range pending {
		if err := s.assertRefInWorkspace(ctx, issueRefQueries[field], field, refID, ws); err != nil {
			return err
		}
	}
	return nil
}

// GetInWorkspace is the exported scoped read for cross-package user-facing callers (ai/…): a foreign
// id yields ErrNotFound, never a cross-tenant disclosure. Same-package handlers use getInWorkspace.
func (s *Store) GetInWorkspace(ctx context.Context, id, workspaceID string) (*model.Issue, error) {
	return s.getInWorkspace(ctx, id, workspaceID)
}

// getInWorkspace is the scoped read the by-id ops fall back to (never the unscoped GetByID).
func (s *Store) getInWorkspace(ctx context.Context, id, workspaceID string) (*model.Issue, error) {
	i, err := scanIssue(s.pool.QueryRow(ctx,
		`SELECT `+issueColumns+` FROM issues WHERE id = $1 AND workspace_id = $2`, id, workspaceID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return i, err
}

// Update mutates an issue only within workspaceID (the caller's authorized workspace) — SEC-5:
// a foreign id yields ErrNotFound, never a cross-tenant write.
func (s *Store) Update(ctx context.Context, id, workspaceID string, updates map[string]any) (*model.Issue, error) {
	if len(updates) == 0 {
		return s.getInWorkspace(ctx, id, workspaceID)
	}

	// Stamp completed_at based on the incoming status, if any.
	if rawStatus, ok := updates["status"]; ok {
		if str, isStr := rawStatus.(string); isStr {
			if str == string(model.StatusDone) {
				updates["completed_at"] = time.Now().UTC()
			} else {
				updates["completed_at"] = nil
			}
		}
	}

	if err := s.validateRefWorkspaces(ctx, id, updates); err != nil {
		return nil, err
	}

	var (
		setClauses []string
		args       []any
		argN       int
	)
	for k, v := range updates {
		if _, ok := updatableFields[k]; !ok && k != "completed_at" {
			continue
		}
		argN++
		setClauses = append(setClauses, fmt.Sprintf("%s = $%d", k, argN))
		args = append(args, v)
	}
	if len(setClauses) == 0 {
		return s.getInWorkspace(ctx, id, workspaceID)
	}
	// updated_at is always bumped — never trust the caller's value.
	argN++
	setClauses = append(setClauses, fmt.Sprintf("updated_at = $%d", argN))
	args = append(args, time.Now().UTC())
	// id + workspace_id are the final positional args for the SEC-5-scoped WHERE clause.
	argN++
	idN := argN
	args = append(args, id)
	argN++
	wsN := argN
	args = append(args, workspaceID)

	sql := fmt.Sprintf(
		`UPDATE issues SET %s WHERE id = $%d AND workspace_id = $%d RETURNING %s`,
		strings.Join(setClauses, ", "), idN, wsN, issueColumns,
	)
	i, err := scanIssue(s.pool.QueryRow(ctx, sql, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return i, err
}

// Delete is a soft delete: the row stays but the status becomes
// "cancelled" so historical reports can still see the identifier.
// updated_at is bumped so audit trails record the transition.
// ErrNotFound is the SEC-5 sentinel: a by-id op resolved to no row in the caller's authorized
// workspace. The handler maps it to 404 (a foreign id and a nonexistent id are indistinguishable).
var ErrNotFound = errors.New("issue: not found in workspace")

// Delete soft-cancels an issue only within workspaceID (the caller's authorized workspace) —
// SEC-5: a foreign id yields ErrNotFound, never a cross-tenant cancel.
func (s *Store) Delete(ctx context.Context, id, workspaceID string) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE issues SET status = 'cancelled', updated_at = NOW() WHERE id = $1 AND workspace_id = $2`,
		id, workspaceID,
	)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordSpendEvent is the WEBHOOK's authoritative, idempotent cost accumulator. It
// records a Lens cost event in ai_spend_events and accumulates it onto every issue
// sharing the lens_feature, ATOMICALLY in one statement. eventKey is the idempotency
// key (a content hash of the event): a re-delivered event with the same key writes no
// new row and adds no cost. One ai_spend_events row is written per credited issue, so
// an issue's ai_cost_usd always equals the SUM of its ai_spend_events rows. Returns
// the number of issues newly credited (0 on a re-delivery, or no feature match).
//
// Cost still fans out to every issue sharing a lens_feature (name-match attribution);
// the durable fix is per-request attribution from Lens — see the T7 notes.
func (s *Store) RecordSpendEvent(ctx context.Context, eventKey, lensFeature string, costUSD float64, tokens int, workspaceID, source string) (int, error) {
	if eventKey == "" || lensFeature == "" || workspaceID == "" {
		return 0, errors.New("issue: RecordSpendEvent requires event_key, lens_feature, workspace_id")
	}
	tag, err := s.pool.Exec(ctx, `
        WITH matched AS (
            SELECT id FROM issues WHERE lens_feature = $2 AND workspace_id = $3
        ),
        ins AS (
            INSERT INTO ai_spend_events (event_key, workspace_id, issue_id, lens_feature, cost_usd, tokens, source)
            SELECT $1, $3, m.id, $2, $4, $5, $6 FROM matched m
            ON CONFLICT (event_key, COALESCE(issue_id, '')) DO NOTHING
            RETURNING issue_id
        )
        UPDATE issues SET ai_cost_usd = ai_cost_usd + $4, ai_tokens = ai_tokens + $5, updated_at = NOW()
        WHERE id IN (SELECT issue_id FROM ins)`,
		eventKey, lensFeature, workspaceID, costUSD, tokens, source,
	)
	if err != nil {
		return 0, fmt.Errorf("issue: record spend event: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// UnattributedSpend totals the workspace's AI spend that reached NO issue — the ledger rows the
// per-request sync wrote with a NULL issue_id because their feature addressed no issue (an untagged
// request, or a feature matching no identifier).
//
// It is deliberately a QUERY over the same append-only ledger that issues.ai_cost_usd sums, not a
// separately-maintained counter: attributed + unattributed = the ledger total BY CONSTRUCTION, over
// the same lifetime window, so the two can never drift into disagreeing. A stored second number
// would be a number to reconcile rather than the reconciliation.
//
// The legacy webhook path (RecordSpendEvent) inserts only `FROM matched m`, so it never produces a
// NULL issue_id row — `issue_id IS NULL` means exactly "unattributed per-request spend" and nothing
// else.
func (s *Store) UnattributedSpend(ctx context.Context, workspaceID string) (costUSD float64, requests int, err error) {
	if workspaceID == "" {
		return 0, 0, errors.New("issue: UnattributedSpend requires workspace_id")
	}
	if err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(cost_usd), 0), COUNT(*)
           FROM ai_spend_events
          WHERE workspace_id = $1 AND issue_id IS NULL`,
		workspaceID,
	).Scan(&costUSD, &requests); err != nil {
		return 0, 0, fmt.Errorf("issue: unattributed spend: %w", err)
	}
	return costUSD, requests, nil
}

// RecordRequestSpend lands ONE per-request cost on the single issue whose identifier == feature, EXACTLY
// ONCE. It is the SYNCER's live accumulator (T7 follow-up, Build 2) — replacing the feature-total
// delta-reconciler (ReconcileFeatureSpend, deleted). Two load-bearing properties:
//
//   - NO FANOUT: it resolves the issue by IDENTIFIER — UNIQUE(workspace_id, identifier) ⇒ 0 or 1 issue —
//     NOT lens_feature. Cost can never land on more than one issue, so shared lens_feature values can't
//     multiply spend.
//   - EXACTLY ONCE: INSERT ... ON CONFLICT (request_id) DO NOTHING, and the issue's ai_cost_usd is credited
//     atomically WITH the insert and ONLY when the insert actually inserted. A re-pulled request_id (the
//     syncer re-reads the last-24h window ~96×/day) conflicts ⇒ no row, no re-credit. The credit is a
//     data-modifying CTE that runs iff `ins` produced a row — never a re-sum toward a total.
//
// UNATTRIBUTED SPEND IS RECORDED, NOT DROPPED. A row whose feature resolves to no issue —
// because it carried no X-Talyvor-Feature at all, or a feature matching no identifier — still
// lands in the ledger, with a NULL issue_id and no issue credited. That is what the ledger was
// designed for: migration 0017 states "the unique index treats a NULL issue_id (orphan spend with
// no matching issue) as the empty string so those dedup too", but this path only ever inserted
// so an unresolved feature produced zero rows and the money left no durable trace. Without it,
// issues.ai_cost_usd is a SUBSET presented as a TOTAL and nothing can be reconciled against the
// Lens invoice. Read it back with UnattributedSpend.
//
// event_key is per-request ('req:'||request_id) on BOTH branches. It has to be: the legacy unique
// index over (event_key, COALESCE(issue_id, empty)) is NOT partial, so two unattributed rows
// both carrying the empty-string column default would collide on the SAME key and the second
// INSERT would fail outright.
//
// A request_id-less row is still REFUSED — it has no dedup key, and the syncer re-reads the same
// window ~96×/day, so writing one would multiply that cost by the number of pulls. An empty
// FEATURE is fine (it simply resolves to nothing); an empty REQUEST_ID is not.
//
// Returns (resolved, landed):
//
//	resolved=false, landed=true  → no issue matched; recorded as unattributed, no issue credited.
//	resolved=true,  landed=true  → fresh insert; issue credited once.
//	landed=false                 → request_id already recorded (a re-pull) ⇒ nothing re-credited.
func (s *Store) RecordRequestSpend(ctx context.Context, requestID, feature string, costUSD float64, tokens int, workspaceID string) (resolved, landed bool, err error) {
	return s.RecordRequestSpendAttributed(ctx, requestID, feature, "", costUSD, tokens, workspaceID)
}

// RecordRequestSpendAttributed is RecordRequestSpend with the ISSUE the caller was working on.
//
// ⚠ WHY THIS EXISTS. Attribution resolved by matching the request's FEATURE against an issue
// identifier. That is right for someone tagging by hand (X-Talyvor-Feature: ENG-42) and wrong for
// the editor we ship: the Code extension sends the feature as an IDE affordance ("code-chat",
// "code-completion"), so it matched no issue and every request from it credited nothing. The issue
// was known all along — the extension sends X-Talyvor-Issue, Lens captures it, and #401 finally made
// it joinable to the spend row and returned it as `issue_id` on /v1/api/spend/by-request.
//
// ⚠ ISSUE FIRST, FEATURE AS FALLBACK, AND THE FALLBACK IS LOAD-BEARING. Manual taggers work today
// and must keep working, so an EMPTY issue resolves exactly as before. The issue wins when both
// resolve: a feature naming some other issue must never outrank the issue the user was actually on.
//
// ⚠ AND WHEN NEITHER RESOLVES the money is still recorded, with a NULL issue_id — #66's rule. This
// is the branch this change touches most directly, so it is asserted rather than assumed: spend that
// matches nothing must be visible as unattributed, never dropped and never guessed onto an issue.
//
// Exactly-once is unchanged and still keyed on request_id: the syncer re-reads the same 24h window
// roughly 96 times a day, so a re-pull must insert nothing and credit nothing.
func (s *Store) RecordRequestSpendAttributed(ctx context.Context, requestID, feature, issueIdentifier string, costUSD float64, tokens int, workspaceID string) (resolved, landed bool, err error) {
	if requestID == "" || workspaceID == "" {
		return false, false, errors.New("issue: RecordRequestSpend requires request_id, workspace_id")
	}
	var resolvedN, insertedN int
	qErr := s.pool.QueryRow(ctx, `
        WITH target AS (
            -- Preference, not a union: the issue header wins outright when it resolves, and the
            -- feature is consulted only when there is no issue to consult. UNIQUE(workspace_id,
            -- identifier) means each arm yields 0 or 1 row, so this can never fan out.
            SELECT id FROM issues WHERE $6 <> '' AND identifier = $6 AND workspace_id = $3
            UNION ALL
            SELECT id FROM issues WHERE $6 = '' AND $2 <> '' AND identifier = $2 AND workspace_id = $3
        ),
        ins AS (
            INSERT INTO ai_spend_events (request_id, event_key, workspace_id, issue_id, lens_feature, cost_usd, tokens, source)
            SELECT $1, 'req:' || $1, $3, (SELECT id FROM target), $2, $4, $5,
                   CASE WHEN EXISTS (SELECT 1 FROM target) THEN 'sync-request' ELSE 'sync-request-unattributed' END
            ON CONFLICT (request_id) WHERE request_id <> '' DO NOTHING
            RETURNING issue_id, cost_usd, tokens
        ),
        upd AS (
            UPDATE issues i SET ai_cost_usd = ai_cost_usd + ins.cost_usd,
                                ai_tokens = ai_tokens + ins.tokens, updated_at = NOW()
            FROM ins WHERE i.id = ins.issue_id
            RETURNING i.id
        )
        SELECT (SELECT count(*) FROM target), (SELECT count(*) FROM ins)`,
		requestID, feature, workspaceID, costUSD, tokens, issueIdentifier,
	).Scan(&resolvedN, &insertedN)
	if qErr != nil {
		return false, false, fmt.Errorf("issue: record request spend: %w", qErr)
	}
	return resolvedN > 0, insertedN > 0, nil
}

// WorkspaceOfIdentifier resolves which of the CALLER'S OWN workspaces holds an issue with
// this human identifier. It exists for the one legitimate shape that cannot name the
// workspace up front — the MCP tools/call chokepoint, where a tool is invoked with
// {"identifier":"ENG-42"} and the workspace must be derived before it can be authorized.
//
// Fail-closed on both ends:
//   - the search is bounded to `allowed` (the caller's resolved memberships), so it can
//     never see a tenant the caller has no relationship with;
//   - AMBIGUITY IS A DENIAL. If the caller belongs to two workspaces that both hold
//     ENG-42, this returns "" rather than picking one. Choosing arbitrarily is exactly
//     what the unscoped lookup did, and "the caller is a member of both" does not make an
//     arbitrary choice correct — it makes it a silent mis-target.
//
// Returns "" (no error) for no match, so callers get the same no-oracle denial for
// "does not exist" and "not yours".
func (s *Store) WorkspaceOfIdentifier(ctx context.Context, identifier string, allowed []string) (string, error) {
	if identifier == "" || len(allowed) == 0 {
		return "", nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT workspace_id FROM issues WHERE identifier = $1 AND workspace_id = ANY($2) LIMIT 2`,
		identifier, allowed,
	)
	if err != nil {
		return "", fmt.Errorf("issue: workspace of identifier: %w", err)
	}
	defer rows.Close()
	var found []string
	for rows.Next() {
		var ws string
		if err := rows.Scan(&ws); err != nil {
			return "", err
		}
		found = append(found, ws)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(found) != 1 {
		return "", nil // 0 = not found/not yours; >1 = ambiguous ⇒ deny, never guess
	}
	return found[0], nil
}

// TopByAICost returns the workspace's most expensive issues in
// descending cost order. Powers the "top spenders" panel on the
// /v1/workspaces/{wsID}/ai-costs dashboard.
func (s *Store) TopByAICost(ctx context.Context, workspaceID string, limit int) ([]model.Issue, error) {
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+issueColumns+` FROM issues
        WHERE workspace_id = $1 AND ai_cost_usd > 0
        ORDER BY ai_cost_usd DESC LIMIT $2`,
		workspaceID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("issue: top by ai cost: %w", err)
	}
	defer rows.Close()
	var out []model.Issue
	for rows.Next() {
		i, err := scanIssue(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *i)
	}
	return out, rows.Err()
}

// Search runs Postgres full-text search across title + description.
// Uses websearch_to_tsquery so callers can pass natural-language
// queries with quoted phrases ("foo bar") and negation (-baz).
func (s *Store) Search(ctx context.Context, workspaceID, query string, limit int) ([]model.Issue, error) {
	if s.pool == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 25
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+issueColumns+` FROM issues
        WHERE workspace_id = $1
          AND to_tsvector('english', title || ' ' || description)
              @@ websearch_to_tsquery('english', $2)
        ORDER BY updated_at DESC
        LIMIT $3`,
		workspaceID, query, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("issue: search: %w", err)
	}
	defer rows.Close()
	var out []model.Issue
	for rows.Next() {
		issue, err := scanIssue(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *issue)
	}
	return out, rows.Err()
}

// ─── BulkUpdate ────────────────────────────────────────────

// BulkUpdateItem is one row in the PATCH /issues/bulk-update payload.
// SortOrder of 0 is treated as "not provided" — the kanban drop
// algorithm never produces 0.0 because it averages neighbouring
// sort_orders (which start at ±1.0). Use Update for the rare case a
// caller really does want to set sort_order to exactly zero.
type BulkUpdateItem struct {
	ID        string  `json:"id"`
	Status    string  `json:"status,omitempty"`
	SortOrder float64 `json:"sort_order,omitempty"`
}

// BulkUpdate applies many status / sort_order patches in a single
// transaction. Powers the kanban drag-and-drop: when a card moves
// columns, the moved card AND every card whose sort_order shifted
// land in one round-trip so the board never looks half-applied.
//
// Mid-batch failures abort the whole transaction — the kanban UI
// rolls back its optimistic state and refetches. Returns the total
// rows affected.
// BulkUpdate applies each patch scoped to workspaceID: the per-item WHERE carries `AND workspace_id`, so
// an id belonging to another workspace matches 0 rows and is silently skipped (excluded from the count) —
// NOT an error. Without this predicate a member of workspace A could flip any workspace's issues by id.
func (s *Store) BulkUpdate(ctx context.Context, workspaceID string, updates []BulkUpdateItem) (int, error) {
	if s.pool == nil {
		return 0, errors.New("issue: store has no pool")
	}
	if len(updates) == 0 {
		return 0, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("issue: bulk update begin: %w", err)
	}
	defer func() {
		// Rollback on any path that returns before Commit. Calling
		// Rollback after a successful Commit is a documented no-op
		// in pgx, so this defer is safe.
		_ = tx.Rollback(ctx)
	}()

	updated := 0
	now := time.Now().UTC()

	for _, u := range updates {
		var (
			set  []string
			args []any
			argN int
		)
		// SET-clause order: status, sort_order, completed_at,
		// updated_at, id-in-WHERE. The fixed order keeps the SQL
		// query plan cache-friendly and the test fixtures readable.
		if u.Status != "" {
			argN++
			set = append(set, fmt.Sprintf("status = $%d", argN))
			args = append(args, u.Status)
		}
		// SortOrder: treat 0.0 as "not provided" — see BulkUpdateItem.
		if u.SortOrder != 0 {
			argN++
			set = append(set, fmt.Sprintf("sort_order = $%d", argN))
			args = append(args, u.SortOrder)
		}
		// completed_at follows status — when status is set we always
		// touch completed_at (stamping it on transitions into "done"
		// and clearing it on transitions out). Mirrors Update().
		if u.Status != "" {
			argN++
			set = append(set, fmt.Sprintf("completed_at = $%d", argN))
			if u.Status == string(model.StatusDone) {
				args = append(args, now)
			} else {
				args = append(args, (*time.Time)(nil))
			}
		}
		if len(set) == 0 {
			continue
		}
		// updated_at is always bumped so the realtime layer can fan a
		// change event out to subscribers.
		argN++
		set = append(set, fmt.Sprintf("updated_at = $%d", argN))
		args = append(args, now)
		argN++
		args = append(args, u.ID)
		idArgN := argN
		argN++
		args = append(args, workspaceID)

		// ITEM A: scope every by-id write to the caller's workspace — a foreign id matches 0 rows.
		sql := fmt.Sprintf(`UPDATE issues SET %s WHERE id = $%d AND workspace_id = $%d`, strings.Join(set, ", "), idArgN, argN)
		tag, err := tx.Exec(ctx, sql, args...)
		if err != nil {
			return 0, fmt.Errorf("issue: bulk update %s: %w", u.ID, err)
		}
		updated += int(tag.RowsAffected())
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("issue: bulk update commit: %w", err)
	}
	return updated, nil
}

// SoleTeam returns the workspace's team when it has exactly one, along with how many it has.
//
// The COUNT is returned rather than a bare (id, found) because the three cases are three different
// answers to the caller: none is a workspace that cannot take issues at all, one is an unambiguous
// default, and several is a genuine question only the caller can settle. Collapsing them into
// "found / not found" is what produced the original failure — a required field reported as missing
// when the real problem was that nothing had ever created the thing it referred to.
//
// ⚠ SCOPED TO THE AUTHORIZED WORKSPACE, which is the caller's own (issue.Handler.Create passes the
// id resolved by the authz middleware, never a body field). The LIMIT 2 is enough to distinguish
// one from many without reading a large team list.
func (s *Store) SoleTeam(ctx context.Context, workspaceID string) (string, int, error) {
	if s.pool == nil {
		return "", 0, errors.New("issue: store has no pool")
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id FROM teams WHERE workspace_id = $1 ORDER BY created_at LIMIT 2`, workspaceID)
	if err != nil {
		return "", 0, fmt.Errorf("issue: sole team: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", 0, fmt.Errorf("issue: sole team scan: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return "", 0, fmt.Errorf("issue: sole team rows: %w", err)
	}
	if len(ids) == 1 {
		return ids[0], 1, nil
	}
	return "", len(ids), nil
}
