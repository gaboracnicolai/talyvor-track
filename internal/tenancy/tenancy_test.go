package tenancy_test

import (
	"context"
	"errors"
	"testing"

	"github.com/talyvor/track/internal/tenancy"
	"github.com/talyvor/track/internal/testutil"
)

// TestAssertRefInWorkspace exercises the shared guard primitive on real Postgres:
// a same-workspace ref passes; a ref in another workspace, an absent ref, and an
// unregistered table are all refused (the first two via ErrCrossWorkspace).
func TestAssertRefInWorkspace(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()

	wsA := d.Workspace(t)
	wsB := d.Workspace(t)
	issueA := d.Issue(t, wsA.ID, "")

	// ref in the same workspace → ok
	if err := tenancy.AssertRefInWorkspace(ctx, d.Pool, "issues", issueA.ID, wsA.ID); err != nil {
		t.Errorf("same-workspace ref must pass: %v", err)
	}
	// ref in another workspace → ErrCrossWorkspace
	if err := tenancy.AssertRefInWorkspace(ctx, d.Pool, "issues", issueA.ID, wsB.ID); !errors.Is(err, tenancy.ErrCrossWorkspace) {
		t.Errorf("cross-workspace ref must be refused with ErrCrossWorkspace, got %v", err)
	}
	// absent ref → ErrCrossWorkspace
	if err := tenancy.AssertRefInWorkspace(ctx, d.Pool, "issues", "no-such-id", wsA.ID); !errors.Is(err, tenancy.ErrCrossWorkspace) {
		t.Errorf("absent ref must be refused with ErrCrossWorkspace, got %v", err)
	}
	// unregistered table → a programmer error, NOT ErrCrossWorkspace
	if err := tenancy.AssertRefInWorkspace(ctx, d.Pool, "not_a_table", issueA.ID, wsA.ID); err == nil || errors.Is(err, tenancy.ErrCrossWorkspace) {
		t.Errorf("unregistered table must error (not ErrCrossWorkspace), got %v", err)
	}
}
