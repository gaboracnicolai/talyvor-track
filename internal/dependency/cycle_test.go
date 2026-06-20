package dependency_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/dependency"
	"github.com/talyvor/track/internal/testutil"
)

// rel creates a relation of the given type and returns the error (if any).
func rel(st *dependency.Store, wsID, src, tgt string, typ dependency.RelationType) error {
	_, err := st.Create(context.Background(), dependency.Relation{
		SourceID: src, TargetID: tgt, Type: typ, WorkspaceID: wsID,
	})
	return err
}

func mustBlock(t *testing.T, st *dependency.Store, wsID, src, tgt string) {
	t.Helper()
	if err := rel(st, wsID, src, tgt, dependency.RelationBlocks); err != nil {
		t.Fatalf("%s blocks %s (expected ok): %v", src, tgt, err)
	}
}

// TestCycle_TwoNode_Rejected — A blocks B, then B blocks A must be refused.
func TestCycle_TwoNode_Rejected(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	a, b := d.Issue(t, ws.ID, ""), d.Issue(t, ws.ID, "")
	st := dependency.NewStore(d.Pool)

	mustBlock(t, st, ws.ID, a.ID, b.ID)
	if err := rel(st, ws.ID, b.ID, a.ID, dependency.RelationBlocks); !errors.Is(err, dependency.ErrCycle) {
		t.Fatalf("B blocks A: got %v, want ErrCycle (2-node)", err)
	}
}

// TestCycle_ThreeNode_Rejected — the case to scrutinize: A→B→C→A is transitive, the
// closing edge shares no endpoint with the others, so only a real reachability walk
// catches it.
func TestCycle_ThreeNode_Rejected(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	a, b, c := d.Issue(t, ws.ID, ""), d.Issue(t, ws.ID, ""), d.Issue(t, ws.ID, "")
	st := dependency.NewStore(d.Pool)

	mustBlock(t, st, ws.ID, a.ID, b.ID) // A→B
	mustBlock(t, st, ws.ID, b.ID, c.ID) // B→C
	if err := rel(st, ws.ID, c.ID, a.ID, dependency.RelationBlocks); !errors.Is(err, dependency.ErrCycle) {
		t.Fatalf("C blocks A: got %v, want ErrCycle (3-node transitive)", err)
	}
}

// TestCycle_FiveNodeChain_Rejected — a longer chain to be sure depth isn't bounded.
func TestCycle_FiveNodeChain_Rejected(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	n := []string{}
	for i := 0; i < 5; i++ {
		n = append(n, d.Issue(t, ws.ID, "").ID)
	}
	st := dependency.NewStore(d.Pool)
	for i := 0; i < 4; i++ {
		mustBlock(t, st, ws.ID, n[i], n[i+1]) // 0→1→2→3→4
	}
	if err := rel(st, ws.ID, n[4], n[0], dependency.RelationBlocks); !errors.Is(err, dependency.ErrCycle) {
		t.Fatalf("closing 5-node chain: got %v, want ErrCycle", err)
	}
}

// TestCycle_Diamond_Allowed — A→B, A→C, B→D, C→D has no cycle; the last edge must NOT
// be over-rejected just because D is reachable from A by two paths.
func TestCycle_Diamond_Allowed(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	a, b, c, dd := d.Issue(t, ws.ID, ""), d.Issue(t, ws.ID, ""), d.Issue(t, ws.ID, ""), d.Issue(t, ws.ID, "")
	st := dependency.NewStore(d.Pool)

	mustBlock(t, st, ws.ID, a.ID, b.ID)  // A→B
	mustBlock(t, st, ws.ID, a.ID, c.ID)  // A→C
	mustBlock(t, st, ws.ID, b.ID, dd.ID) // B→D
	if err := rel(st, ws.ID, c.ID, dd.ID, dependency.RelationBlocks); err != nil {
		t.Fatalf("diamond C blocks D wrongly rejected (over-reject): %v", err)
	}
}

// TestCycle_BlockedBy_Rejected — the inverse direction must be caught too: A blocked_by
// B (= B blocks A), then B blocked_by A (= A blocks B) closes the cycle.
func TestCycle_BlockedBy_Rejected(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	a, b := d.Issue(t, ws.ID, ""), d.Issue(t, ws.ID, "")
	st := dependency.NewStore(d.Pool)

	if err := rel(st, ws.ID, a.ID, b.ID, dependency.RelationBlockedBy); err != nil {
		t.Fatalf("A blocked_by B (expected ok): %v", err)
	}
	if err := rel(st, ws.ID, b.ID, a.ID, dependency.RelationBlockedBy); !errors.Is(err, dependency.ErrCycle) {
		t.Fatalf("B blocked_by A: got %v, want ErrCycle", err)
	}
}

// TestCycle_RelatesTo_NotAffected — a non-dependency relation never triggers cycle
// detection (relates_to A↔B both directions is fine).
func TestCycle_RelatesTo_NotAffected(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	a, b := d.Issue(t, ws.ID, ""), d.Issue(t, ws.ID, "")
	st := dependency.NewStore(d.Pool)

	if err := rel(st, ws.ID, a.ID, b.ID, dependency.RelationRelates); err != nil {
		t.Fatalf("A relates_to B: %v", err)
	}
	if err := rel(st, ws.ID, b.ID, a.ID, dependency.RelationRelates); err != nil {
		t.Fatalf("B relates_to A wrongly rejected (relates_to can't cycle): %v", err)
	}
}

// TestCycle_Handler_Returns409 — the documented route maps the cycle to 409 Conflict.
func TestCycle_Handler_Returns409(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	a, b := d.Issue(t, ws.ID, ""), d.Issue(t, ws.ID, "")
	st := dependency.NewStore(d.Pool)
	mustBlock(t, st, ws.ID, a.ID, b.ID)

	h := dependency.NewHandler(st)
	r := chi.NewRouter()
	r.Post("/ws/{wsID}/issues/{id}/relations", h.Create)

	body := `{"target_id":"` + a.ID + `","type":"blocks"}` // B blocks A → cycle
	req := httptest.NewRequest(http.MethodPost, "/ws/"+ws.ID+"/issues/"+b.ID+"/relations", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("cycle via handler = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
}
