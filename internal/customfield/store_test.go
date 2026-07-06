package customfield

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

func newMockStore(t *testing.T) (*Store, pgxmock.PgxPoolIface) {
	t.Helper()
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	t.Cleanup(pool.Close)
	return newStore(pool), pool
}

func ptr[T any](v T) *T { return &v }

// ─── CreateField ────────────────────────────────────────────

func TestCreateField_TextType(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	pool.ExpectQuery(`SELECT COUNT\(\*\) FROM custom_fields`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(2)))
	pool.ExpectQuery(`INSERT INTO custom_fields`).
		WithArgs("ws-1", (*string)(nil), "Customer", "text", []string{}, false, 0).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "workspace_id", "team_id", "name", "type", "options", "required", "position", "created_at",
		}).AddRow("f-1", "ws-1", (*string)(nil), "Customer", "text", []string{}, false, 0, now))

	out, err := store.CreateField(context.Background(), CustomField{
		WorkspaceID: "ws-1",
		Name:        "Customer",
		Type:        FieldTypeText,
	})
	if err != nil {
		t.Fatalf("CreateField: %v", err)
	}
	if out.ID != "f-1" || out.Type != FieldTypeText {
		t.Errorf("got %+v", out)
	}
}

func TestCreateField_TeamScoped_GuardsTeamRef(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	// Team-scoped field: the cross-object guard must verify the team is in
	// the field's workspace BEFORE the count/insert.
	pool.ExpectQuery(`SELECT EXISTS`).
		WithArgs("team-1", "ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	pool.ExpectQuery(`SELECT COUNT\(\*\) FROM custom_fields`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(0)))
	pool.ExpectQuery(`INSERT INTO custom_fields`).
		WithArgs("ws-1", ptr("team-1"), "TeamField", "text", []string{}, false, 0).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "workspace_id", "team_id", "name", "type", "options", "required", "position", "created_at",
		}).AddRow("f-1", "ws-1", ptr("team-1"), "TeamField", "text", []string{}, false, 0, now))

	out, err := store.CreateField(context.Background(), CustomField{
		WorkspaceID: "ws-1",
		TeamID:      ptr("team-1"),
		Name:        "TeamField",
		Type:        FieldTypeText,
	})
	if err != nil {
		t.Fatalf("CreateField: %v", err)
	}
	if out.TeamID == nil || *out.TeamID != "team-1" {
		t.Errorf("got %+v", out)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestCreateField_TeamScoped_CrossWorkspaceTeamRejected(t *testing.T) {
	store, pool := newMockStore(t)
	// Guard sees the team in another workspace → EXISTS false → reject, no
	// count or insert should run.
	pool.ExpectQuery(`SELECT EXISTS`).
		WithArgs("team-other", "ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))

	_, err := store.CreateField(context.Background(), CustomField{
		WorkspaceID: "ws-1",
		TeamID:      ptr("team-other"),
		Name:        "TeamField",
		Type:        FieldTypeText,
	})
	if err == nil {
		t.Fatal("expected cross-workspace team ref to be rejected")
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestCreateField_SelectTypeRequiresOptions(t *testing.T) {
	store, _ := newMockStore(t)
	_, err := store.CreateField(context.Background(), CustomField{
		WorkspaceID: "ws-1",
		Name:        "Stage",
		Type:        FieldTypeSelect,
		Options:     nil,
	})
	if err == nil {
		t.Fatal("expected error for select with no options")
	}
}

func TestCreateField_SelectTypeWithOptions(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	pool.ExpectQuery(`SELECT COUNT\(\*\) FROM custom_fields`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(0)))
	pool.ExpectQuery(`INSERT INTO custom_fields`).
		WithArgs("ws-1", (*string)(nil), "Stage", "select",
			[]string{"discovery", "evaluation", "negotiation", "won", "lost"},
			true, 5).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "workspace_id", "team_id", "name", "type", "options", "required", "position", "created_at",
		}).AddRow("f-2", "ws-1", (*string)(nil), "Stage", "select",
			[]string{"discovery", "evaluation", "negotiation", "won", "lost"},
			true, 5, now))

	out, err := store.CreateField(context.Background(), CustomField{
		WorkspaceID: "ws-1",
		Name:        "Stage",
		Type:        FieldTypeSelect,
		Options:     []string{"discovery", "evaluation", "negotiation", "won", "lost"},
		Required:    true,
		Position:    5,
	})
	if err != nil {
		t.Fatalf("CreateField: %v", err)
	}
	if len(out.Options) != 5 || out.Required != true {
		t.Errorf("got %+v", out)
	}
}

