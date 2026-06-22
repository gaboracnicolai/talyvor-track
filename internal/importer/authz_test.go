package importer_test

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/gatewayauth"
	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/testutil"
)

const secret = "test-gateway-transit-secret-0123456789"

func seedMember(t *testing.T, d *testutil.DB, wsID, email string) {
	t.Helper()
	if _, err := d.Pool.Exec(context.Background(),
		`INSERT INTO members (workspace_id, name, email, role) VALUES ($1,$2,$3,'member')`,
		wsID, email, email); err != nil {
		t.Fatalf("seed member: %v", err)
	}
}

func issueCount(t *testing.T, d *testutil.DB, wsID string) int {
	t.Helper()
	var n int
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM issues WHERE workspace_id=$1`, wsID).Scan(&n); err != nil {
		t.Fatalf("count issues: %v", err)
	}
	return n
}

// importChain wires the REAL T9 (transit proof) + T10 (membership) middleware in front of
// the importer — the same stack production uses.
func importChain(d *testutil.DB) http.Handler {
	noExempt := func(string) bool { return false }
	h := importer.NewHandler(importer.New(issue.NewStore(d.Pool)))
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(gatewayauth.Middleware(secret, noExempt))
		r.Use(authz.Middleware(authz.NewPGResolver(d.Pool), noExempt))
		h.Mount(r)
	})
	return r
}

const linearCSV = "Title,Description,Status,Priority,Labels\nImported Issue,a description,Todo,High,bug\n"

func importReq(t *testing.T, wsID, teamID, email string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "import.csv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte(linearCSV)); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/v1/import/linear?workspace_id="+wsID+"&team_id="+teamID, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set(gatewayauth.HeaderGatewayAuth, secret)
	req.Header.Set(gatewayauth.HeaderUserEmail, email)
	return req
}

// TestImporter_MemberOfA_CannotImportIntoB — the core proof. X is a member of A only.
// Import into B → 403, B untouched. Import into A → 200, issues land in A. Same caller,
// same request shape — only membership differs, so the deny SOURCE is membership (the
// caller-supplied workspace_id checked against Memberships), not the URL or a header.
func TestImporter_MemberOfA_CannotImportIntoB(t *testing.T) {
	d := testutil.New(t)
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	teamA := d.Team(t, wsA.ID)
	teamB := d.Team(t, wsB.ID)
	seedMember(t, d, wsA.ID, "x@corp.com") // X is a member of A only

	h := importChain(d)

	// member-of-A → import into B → 403, B untouched
	rrB := httptest.NewRecorder()
	h.ServeHTTP(rrB, importReq(t, wsB.ID, teamB.ID, "x@corp.com"))
	if rrB.Code != http.StatusForbidden {
		t.Fatalf("import into B = %d, want 403; body=%s", rrB.Code, rrB.Body.String())
	}
	if n := issueCount(t, d, wsB.ID); n != 0 {
		t.Fatalf("workspace B has %d issues — importer wrote into a non-member workspace", n)
	}

	// member-of-A → import into A → 200, issues land in A
	rrA := httptest.NewRecorder()
	h.ServeHTTP(rrA, importReq(t, wsA.ID, teamA.ID, "x@corp.com"))
	if rrA.Code != http.StatusOK {
		t.Fatalf("import into A = %d, want 200; body=%s", rrA.Code, rrA.Body.String())
	}
	if n := issueCount(t, d, wsA.ID); n == 0 {
		t.Fatal("import into A created 0 issues — legitimate import broke")
	}
}

// TestImporter_NoMembership_403 — a verified user with no membership row → 403.
func TestImporter_NoMembership_403(t *testing.T) {
	d := testutil.New(t)
	wsA := d.Workspace(t)
	teamA := d.Team(t, wsA.ID)
	seedMember(t, d, wsA.ID, "x@corp.com")

	h := importChain(d)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, importReq(t, wsA.ID, teamA.ID, "nobody@corp.com"))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("no-membership import = %d, want 403", rr.Code)
	}
}

// TestImporter_TeamFromOtherWorkspace_RejectedByStore — workspace authorized (X member of
// A, workspace_id=A) but team_id is B's team. The handler passes the authz gate; the issue
// store's EXISTING tenancy (team must belong to the workspace) rejects every row, so
// nothing lands in A. Proves T5b still guards the import path — asserted, not rebuilt.
func TestImporter_TeamFromOtherWorkspace_RejectedByStore(t *testing.T) {
	d := testutil.New(t)
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	teamB := d.Team(t, wsB.ID) // a team in the OTHER workspace
	seedMember(t, d, wsA.ID, "x@corp.com")

	h := importChain(d)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, importReq(t, wsA.ID, teamB.ID, "x@corp.com"))
	if rr.Code != http.StatusOK {
		t.Fatalf("authorized import = %d, want 200 (rows individually rejected by the store)", rr.Code)
	}
	if n := issueCount(t, d, wsA.ID); n != 0 {
		t.Fatalf("workspace A has %d issues — a cross-workspace team was accepted (T5b breach)", n)
	}
}
