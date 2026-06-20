// Package customfield implements per-workspace and per-team
// extensions to the issue schema. Each field has a type that drives
// rendering and value validation; values are stored as text (JSON-
// encoded for multi-select) so the schema stays static no matter
// how many fields a workspace defines.
//
// The Store owns both the catalogue (custom_fields) and the values
// (issue_field_values) — keeping them together means the validation
// path that lives in SetValue can read the field's type without a
// cross-package call.
package customfield

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/talyvor/track/internal/tenancy"
)

// ─── types ──────────────────────────────────────────────────

type FieldType string

const (
	FieldTypeText     FieldType = "text"
	FieldTypeNumber   FieldType = "number"
	FieldTypeDate     FieldType = "date"
	FieldTypeSelect   FieldType = "select" // single option
	FieldTypeMulti    FieldType = "multi"  // multiple options
	FieldTypeURL      FieldType = "url"
	FieldTypeMember   FieldType = "member" // user reference
	FieldTypeCheckbox FieldType = "checkbox"
)

// MaxFieldsPerWorkspace caps the total number of custom fields per
// workspace. The limit keeps the per-issue join + the issue payload
// bounded; teams that need more should split into multiple workspaces.
const MaxFieldsPerWorkspace = 50

// MaxOptionsPerField caps select / multi option lists. Large option
// sets are usually a smell — a "country" picker belongs in its own
// member-style index, not a hard-coded list.
const MaxOptionsPerField = 100

type CustomField struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	TeamID      *string   `json:"team_id,omitempty"` // nil = workspace-wide
	Name        string    `json:"name"`
	Type        FieldType `json:"type"`
	Options     []string  `json:"options"`
	Required    bool      `json:"required"`
	Position    int       `json:"position"`
	CreatedAt   time.Time `json:"created_at"`
}

// FieldValue is the public shape returned by GetValue. The Store
// itself reads/writes via the (issue_id, field_id) → value map, so
// this struct is convenience-only for handler code that wants a
// stable JSON shape.
type FieldValue struct {
	IssueID   string    `json:"issue_id"`
	FieldID   string    `json:"field_id"`
	Value     string    `json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}

// pgxDB is the subset of *pgxpool.Pool the store uses, exposed as an
// interface so pgxmock can stand in for unit tests.
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

// ─── catalogue (custom_fields) ──────────────────────────────

const fieldColumns = `id, workspace_id, team_id, name, type, options, required, position, created_at`

func scanField(scanner interface {
	Scan(...any) error
}) (*CustomField, error) {
	var (
		f       CustomField
		typeStr string
	)
	if err := scanner.Scan(
		&f.ID, &f.WorkspaceID, &f.TeamID, &f.Name, &typeStr, &f.Options,
		&f.Required, &f.Position, &f.CreatedAt,
	); err != nil {
		return nil, err
	}
	f.Type = FieldType(typeStr)
	if f.Options == nil {
		f.Options = []string{}
	}
	return &f, nil
}

// validTypes is the closed set of FieldType values we accept. Lookup
// instead of switch so future tooling (admin UI, MCP discovery) can
// iterate the same source of truth.
var validTypes = map[FieldType]struct{}{
	FieldTypeText:     {},
	FieldTypeNumber:   {},
	FieldTypeDate:     {},
	FieldTypeSelect:   {},
	FieldTypeMulti:    {},
	FieldTypeURL:      {},
	FieldTypeMember:   {},
	FieldTypeCheckbox: {},
}

// CreateField validates the field metadata, checks the per-workspace
// cap, and inserts. The unique (workspace_id, name) constraint catches
// dupe names; callers see the pg error message back through the HTTP
// layer with a 400.
func (s *Store) CreateField(ctx context.Context, f CustomField) (*CustomField, error) {
	if s.pool == nil {
		return nil, errors.New("customfield: store has no pool")
	}
	if strings.TrimSpace(f.Name) == "" {
		return nil, errors.New("customfield: name required")
	}
	if _, ok := validTypes[f.Type]; !ok {
		return nil, fmt.Errorf("customfield: invalid type %q", f.Type)
	}
	if f.Type == FieldTypeSelect || f.Type == FieldTypeMulti {
		if len(f.Options) == 0 {
			return nil, fmt.Errorf("customfield: %s type requires options", f.Type)
		}
	}
	if len(f.Options) > MaxOptionsPerField {
		return nil, fmt.Errorf("customfield: options exceed max of %d", MaxOptionsPerField)
	}
	if f.Options == nil {
		f.Options = []string{}
	}
	f.Name = strings.TrimSpace(f.Name)

	// Cross-object tenancy: a team-scoped field must reference a team in
	// its own workspace. Workspace-wide fields (nil/empty team_id) skip the
	// guard — there is no reference to validate.
	if f.TeamID != nil && *f.TeamID != "" {
		if err := tenancy.AssertRefInWorkspace(ctx, s.pool, "teams", *f.TeamID, f.WorkspaceID); err != nil {
			return nil, err
		}
	}

	// Per-workspace cap. Reads + insert aren't in a transaction — a
	// simultaneous CreateField could squeak past, but the worst case
	// is 51 fields, which doesn't break anything. Adding SERIALIZABLE
	// for one cap check would dwarf the benefit.
	var count int64
	if err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM custom_fields WHERE workspace_id = $1`,
		f.WorkspaceID,
	).Scan(&count); err != nil {
		return nil, fmt.Errorf("customfield: count: %w", err)
	}
	if count >= MaxFieldsPerWorkspace {
		return nil, fmt.Errorf("customfield: workspace at field limit (max %d)", MaxFieldsPerWorkspace)
	}

	return scanField(s.pool.QueryRow(ctx,
		`INSERT INTO custom_fields (workspace_id, team_id, name, type, options, required, position)
        VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING `+fieldColumns,
		f.WorkspaceID, f.TeamID, f.Name, string(f.Type), f.Options, f.Required, f.Position,
	))
}