func TestCreateField_RejectsInvalidType(t *testing.T) {
	store, _ := newMockStore(t)
	_, err := store.CreateField(context.Background(), CustomField{
		WorkspaceID: "ws-1",
		Name:        "Bad",
		Type:        FieldType("haxxor; DROP TABLE"),
	})
	if err == nil {
		t.Fatal("expected error for invalid type")
	}
}

func TestCreateField_RejectsEmptyName(t *testing.T) {
	store, _ := newMockStore(t)
	_, err := store.CreateField(context.Background(), CustomField{
		WorkspaceID: "ws-1",
		Name:        "  ",
		Type:        FieldTypeText,
	})
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestCreateField_EnforcesPerWorkspaceCap(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`SELECT COUNT\(\*\) FROM custom_fields`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(MaxFieldsPerWorkspace)))

	_, err := store.CreateField(context.Background(), CustomField{
		WorkspaceID: "ws-1",
		Name:        "FiftyFirst",
		Type:        FieldTypeText,
	})
	if err == nil {
		t.Fatal("expected error when at workspace field cap")
	}
	if !strings.Contains(err.Error(), "limit") && !strings.Contains(err.Error(), "max") {
		t.Errorf("error should mention the limit; got %v", err)
	}
}

func TestCreateField_RejectsTooManyOptions(t *testing.T) {
	store, _ := newMockStore(t)
	opts := make([]string, MaxOptionsPerField+1)
	for i := range opts {
		opts[i] = "opt"
	}
	_, err := store.CreateField(context.Background(), CustomField{
		WorkspaceID: "ws-1",
		Name:        "Huge",
		Type:        FieldTypeSelect,
		Options:     opts,
	})
	if err == nil {
		t.Fatalf("expected error for %d options", MaxOptionsPerField+1)
	}
}

// ─── ListFields ─────────────────────────────────────────────

func TestListFields_ReturnsWorkspaceAndTeamFields(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	pool.ExpectQuery(`FROM custom_fields`).
		WithArgs("ws-1", "team-1").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "workspace_id", "team_id", "name", "type", "options", "required", "position", "created_at",
		}).
			AddRow("f-1", "ws-1", (*string)(nil), "Customer", "text", []string{}, false, 0, now).
			AddRow("f-2", "ws-1", ptr("team-1"), "Stage", "select",
				[]string{"a", "b"}, true, 1, now))

	out, err := store.ListFields(context.Background(), "ws-1", ptr("team-1"))
	if err != nil {
		t.Fatalf("ListFields: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d, want 2", len(out))
	}
	if out[0].TeamID != nil {
		t.Errorf("first field expected workspace-wide (nil team)")
	}
	if out[1].TeamID == nil || *out[1].TeamID != "team-1" {
		t.Errorf("second field expected team-scoped")
	}
}

func TestListFields_NoTeamID_ReturnsWorkspaceWideOnly(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	// When teamID is nil the store should query with team_id IS NULL
	// (so the args list is just the workspace ID).
	pool.ExpectQuery(`team_id IS NULL`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "workspace_id", "team_id", "name", "type", "options", "required", "position", "created_at",
		}).
			AddRow("f-1", "ws-1", (*string)(nil), "Customer", "text", []string{}, false, 0, now))

	out, err := store.ListFields(context.Background(), "ws-1", nil)
	if err != nil {
		t.Fatalf("ListFields: %v", err)
	}
	if len(out) != 1 {
		t.Errorf("got %d fields, want 1", len(out))
	}
}

// ─── UpdateField ────────────────────────────────────────────

func TestUpdateField_UpdatesNameAndOptions(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	// SEC-5: UpdateField asserts the field is in the workspace first.
	pool.ExpectQuery(`SELECT EXISTS`).
		WithArgs("f-1", "ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	pool.ExpectQuery(`UPDATE custom_fields`).
		WithArgs("Customer Renamed", []string{"a", "b", "c"}, false, "f-1", "ws-1").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "workspace_id", "team_id", "name", "type", "options", "required", "position", "created_at",
		}).AddRow("f-1", "ws-1", (*string)(nil), "Customer Renamed", "select",
			[]string{"a", "b", "c"}, false, 0, now))

	out, err := store.UpdateField(context.Background(), "f-1", "ws-1", "Customer Renamed", []string{"a", "b", "c"}, false)
	if err != nil {
		t.Fatalf("UpdateField: %v", err)
	}
	if out.Name != "Customer Renamed" || len(out.Options) != 3 {
		t.Errorf("got %+v", out)
	}
}

// ─── DeleteField ────────────────────────────────────────────

