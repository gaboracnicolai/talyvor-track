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

type IssuePriority int

const (
	PriorityNone   IssuePriority = 0
	PriorityUrgent IssuePriority = 1
	PriorityHigh   IssuePriority = 2
	PriorityMedium IssuePriority = 3
	PriorityLow    IssuePriority = 4
)

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
	DueDate     *time.Time    `json:"due_date,omitempty"    db:"due_date"`
	CompletedAt *time.Time    `json:"completed_at,omitempty" db:"completed_at"`

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
