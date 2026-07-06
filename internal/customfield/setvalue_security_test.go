package customfield_test

import (
	"context"
	"testing"

	"github.com/talyvor/track/internal/customfield"
	"github.com/talyvor/track/internal/testutil"
)

// TestSetValue_ObjectGraph_RejectsCrossWorkspace — setting a custom-field value
// must refuse when the field and the target issue belong to different workspaces
// (object-graph integrity), while a same-workspace set still works. Real Postgres
// via the harness; the field is seeded in workspace A, the issue in workspace B.
func TestSetValue_ObjectGraph_RejectsCrossWorkspace(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	cf := customfield.NewStore(d.Pool)

	wsA := d.Workspace(t)
	wsB := d.Workspace(t)
	fieldA := d.CustomField(t, wsA.ID, "Severity") // field in workspace A
	issueA := d.Issue(t, wsA.ID, "")               // issue in workspace A
	issueB := d.Issue(t, wsB.ID, "")               // issue in workspace B

	// Positive control: a field set on a same-workspace issue, by the owning workspace, works.
	if err := cf.SetValue(ctx, issueA.ID, fieldA.ID, wsA.ID, "high"); err != nil {
		t.Fatalf("same-workspace SetValue must succeed: %v", err)
	}

	// Object-graph: field A must NOT be settable on an issue in workspace B (field/issue mismatch),
	// even by a member authorized for B.
	if err := cf.SetValue(ctx, issueB.ID, fieldA.ID, wsB.ID, "high"); err == nil {
		t.Error("LEAK: a field from workspace A was set on an issue in workspace B")
	}

	// SEC-5 caller-scope: a member authorized for B must NOT set a value on A's issue with A's own
	// field — the target issue is not in the caller's authorized workspace.
	if err := cf.SetValue(ctx, issueA.ID, fieldA.ID, wsB.ID, "high"); err == nil {
		t.Error("LEAK: a member of workspace B set a field value on workspace A's issue")
	}
}
