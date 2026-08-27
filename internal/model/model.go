// Package model defines the core Talyvor Track data types.
//
// The struct tags carry both JSON (for the HTTP API) and db (for the
// hand-rolled pgx Scan helpers in each store). Optional fields use
// pointers so a missing value distinguishes from a zero value.
package model

import (
	"errors"
	"time"
)

// Workspace is the top-level tenant. Every team, project, issue,
// member, and cycle belongs to exactly one workspace.
type Workspace struct {
	ID        string    `json:"id"         db:"id"`
	Name      string    `json:"name"       db:"name"`
	Slug      string    `json:"slug"       db:"slug"`
	LogoURL   string    `json:"logo_url"   db:"logo_url"`
	Plan      string    `json:"plan"       db:"plan"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
}

// Team is a group of members within a workspace. Each team has a
// short identifier ("ENG", "MKT") used as the prefix in issue
// identifiers like ENG-42.
type Team struct {
	ID          string    `json:"id"           db:"id"`
	WorkspaceID string    `json:"workspace_id" db:"workspace_id"`
	Name        string    `json:"name"         db:"name"`
	Identifier  string    `json:"identifier"   db:"identifier"`
	Color       string    `json:"color"        db:"color"`
	Icon        string    `json:"icon"         db:"icon"`
	CreatedAt   time.Time `json:"created_at"   db:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"   db:"updated_at"`
}

// Project groups issues across a time-bounded effort. A project always
// belongs to exactly one team but can be referenced by issues from
// other teams within the same workspace.
type Project struct {
	ID          string     `json:"id"                   db:"id"`
	WorkspaceID string     `json:"workspace_id"         db:"workspace_id"`
	TeamID      string     `json:"team_id"              db:"team_id"`
	Name        string     `json:"name"                 db:"name"`
	Identifier  string     `json:"identifier"           db:"identifier"`
	Description string     `json:"description"          db:"description"`
	Status      string     `json:"status"               db:"status"`
	Priority    int        `json:"priority"             db:"priority"`
	StartDate   *time.Time `json:"start_date,omitempty" db:"start_date"`
	TargetDate  *time.Time `json:"target_date,omitempty" db:"target_date"`
	CreatedAt   time.Time  `json:"created_at"           db:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"           db:"updated_at"`
}

type IssueStatus string

const (
	StatusBacklog    IssueStatus = "backlog"
	StatusTodo       IssueStatus = "todo"
	StatusInProgress IssueStatus = "in_progress"
	StatusInReview   IssueStatus = "in_review"
	StatusDone       IssueStatus = "done"
	StatusCancelled  IssueStatus = "cancelled"
)

// StatusLabelOther is the Prometheus label every status OUTSIDE the six constants above is counted
// under, and it is not itself a status — pinned by
// issue.TestIssueMetrics_TheBucketIsNotItselfAStatus.
//
// ⚠ IT EXISTS BECAUSE `status` IS THE ONE LABEL ON track_issues_created_total /
// track_issues_updated_total THAT A CALLER CHOOSES. internal/metrics/metrics.go's header states the
// rule — "Keep label cardinality bounded — workspace ID is fine (one workspace = one tenant), but
// never use issue ID or arbitrary user-supplied values" — and `workspace` and `team` obey it because
// they are server-generated ids. `status` did not: `issues.status` is TEXT NOT NULL DEFAULT
// 'backlog' with NO CHECK constraint, `status` is in issue.updatableFields, and nothing between the
// request body and the column compares the value to the constants above. MEASURED through the
// shipped store on real Postgres: Create with "Deployed to prod 🚀" is accepted and produces
// track_issues_created_total{status="Deployed to prod 🚀"}; Update with "'; DROP TABLE issues; --"
// is accepted and stored. Both doors, both counters.
//
// ⚠⚠ AND /metrics IS OUTSIDE THE AUTH BOUNDARY. cmd/track/main.go registers it at the TOP LEVEL —
// above every r.Group/r.Route that installs gatewayauth + authz — so an authenticated workspace
// member could mint an unbounded number of time-series carrying text they chose, readable by anyone
// who can reach the endpoint. The cardinality half is what metrics.go's rule is about; the
// publication half is worse and the rule does not name it.
//
// ⚠ IT BOUNDS THE LABEL AND NOT THE COLUMN, DELIBERATELY. BoundStatusLabel is called only from
// issue.countCreated and issue.countUpdatedLabels — the two increment sites metrics_reach_test.go
// pins as the only ones — and the write path is untouched: the tests assert the raw value still
// reaches the column. Whether an arbitrary status may be WRITTEN is an open product question and
// not a session's to answer: internal/workflow ships a per-team status pipeline whose package
// comment says "any team can add custom ones", so narrowing the column would foreclose a feature
// this repository already has code for. See the queue entry for what a decision there needs.
//
// ⚠ THE BUCKET PRESERVES THE TOTAL. An unknown status is still a create and still an update, which
// is what these counters exist to total; dropping the increment instead would UNDERCOUNT, and
// undercounting is the exact defect countCreated and countUpdatedLabels were each written to end.
const StatusLabelOther = "other"

