package member

import (
	"net/http/httptest"
	"testing"
)

// page_cap_reach_test.go — DOES THE ROSTER HARD CAP BITE?
//
// maxLimit's own comment says "hard cap — the roster read can never return more per page".
// ⚠ MEASURED (W3.50, tab-k4m7): neuter `if limit > maxLimit` and THE WHOLE SUITE STAYS
// GREEN. That sentence was defended by nothing. It matters more here than on an ordinary
// paging cap because this route is gwExempt and service-authenticated: one bearer token
// reads EVERY workspace's roster, so the page size is the only thing bounding how much of
// the all-tenant member table one request can pull.
//
// ⚠ THIS TEST IS IN-PACKAGE ON PURPOSE. pageParams is unexported, and driving the clamp
// through List would need 500+ seeded members to observe — a fixture heavy enough that
// somebody deletes it. What that costs is stated rather than hidden: this pins the CLAMP,
// and the hop from clamp to query is covered only by List calling pageParams (handler.go)
// and not by an assertion here. Pinning is not enforcement, one level down.

func TestPageParams_ClampsAboveTheHardCap(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  int
	}{
		{"absent", "", defaultLimit},
		{"under the cap is honoured", "?limit=10", 10},
		{"exactly the cap is honoured", "?limit=500", maxLimit},
		{"over the cap is clamped", "?limit=501", maxLimit},
		{"absurdly over the cap is clamped", "?limit=100000000", maxLimit},
		{"negative is ignored, not passed through", "?limit=-5", defaultLimit},
		{"zero is ignored, not passed through", "?limit=0", defaultLimit},
		{"unparseable is ignored", "?limit=all", defaultLimit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := pageParams(httptest.NewRequest("GET", "/v1/service/members"+tc.query, nil))
			if got != tc.want {
				t.Fatalf("limit%s -> %d, want %d. This endpoint serves EVERY workspace's "+
					"roster; the page size is what bounds one request", tc.query, got, tc.want)
			}
			if got > maxLimit {
				t.Fatalf("limit%s -> %d, which is above the hard cap %d — 'the roster read can "+
					"never return more per page' is the comment on that constant", tc.query, got, maxLimit)
			}
		})
	}
}

func TestPageParams_OffsetIsNotNegative(t *testing.T) {
	for _, q := range []string{"?offset=-1", "?offset=abc", ""} {
		if _, off := pageParams(httptest.NewRequest("GET", "/v1/service/members"+q, nil)); off < 0 {
			t.Fatalf("offset%s -> %d; a negative offset reaches SQL", q, off)
		}
	}
}
