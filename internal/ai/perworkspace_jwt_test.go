package ai

import (
	"context"
	"testing"

	"github.com/talyvor/track/internal/lensintegration"
	"github.com/talyvor/track/internal/model"
)

// Property 2: a data-path call for workspace W must carry a per-workspace
// JWT claiming W — NEVER the raw shared/admin key. Riding the shared key
// is exactly what collapses every tenant into Lens's empty-workspace
// bucket and the "default" spend attribution.
func TestDataPath_CarriesPerWorkspaceJWT_NotSharedKey(t *testing.T) {
	f := newAIFakeLens(t)
	engine := New(lensintegration.New(f.server.URL, sharedAdminKey), nil, nil)

	_, err := engine.TriageIssue(context.Background(), model.Issue{
		ID: "i-1", Identifier: "ENG-1", WorkspaceID: "ws-A",
		Title: "t", Description: "d",
	})
	if err != nil {
		t.Fatalf("TriageIssue: %v", err)
	}

	_, _, mintAuth, calls := f.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 data-path call, got %d", len(calls))
	}
	c := calls[0]

	if c.bearer == sharedAdminKey {
		t.Fatalf("data-path call carried the RAW SHARED KEY — this is the tenant collapse the fix must prevent")
	}
	ws, ok := workspaceOf(c.bearer)
	if !ok {
		t.Fatalf("data-path bearer %q is not a per-workspace JWT", c.bearer)
	}
	if ws != "ws-A" {
		t.Errorf("data-path JWT claims workspace %q, want ws-A", ws)
	}
	// The shared key may ride ONLY the mint endpoint.
	for _, a := range mintAuth {
		if a != "Bearer "+sharedAdminKey {
			t.Errorf("mint used %q, want the admin key", a)
		}
	}
	if len(mintAuth) == 0 {
		t.Errorf("expected the admin key to be spent on a mint before the data-path call")
	}
}