// issueStatuses is the closed set, in the column's own spelling. It is derived from the constants
// above rather than restated so the two cannot disagree.
var issueStatuses = []IssueStatus{
	StatusBacklog, StatusTodo, StatusInProgress, StatusInReview, StatusDone, StatusCancelled,
}

// IssueStatuses returns the closed set of issue statuses. A copy, so a caller cannot edit the set
// every metric label and every enumeration test reads.
func IssueStatuses() []IssueStatus {
	out := make([]IssueStatus, len(issueStatuses))
	copy(out, issueStatuses)
	return out
}

// BoundStatusLabel maps a status to the Prometheus label it may be counted under: itself when it is
// one of the six, StatusLabelOther otherwise.
//
// ⚠ THE COMPARISON IS EXACT, NOT CASE-FOLDED OR TRIMMED, and that is the point rather than an
// oversight. "Done" is not "done": internal/workflow seeds every team six workflow_statuses named
// "Backlog"/"Todo"/"In Progress"/"In Review"/"Done"/"Cancelled" — Title Case, spaces — while this
// column and internal/analytics's `status IN ('done','cancelled')` filters use the snake_case
// spellings above. Folding here would quietly assert those two vocabularies are the same set, which
// is the very question nothing in this repository currently answers.
func BoundStatusLabel(status string) string {
	for _, s := range issueStatuses {
		if string(s) == status {
			return status
		}
	}
	return StatusLabelOther
}

// ImporterCreatorID is the creator_id the import pipeline stamps on every row it writes
// (importer.run). It is the ONLY provenance Track has for "this issue came from a provider
// import", and issue.Store.UpsertByIdentifier keys its re-import policy on it: a provider may
// update the rows the importer created and may not overwrite one a user created. Both halves
// must agree on this exact string, which is why it lives here rather than as two literals.
const ImporterCreatorID = "importer"

// ErrIdentifierNotImportOwned is the outcome of the refusal ImporterCreatorID exists to make: a
// provider key collided with an issue a HUMAN created, so UpsertByIdentifier declined to overwrite
// it. issue.Store returns it (wrapped); importer.run tests for it with errors.Is.
//
// It lives here for the same reason ImporterCreatorID does — both halves must agree on ONE value,
// and here the agreement is load-bearing in a way a string is not: errors.Is compares IDENTITY, so
// two packages each declaring their own "identifier not owned by an import" would compare unequal
// and the importer would silently classify every refusal as a failure. That is precisely the state
// this constant was introduced to end, so it may not be reachable by re-declaration.
//
// ⚠ WHY THE IMPORTER MUST DISTINGUISH IT AT ALL, measured at dcfbaa3 and stated where the next
// reader looks: a refused row is not a failed row. It is the system working exactly as designed —
// #71 built the refusal so an import cannot overwrite a human's issue — and before this was wired
// up an import that protected three human-written issues reported {status:"failed", failed:3}.
var ErrIdentifierNotImportOwned = errors.New("identifier not owned by an import")

