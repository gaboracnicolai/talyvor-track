// Package template implements pre-filled issue forms.
//
// Templates are scoped to a workspace (and optionally a team). Each
// workspace gets the five canonical templates (Bug Report, Feature
// Request, Technical Debt, Incident Report, Task) seeded on creation
// via SeedDefaults. The seed is idempotent — re-running it on an
// existing workspace touches no rows.
package template

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/tenancy"
)

// ─── types ──────────────────────────────────────────────────

type IssueTemplate struct {
	ID              string            `json:"id"`
	WorkspaceID     string            `json:"workspace_id"`
	TeamID          *string           `json:"team_id,omitempty"`
	Name            string            `json:"name"`
	Description     string            `json:"description"`
	Icon            string            `json:"icon"`
	TitleFormat     string            `json:"title_format"`
	Body            string            `json:"body"`
	DefaultStatus   string            `json:"default_status"`
	DefaultPriority int               `json:"default_priority"`
	DefaultLabels   []string          `json:"default_labels"`
	DefaultAssignee *string           `json:"default_assignee,omitempty"`
	FieldDefaults   map[string]string `json:"field_defaults"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
}

// ApplyTo merges this template's defaults into an Issue that the
// caller is about to insert. The caller's explicit values always
// win — only zero / nil / empty fields are filled. Lives on the
// template type so the merge stays in one place and the issue
// handler doesn't grow per-field branches.
func (t *IssueTemplate) ApplyTo(into *model.Issue) {
	if into == nil {
		return
	}
	if into.Title == "" && t.TitleFormat != "" {
		into.Title = t.TitleFormat
	}
	if into.Description == "" {
		into.Description = t.Body
	}
	if into.Status == "" && t.DefaultStatus != "" {
		into.Status = model.IssueStatus(t.DefaultStatus)
	}
	if into.Priority == 0 && t.DefaultPriority != 0 {
		into.Priority = model.IssuePriority(t.DefaultPriority)
	}
	if len(into.Labels) == 0 && len(t.DefaultLabels) > 0 {
		into.Labels = append(into.Labels, t.DefaultLabels...)
	}
	if into.AssigneeID == nil && t.DefaultAssignee != nil {
		a := *t.DefaultAssignee
		into.AssigneeID = &a
	}
	if len(t.FieldDefaults) > 0 {
		if into.FieldValues == nil {
			into.FieldValues = map[string]string{}
		}
		for k, v := range t.FieldDefaults {
			if _, present := into.FieldValues[k]; !present {
				into.FieldValues[k] = v
			}
		}
	}
}

// ─── store ──────────────────────────────────────────────────

type pgxDB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Store struct{ pool pgxDB }

func NewStore(pool *pgxpool.Pool) *Store {
	var db pgxDB
	if pool != nil {
		db = pool
	}
	return newStore(db)
}

func newStore(db pgxDB) *Store { return &Store{pool: db} }

const templateColumns = `id, workspace_id, team_id, name, description, icon,
    title_format, body, default_status, default_priority,
    default_labels, default_assignee, field_defaults,
    created_at, updated_at`

func scanTemplate(s interface{ Scan(...any) error }) (*IssueTemplate, error) {
	var (
		t   IssueTemplate
		raw []byte
	)
	if err := s.Scan(
		&t.ID, &t.WorkspaceID, &t.TeamID, &t.Name, &t.Description, &t.Icon,
		&t.TitleFormat, &t.Body, &t.DefaultStatus, &t.DefaultPriority,
		&t.DefaultLabels, &t.DefaultAssignee, &raw,
		&t.CreatedAt, &t.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if t.DefaultLabels == nil {
		t.DefaultLabels = []string{}
	}
	t.FieldDefaults = map[string]string{}
	if len(raw) > 0 {
		// Ignore unmarshal errors — corrupt JSON shouldn't kill the
		// read; the caller sees an empty map and can still use the
		// template's other fields.
		_ = json.Unmarshal(raw, &t.FieldDefaults)
	}
	return &t, nil
}

// ─── Create ─────────────────────────────────────────────────

func (s *Store) Create(ctx context.Context, t IssueTemplate) (*IssueTemplate, error) {
	if s.pool == nil {
		return nil, errors.New("template: store has no pool")
	}
	if strings.TrimSpace(t.Name) == "" {
		return nil, errors.New("template: name required")
	}
	if t.Icon == "" {
		t.Icon = "📋"
	}
	if t.DefaultStatus == "" {
		t.DefaultStatus = "backlog"
	}
	if t.DefaultPriority == 0 {
		t.DefaultPriority = 3
	}
	if t.DefaultLabels == nil {
		t.DefaultLabels = []string{}
	}
	if t.FieldDefaults == nil {
		t.FieldDefaults = map[string]string{}
	}

	// Cross-object tenancy: a team-scoped template must reference a team in
	// its own workspace. Workspace-wide templates (nil/empty team_id) skip
	// the guard — there is no reference to validate.
	if t.TeamID != nil && *t.TeamID != "" {
		if err := tenancy.AssertRefInWorkspace(ctx, s.pool, "teams", *t.TeamID, t.WorkspaceID); err != nil {
			return nil, err
		}
	}

	defaults, err := json.Marshal(t.FieldDefaults)
	if err != nil {
		return nil, fmt.Errorf("template: encode field_defaults: %w", err)
	}

	return scanTemplate(s.pool.QueryRow(ctx,
		`INSERT INTO issue_templates (workspace_id, team_id, name, description, icon,
            title_format, body, default_status, default_priority,
            default_labels, default_assignee, field_defaults)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
        RETURNING `+templateColumns,
		t.WorkspaceID, t.TeamID, strings.TrimSpace(t.Name), t.Description, t.Icon,
		t.TitleFormat, t.Body, t.DefaultStatus, t.DefaultPriority,
		t.DefaultLabels, t.DefaultAssignee, defaults,
	))
}

// ─── List ───────────────────────────────────────────────────

func (s *Store) List(ctx context.Context, workspaceID string, teamID *string) ([]IssueTemplate, error) {
	if s.pool == nil {
		return nil, nil
	}
	var (
		rows pgx.Rows
		err  error
	)
	if teamID == nil {
		rows, err = s.pool.Query(ctx,
			`SELECT `+templateColumns+` FROM issue_templates
            WHERE workspace_id = $1 AND team_id IS NULL
            ORDER BY name ASC`,
			workspaceID,
		)
	} else {
		rows, err = s.pool.Query(ctx,
			`SELECT `+templateColumns+` FROM issue_templates
            WHERE workspace_id = $1 AND (team_id IS NULL OR team_id = $2)
            ORDER BY (team_id IS NULL) DESC, name ASC`,
			workspaceID, *teamID,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("template: list: %w", err)
	}
	defer rows.Close()
	var out []IssueTemplate
	for rows.Next() {
		t, err := scanTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// ─── GetByID ────────────────────────────────────────────────

func (s *Store) GetByID(ctx context.Context, id string) (*IssueTemplate, error) {
	if s.pool == nil {
		return nil, errors.New("template: store has no pool")
	}
	return scanTemplate(s.pool.QueryRow(ctx,
		`SELECT `+templateColumns+` FROM issue_templates WHERE id = $1`,
		id,
	))
}

// ─── Update ─────────────────────────────────────────────────

var updatableFields = map[string]struct{}{
	"name":             {},
	"description":      {},
	"icon":             {},
	"title_format":     {},
	"body":             {},
	"default_status":   {},
	"default_priority": {},
	"default_labels":   {},
	"default_assignee": {},
	"field_defaults":   {},
}

func (s *Store) Update(ctx context.Context, id string, updates map[string]any) (*IssueTemplate, error) {
	if s.pool == nil {
		return nil, errors.New("template: store has no pool")
	}
	if len(updates) == 0 {
		return s.GetByID(ctx, id)
	}
	var (
		set  []string
		args []any
		n    int
	)
	for k, v := range updates {
		if _, ok := updatableFields[k]; !ok {
			continue
		}
		// field_defaults arrives as map[string]any; the column is JSONB
		// so we re-encode to bytes for the driver to land cleanly.
		if k == "field_defaults" {
			b, err := json.Marshal(v)
			if err != nil {
				return nil, fmt.Errorf("template: encode field_defaults: %w", err)
			}
			v = b
		}
		n++
		set = append(set, fmt.Sprintf("%s = $%d", k, n))
		args = append(args, v)
	}
	if len(set) == 0 {
		return s.GetByID(ctx, id)
	}
	n++
	set = append(set, fmt.Sprintf("updated_at = $%d", n))
	args = append(args, time.Now().UTC())
	n++
	args = append(args, id)

	sql := fmt.Sprintf(
		`UPDATE issue_templates SET %s WHERE id = $%d RETURNING %s`,
		strings.Join(set, ", "), n, templateColumns,
	)
	return scanTemplate(s.pool.QueryRow(ctx, sql, args...))
}

// ApplyTemplate is the thin convenience the issue handler calls when
// a Create request includes template_id. Looks up the template and
// delegates to ApplyTo. Returning the lookup error lets the caller
// log/ignore — per spec, missing templates never block creation.
func (s *Store) ApplyTemplate(ctx context.Context, templateID string, into *model.Issue) error {
	if templateID == "" {
		return nil
	}
	// Cross-object tenancy: the template must belong to the issue's own
	// workspace — a caller must not apply another workspace's template by
	// bare ID. Checked before the lookup so a cross-workspace ID never
	// loads a foreign template into the issue.
	if err := tenancy.AssertRefInWorkspace(ctx, s.pool, "issue_templates", templateID, into.WorkspaceID); err != nil {
		return err
	}
	t, err := s.GetByID(ctx, templateID)
	if err != nil {
		return fmt.Errorf("template: apply lookup: %w", err)
	}
	t.ApplyTo(into)
	return nil
}

// ─── Delete ─────────────────────────────────────────────────

func (s *Store) Delete(ctx context.Context, id string) error {
	if s.pool == nil {
		return errors.New("template: store has no pool")
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM issue_templates WHERE id = $1`, id)
	return err
}

