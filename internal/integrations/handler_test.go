package integrations_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/gatewayauth"
	"github.com/talyvor/track/internal/integrations"
	"github.com/talyvor/track/internal/testutil"
)

const testSecret = "test-gateway-transit-secret-0123456789"

func seedMember(t *testing.T, d *testutil.DB, wsID, email string) {
	t.Helper()
	if _, err := d.Pool.Exec(context.Background(),
		`INSERT INTO members (workspace_id, name, email, role) VALUES ($1,$2,$3,'member')`,
		wsID, email, email); err != nil {
		t.Fatalf("seed member: %v", err)
	}
}

// intChain wires the config handler behind the REAL gateway-transit + membership middleware.
func intChain(t *testing.T, d *testutil.DB) http.Handler {
	t.Helper()
	c, err := integrations.NewCipher(bytes.Repeat([]byte("k"), 32))
	if err != nil {
		t.Fatal(err)
	}
	h := integrations.NewHandler(integrations.NewStore(d.Pool, c))
	noExempt := func(string) bool { return false }
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(gatewayauth.Middleware(testSecret, noExempt))
		r.Use(authz.Middleware(authz.NewPGResolver(d.Pool), noExempt))
		h.Mount(r)
	})
	return r
}

func setReq(wsID, email, body string) *http.Request {
	req := httptest.NewRequest("POST", "/v1/integrations?workspace_id="+wsID, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(gatewayauth.HeaderGatewayAuth, testSecret)
	req.Header.Set(gatewayauth.HeaderUserEmail, email)
	return req
}

// (d) NO-ECHO: the config POST response does NOT contain the token, and the GET status omits it too.
func TestHandler_Set_DoesNotEchoToken(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	seedOwner(t, d, ws.ID, "a@corp.com") // set is owner-gated; the no-echo behavior is unchanged
	h := intChain(t, d)
	const token = "super-secret-echo-marker"

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, setReq(ws.ID, "a@corp.com",
		`{"provider":"linear","token":"`+token+`","project_or_team_key":"ENG"}`))
	if rr.Code != http.StatusCreated {
		t.Fatalf("set = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), token) {
		t.Fatalf("NO-ECHO VIOLATION: POST response contains the token: %s", rr.Body.String())
	}

	greq := httptest.NewRequest("GET", "/v1/integrations/linear?workspace_id="+ws.ID, nil)
	greq.Header.Set(gatewayauth.HeaderGatewayAuth, testSecret)
	greq.Header.Set(gatewayauth.HeaderUserEmail, "a@corp.com")
	grr := httptest.NewRecorder()
	h.ServeHTTP(grr, greq)
	if grr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", grr.Code, grr.Body.String())
	}
	if strings.Contains(grr.Body.String(), token) {
		t.Fatalf("NO-ECHO VIOLATION: status response contains the token: %s", grr.Body.String())
	}
	if !strings.Contains(grr.Body.String(), `"configured":true`) {
		t.Fatalf("status should report configured:true; got %s", grr.Body.String())
	}
}

// refusalCode reads the machine-readable `code` out of a writeErr body. The STATUS is not
// enough to name which gate refused: these two flat routes stack a membership gate and an
// owner gate, and both answer 403. Measured — with the membership refusal deleted, the
// cross-tenant set below still returns 403, from OWNER_REQUIRED instead of FORBIDDEN.
func refusalCode(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode refusal body %q: %v", rr.Body.String(), err)
	}
	return body.Code
}

// (c, HTTP) TENANCY: a member of A cannot write B's integration (authz.AuthorizeWorkspace) — 403
// FROM THE MEMBERSHIP GATE, nothing persisted into B.
//
// The code assertion is the point, not decoration. This route's owner gate sits directly below
// the membership gate and reads the role off the membership AuthorizeWorkspace resolved, so a
// zero Membership is not an owner either: delete the membership refusal and this caller is STILL
// refused 403, by the gate below, with 0 rows written. Every assertion this test used to make
// held. It was covered by accident, and `.semgrep/workspace-authz.yml` cannot see the difference
// because its rule is satisfied by the AuthorizeWorkspace CALL, not by anything done with the
// answer. Naming the code is what makes the membership gate's own refusal observable.
func TestHandler_Set_CrossTenant_403(t *testing.T) {
	d := testutil.New(t)
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	seedMember(t, d, wsA.ID, "a@corp.com") // member of A only
	h := intChain(t, d)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, setReq(wsB.ID, "a@corp.com", `{"provider":"linear","token":"x"}`))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("cross-tenant set = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	if got := refusalCode(t, rr); got != "FORBIDDEN" {
		t.Fatalf("cross-tenant set refused with %q, want FORBIDDEN (the membership gate); "+
			"OWNER_REQUIRED means the membership refusal did not run", got)
	}
	var n int
	_ = d.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM workspace_integrations WHERE workspace_id=$1`, wsB.ID).Scan(&n)
	if n != 0 {
		t.Fatalf("cross-tenant set wrote %d rows into B", n)
	}
}

// TENANCY, THE READ HALF: a member of A cannot read B's integration status. There was no
// cross-tenant test on this route at all — `status` has no gate under the membership check, so
// deleting that check answers a non-member 200 (about workspace "", since the handler reads
// m.WorkspaceID) where it used to answer 403. Measured: with the refusal removed the whole
// integrations package stayed GREEN and this route returned
// `200 {"provider":"linear","configured":false}` to a caller who is not a member.
//
// Nothing of B's leaks today, and that is a property of ONE identifier: the handler passes
// m.WorkspaceID (server-resolved) to the store, not the workspaceID it read from the caller's
// query — both are in scope on adjacent lines and only a comment tells them apart.
func TestHandler_Status_CrossTenant_403(t *testing.T) {
	d := testutil.New(t)
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	seedMember(t, d, wsA.ID, "a@corp.com") // member of A only
	seedOwner(t, d, wsB.ID, "b@corp.com")  // B's own owner, who configures B
	h := intChain(t, d)

	// B configures a real credential, so the route has something to be asked about.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, setReq(wsB.ID, "b@corp.com", `{"provider":"linear","token":"b-token","project_key":"BKEY"}`))
	if rr.Code != http.StatusCreated {
		t.Fatalf("owner set = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}

	req := httptest.NewRequest("GET", "/v1/integrations/linear?workspace_id="+wsB.ID, nil)
	req.Header.Set(gatewayauth.HeaderGatewayAuth, testSecret)
	req.Header.Set(gatewayauth.HeaderUserEmail, "a@corp.com")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("cross-tenant status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	if got := refusalCode(t, rr); got != "FORBIDDEN" {
		t.Fatalf("cross-tenant status refused with %q, want FORBIDDEN (the membership gate)", got)
	}
	if strings.Contains(rr.Body.String(), "BKEY") {
		t.Fatalf("cross-tenant status echoed B's project key: %s", rr.Body.String())
	}

	// Must-stay-green companion: B's OWN owner still gets the configured answer, so the
	// refusal above is not passing because this route refuses everyone.
	req = httptest.NewRequest("GET", "/v1/integrations/linear?workspace_id="+wsB.ID, nil)
	req.Header.Set(gatewayauth.HeaderGatewayAuth, testSecret)
	req.Header.Set(gatewayauth.HeaderUserEmail, "b@corp.com")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"configured":true`) {
		t.Fatalf("B's own owner status = %d, body=%s; want 200 and configured:true", rr.Code, rr.Body.String())
	}
}
