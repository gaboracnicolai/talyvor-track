package importer_test

// jobs_upload_authz_order_test.go — THE SECOND COPY OF THE SEAM #90 CLOSED.
//
// `6d5824b` (#90) hoisted authz above the parse in handler.run — the SYNCHRONOUS import
// endpoints, POST /v1/import/linear and /v1/import/jira. It said nothing about the OTHER
// multipart handler in this package, and there is exactly one other: JobHandler.create
// (job_handler.go), the ASYNC endpoint POST /v1/import/jobs, which is the T8 Build B
// surface — the one built precisely so a bulk import can outlive the 30s inline timeout,
// i.e. the one a big upload is supposed to go to.
//
// `grep -rn 'ParseMultipartForm|FormFile' --include='*.go'` over this repo returns TWO
// call sites outside tests: handler.go:103 (fixed at #90, authz five lines above it) and
// job_handler.go:72 (parse), with authz.AuthorizeWorkspace seventeen lines BELOW at :89.
//
// MEASURED on 6d5824b on the production middleware stack against real Postgres: a caller
// with a valid gateway identity and NO membership in the target workspace POSTed a 4 MiB
// multipart to /v1/import/jobs and the server read 4,194,830 bytes — the entire body —
// before answering 403 FORBIDDEN. Identical in kind to what #90 measured at 40 MiB on the
// sync route, on the endpoint that is MORE exposed: the async path exists for the uploads
// too big to process inline, and its cap is the same 96 MiB httpx.ImportMaxBody (measured
// by #90 on that shared middleware, not re-measured here — what is measured here is that
// the whole body, whatever its size, is read before the refusal).
//
// ⚠ THE TWO CHECKS BEING HOISTED READ NO BODY, same as #90: workspace_id/team_id/
// source_type come from r.URL.Query() (never r.FormValue, so no parse dependency) and the
// membership was resolved into the request context by the T10 middleware. The only
// behaviour that changes for any caller is WHICH refusal a request that is both malformed
// and unauthorized receives — an unauthorized caller is now told FORBIDDEN instead of
// BAD_UPLOAD, which is the correct order to answer those two questions in anyway.
//
// ⚠ THE STATUS CODE IS 403 EITHER WAY. Nothing already in this package could see this:
// the entire difference is how many bytes were spent before the 403, so the byte counter
// IS the instrument and the member half below is what stops a zero from meaning "the
// fixture produced nothing".

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/gatewayauth"
	"github.com/talyvor/track/internal/httpx"
	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/testutil"
)

// prodImportJobsChain mirrors cmd/track/main.go for the ASYNC surface: httpx.BodyLimit at
// the ROUTER (main.go:351), above the /v1 gateway + membership middleware, then
// importJobHandler.Mount (main.go:443).
//
// ⚠ A SEPARATE HELPER FROM prodImportChain ON PURPOSE. That one mounts importer.Handler
// (the sync routes) and its evidence was gathered about those routes; pointing it at a
// different handler would lend it provenance it never earned. The two differ in exactly
// one line — the handler being mounted — and that line is the whole subject here.
func prodImportJobsChain(d *testutil.DB) http.Handler {
	noExempt := func(string) bool { return false }
	h := importer.NewJobHandler(importer.NewJobStore(d.Pool))
	r := chi.NewRouter()
	r.Use(httpx.BodyLimit)
	r.Route("/v1", func(r chi.Router) {
		r.Use(gatewayauth.Middleware(secret, noExempt))
		r.Use(authz.Middleware(authz.NewPGResolver(d.Pool), noExempt))
		h.Mount(r)
	})
	return r
}

// jobUploadReq builds the SAME multipart body importUpload produces for the sync test —
// a valid one-issue Linear CSV plus filler to uploadPayload — aimed at the async route.
func jobUploadReq(t *testing.T, wsID, teamID, email string) (*http.Request, *countingBody) {
	t.Helper()
	body, ctype := importUpload(t)
	cb := &countingBody{r: body}
	req := httptest.NewRequest("POST",
		"/v1/import/jobs?workspace_id="+wsID+"&team_id="+teamID+"&source_type=linear_csv", cb)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set(gatewayauth.HeaderGatewayAuth, secret)
	req.Header.Set(gatewayauth.HeaderUserEmail, email)
	return req, cb
}

