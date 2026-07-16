package ai

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/talyvor/track/internal/lenscreds"
	"github.com/talyvor/track/internal/lensintegration"
	"github.com/talyvor/track/internal/model"
)

// stubExecDB is the minimum pgxDB the embeddings/index data path needs:
// IndexIssue only Exec's the upsert. Query/QueryRow are unused here.
type stubExecDB struct{ execs int }

func (s *stubExecDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	s.execs++
	return pgconn.CommandTag{}, nil
}
func (s *stubExecDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("stubExecDB: Query unused")
}
func (s *stubExecDB) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

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

// Property 3 — THE DECISIVE PROOF. Two tenants, driven end-to-end through
// a fake Lens that serves the mint endpoint AND both LLM proxy data paths.
// It proves, for every data-path call the engine makes:
//   - the call Track made FOR tenant X carries a JWT claiming X — never
//     another tenant, never the raw shared key (no cross-tenant leak, no
//     collapse onto the empty-workspace bucket / "default" attribution);
//   - both data paths (anthropic + embeddings) are covered for each tenant;
//   - the shared/admin key is spent ONLY on minting, once per workspace
//     (the token is cached across that workspace's data-path calls).
//
// Each call is tied to its originating tenant by X-Talyvor-Feature (the
// issue identifier), so the assertion never relies on call ordering.
func TestTwoTenants_IsolatedEndToEnd_NoSharedKeyOnDataPath(t *testing.T) {
	f := newAIFakeLens(t)
	client := lensintegration.New(f.server.URL, sharedAdminKey)
	// Build creds exactly as production New() does, but inject a stub DB
	// so the embeddings/index path runs without a real Postgres.
	creds := lenscreds.New(client.BaseURL(), client.APIKey())
	engine := newEngine(client, creds, nil, &stubExecDB{})

	tenants := []struct {
		ws      string
		feature string // == issue identifier
	}{
		{"ws-A", "A-1"},
		{"ws-B", "B-1"},
	}
	for _, tn := range tenants {
		iss := model.Issue{ID: tn.feature, Identifier: tn.feature, WorkspaceID: tn.ws, Title: "t", Description: "d"}
		if _, err := engine.TriageIssue(context.Background(), iss); err != nil { // anthropic data path
			t.Fatalf("%s TriageIssue: %v", tn.ws, err)
		}
		if err := engine.IndexIssue(context.Background(), iss); err != nil { // embeddings data path
			t.Fatalf("%s IndexIssue: %v", tn.ws, err)
		}
	}

	mints, mintByWs, mintAuth, calls := f.snapshot()

	// 2 tenants × 2 data paths = 4 data-path calls.
	if len(calls) != 4 {
		t.Fatalf("expected 4 data-path calls, got %d: %+v", len(calls), calls)
	}

	// The heart of the proof: the JWT on each call claims the SAME
	// workspace that originated it (keyed by feature, not by order), and
	// the raw shared key never appears.
	want := map[string]string{"A-1": "ws-A", "B-1": "ws-B"}
	seen := map[string]map[string]bool{} // workspace -> set of data-path endpoints
	for _, c := range calls {
		if c.bearer == sharedAdminKey {
			t.Fatalf("SHARED KEY rode a data-path call (path=%s feature=%s) — tenant collapse", c.path, c.feature)
		}
		gotWs, ok := workspaceOf(c.bearer)
		if !ok {
			t.Fatalf("data-path bearer %q is not a per-workspace JWT (path=%s)", c.bearer, c.path)
		}
		wantWs, known := want[c.feature]
		if !known {
			t.Fatalf("unexpected feature %q on a data-path call", c.feature)
		}
		if gotWs != wantWs {
			t.Errorf("CROSS-TENANT LEAK: call for tenant %s (feature %s, path %s) carried a JWT claiming %q",
				wantWs, c.feature, c.path, gotWs)
		}
		if seen[gotWs] == nil {
			seen[gotWs] = map[string]bool{}
		}
		seen[gotWs][c.path] = true
	}

	// Both data paths exercised for each tenant.
	for _, ws := range []string{"ws-A", "ws-B"} {
		if !seen[ws]["/v1/proxy/anthropic/v1/messages"] || !seen[ws]["/v1/proxy/openai/v1/embeddings"] {
			t.Errorf("tenant %s did not exercise both data paths: %v", ws, seen[ws])
		}
	}

	// The admin key mints once per workspace and is cached across that
	// workspace's two data-path calls; it never rides a data path.
	if mints != 2 {
		t.Errorf("expected exactly 2 mints (one per workspace, cached across its data paths), got %d", mints)
	}
	if mintByWs["ws-A"] != 1 || mintByWs["ws-B"] != 1 {
		t.Errorf("each workspace must mint exactly once; byWorkspace=%v", mintByWs)
	}
	for _, a := range mintAuth {
		if a != "Bearer "+sharedAdminKey {
			t.Errorf("mint used %q, want the admin key (mint is the ONLY place the shared key may appear)", a)
		}
	}
}