// ─── SeedDefaults ──────────────────────────────────────────

// SeedDefaults inserts the five canonical templates for a fresh
// workspace. Idempotent via ON CONFLICT (workspace_id, name) DO
// NOTHING — calling it twice on the same workspace is a no-op, so
// we can call it on every Create without first checking whether the
// templates already exist.
func (s *Store) SeedDefaults(ctx context.Context, workspaceID string) error {
	if s.pool == nil {
		return errors.New("template: store has no pool")
	}
	for _, d := range defaultTemplates {
		labels := d.DefaultLabels
		if labels == nil {
			labels = []string{}
		}
		fd, err := json.Marshal(map[string]string{})
		if err != nil {
			return err
		}
		if _, err := s.pool.Exec(ctx,
			`INSERT INTO issue_templates (workspace_id, name, description, icon,
                title_format, body, default_status, default_priority,
                default_labels, field_defaults)
            VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
            ON CONFLICT (workspace_id, name) DO NOTHING`,
			workspaceID, d.Name, d.Description, d.Icon,
			d.TitleFormat, d.Body, d.DefaultStatus, d.DefaultPriority,
			labels, fd,
		); err != nil {
			return fmt.Errorf("template: seed %q: %w", d.Name, err)
		}
	}
	return nil
}

// defaultTemplates is the five-template starter set every workspace
// receives. Bodies are markdown — line endings are `\n`, no trailing
// blank lines (the editor adds them).
var defaultTemplates = []IssueTemplate{
	{
		Name:        "Bug Report",
		Description: "Report something broken with steps to reproduce.",
		Icon:        "🐛",
		TitleFormat: "[Bug] ",
		Body: "## Description\n\n<!-- What happened? -->\n\n" +
			"## Steps to Reproduce\n\n1. \n2. \n3. \n\n" +
			"## Expected Behavior\n\n\n" +
			"## Actual Behavior\n\n\n" +
			"## Environment\n\n- OS: \n- Browser: \n- Version: ",
		DefaultStatus:   "backlog",
		DefaultPriority: 2,
		DefaultLabels:   []string{"bug"},
	},
	{
		Name:        "Feature Request",
		Description: "Propose new functionality with acceptance criteria.",
		Icon:        "✨",
		TitleFormat: "[Feature] ",
		Body: "## Problem\n\n<!-- What problem does this solve? -->\n\n" +
			"## Proposed Solution\n\n\n" +
			"## Acceptance Criteria\n\n- [ ] \n- [ ] \n- [ ] \n\n" +
			"## Alternatives Considered\n\n",
		DefaultStatus:   "backlog",
		DefaultPriority: 3,
		DefaultLabels:   []string{"feature"},
	},
	{
		Name:        "Technical Debt",
		Description: "Improvements to existing systems.",
		Icon:        "🔧",
		TitleFormat: "[Tech Debt] ",
		Body: "## Current State\n\n\n" +
			"## Desired State\n\n\n" +
			"## Impact if Not Addressed\n\n\n" +
			"## Estimated Effort\n\n",
		DefaultStatus:   "backlog",
		DefaultPriority: 4,
		DefaultLabels:   []string{"tech-debt"},
	},
	{
		Name:        "Incident Report",
		Description: "Production outage or critical issue.",
		Icon:        "🚨",
		TitleFormat: "[Incident] ",
		Body: "## Severity\n\n<!-- P0/P1/P2/P3 -->\n\n" +
			"## Impact\n\n<!-- Who is affected? How many users? -->\n\n" +
			"## Timeline\n\n- Detected: \n- Acknowledged: \n- Resolved: \n\n" +
			"## Root Cause\n\n\n" +
			"## Action Items\n\n- [ ] ",
		DefaultStatus:   "backlog",
		DefaultPriority: 1,
		DefaultLabels:   []string{"incident"},
	},
	{
		Name:            "Task",
		Description:     "A generic unit of work.",
		Icon:            "✅",
		TitleFormat:     "",
		Body:            "## What needs to be done\n\n\n## Definition of Done\n\n- [ ] ",
		DefaultStatus:   "backlog",
		DefaultPriority: 3,
		DefaultLabels:   []string{},
	},
}