// ErrIdentifierOwnedByAnotherTeam is the SECOND refusal the same conflict arm makes, and it exists
// because ErrIdentifierNotImportOwned answers only half of the question that arm has to ask.
// `issues.identifier` is UNIQUE per (workspace_id, identifier) and carries no team in it, so the
// row an import collides with may be a HUMAN's (that one) or ANOTHER TEAM'S IMPORT (this one).
//
// ⚠ MEASURED before this existed, through the async runner on real Postgres, two teams in one
// workspace: importing the same export into team B after team A reported `succeeded imported=2`
// with ZERO issues in team B, and a Linear export whose keys collide overwrote team A's Jira
// issues' title, description and labels — the three columns the conflict arm clobbers — while
// reporting `succeeded` again.
//
// ⚠ THE COLLISION IS THE NAMESPACE RATHER THAN A COINCIDENCE: across 305 real Jira exports, NINE
// project keys are carried by two or more distinct repository owners, headed by SCRUM (9 owners)
// and KAN (3) — the keys Jira's own Scrum and Kanban templates assign by default.
//
// ⚠ IT IS A DISTINCT VALUE RATHER THAN A REUSE, and the reason is the sentence rather than the
// control flow: both refusals are counted the same way (Refused, never Failed), but telling an
// operator their issue "was not created by an import" when it was created by their own earlier
// import sends them to look for a duplicate that does not exist. The two halves must be tellable
// apart at the point a human reads the job row.
var ErrIdentifierOwnedByAnotherTeam = errors.New("identifier already imported into another team")

type IssuePriority int

const (
	PriorityNone   IssuePriority = 0
	PriorityUrgent IssuePriority = 1
	PriorityHigh   IssuePriority = 2
	PriorityMedium IssuePriority = 3
	PriorityLow    IssuePriority = 4
)

// ValidPriority reports whether p is one of the five priorities this type declares.
//
// ⚠ IT EXISTS BECAUSE THE DECLARATION ABOVE WAS THE ONLY THING SAYING SO. `issues.priority` is
// `INTEGER NOT NULL DEFAULT 0` with no CHECK constraint, and nothing between a request body and
// that column compared the value to these five constants — measured on real Postgres, `Create`
// and `Update` both stored 99 and -7, and the product then draws a BLANK priority control for a
// value it has no label for. A type whose domain only the reader enforces is a comment.
//
// ⚠ AND IT IS DELIBERATELY A PREDICATE ON THE MODEL, NOT A CHECK CONSTRAINT ON THE COLUMN. Rows
// already carrying an out-of-domain value must keep loading — a constraint added to the table
// would make them unreadable, turning a cosmetic defect into an outage. This gates WRITES.
func ValidPriority(p IssuePriority) bool {
	return p >= PriorityNone && p <= PriorityLow
}

// PriorityDomain is the human-readable domain, for error messages that tell a caller what to send
// instead of only what was wrong.
const PriorityDomain = "0 (none), 1 (urgent), 2 (high), 3 (medium), 4 (low)"