// jobRowCount is api_enqueue_test.go:62 — the same `count(*) FROM import_jobs WHERE
// workspace_id=$1` this file needs, already written for the *_api enqueue tests. Reused
// rather than duplicated because the query is the whole helper and it is asserting the
// same fact about the same table.

func jobPayloadLen(t *testing.T, d *testutil.DB, jobID string) int {
	t.Helper()
	var n int
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT octet_length(payload) FROM import_job_payloads WHERE job_id=$1`, jobID).Scan(&n); err != nil {
		t.Fatalf("payload length for job %s: %v", jobID, err)
	}
	return n
}

// TestJobHandler_NonMemberUploadIsNotRead — the finding, and its own positive control.
//
// The two halves are inseparable ON PURPOSE, for the same reason #90's are: "0 bytes read"
// is exactly what a broken fixture producing an EMPTY body would report, so the assertion
// alone could pass while measuring nothing. The member half sends a byte-identical upload
// through the same chain and requires the server to read ALL of it AND to persist a payload
// row of at least that size — which is what makes the non-member's zero mean "refused
// before reading" rather than "there was nothing to read".
func TestJobHandler_NonMemberUploadIsNotRead(t *testing.T) {
	d := testutil.New(t)
	wsA := d.Workspace(t)
	teamA := d.Team(t, wsA.ID)
	seedMember(t, d, wsA.ID, "x@corp.com")
	h := prodImportJobsChain(d)

	// ── the finding: a non-member's upload must not be read ──────────────────────────
	req, cb := jobUploadReq(t, wsA.ID, teamA.ID, "nobody@corp.com")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("non-member enqueue = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	// Zero, not "small": this handler reads the body only through ParseMultipartForm and
	// FormFile, and with the authorization hoisted above both there is no path that reads
	// a single byte. (A real net/http server may drain a bounded amount of an unread body
	// to keep the connection alive; that is transport behaviour below this handler.)
	if cb.n != 0 {
		t.Fatalf("server read %d bytes (%.1f MiB) of a NON-MEMBER's upload before answering 403 — "+
			"the async endpoint parses and buffers the body before the membership check",
			cb.n, float64(cb.n)/(1<<20))
	}
	if n := jobRowCount(t, d, wsA.ID); n != 0 {
		t.Fatalf("workspace A has %d import_jobs rows after a 403 — the refused upload enqueued work", n)
	}

	// ── the control: the SAME upload from a member must be read in full and land ─────
	okReq, okCB := jobUploadReq(t, wsA.ID, teamA.ID, "x@corp.com")
	okRR := httptest.NewRecorder()
	h.ServeHTTP(okRR, okReq)

	if okRR.Code != http.StatusAccepted {
		t.Fatalf("member enqueue = %d, want 202; body=%s", okRR.Code, okRR.Body.String())
	}
	if okCB.n < uploadPayload {
		t.Fatalf("member upload: server read only %d bytes of a >=%d byte body — the fixture is "+
			"not producing the payload, so the non-member's zero above proves nothing",
			okCB.n, uploadPayload)
	}
	var accepted struct {
		JobID  string `json:"job_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(okRR.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("decode 202 body %q: %v", okRR.Body.String(), err)
	}
	if accepted.JobID == "" {
		t.Fatalf("member enqueue returned no job_id (%q) — nothing was created, so the "+
			"non-member's zero above proves nothing", okRR.Body.String())
	}
	if n := jobRowCount(t, d, wsA.ID); n != 1 {
		t.Fatalf("member enqueue created %d import_jobs rows, want 1 — so the non-member's "+
			"zero above proves nothing", n)
	}
	// The stored payload is the file part itself. Requiring it to be at least the fixture
	// size is what proves the body was not merely COUNTED but read through to the handler
	// and persisted — a member half that 202'd on a truncated read would still be a fixture
	// that cannot vouch for the zero.
	if got := jobPayloadLen(t, d, accepted.JobID); got < uploadPayload {
		t.Fatalf("member enqueue persisted a %d-byte payload for a >=%d byte upload — the file "+
			"part did not survive, so the non-member's zero above proves nothing", got, uploadPayload)
	}
}
