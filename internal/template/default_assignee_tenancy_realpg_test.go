package template_test

import (
	"context"
	"errors"
	"testing"

	"github.com/talyvor/track/internal/template"
	"github.com/talyvor/track/internal/tenancy"
	"github.com/talyvor/track/internal/testutil"
)

// TestTemplateDefaultAssignee_IsWorkspaceScoped pins the tenancy rule for
// issue_templates.default_assignee on BOTH write paths.
//
// The column is `TEXT REFERENCES members(id) ON DELETE SET NULL` (0013_templates.sql:21)
// and members are workspace-scoped, so the foreign key proves the member EXISTS and says
// nothing about WHOSE it is. It is the second cross-object reference on the template
// INSERT — team_id is the first, and team_id has been guarded since the object-graph work.
//
// MEASURED against real Postgres before the guard existed: a template in workspace B
// accepted a member of workspace A on Create AND on Update, and `ApplyTemplate` then
// injected that foreign member into a workspace-B issue. issue.Store.Create refuses it
// there (assertRefInWorkspace), so the edge failed CLOSED at the single point of use —
// but the edge was stored, and the whole defence sat in another package. This test is
// what makes the reference's own tenancy rule falsifiable where the write happens.
//
// ⚠ EVERY REFUSAL IS ASSERTED BY ERROR IDENTITY (errors.Is ... ErrCrossWorkspace), NOT BY
// "an error came back". The probe that found this defect first reported REFUSED on a call
// that never reached the guard at all — the error was a missing-required-field complaint
// from three layers away, and "some error" read as "the guard works". A tenancy test that
// accepts any error passes just as well when the guard is deleted and something else
// happens to fail.
//
// ⚠ EVERY SUBTEST HAS AN ANTI-VACUITY HALF asserting the SAME-workspace reference is
// ACCEPTED and stored. Without it a guard that refused every assignee — including the
// legitimate ones — would pass every assertion here.
func TestTemplateDefaultAssignee_IsWorkspaceScoped(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	wsA := d.Workspace(t)
	wsB := d.Workspace(t)

	member := func(t *testing.T, wsID, email string) string {
		t.Helper()
		var id string
		if err := d.Pool.QueryRow(ctx,
			`INSERT INTO members (workspace_id, name, email) VALUES ($1,'M',$2) RETURNING id`,
			wsID, email).Scan(&id); err != nil {
			t.Fatalf("seed member in %s: %v", wsID, err)
		}
		return id
	}

	foreign := member(t, wsA.ID, "foreign@a.com")
	local := member(t, wsB.ID, "local@b.com")
	s := template.NewStore(d.Pool)

	// assigneeOf reads the column straight from Postgres rather than from the store's
	// RETURNING row: the question is what was STORED, and a store that returns a stale
	// struct must not be the witness to its own write.
	assigneeOf := func(t *testing.T, id string) *string {
		t.Helper()
		var got *string
		if err := d.Pool.QueryRow(ctx,
			`SELECT default_assignee FROM issue_templates WHERE id = $1`, id).Scan(&got); err != nil {
			t.Fatalf("read back default_assignee: %v", err)
		}
		return got
	}

	t.Run("Create refuses a member of another workspace", func(t *testing.T) {
		_, err := s.Create(ctx, template.IssueTemplate{
			WorkspaceID: wsB.ID, Name: "cross-create", DefaultAssignee: &foreign,
		})
		if !errors.Is(err, tenancy.ErrCrossWorkspace) {
			t.Errorf("want ErrCrossWorkspace, got %v", err)
		}
	})

	t.Run("Create accepts a member of its own workspace", func(t *testing.T) {
		got, err := s.Create(ctx, template.IssueTemplate{
			WorkspaceID: wsB.ID, Name: "local-create", DefaultAssignee: &local,
		})
		if err != nil {
			t.Fatalf("same-workspace assignee must be accepted: %v", err)
		}
		if stored := assigneeOf(t, got.ID); stored == nil || *stored != local {
			t.Errorf("stored default_assignee = %v, want %s", stored, local)
		}
	})

	t.Run("Create accepts no assignee at all", func(t *testing.T) {
		got, err := s.Create(ctx, template.IssueTemplate{WorkspaceID: wsB.ID, Name: "no-assignee"})
		if err != nil {
			t.Fatalf("a template with no default assignee must be accepted: %v", err)
		}
		if stored := assigneeOf(t, got.ID); stored != nil && *stored != "" {
			t.Errorf("stored default_assignee = %v, want nil", *stored)
		}
	})

	t.Run("Update refuses a member of another workspace and writes nothing", func(t *testing.T) {
		base, err := s.Create(ctx, template.IssueTemplate{
			WorkspaceID: wsB.ID, Name: "cross-update", DefaultAssignee: &local,
		})
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
		_, err = s.Update(ctx, base.ID, wsB.ID, map[string]any{"default_assignee": foreign})
		if !errors.Is(err, tenancy.ErrCrossWorkspace) {
			t.Errorf("want ErrCrossWorkspace, got %v", err)
		}
		// The refusal must be TOTAL. A guard that returns an error after the UPDATE has
		// already run would satisfy the assertion above and still have moved the row.
		if stored := assigneeOf(t, base.ID); stored == nil || *stored != local {
			t.Errorf("refused update still changed the row: default_assignee = %v, want %s", stored, local)
		}
	})

	t.Run("Update accepts a member of its own workspace", func(t *testing.T) {
		base, err := s.Create(ctx, template.IssueTemplate{WorkspaceID: wsB.ID, Name: "local-update"})
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
		if _, err := s.Update(ctx, base.ID, wsB.ID, map[string]any{"default_assignee": local}); err != nil {
			t.Fatalf("same-workspace assignee must be accepted: %v", err)
		}
		if stored := assigneeOf(t, base.ID); stored == nil || *stored != local {
			t.Errorf("stored default_assignee = %v, want %s", stored, local)
		}
	})

	t.Run("Update can still clear the assignee", func(t *testing.T) {
		base, err := s.Create(ctx, template.IssueTemplate{
			WorkspaceID: wsB.ID, Name: "clear-update", DefaultAssignee: &local,
		})
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
		// A JSON null decodes to a nil any. Clearing is not a reference and must not be
		// refused — the guard must scope the reference, not forbid removing it.
		if _, err := s.Update(ctx, base.ID, wsB.ID, map[string]any{"default_assignee": nil}); err != nil {
			t.Fatalf("clearing default_assignee must be accepted: %v", err)
		}
		if stored := assigneeOf(t, base.ID); stored != nil {
			t.Errorf("default_assignee = %v after an explicit clear, want nil", *stored)
		}
	})
}
