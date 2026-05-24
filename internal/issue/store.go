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
	GetValues(ctx context.Context, issueID string) (map[string]string, error)
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

type Store struct {
	pool    pgxDB
	fetcher fieldFetcher
	blocked blockedChecker
	timer   timeSummariser
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
		`SELECT identifier FROM teams WHERE id = $1`,
		issue.TeamID,
	).Scan(&teamIdentifier); err != nil {
		return nil, fmt.Errorf("issue: lookup team identifier: %w", err)
	}

	var nextNumber int
	if err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(MAX(number), 0) + 1 FROM issues WHERE team_id = $1`,
		issue.TeamID,
	).Scan(&nextNumber); err != nil {
		return nil, fmt.Errorf("issue: compute next number: %w", err)
	}
	issue.Number = nextNumber
	issue.Identifier = fmt.Sprintf("%s-%d", teamIdentifier, nextNumber)

	const insertSQL = `INSERT INTO issues
        (workspace_id, team_id, project_id, number, identifier,
         title, description, status, priority,
         assignee_id, creator_id, cycle_id, parent_id,
         due_date, lens_feature, labels, sort_order)
    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
    RETURNING ` + issueColumns
	return scanIssue(s.pool.QueryRow(ctx, insertSQL,
		issue.WorkspaceID, issue.TeamID, issue.ProjectID, issue.Number, issue.Identifier,
		issue.Title, issue.Description, string(issue.Status), int(issue.Priority),
		issue.AssigneeID, issue.CreatorID, issue.CycleID, issue.ParentID,
		issue.DueDate, issue.LensFeature, issue.Labels, issue.SortOrder,
	))
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
	return out, nil
}

func (s *Store) GetByIdentifier(ctx context.Context, identifier string) (*model.Issue, error) {
	out, err := scanIssue(s.pool.QueryRow(ctx,
		`SELECT `+issueColumns+` FROM issues WHERE identifier = $1`,
		identifier,
	))
	if err != nil {
		return nil, err
	}
	s.attachFieldValues(ctx, out)
	s.attachBlocked(ctx, out)
	s.attachTimeTracked(ctx, out)
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
	vals, err := s.fetcher.GetValues(ctx, i.ID)
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
func (s *Store) Update(ctx context.Context, id string, updates map[string]any) (*model.Issue, error) {
	if len(updates) == 0 {
		return s.GetByID(ctx, id)
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
		return s.GetByID(ctx, id)
	}
	// updated_at is always bumped — never trust the caller's value.
	argN++
	setClauses = append(setClauses, fmt.Sprintf("updated_at = $%d", argN))
	args = append(args, time.Now().UTC())
	// id is the final positional arg for the WHERE clause.
	argN++
	args = append(args, id)

	sql := fmt.Sprintf(
		`UPDATE issues SET %s WHERE id = $%d RETURNING %s`,
		strings.Join(setClauses, ", "), argN, issueColumns,
	)
	return scanIssue(s.pool.QueryRow(ctx, sql, args...))
}

// Delete is a soft delete: the row stays but the status becomes
// "cancelled" so historical reports can still see the identifier.
// updated_at is bumped so audit trails record the transition.
func (s *Store) Delete(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE issues SET status = 'cancelled', updated_at = NOW() WHERE id = $1`,
		id,
	)
	return err
}

// UpdateAICost is the Lens-side reconciliation hook: when the proxy
// records a spend row with X-Talyvor-Feature=<feat>, the Track
// recorder calls here to accumulate the cost on every issue that
// declared the same lens_feature within the same workspace.
func (s *Store) UpdateAICost(ctx context.Context, lensFeature string, costUSD float64, tokens int, workspaceID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE issues SET
        ai_cost_usd = ai_cost_usd + $2,
        ai_tokens = ai_tokens + $3,
        updated_at = NOW()
    WHERE lens_feature = $1 AND workspace_id = $4`,
		lensFeature, costUSD, tokens, workspaceID,
	)
	return err
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
func (s *Store) BulkUpdate(ctx context.Context, updates []BulkUpdateItem) (int, error) {
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

		sql := fmt.Sprintf(`UPDATE issues SET %s WHERE id = $%d`, strings.Join(set, ", "), argN)
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