// Issue is the atomic work unit. Issue numbers auto-increment per team
// (not per workspace) so two teams can have ENG-1 and DES-1 in the
// same workspace. Identifiers are NEVER reused — a cancelled issue
// keeps its number forever.
type Issue struct {
	ID          string        `json:"id"                    db:"id"`
	WorkspaceID string        `json:"workspace_id"          db:"workspace_id"`
	TeamID      string        `json:"team_id"               db:"team_id"`
	ProjectID   *string       `json:"project_id,omitempty"  db:"project_id"`
	Number      int           `json:"number"                db:"number"`
	Identifier  string        `json:"identifier"            db:"identifier"`
	Title       string        `json:"title"                 db:"title"`
	Description string        `json:"description"           db:"description"`
	Status      IssueStatus   `json:"status"                db:"status"`
	Priority    IssuePriority `json:"priority"              db:"priority"`
	AssigneeID  *string       `json:"assignee_id,omitempty" db:"assignee_id"`
	CreatorID   string        `json:"creator_id"            db:"creator_id"`
	CycleID     *string       `json:"cycle_id,omitempty"    db:"cycle_id"`
	ParentID    *string       `json:"parent_id,omitempty"   db:"parent_id"`
	// MilestoneID is the FIFTH cross-object reference, and it is the one the struct was missing
	// while `issues.milestone_id` existed. Migration 0004 declares and indexes the column,
	// milestone.Store.GetProgress counts on it and project/roadmap.go joins on it to render
	// "<completed>/<issues> done" plus an AI-cost line per milestone — and no write path in this
	// repo named it: absent from both INSERTs, absent from updatableFields (which drops unknown
	// keys in silence, so a PATCH carrying it answered 200 and changed nothing), and absent from
	// here, so no handler could carry one in. Both rendered numbers were therefore structurally
	// zero for every milestone in every workspace. See roadmap_milestone_realpg_test.go.
	MilestoneID *string    `json:"milestone_id,omitempty" db:"milestone_id"`
	DueDate     *time.Time `json:"due_date,omitempty"    db:"due_date"`
	CompletedAt *time.Time `json:"completed_at,omitempty" db:"completed_at"`

	// Talyvor Lens integration. LensFeature is the value teams set in
	// X-Talyvor-Feature when calling Lens; AICostUSD and AITokens are
	// reconciled in by the Lens-side recorder.
	LensFeature string  `json:"lens_feature" db:"lens_feature"`
	AICostUSD   float64 `json:"ai_cost_usd"  db:"ai_cost_usd"`
	AITokens    int     `json:"ai_tokens"    db:"ai_tokens"`

	Labels    []string  `json:"labels"     db:"labels"`
	SortOrder float64   `json:"sort_order" db:"sort_order"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`

	// FieldValues holds custom-field values (field_id → value) for the
	// issue. Populated by the read paths in issue.Store when a
	// customfield fetcher is wired via WithFieldFetcher. omitempty so
	// existing JSON shapes that never set the field stay byte-stable.
	FieldValues map[string]string `json:"field_values,omitempty"`

	// IsBlocked is populated by GetByID when a blocked-checker is wired
	// via issue.Store.WithBlockedChecker. true means the issue has at
	// least one open blocker (a "blocks" relation from an issue whose
	// status is not done/cancelled). omitempty keeps the JSON terse
	// when the field isn't populated (e.g. on bulk reads).
	IsBlocked bool `json:"is_blocked,omitempty"`

	// Relations is a placeholder list of relation IDs attached to the
	// issue. Reserved for future bulk-relation prefetch; not populated
	// by current read paths.
	Relations []string `json:"relations,omitempty"`

	// TimeTracked is the total tracked time in seconds across every
	// member who has worked on the issue. Populated by GetByID when a
	// time-tracking summariser is wired via WithTimeTracker. omitempty
	// so the JSON stays terse on bulk reads.
	TimeTracked int `json:"time_tracked_sec,omitempty"`

	// RICEScore / ICEScore are populated by GetByID when a scoring
	// store is wired via WithScorer. Pointers so "unset" is
	// distinguishable from "0" — a deliberately-zero score is valid.
	RICEScore *float64 `json:"rice_score,omitempty"`
	ICEScore  *float64 `json:"ice_score,omitempty"`
}

// Comment is markdown-formatted user content attached to an issue.
type Comment struct {
	ID        string     `json:"id"                  db:"id"`
	IssueID   string     `json:"issue_id"            db:"issue_id"`
	AuthorID  string     `json:"author_id"           db:"author_id"`
	Body      string     `json:"body"                db:"body"`
	EditedAt  *time.Time `json:"edited_at,omitempty" db:"edited_at"`
	CreatedAt time.Time  `json:"created_at"          db:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"          db:"updated_at"`
}

// Cycle is a time-bounded planning window (sprint). Each team
// numbers its own cycles.
type Cycle struct {
	ID          string    `json:"id"           db:"id"`
	TeamID      string    `json:"team_id"      db:"team_id"`
	WorkspaceID string    `json:"workspace_id" db:"workspace_id"`
	Name        string    `json:"name"         db:"name"`
	Number      int       `json:"number"       db:"number"`
	Status      string    `json:"status"       db:"status"`
	StartDate   time.Time `json:"start_date"   db:"start_date"`
	EndDate     time.Time `json:"end_date"     db:"end_date"`
	CreatedAt   time.Time `json:"created_at"   db:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"   db:"updated_at"`
}

// Member is a person who can be referenced as an assignee, creator,
// or commenter within a workspace.
type Member struct {
	ID          string    `json:"id"           db:"id"`
	WorkspaceID string    `json:"workspace_id" db:"workspace_id"`
	Name        string    `json:"name"         db:"name"`
	Email       string    `json:"email"        db:"email"`
	AvatarURL   string    `json:"avatar_url"   db:"avatar_url"`
	Role        string    `json:"role"         db:"role"`
	CreatedAt   time.Time `json:"created_at"   db:"created_at"`
}