// ListFields returns both workspace-wide (team_id IS NULL) and
// team-scoped fields when teamID is non-nil. With teamID nil only
// the workspace-wide set is returned — handy for the workspace
// settings page where there's no team context.
func (s *Store) ListFields(ctx context.Context, workspaceID string, teamID *string) ([]CustomField, error) {
	if s.pool == nil {
		return nil, nil
	}
	var (
		rows pgx.Rows
		err  error
	)
	if teamID == nil {
		rows, err = s.pool.Query(ctx,
			`SELECT `+fieldColumns+` FROM custom_fields
            WHERE workspace_id = $1 AND team_id IS NULL
            ORDER BY position ASC, name ASC`,
			workspaceID,
		)
	} else {
		rows, err = s.pool.Query(ctx,
			`SELECT `+fieldColumns+` FROM custom_fields
            WHERE workspace_id = $1 AND (team_id IS NULL OR team_id = $2)
            ORDER BY position ASC, name ASC`,
			workspaceID, *teamID,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("customfield: list: %w", err)
	}
	defer rows.Close()

	var out []CustomField
	for rows.Next() {
		f, err := scanField(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

// GetField fetches a single field by ID. Returns pgx.ErrNoRows if
// the row is absent — callers can branch on errors.Is.
func (s *Store) GetField(ctx context.Context, id string) (*CustomField, error) {
	if s.pool == nil {
		return nil, errors.New("customfield: store has no pool")
	}
	return scanField(s.pool.QueryRow(ctx,
		`SELECT `+fieldColumns+` FROM custom_fields WHERE id = $1`,
		id,
	))
}

// UpdateField patches the editable fields. Type is intentionally not
// editable — switching from "text" to "select" would invalidate every
// stored value. Workflows that need a type change should create a new
// field and re-import values.
func (s *Store) UpdateField(ctx context.Context, id, name string, options []string, required bool) (*CustomField, error) {
	if s.pool == nil {
		return nil, errors.New("customfield: store has no pool")
	}
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("customfield: name required")
	}
	if len(options) > MaxOptionsPerField {
		return nil, fmt.Errorf("customfield: options exceed max of %d", MaxOptionsPerField)
	}
	if options == nil {
		options = []string{}
	}
	return scanField(s.pool.QueryRow(ctx,
		`UPDATE custom_fields SET name = $1, options = $2, required = $3
        WHERE id = $4 RETURNING `+fieldColumns,
		strings.TrimSpace(name), options, required, id,
	))
}

// DeleteField removes the field and every value attached to it.
// The DELETE on issue_field_values runs first so an abort half-way
// can never leave orphan values pointing at a missing field.
func (s *Store) DeleteField(ctx context.Context, id string) error {
	if s.pool == nil {
		return errors.New("customfield: store has no pool")
	}
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM issue_field_values WHERE field_id = $1`, id,
	); err != nil {
		return fmt.Errorf("customfield: delete values: %w", err)
	}
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM custom_fields WHERE id = $1`, id,
	); err != nil {
		return fmt.Errorf("customfield: delete field: %w", err)
	}
	return nil
}

// ─── value storage ──────────────────────────────────────────

// SetValue validates the incoming value against the field's type
// then UPSERTs. Validation runs server-side so a misbehaving client
// (or someone hand-crafting a curl) can't bypass the per-type rules.
func (s *Store) SetValue(ctx context.Context, issueID, fieldID, value string) error {
	if s.pool == nil {
		return errors.New("customfield: store has no pool")
	}
	field, err := s.GetField(ctx, fieldID)
	if err != nil {
		return fmt.Errorf("customfield: lookup field: %w", err)
	}
	if err := validateValue(field, value); err != nil {
		return err
	}
	// Object-graph integrity: the field and the target issue must belong to the
	// same workspace — a field from one workspace must not be settable on another
	// workspace's issue by bare ID.
	var issueWS string
	if err := s.pool.QueryRow(ctx, `SELECT workspace_id FROM issues WHERE id = $1`, issueID).Scan(&issueWS); err != nil {
		return fmt.Errorf("customfield: lookup issue: %w", err)
	}
	if issueWS != field.WorkspaceID {
		return errors.New("customfield: field and issue belong to different workspaces")
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO issue_field_values (issue_id, field_id, value)
        VALUES ($1, $2, $3)
        ON CONFLICT (issue_id, field_id)
        DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()`,
		issueID, fieldID, value,
	)
	if err != nil {
		return fmt.Errorf("customfield: upsert value: %w", err)
	}
	return nil
}

// validateValue checks the candidate against the field's type rules.
// Empty values are allowed regardless of type so the UI can clear a
// field — Required enforcement lives in ValidateRequired (called at
// issue-create time), not here.
func validateValue(f *CustomField, value string) error {
	if value == "" {
		return nil
	}
	switch f.Type {
	case FieldTypeText, FieldTypeMember:
		// Free text — nothing to validate.
	case FieldTypeNumber:
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			return fmt.Errorf("customfield %s: value %q is not a number", f.Name, value)
		}
	case FieldTypeDate:
		if _, err := time.Parse(time.RFC3339, value); err != nil {
			return fmt.Errorf("customfield %s: value %q is not RFC3339", f.Name, value)
		}
	case FieldTypeURL:
		u, err := url.Parse(value)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("customfield %s: value must be an http(s) URL", f.Name)
		}
	case FieldTypeCheckbox:
		if value != "true" && value != "false" {
			return fmt.Errorf("customfield %s: value must be 'true' or 'false'", f.Name)
		}
	case FieldTypeSelect:
		if !contains(f.Options, value) {
			return fmt.Errorf("customfield %s: value %q is not in the option set", f.Name, value)
		}
	case FieldTypeMulti:
		var picks []string
		if err := json.Unmarshal([]byte(value), &picks); err != nil {
			return fmt.Errorf("customfield %s: multi value must be a JSON array of strings", f.Name)
		}
		for _, p := range picks {
			if !contains(f.Options, p) {
				return fmt.Errorf("customfield %s: value %q is not in the option set", f.Name, p)
			}
		}
	default:
		return fmt.Errorf("customfield %s: unknown type %q", f.Name, f.Type)
	}
	return nil
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// GetValues returns the field-id → value map for one issue. Empty
// map (not nil) is returned for issues with no values so JSON encodes
// `{}` rather than `null`.
func (s *Store) GetValues(ctx context.Context, issueID string) (map[string]string, error) {
	out := map[string]string{}
	if s.pool == nil {
		return out, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT field_id, value FROM issue_field_values WHERE issue_id = $1`,
		issueID,
	)
	if err != nil {
		return nil, fmt.Errorf("customfield: get values: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var fieldID, value string
		if err := rows.Scan(&fieldID, &value); err != nil {
			return nil, err
		}
		out[fieldID] = value
	}
	return out, rows.Err()
}

// GetValuesBulk reads values for many issues in a single query — the
// list view's escape hatch from N+1. Returns issueID → fieldID →
// value; issues with no values are absent from the outer map.
func (s *Store) GetValuesBulk(ctx context.Context, issueIDs []string) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	if s.pool == nil || len(issueIDs) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT issue_id, field_id, value FROM issue_field_values WHERE issue_id = ANY($1)`,
		issueIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("customfield: bulk values: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var issueID, fieldID, value string
		if err := rows.Scan(&issueID, &fieldID, &value); err != nil {
			return nil, err
		}
		if out[issueID] == nil {
			out[issueID] = map[string]string{}
		}
		out[issueID][fieldID] = value
	}
	return out, rows.Err()
}

// ValidateRequired checks the incoming value map against every
// required field for the (workspace, team). Returns an error naming
// the first missing field so the API can surface it to the user
// without guessing. Empty / whitespace values count as missing.
func (s *Store) ValidateRequired(ctx context.Context, workspaceID string, teamID *string, provided map[string]string) error {
	fields, err := s.ListFields(ctx, workspaceID, teamID)
	if err != nil {
		return err
	}
	for _, f := range fields {
		if !f.Required {
			continue
		}
		if v, ok := provided[f.ID]; !ok || strings.TrimSpace(v) == "" {
			return fmt.Errorf("custom field %q is required", f.Name)
		}
	}
	return nil
}
