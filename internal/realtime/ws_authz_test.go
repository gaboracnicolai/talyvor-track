package realtime

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Audit residual (class 3, WS): /v1/ws was in gwExempt, so ServeWS ran with no verified identity and
// trusted workspace_id/member_id from the query — GET /v1/ws?workspace_id=<victim> subscribed the socket
// to another tenant's live issue/comment/cycle stream. RED: a caller with query params but NO authorized
// context is not refused (reaches the upgrade). GREEN: it is refused with 403 before any upgrade.
func TestServeWS_RequiresWorkspaceAuthorization(t *testing.T) {
	h := NewHub()
	// No authz context on the request (as when the route bypassed gwAuth+wsAuthz), but a victim
	// workspace named in the query — the classic unauthenticated cross-tenant subscribe.
	req := httptest.NewRequest(http.MethodGet, "/v1/ws?workspace_id=victim-ws&member_id=attacker", nil)
	rr := httptest.NewRecorder()

	h.ServeWS(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("ServeWS with no workspace authorization = %d, want 403 (unauth cross-tenant subscribe must be refused before upgrade)", rr.Code)
	}
}
