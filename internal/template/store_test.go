package template

import (
	"context"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"

	"github.com/talyvor/track/internal/model"
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

func templateCols() *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"id", "workspace_id", "team_id", "name", "description", "icon",
		"title_format", "body", "default_status", "default_priority",
		"default_labels", "default_assignee", "field_defaults",
		"created_at", "updated_at",
	})
}

// ─── Create ─────────────────────────────────────────────────

func TestCreate_StoresTemplateCorrectly(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()

	pool.ExpectQuery(`INSERT INTO issue_templates`).
		WithArgs("ws-1", (*string)(nil), "Bug", "describe", "🐛",
			"[Bug] ", "## Body", "backlog", 2,
			[]string{"bug"}, (*string)(nil), []byte("{}")).
		WillReturnRows(templateCols().AddRow(
			"t-1", "ws-1", (*string)(nil), "Bug", "describe", "🐛",
			"[Bug] ", "## Body", "backlog", 2,
			[]string{"bug"}, (*string)(nil), []byte("{}"),
			now, now,
		))

	out, err := store.Create(context.Background(), IssueTemplate{
		WorkspaceID:     "ws-1",
		Name:            "Bug",
		Description:     "describe",
		Icon:            "🐛",
		TitleFormat:     "[Bug] ",
		Body:            "## Body",
		DefaultStatus:   "backlog",
		DefaultPriority: 2,
		DefaultLabels:   []string{"bug"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.Name != "Bug" || out.Icon != "🐛" {
		t.Errorf("got %+v", out)
	}
	if len(out.DefaultLabels) != 1 || out.DefaultLabels[0] != "bug" {
		t.Errorf("labels = %v", out.DefaultLabels)
	}
}

func TestCreate_RejectsEmptyName(t *testing.T) {
	store, _ := newMockStore(t)
	_, err := store.Create(context.Background(), IssueTemplate{
		WorkspaceID: "ws-1",
		Name:        "  ",
	})
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestCreate_TeamScoped_GuardsTeamRef(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	// Team-scoped template: the cross-object guard verifies the team is in
	// the template's workspace before the insert.
	pool.ExpectQuery(`SELECT EXISTS`).
		WithArgs("team-1", "ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	pool.ExpectQuery(`INSERT INTO issue_templates`).
		WithArgs("ws-1", ptr("team-1"), "Bug", "describe", "🐛",
			"[Bug] ", "## Body", "backlog", 2,
			[]string{"bug"}, (*string)(nil), []byte("{}")).
		WillReturnRows(templateCols().AddRow(
			"t-1", "ws-1", ptr("team-1"), "Bug", "describe", "🐛",
			"[Bug] ", "## Body", "backlog", 2,
			[]string{"bug"}, (*string)(nil), []byte("{}"),
			now, now,
		))

	out, err := store.Create(context.Background(), IssueTemplate{
		WorkspaceID:     "ws-1",
		TeamID:          ptr("team-1"),
		Name:            "Bug",
		Description:     "describe",
		Icon:            "🐛",
		TitleFormat:     "[Bug] ",
		Body:            "## Body",
		DefaultStatus:   "backlog",
		DefaultPriority: 2,
		DefaultLabels:   []string{"bug"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.TeamID == nil || *out.TeamID != "team-1" {
		t.Errorf("got %+v", out)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestCreate_TeamScoped_CrossWorkspaceTeamRejected(t *testing.T) {
	store, pool := newMockStore(t)
	// Team in another workspace → EXISTS false → reject before insert.
	pool.ExpectQuery(`SELECT EXISTS`).
		WithArgs("team-other", "ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))

	_, err := store.Create(context.Background(), IssueTemplate{
		WorkspaceID: "ws-1",
		TeamID:      ptr("team-other"),
		Name:        "Bug",
	})
	if err == nil {
		t.Fatal("expected cross-workspace team ref to be rejected")
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// ─── ApplyTemplate ──────────────────────────────────────────

func TestApplyTemplate_GuardsTemplateAgainstIssueWorkspace(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	// Guard runs first (template_id, into.WorkspaceID) → ok, then the
	// GetByID lookup, then ApplyTo fills the issue.
	pool.ExpectQuery(`SELECT EXISTS`).
		WithArgs("t-9", "ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	pool.ExpectQuery(`WHERE id = \$1`).
		WithArgs("t-9").
		WillReturnRows(templateCols().AddRow(
			"t-9", "ws-1", (*string)(nil), "Bug", "", "🐛",
			"[Bug] ", "## Body", "backlog", 2,
			[]string{"bug"}, (*string)(nil), []byte("{}"),
			now, now,
		))

	into := &model.Issue{WorkspaceID: "ws-1"}
	if err := store.ApplyTemplate(context.Background(), "t-9", into); err != nil {
		t.Fatalf("ApplyTemplate: %v", err)
	}
	if into.Title != "[Bug] " {
		t.Errorf("template not applied; title = %q", into.Title)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestApplyTemplate_CrossWorkspaceTemplateRejected(t *testing.T) {
	store, pool := newMockStore(t)
	// Template not in the issue's workspace → EXISTS false → reject, the
	// GetByID lookup must never run (no foreign template loaded).
	pool.ExpectQuery(`SELECT EXISTS`).
		WithArgs("t-foreign", "ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))

	into := &model.Issue{WorkspaceID: "ws-1"}
	if err := store.ApplyTemplate(context.Background(), "t-foreign", into); err == nil {
		t.Fatal("expected cross-workspace template to be rejected")
	}
	if into.Title != "" {
		t.Errorf("foreign template must not be applied; title = %q", into.Title)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// ─── List ───────────────────────────────────────────────────

func TestList_ReturnsWorkspaceAndTeamTemplates(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	pool.ExpectQuery(`FROM issue_templates`).
		WithArgs("ws-1", "team-1").
		WillReturnRows(templateCols().
			AddRow("t-1", "ws-1", (*string)(nil), "Bug", "", "🐛",
				"", "", "backlog", 2,
				[]string{"bug"}, (*string)(nil), []byte("{}"),
				now, now).
			AddRow("t-2", "ws-1", ptr("team-1"), "Custom", "", "📋",
				"", "", "backlog", 3,
				[]string{}, (*string)(nil), []byte("{}"),
				now, now))

	out, err := store.List(context.Background(), "ws-1", ptr("team-1"))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d, want 2", len(out))
	}
	if out[0].TeamID != nil {
		t.Error("first template should be workspace-wide")
	}
	if out[1].TeamID == nil || *out[1].TeamID != "team-1" {
		t.Error("second template should be team-scoped")
	}
}

func TestList_NoTeamID_WorkspaceWideOnly(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	pool.ExpectQuery(`team_id IS NULL`).
		WithArgs("ws-1").
		WillReturnRows(templateCols().
			AddRow("t-1", "ws-1", (*string)(nil), "Bug", "", "🐛",
				"", "", "backlog", 2,
				[]string{"bug"}, (*string)(nil), []byte("{}"),
				now, now))

	out, err := store.List(context.Background(), "ws-1", nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) != 1 {
		t.Errorf("got %d, want 1", len(out))
	}
}

// ─── GetByID ────────────────────────────────────────────────

func TestGetByID_ReturnsCorrectTemplate(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	pool.ExpectQuery(`WHERE id = \$1`).
		WithArgs("t-9").
		WillReturnRows(templateCols().AddRow(
			"t-9", "ws-1", (*string)(nil), "Bug", "", "🐛",
			"[Bug] ", "## Body", "backlog", 2,
			[]string{"bug"}, (*string)(nil), []byte("{\"f-1\":\"value\"}"),
			now, now,
		))

	out, err := store.GetByID(context.Background(), "t-9")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if out.ID != "t-9" || out.TitleFormat != "[Bug] " {
		t.Errorf("got %+v", out)
	}
	// field_defaults JSON should be parsed into the map.
	if v, ok := out.FieldDefaults["f-1"]; !ok || v != "value" {
		t.Errorf("FieldDefaults = %v", out.FieldDefaults)
	}
}

// ─── Update ─────────────────────────────────────────────────

func TestUpdate_ChangesFields(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	// Map iteration order isn't deterministic, so the SET-clause
	// args could land in either order. Match by arg count instead.
	pool.ExpectQuery(`UPDATE issue_templates`).
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(),
		).
		WillReturnRows(templateCols().AddRow(
			"t-1", "ws-1", (*string)(nil), "Renamed", "new desc", "🐛",
			"[Bug] ", "## Body", "backlog", 2,
			[]string{"bug"}, (*string)(nil), []byte("{}"),
			now, now,
		))

	out, err := store.Update(context.Background(), "t-1", "ws-1", map[string]any{
		"name":        "Renamed",
		"description": "new desc",
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if out.Name != "Renamed" || out.Description != "new desc" {
		t.Errorf("got %+v", out)
	}
}

// ─── Delete ─────────────────────────────────────────────────

func TestDelete_RemovesTemplate(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectExec(`DELETE FROM issue_templates`).
		WithArgs("t-1", "ws-1").
		WillReturnResult(pgxmock.NewResult("DELETE", 1))
	if err := store.Delete(context.Background(), "t-1", "ws-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

// ─── SeedDefaults ───────────────────────────────────────────

func TestSeedDefaults_CreatesFiveTemplates(t *testing.T) {
	store, pool := newMockStore(t)
	// 5 INSERT ... ON CONFLICT DO NOTHING calls — the order of names
	// doesn't matter, but the count does.
	for _, name := range []string{"Bug Report", "Feature Request", "Technical Debt", "Incident Report", "Task"} {
		pool.ExpectExec(`INSERT INTO issue_templates`).
			WithArgs("ws-1", name,
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("INSERT", 1))
	}

	if err := store.SeedDefaults(context.Background(), "ws-1"); err != nil {
		t.Fatalf("SeedDefaults: %v", err)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestSeedDefaults_Idempotent(t *testing.T) {
	store, pool := newMockStore(t)
	// All conflicts → INSERT returns 0 rows affected; the function
	// must NOT error out even though nothing changed. Each seed
	// passes 10 args (workspace_id + 9 template fields).
	for range 5 {
		pool.ExpectExec(`INSERT INTO issue_templates`).
			WithArgs(
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(),
			).
			WillReturnResult(pgxmock.NewResult("INSERT", 0))
	}
	if err := store.SeedDefaults(context.Background(), "ws-1"); err != nil {
		t.Fatalf("SeedDefaults: %v", err)
	}
}
