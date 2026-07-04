package integrations_test

import (
	"bytes"
	"context"
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
	seedMember(t, d, ws.ID, "a@corp.com")
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

// (c, HTTP) TENANCY: a member of A cannot write B's integration (authz.AuthorizeWorkspace) — 403, nothing
// persisted into B.
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
	var n int
	_ = d.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM workspace_integrations WHERE workspace_id=$1`, wsB.ID).Scan(&n)
	if n != 0 {
		t.Fatalf("cross-tenant set wrote %d rows into B", n)
	}
}
