package importer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync/atomic"
	"testing"

	"github.com/talyvor/track/internal/safehttp"
)

// api_source_ssrf_test.go — SEC-6, the COMPLETENESS half.
//
// WHY THIS EXISTS RATHER THAN A SECOND HAND-WRITTEN PER-PROVIDER TEST. `jira_ssrf_test.go` names
// the Jira client directly, and `internal/automation/slack_ssrf_test.go` names Slack. Linear —
// the other transport that fetches a workspace-supplied base URL — was named by NEITHER, and the
// hole was invisible: MEASURED at `2e60259`, replacing linear.go's `http: clientOrSafe(httpc)`
// with a plain `&http.Client{}` left `go test ./...` GREEN ACROSS THE WHOLE REPOSITORY, while the
// byte-identical mutation of jira.go went RED. A per-file test can only ever cover the files
// somebody remembered to write one for.
//
// So the population is DERIVED, not typed: `validAPISourceTypes` is the allowlist `JobHandler.create`
// consults to decide whether an *_api import job may be enqueued at all. Every source type a caller
// can get past the HTTP boundary is swept here, through `Runner.sourceFor` — the same dispatch
// production uses — with NO injected client, so what is exercised is the production default.
//
// ⚠ THE `errors.Is(..., safehttp.ErrBlockedAddress)` ASSERTION IS THE LOAD-BEARING ONE, and it is
// what distinguishes this from the test shape it supplements. "the internal server got no hits AND
// the error was non-nil" is ALSO satisfied by a provider client that is broken outright and fetches
// nothing at all — measured: with `linearClient.fetchPage` stubbed to fail before it dials,
// hits==0 and err!=nil both hold. Demanding the BLOCKED-ADDRESS error specifically means this test
// can only be satisfied by a guard that ran, not by an importer that died first.

// ssrfConfigStub is the C.1 credential store stubbed down to the one field this sweep varies: the
// workspace-supplied base URL. Returning a token for every provider is deliberate — a transport
// that refuses internal addresses only when it happens to be unconfigured is not a guard.
type ssrfConfigStub struct{ baseURL string }

func (s ssrfConfigStub) GetDecrypted(_ context.Context, _, _ string) (token, projectKey, baseURL string, err error) {
	return "user@example.com:token", "TEAM", s.baseURL, nil
}

func TestAPISources_SSRF_EveryEnqueueableProviderRefusesAnInternalBaseURL(t *testing.T) {
	// POPULATION FLOOR. An empty allowlist would make every assertion below unreachable and this
	// test green for the worst possible reason. The count is not pinned — a new provider should be
	// swept, not rejected — but the set may not be empty.
	types := make([]string, 0, len(validAPISourceTypes))
	for st, ok := range validAPISourceTypes {
		if ok {
			types = append(types, st)
		}
	}
	sort.Strings(types)
	if len(types) == 0 {
		t.Fatal("validAPISourceTypes is empty — this sweep would assert nothing; if live API import was " +
			"removed, remove this test deliberately rather than letting it pass over an empty set")
	}

	var hits int32
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer internal.Close()

	swept := 0
	for _, sourceType := range types {
		t.Run(sourceType, func(t *testing.T) {
			atomic.StoreInt32(&hits, 0)

			// NO WithHTTPClient: the whole point is the client production builds when nobody
			// injects one. WithProviderConfig is what makes an *_api job runnable at all.
			r := NewRunner(nil, nil).WithProviderConfig(ssrfConfigStub{baseURL: internal.URL})

			src, err := r.sourceFor(context.Background(), &Job{
				ID: "job-ssrf", WorkspaceID: "ws-ssrf", TeamID: "team-ssrf", SourceType: sourceType,
			})
			if err != nil {
				// An enqueueable source_type the runner cannot build is its own defect: the HTTP
				// boundary accepts a job nothing can run.
				t.Fatalf("%s is in validAPISourceTypes but Runner.sourceFor refused it: %v", sourceType, err)
			}

			row, ok := src.Next()
			if !ok {
				t.Fatalf("%s: source reported clean exhaustion against a blocked address — a refused "+
					"fetch must surface as an error row, not as an empty successful import", sourceType)
			}

			if got := atomic.LoadInt32(&hits); got != 0 {
				t.Errorf("SSRF: %s REACHED the internal loopback %s (%d hits) — a workspace-supplied "+
					"base URL must never be dialed when it resolves to a non-public address",
					sourceType, internal.URL, got)
			}
			if !errors.Is(row.Err, safehttp.ErrBlockedAddress) {
				t.Errorf("SSRF: %s did not refuse with safehttp.ErrBlockedAddress; got %v. Reaching "+
					"nothing is not enough — that is equally true of a transport that is simply "+
					"broken. The refusal must come from the address guard.", sourceType, row.Err)
			}
			swept++
		})
	}

	// The loop above runs in subtests; a `continue` or an early return added later could skip one
	// silently. Assert the sweep covered the whole derived population.
	if swept != len(types) {
		t.Errorf("swept %d of %d enqueueable API source types (%v) — every one must be asserted",
			swept, len(types), types)
	}
}