func TestDeleteField_RemovesField(t *testing.T) {
	store, pool := newMockStore(t)
	// SEC-5: DeleteField asserts the field is in the workspace before touching anything.
	pool.ExpectQuery(`SELECT EXISTS`).
		WithArgs("f-1", "ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	// DELETE values first so referencing constraint won't fight us.
	pool.ExpectExec(`DELETE FROM issue_field_values WHERE field_id`).
		WithArgs("f-1").
		WillReturnResult(pgxmock.NewResult("DELETE", 4))
	pool.ExpectExec(`DELETE FROM custom_fields`).
		WithArgs("f-1", "ws-1").
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	if err := store.DeleteField(context.Background(), "f-1", "ws-1"); err != nil {
		t.Fatalf("DeleteField: %v", err)
	}
}

// ─── SetValue validation ────────────────────────────────────

func TestSetValue_ValidatesNumber(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`SELECT id, workspace_id, team_id, name, type, options`).
		WithArgs("f-num").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "workspace_id", "team_id", "name", "type", "options", "required", "position", "created_at",
		}).AddRow("f-num", "ws-1", (*string)(nil), "Amount", "number", []string{}, false, 0, time.Now()))

	if err := store.SetValue(context.Background(), "i-1", "f-num", "not-a-number"); err == nil {
		t.Fatal("expected validation error for non-numeric value")
	}
}

func TestSetValue_ValidatesSelectOption(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`SELECT id, workspace_id, team_id, name, type, options`).
		WithArgs("f-sel").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "workspace_id", "team_id", "name", "type", "options", "required", "position", "created_at",
		}).AddRow("f-sel", "ws-1", (*string)(nil), "Stage", "select",
			[]string{"discovery", "evaluation"}, false, 0, time.Now()))

	if err := store.SetValue(context.Background(), "i-1", "f-sel", "negotiation"); err == nil {
		t.Fatal("expected error for value outside option set")
	}
}

func TestSetValue_AcceptsValidSelectOption(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`SELECT id, workspace_id, team_id, name, type, options`).
		WithArgs("f-sel").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "workspace_id", "team_id", "name", "type", "options", "required", "position", "created_at",
		}).AddRow("f-sel", "ws-1", (*string)(nil), "Stage", "select",
			[]string{"discovery", "evaluation"}, false, 0, time.Now()))
	pool.ExpectQuery(`SELECT workspace_id FROM issues`).
		WithArgs("i-1").
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id"}).AddRow("ws-1"))
	pool.ExpectExec(`INSERT INTO issue_field_values`).
		WithArgs("i-1", "f-sel", "evaluation").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	if err := store.SetValue(context.Background(), "i-1", "f-sel", "evaluation"); err != nil {
		t.Errorf("SetValue: %v", err)
	}
}

func TestSetValue_ValidatesMultiSelectJSON(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`SELECT id, workspace_id, team_id, name, type, options`).
		WithArgs("f-multi").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "workspace_id", "team_id", "name", "type", "options", "required", "position", "created_at",
		}).AddRow("f-multi", "ws-1", (*string)(nil), "Tags", "multi",
			[]string{"red", "green", "blue"}, false, 0, time.Now()))

	if err := store.SetValue(context.Background(), "i-1", "f-multi", `["red","yellow"]`); err == nil {
		t.Fatal("expected error: yellow not in option set")
	}
}

func TestSetValue_ValidatesURL(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`SELECT id, workspace_id, team_id, name, type, options`).
		WithArgs("f-url").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "workspace_id", "team_id", "name", "type", "options", "required", "position", "created_at",
		}).AddRow("f-url", "ws-1", (*string)(nil), "Doc", "url", []string{}, false, 0, time.Now()))

	if err := store.SetValue(context.Background(), "i-1", "f-url", "javascript:alert(1)"); err == nil {
		t.Fatal("expected error for non-http(s) URL")
	}
}

func TestSetValue_ValidatesDate(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`SELECT id, workspace_id, team_id, name, type, options`).
		WithArgs("f-date").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "workspace_id", "team_id", "name", "type", "options", "required", "position", "created_at",
		}).AddRow("f-date", "ws-1", (*string)(nil), "Due", "date", []string{}, false, 0, time.Now()))

	if err := store.SetValue(context.Background(), "i-1", "f-date", "next Tuesday"); err == nil {
		t.Fatal("expected error for non-RFC3339 date")
	}
}

func TestSetValue_ValidatesCheckbox(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`SELECT id, workspace_id, team_id, name, type, options`).
		WithArgs("f-chk").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "workspace_id", "team_id", "name", "type", "options", "required", "position", "created_at",
		}).AddRow("f-chk", "ws-1", (*string)(nil), "Done", "checkbox", []string{}, false, 0, time.Now()))

	if err := store.SetValue(context.Background(), "i-1", "f-chk", "maybe"); err == nil {
		t.Fatal("expected error: checkbox must be true/false")
	}
}

