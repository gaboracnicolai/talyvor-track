package httpx_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/talyvor/track/internal/httpx"
)

// body_cap_reach_test.go — DOES EACH BODY CAP BITE, AND AT ITS OWN VALUE?
//
// ⚠ MEASURED BEFORE THIS FILE EXISTED (W3.50, tab-k4m7,
// ~/talyvor-queue/w350-enforcement-reach-k4m7.py). The probe DISABLES a bound at its
// enforcement site — rather than nudging its value, which is what W3.49 did — and asks
// whether any test's outcome depends on the bound applying at all. Result on `6fdb27d`:
//
//	DefaultMaxBody        raised to 1 TiB -> RED   (already defended)
//	ImportMaxBody         raised to 1 TiB -> GREEN, whole suite
//	GitHubWebhookMaxBody  raised to 1 TiB -> GREEN, whole suite
//
// ⚠⚠ AND THE REASON IS A TEST THAT LOOKS LIKE IT COVERS THE IMPORT CAP AND CANNOT.
// TestBodyLimit_ImportRouteHasHigherCap posts a 2 MiB body and asserts the import route
// does NOT 413 while the default route does. Both halves stay true when ImportMaxBody is
// raised to a terabyte — 2 MiB is under 96 MiB and under 1 TiB alike. What it pins is the
// RELATIVE claim "import > default", which is a real claim and is not this one. Raise the
// DEFAULT and it reds; raise the IMPORT cap, the thing in its name, and it cannot see it.
//
// ⚠ SO THIS FILE ASSERTS THE VALUE THE CAP ACTUALLY BITES AT, not that some cap exists.
// http.MaxBytesReader surfaces *http.MaxBytesError with the limit it enforced, so one
// over-size read reports which constant was installed on that path — a cap pointed at the
// wrong constant is caught, not just a cap that is missing.
//
// ⚠ NO 96 MiB IS EVER ALLOCATED. The body is a generator, not a buffer: countingReader
// emits bytes on demand and the handler discards them, so the cost is a memcpy of the cap
// and not a resident allocation. A test that had to hold 96 MiB would be a test somebody
// deletes the first time CI is slow.

// countingReader yields n bytes of 'a' without ever holding them.
type countingReader struct{ left int64 }

func (c *countingReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > c.left {
		n = c.left
	}
	for i := int64(0); i < n; i++ {
		p[i] = 'a'
	}
	c.left -= n
	return int(n), nil
}

// drainLimit sends `size` bytes to `path` through the real BodyLimit middleware and reports
// the limit the middleware installed, as observed from the error the read produced.
// (0, nil) means the body was accepted whole.
func drainLimit(t *testing.T, path string, size int64) (int64, error) {
	t.Helper()
	var readErr error
	h := httpx.BodyLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.Copy(io.Discard, r.Body)
	}))
	req := httptest.NewRequest(http.MethodPost, path, &countingReader{left: size})
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if readErr == nil {
		return 0, nil
	}
	var mbe *http.MaxBytesError
	if errors.As(readErr, &mbe) {
		return mbe.Limit, nil
	}
	return 0, readErr
}

func TestBodyCapReach_EachRouteClassBitesAtItsOwnValue(t *testing.T) {
	cases := []struct {
		name string
		path string
		want int64
	}{
		{"default JSON route", "/v1/issues", httpx.DefaultMaxBody},
		{"multipart CSV import", "/v1/import/linear", httpx.ImportMaxBody},
		{"GitHub webhook", "/v1/webhooks/github", httpx.GitHubWebhookMaxBody},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// One byte over: the cap must bite, and must report ITS OWN value. A cap
			// pointed at the wrong constant fails here rather than silently passing
			// because *some* cap fired.
			got, err := drainLimit(t, tc.path, tc.want+1)
			if err != nil {
				t.Fatalf("reading %d bytes on %s failed for an unexpected reason: %v", tc.want+1, tc.path, err)
			}
			if got != tc.want {
				if got == 0 {
					t.Fatalf("%s accepted %d bytes whole — the cap did not bite. This route is "+
						"supposed to be bounded by %d", tc.path, tc.want+1, tc.want)
				}
				t.Fatalf("%s enforced a limit of %d, but this route's declared cap is %d — the "+
					"middleware installed the wrong constant for this path", tc.path, got, tc.want)
			}

			// Exactly at the cap must be ACCEPTED. Without this half the assertion above
			// is satisfied by a cap of one byte, and "bounded" would mean "unusable".
			if got, err := drainLimit(t, tc.path, tc.want); err != nil || got != 0 {
				t.Fatalf("%s rejected a body of exactly %d bytes (limit reported %d, err %v) — "+
					"the cap must admit its own value, not bite one byte early", tc.path, tc.want, got, err)
			}
		})
	}
}
