package importer_test

// upload_authz_order_test.go — the import endpoint must decide WHO MAY IMPORT before it
// reads WHAT IS BEING IMPORTED.
//
// MEASURED on 666ce7a, on the production middleware stack, with a real Postgres: a caller
// holding a valid gateway identity but NO membership in the target workspace POSTed a
// 40 MiB multipart to /v1/import/linear. The server read 41,943,379 bytes — the ENTIRE
// body — and then answered 403 FORBIDDEN. handler.run called ParseMultipartForm
// (handler.go:60) fourteen lines before authz.AuthorizeWorkspace (handler.go:74), so every
// byte of an upload from a caller who may not import was parsed, buffered and (past 64 MiB)
// spilled to a temp file before the request was refused.
//
// ⚠ THE HANDLER'S OWN DOC COMMENT CLAIMED THIS COULD NOT HAPPEN — "the cap is generous
// enough ... without letting a single request consume unbounded memory". The memory IS
// bounded, at 64 MiB of heap per in-flight request, and it was being spent on requests that
// were never authorized. The comment is corrected in the same change as the code.
//
// Nothing in the three checks now hoisted above the parse reads the body: workspace_id and
// team_id come from the URL query (r.URL.Query(), not r.FormValue, so no parse dependency)
// and the membership comes from the request context the T10 middleware already resolved.

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/gatewayauth"
	"github.com/talyvor/track/internal/httpx"
	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/testutil"
)

// countingBody reports how many bytes the server actually pulled out of the request body.
// It is the whole instrument: "was the upload read" is not observable from the status code,
// which is 403 either way.
type countingBody struct {
	r io.Reader
	n int64
}

func (c *countingBody) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// prodImportChain mirrors cmd/track/main.go: httpx.BodyLimit is installed at the ROUTER
// (main.go:351), ABOVE the /v1 gateway + membership middleware.
//
// ⚠ IT DELIBERATELY DOES NOT REUSE importChain (authz_test.go). That helper omits
// httpx.BodyLimit, which is the middleware that decides how many bytes of an over-cap
// upload reach this handler at all — a measurement of read-volume taken on a stack
// production does not run would be a measurement of the harness.
func prodImportChain(d *testutil.DB) http.Handler {
	noExempt := func(string) bool { return false }
	h := importer.NewHandler(importer.New(issue.NewStore(d.Pool)))
	r := chi.NewRouter()
	r.Use(httpx.BodyLimit)
	r.Route("/v1", func(r chi.Router) {
		r.Use(gatewayauth.Middleware(secret, noExempt))
		r.Use(authz.Middleware(authz.NewPGResolver(d.Pool), noExempt))
		h.Mount(r)
	})
	return r
}

// uploadPayload is big enough that "read all of it" and "read none of it" cannot be
// confused for one another, and small enough to build in memory in a test.
const uploadPayload = 4 << 20 // 4 MiB

// importUpload builds a multipart body carrying a VALID one-issue Linear CSV followed by
// enough filler to reach uploadPayload. The filler rows have an empty Title so the mapper
// skips them: the parse path is fully exercised and only the first row writes to Postgres.
func importUpload(t *testing.T) (io.Reader, string) {
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
	filler := []byte("," + strings.Repeat("d", 200) + ",Todo,High,bug\n")
	for written := 0; written < uploadPayload; written += len(filler) {
		if _, err := fw.Write(filler); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, mw.FormDataContentType()
}

func uploadReq(t *testing.T, wsID, teamID, email string) (*http.Request, *countingBody) {
	t.Helper()
	body, ctype := importUpload(t)
	cb := &countingBody{r: body}
	req := httptest.NewRequest("POST", "/v1/import/linear?workspace_id="+wsID+"&team_id="+teamID, cb)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set(gatewayauth.HeaderGatewayAuth, secret)
	req.Header.Set(gatewayauth.HeaderUserEmail, email)
	return req, cb
}

// TestImporter_NonMemberUploadIsNotRead — the finding, and its own positive control.
//
// The two halves are inseparable ON PURPOSE. "0 bytes read" is exactly what a broken
// fixture that produced an EMPTY body would also report, so the assertion alone could pass
// while measuring nothing. The member half sends a byte-identical upload through the same
// chain and requires the server to read ALL of it and import the one real row — which is
// what makes the non-member's zero mean "refused before reading" rather than "there was
// nothing to read".
func TestImporter_NonMemberUploadIsNotRead(t *testing.T) {
	d := testutil.New(t)
	wsA := d.Workspace(t)
	teamA := d.Team(t, wsA.ID)
	seedMember(t, d, wsA.ID, "x@corp.com")
	h := prodImportChain(d)

	// ── the finding: a non-member's upload must not be read ──────────────────────────
	req, cb := uploadReq(t, wsA.ID, teamA.ID, "nobody@corp.com")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("non-member import = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	// Zero, not "small": this handler reads the body only through ParseMultipartForm, and
	// with the authorization hoisted above it there is no path that reads a single byte.
	// (A real net/http server may drain a bounded amount of an unread body to keep the
	// connection alive; that is transport behaviour below this handler, not a read by it.)
	if cb.n != 0 {
		t.Fatalf("server read %d bytes (%.1f MiB) of a NON-MEMBER's upload before answering 403 — "+
			"the body is parsed and buffered before the membership check",
			cb.n, float64(cb.n)/(1<<20))
	}
	if n := issueCount(t, d, wsA.ID); n != 0 {
		t.Fatalf("workspace A has %d issues after a 403 — the refused upload wrote rows", n)
	}

	// ── the control: the SAME upload from a member must be read in full and land ─────
	okReq, okCB := uploadReq(t, wsA.ID, teamA.ID, "x@corp.com")
	okRR := httptest.NewRecorder()
	h.ServeHTTP(okRR, okReq)

	if okRR.Code != http.StatusOK {
		t.Fatalf("member import = %d, want 200; body=%s", okRR.Code, okRR.Body.String())
	}
	if okCB.n < uploadPayload {
		t.Fatalf("member upload: server read only %d bytes of a >=%d byte body — the fixture is "+
			"not producing the payload, so the non-member's zero above proves nothing",
			okCB.n, uploadPayload)
	}
	if n := issueCount(t, d, wsA.ID); n != 1 {
		t.Fatalf("member import created %d issues, want 1 — the payload's one real row did not land, "+
			"so the non-member's zero above proves nothing", n)
	}
}
