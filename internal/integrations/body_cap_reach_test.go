package integrations_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/testutil"
)

// body_cap_reach_test.go — DOES THE PROVIDER-TOKEN ROUTE'S BODY CAP BITE?
//
// ⚠ MEASURED (W3.50, tab-k4m7): delete the http.MaxBytesReader wrapper from this handler
// entirely and THE WHOLE SUITE STAYS GREEN. maxIntegrationBody is 1 MiB with the comment
// "a token payload is tiny", and it is the cap on an OWNER-gated route that writes a live
// provider credential.
//
// ⚠ THE ASSERTION IS THE ERROR CODE, NOT THE STATUS, AND THAT IS THE WHOLE DESIGN. With the
// cap removed an over-size body still ends in a 400 — it decodes fine and then trips
// BAD_PROVIDER or succeeds outright. A test asserting "400" would pass either way and would
// be one more guard that cannot fail. BAD_JSON is reachable ONLY through the decode error
// the cap produces, so this reds the moment the cap stops applying.

func TestIntegrationsBodyCap_OverSizePayloadIsRejectedByTheCap(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	owner := "owner@example.com"
	if _, err := d.Pool.Exec(t.Context(),
		`INSERT INTO members (workspace_id, name, email, role) VALUES ($1,$2,$3,'owner')`,
		ws.ID, owner, owner); err != nil {
		t.Fatalf("seed owner: %v", err)
	}

	// Valid JSON, valid provider, non-empty token — everything downstream of the decode
	// would ACCEPT this. Only the size makes it fail, which is what makes the assertion
	// below a statement about the cap.
	body := `{"provider":"linear","token":"` + strings.Repeat("t", 2<<20) + `"}`
	rr := httptest.NewRecorder()
	intChain(t, d).ServeHTTP(rr, setReq(ws.ID, owner, body))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("a %d-byte token payload returned %d, want 400 — the 1 MiB cap did not bite",
			len(body), rr.Code)
	}
	var out struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode error body: %v (%s)", err, rr.Body.String())
	}
	if out.Code != "BAD_JSON" {
		t.Fatalf("over-size payload rejected as %q, want BAD_JSON. Any other code means the "+
			"body DECODED and was refused for some later reason — i.e. the size cap did not "+
			"apply, and this route accepted a %d-byte credential payload", out.Code, len(body))
	}

	// COUNTERWEIGHT: an ordinary payload on the same path must NOT be rejected as BAD_JSON,
	// or the assertion above is satisfied by a handler that refuses everything.
	rr2 := httptest.NewRecorder()
	intChain(t, d).ServeHTTP(rr2, setReq(ws.ID, owner, `{"provider":"linear","token":"small-token"}`))
	if rr2.Code >= 400 {
		t.Fatalf("an ordinary token payload returned %d (%s) — this test would pass on a "+
			"handler that rejects every body", rr2.Code, rr2.Body.String())
	}
}