func TestSetValue_UpsertsCorrectly(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`SELECT id, workspace_id, team_id, name, type, options`).
		WithArgs("f-txt").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "workspace_id", "team_id", "name", "type", "options", "required", "position", "created_at",
		}).AddRow("f-txt", "ws-1", (*string)(nil), "Customer", "text", []string{}, false, 0, time.Now()))
	pool.ExpectQuery(`SELECT workspace_id FROM issues`).
		WithArgs("i-1").
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id"}).AddRow("ws-1"))
	// ON CONFLICT update path means the UPSERT runs even when a row
	// already exists; the test only needs to verify the query fires
	// with the right args.
	pool.ExpectExec(`INSERT INTO issue_field_values`).
		WithArgs("i-1", "f-txt", "Acme Corp").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	if err := store.SetValue(context.Background(), "i-1", "f-txt", "Acme Corp"); err != nil {
		t.Errorf("SetValue: %v", err)
	}
}

// ─── GetValues / Bulk ───────────────────────────────────────

func TestGetValues_ReturnsFieldMap(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`SELECT field_id, value FROM issue_field_values`).
		WithArgs("i-1").
		WillReturnRows(pgxmock.NewRows([]string{"field_id", "value"}).
			AddRow("f-1", "Acme Corp").
			AddRow("f-2", "evaluation"))

	out, err := store.GetValues(context.Background(), "i-1")
	if err != nil {
		t.Fatalf("GetValues: %v", err)
	}
	if out["f-1"] != "Acme Corp" || out["f-2"] != "evaluation" {
		t.Errorf("values = %v", out)
	}
}

func TestGetValuesBulk_ReturnsNestedMap(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`SELECT issue_id, field_id, value FROM issue_field_values`).
		WithArgs([]string{"i-1", "i-2"}).
		WillReturnRows(pgxmock.NewRows([]string{"issue_id", "field_id", "value"}).
			AddRow("i-1", "f-1", "Acme Corp").
			AddRow("i-1", "f-2", "evaluation").
			AddRow("i-2", "f-1", "Globex"))

	out, err := store.GetValuesBulk(context.Background(), []string{"i-1", "i-2"})
	if err != nil {
		t.Fatalf("GetValuesBulk: %v", err)
	}
	if out["i-1"]["f-2"] != "evaluation" {
		t.Errorf("i-1.f-2 = %v", out["i-1"]["f-2"])
	}
	if out["i-2"]["f-1"] != "Globex" {
		t.Errorf("i-2.f-1 = %v", out["i-2"]["f-1"])
	}
	if _, exists := out["i-2"]["f-2"]; exists {
		t.Error("i-2 should not have f-2")
	}
}

func TestGetValuesBulk_EmptyInput(t *testing.T) {
	store, _ := newMockStore(t)
	out, err := store.GetValuesBulk(context.Background(), nil)
	if err != nil {
		t.Fatalf("GetValuesBulk(nil): %v", err)
	}
	if len(out) != 0 {
		t.Errorf("got %v, want empty map", out)
	}
}

// ─── Required-field validation ──────────────────────────────

func TestValidateRequired_ReturnsErrorWhenRequiredMissing(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	pool.ExpectQuery(`FROM custom_fields`).
		WithArgs("ws-1", "team-1").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "workspace_id", "team_id", "name", "type", "options", "required", "position", "created_at",
		}).
			AddRow("f-1", "ws-1", (*string)(nil), "Customer", "text", []string{}, true, 0, now).
			AddRow("f-2", "ws-1", ptr("team-1"), "Stage", "select",
				[]string{"a", "b"}, false, 1, now))

	err := store.ValidateRequired(context.Background(), "ws-1", ptr("team-1"), map[string]string{})
	if err == nil {
		t.Fatal("expected error for missing required field")
	}
	if !strings.Contains(err.Error(), "Customer") {
		t.Errorf("error should name the missing field; got %v", err)
	}
}

func TestValidateRequired_PassesWhenAllRequiredPresent(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	pool.ExpectQuery(`FROM custom_fields`).
		WithArgs("ws-1", "team-1").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "workspace_id", "team_id", "name", "type", "options", "required", "position", "created_at",
		}).
			AddRow("f-1", "ws-1", (*string)(nil), "Customer", "text", []string{}, true, 0, now))

	err := store.ValidateRequired(context.Background(), "ws-1", ptr("team-1"),
		map[string]string{"f-1": "Acme Corp"})
	if err != nil {
		t.Errorf("ValidateRequired: %v", err)
	}
}
