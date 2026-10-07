package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const scrapeToken = "0123456789abcdef-scrape"

func scrape(t *testing.T, token, authorization string) *httptest.ResponseRecorder {
	t.Helper()
	// Put a tenant-labelled series on the page so a leak would be visible in the body.
	IssuesCreated.WithLabelValues("ws-tenant-a", "team-a", "todo").Inc()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	Handler(token).ServeHTTP(rec, req)
	return rec
}

// B28.436: an unauthenticated GET /metrics is 401 and shows no tenant id.
func TestHandler_RefusesWithoutTheToken(t *testing.T) {
	for name, tc := range map[string]struct{ token, auth string }{
		"no header":            {scrapeToken, ""},
		"wrong token":          {scrapeToken, "Bearer not-the-token-at-all"},
		"token without Bearer": {scrapeToken, scrapeToken},
		"no token configured":  {"", "Bearer "},
	} {
		t.Run(name, func(t *testing.T) {
			rec := scrape(t, tc.token, tc.auth)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			if strings.Contains(rec.Body.String(), "ws-tenant-a") {
				t.Fatalf("401 body leaks a workspace id: %q", rec.Body.String())
			}
		})
	}
}

func TestHandler_ServesTheScraperHoldingTheToken(t *testing.T) {
	rec := scrape(t, scrapeToken, "Bearer "+scrapeToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `track_issues_created_total{status="todo",team="team-a",workspace="ws-tenant-a"}`) {
		t.Fatalf("authorised scrape is missing the issue counter:\n%s", rec.Body.String())
	}
}
