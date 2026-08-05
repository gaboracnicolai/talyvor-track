package lensintegration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/model"
)

// ⚠ "NO AI SPEND" AND "I COULD NOT READ THE AI SPEND" MUST NOT LOOK THE SAME.
//
// The live box has TRACK_LENS_URL set and no credential. In that state every authenticated read
// against Lens 401s, and GetAICosts answered:
//
//	{"lens_configured": true, "lens_healthy": true, "top_issues": [], "anomalies": []}
//
// which is byte-for-byte what a correctly configured deployment with genuinely zero spend returns.
// Each failure is swallowed by an `if err == nil`, and nothing left on the response could tell a
// user their credential was missing. A number that is structurally unobtainable was being reported
// as measured.
//
// ⚠ THE FILE ALREADY KNEW THE RULE. unattributedBlock omits its field and logs rather than
// reporting $0 when its read fails — the correct pattern, one function above, applied to one field
// and not to the others.

type zeroIssues struct{}

func (zeroIssues) GetByID(context.Context, string) (*model.Issue, error) { return nil, nil }
func (zeroIssues) GetByIdentifier(context.Context, string, string) (*model.Issue, error) {
	return nil, nil
}
func (zeroIssues) TopByAICost(context.Context, string, int) ([]model.Issue, error) {
	return nil, nil
}

// lensThatRefuses stands in for Lens as it behaves with no credential: the unauthenticated health
// probe answers 200, every authenticated read 401s.
func lensThatRefuses(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/api/health" {
			w.WriteHeader(http.StatusOK) // Lens mounts this UNAUTHENTICATED
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"API key required"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func getAICosts(t *testing.T, h *Handler) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/workspaces/ws-1/ai-costs", nil)
	req = req.WithContext(authz.WithAuthorized(req.Context(), "ws-1", "m"))
	rec := httptest.NewRecorder()
	h.GetAICosts(rec, req)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %s", rec.Body.String())
	}
	return body
}

// ⚠ THE HEADLINE. An unreadable spend total must not be presented as an empty one.
func TestCosts_AnUnreadableTotalIsNotReportedAsZero(t *testing.T) {
	srv := lensThatRefuses(t)
	h := NewHandler(New(srv.URL, ""), zeroIssues{}) // URL set, no credential — the live box

	body := getAICosts(t, h)

	if _, present := body["summary"]; present {
		t.Error("a summary was reported although Lens refused the read")
	}
	// Something on the response must say the read failed. Without it, this answer is
	// indistinguishable from a healthy deployment that has spent nothing.
	failed, ok := body["spend_unreadable"].(bool)
	if !ok || !failed {
		t.Errorf("nothing on the response says the spend could not be read: %v", body)
	}
}

// ⚠ AND IT MUST SAY WHY, in terms the operator can act on — the same rule Item 1 applied to AI.
func TestCosts_TheFailureNamesWhatIsMissing(t *testing.T) {
	srv := lensThatRefuses(t)
	h := NewHandler(New(srv.URL, ""), zeroIssues{})

	reason, _ := getAICosts(t, h)["spend_unreadable_reason"].(string)
	if reason == "" {
		t.Fatal("no reason given for the failed read")
	}
	if !strings.Contains(reason, "TRACK_LENS_API_KEY") {
		t.Errorf("the reason does not name the credential to set: %q", reason)
	}
}

// ⚠ THE CONTROL. A working Lens must still report its numbers, and must NOT claim a failure.
// Without this, a change that always reported "unreadable" would pass both tests above and make
// the endpoint useless.
func TestCosts_AWorkingLensStillReportsItsNumbers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/api/health":
			w.WriteHeader(http.StatusOK)
		case "/v1/api/spend/summary":
			_, _ = w.Write([]byte(`{"total_cost_usd":12.5,"total_requests":7}`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	defer srv.Close()
	h := NewHandler(New(srv.URL, "tlv_key"), zeroIssues{})

	body := getAICosts(t, h)
	if _, present := body["summary"]; !present {
		t.Error("a working Lens did not report a summary")
	}
	if failed, _ := body["spend_unreadable"].(bool); failed {
		t.Error("a working Lens was reported as unreadable")
	}
}

// ⚠ HEALTHY MUST MEAN HEALTHY. The doc comment on Healthy says it returns false on "any error,
// timeout, or non-2xx status". The code returned StatusCode < 500, so 401, 403 and 404 all read as
// healthy — including the case where the health route does not exist at all, which is how a 200
// from a fallback proves nothing.
func TestHealthy_NonSuccessIsNotHealthy(t *testing.T) {
	for _, code := range []int{
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusTooManyRequests,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		}))
		if New(srv.URL, "k").Healthy(context.Background()) {
			t.Errorf("Healthy() = true on a %d response, but the contract says non-2xx is not healthy", code)
		}
		srv.Close()
	}
}

// The control for that one: a 200 IS healthy.
func TestHealthy_TwoHundredIsHealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if !New(srv.URL, "k").Healthy(context.Background()) {
		t.Error("Healthy() = false on 200 — the probe would report every deployment down")
	}
}
