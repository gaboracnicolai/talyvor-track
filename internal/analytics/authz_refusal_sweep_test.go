package analytics_test

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/analytics"
	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/testutil"
)

// authz_refusal_sweep_test.go — every analytics route must refuse a request that carries no
// authorized workspace, and none may refuse one that does.
//
// WHY, AND WHY A SWEEP. All seven handlers open with the same four lines:
//
//	wsID, ok := authz.WorkspaceID(r.Context())
//	if !ok { writeErr(w, http.StatusForbidden, ...); return }
//
// MEASURED at 890459a: disabling ALL SEVEN of those branches at once (`if !ok && false`) left
// `go test ./...` GREEN ACROSS THE WHOLE REPOSITORY. Not one test drove any of these routes
// without an authorized workspace, so the branch read as dead code — the state in which someone
// deletes it as an unreachable leftover. It is not dead: it is the last line of defence if a
// route is ever mounted outside the gwAuth/wsAuthz pair it sits behind in main.go today, and
// without it an unauthorized caller gets 200 and an empty report instead of a refusal.
//
// The route list is WALKED OFF THE MOUNTED ROUTER, never typed out here. A hand-written list of
// seven paths is the shape that hid this in the first place: it can only ever cover the routes
// somebody remembered, and a route added to Mount tomorrow joins this sweep automatically.
//
// ⚠ THE SECOND HALF IS NOT DECORATION — IT IS WHAT STOPS THE FIRST HALF BEING VACUOUS.
// "no authorized workspace => 403" is satisfied completely by a handler that answers 403 to
// EVERYONE. The authorized half fails on exactly that, which is why both run over the same
// walked list rather than one being a spot check.

// mountedAnalyticsPatterns returns every (method, pattern) the handler actually registers.
func mountedAnalyticsPatterns(t *testing.T, h *analytics.Handler) []string {
	t.Helper()
	router := chi.NewRouter()
	h.Mount(router)

	var out []string
	err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		out = append(out, method+" "+route)
		return nil
	})
	if err != nil {
		t.Fatalf("walking the mounted analytics router: %v", err)
	}
	sort.Strings(out)

	// POPULATION FLOOR. A walk that returned nothing would make every assertion below unreachable
	// and both halves green for the worst possible reason. The count is deliberately NOT pinned —
	// a new analytics route should be swept, not rejected — but the set may not be empty.
	if len(out) == 0 {
		t.Fatal("the analytics router mounted ZERO routes — this sweep would assert nothing")
	}
	return out
}

// requestFor turns a walked chi pattern into a concrete request path.
func requestFor(t *testing.T, methodAndRoute, wsID string) *http.Request {
	t.Helper()
	parts := strings.SplitN(methodAndRoute, " ", 2)
	if len(parts) != 2 {
		t.Fatalf("unparseable walked route %q", methodAndRoute)
	}
	method, route := parts[0], parts[1]
	path := strings.ReplaceAll(route, "{wsID}", wsID)
	if strings.Contains(path, "{") {
		t.Fatalf("route %q still carries an unsubstituted URL parameter — this sweep would request "+
			"a literal pattern and measure chi's 404, not the handler's refusal", path)
	}
	return httptest.NewRequest(method, path, nil)
}

func TestAnalytics_EveryMountedRoute_RefusesAnUnauthorizedWorkspace(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	h := analytics.NewHandler(analytics.New(d.Pool))
	router := chi.NewRouter()
	h.Mount(router)

	routes := mountedAnalyticsPatterns(t, h)

	// ---- HALF 1: no authorized workspace in the context => every route refuses. ----
	refused := 0
	for _, route := range routes {
		t.Run("unauthorized "+route, func(t *testing.T) {
			// Deliberately NO authz.WithAuthorizedRole: this is the state the four-line branch
			// at the top of every handler exists for.
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, requestFor(t, route, ws.ID))

			if rec.Code != http.StatusForbidden {
				t.Errorf("%s answered %d to a request carrying NO authorized workspace — want 403. "+
					"Body: %s", route, rec.Code, strings.TrimSpace(rec.Body.String()))
			}
			// A refusal must not also ship a payload: the export route commits download headers,
			// and a 403 that still looks like a file is the #131 defect wearing a different status.
			if cd := rec.Header().Get("Content-Disposition"); cd != "" {
				t.Errorf("%s refused with 403 but still sent Content-Disposition %q — a refusal is "+
					"not a download", route, cd)
			}
			refused++
		})
	}

	// ---- HALF 2 (ANTI-VACUITY): an authorized workspace is NOT refused. ----
	// Without this, a handler that answered 403 unconditionally would satisfy half 1 perfectly.
	// No per-route fixtures and no query parameters on purpose: the assertion is only "not 403",
	// so routes that go on to answer 400 (velocity's MISSING_TEAM, burndown's MISSING_CYCLE,
	// export's MISSING_REPORT) still make the point without this sweep having to know which
	// parameters each report takes. That keeps the list derived rather than described.
	allowed := 0
	for _, route := range routes {
		t.Run("authorized "+route, func(t *testing.T) {
			req := requestFor(t, route, ws.ID)
			req = req.WithContext(authz.WithAuthorizedRole(req.Context(), ws.ID, "m1", authz.RoleMember))

			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code == http.StatusForbidden {
				t.Errorf("%s refused an AUTHORIZED workspace with 403 — half 1 of this sweep is "+
					"satisfied by a handler that refuses everyone, and this is the assertion that "+
					"says it must not. Body: %s", route, strings.TrimSpace(rec.Body.String()))
			}
			allowed++
		})
	}

	// Both halves must have covered the whole walked population; a subtest skipped by a later
	// edit would otherwise shrink the sweep silently.
	if refused != len(routes) || allowed != len(routes) {
		t.Errorf("swept %d unauthorized and %d authorized of %d mounted routes (%v) — every mounted "+
			"route must be asserted in BOTH directions", refused, allowed, len(routes), routes)
	}
}
