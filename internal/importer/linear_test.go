package importer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/talyvor/track/internal/model"
)

// ── T8 Build C.3 — Linear GraphQL source proofs (httptest canned responses, no live API). ──

func noopSleep(time.Duration) {}

// writeRaw serves a canned test fixture. It takes io.Writer (not http.ResponseWriter) so the response bytes
// are opaque test JSON, not templated HTML — these are provider-API stubs, not a rendered web page.
func writeRaw(w io.Writer, s string) { _, _ = io.WriteString(w, s) }

func linNode(id, status string, prio int) string {
	return fmt.Sprintf(`{"identifier":%q,"title":"T-%s","description":"d","state":{"name":%q},"priority":%d,"labels":{"nodes":[{"name":"bug"}]}}`, id, id, status, prio)
}

func linPage(hasNext bool, cursor string, nodes ...string) string {
	return fmt.Sprintf(`{"data":{"team":{"issues":{"pageInfo":{"hasNextPage":%t,"endCursor":%q},"nodes":[%s]}}}}`,
		hasNext, cursor, strings.Join(nodes, ","))
}

// fakeUpsertStore implements BOTH issueCreator and issueUpserter, so New() detects the upsert path (the API
// route) and run() upserts API issues (Identifier set).
type fakeUpsertStore struct {
	created  []model.Issue
	upserted []model.Issue
}

func (f *fakeUpsertStore) Create(_ context.Context, i model.Issue) (*model.Issue, error) {
	f.created = append(f.created, i)
	return &i, nil
}

func (f *fakeUpsertStore) UpsertByIdentifier(_ context.Context, i model.Issue) (*model.Issue, bool, error) {
	f.upserted = append(f.upserted, i)
	return &i, true, nil
}

func drainIDs(t *testing.T, src IssueSource) []string {
	t.Helper()
	var ids []string
	for {
		row, ok := src.Next()
		if !ok {
			break
		}
		if row.Err != nil {
			t.Fatalf("unexpected source error: %v", row.Err)
		}
		ids = append(ids, row.Issue.Identifier)
	}
	return ids
}

// (a) LINEAR PAGINATION: two pages (hasNextPage=true then false) → all issues yielded, Identifier=provider-key.
func TestLinearSource_Paginates(t *testing.T) {
	srv := httptest.NewServer(cannedPages([]string{
		linPage(true, "c1", linNode("ENG-1", "Todo", 1), linNode("ENG-2", "Done", 2)),
		linPage(false, "", linNode("ENG-3", "In Progress", 3)),
	}, linPage(false, "")))
	defer srv.Close()

	src := newLinearSource("api-key", "TEAM-UUID", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep
	ids := drainIDs(t, src)
	if len(ids) != 3 || ids[0] != "ENG-1" || ids[2] != "ENG-3" {
		t.Fatalf("paginated identifiers = %v, want [ENG-1 ENG-2 ENG-3]", ids)
	}
}

// (b) LINEAR RATE-LIMIT: HTTP 400 + extensions.code=RATELIMITED is retryable (retry then succeed); a give-up
// is a DISTINCT errRateLimited; and a 200 carrying errors[] is an error, not a silent empty success.
func TestLinearSource_RateLimit_RetriesThenSucceeds(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			writeRaw(w, `{"errors":[{"message":"slow down","extensions":{"code":"RATELIMITED"}}]}`)
			return
		}
		writeRaw(w, linPage(false, "", linNode("ENG-9", "Todo", 1)))
	}))
	defer srv.Close()

	src := newLinearSource("k", "TEAM", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep
	row, ok := src.Next()
	if !ok || row.Err != nil {
		t.Fatalf("400/RATELIMITED must be retried then succeed: ok=%v err=%v", ok, row.Err)
	}
	if row.Issue.Identifier != "ENG-9" {
		t.Fatalf("identifier = %q, want ENG-9", row.Issue.Identifier)
	}
}

func TestLinearClient_RateLimitGiveUp_IsDistinct(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		writeRaw(w, `{"errors":[{"extensions":{"code":"RATELIMITED"}}]}`)
	}))
	defer srv.Close()
	c := newLinearClient("k", "TEAM", srv.URL, srv.Client())
	c.retry.sleep = noopSleep
	_, err := c.fetchPage(context.Background(), "")
	if !errors.Is(err, errRateLimited) {
		t.Fatalf("a rate-limit give-up must be the distinct errRateLimited signal, got %v", err)
	}
}

func TestLinearSource_200WithErrors_NotSilent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeRaw(w, `{"errors":[{"message":"boom"}]}`) // HTTP 200 + errors[]
	}))
	defer srv.Close()
	src := newLinearSource("k", "TEAM", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep
	row, ok := src.Next()
	if !ok || row.Err == nil {
		t.Fatal("a 200 with errors[] must surface an error, NOT a silent empty success")
	}
}

// (f) FETCH-FAILURE OBSERVABILITY (the gate): page 1 succeeds, page 2 errors → run() imports page 1 AND
// records the failure (skipped>0) → terminalStatus = partial, NOT a silent complete import.
func TestRun_FetchFailureMidPagination_Observable(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) == 1 {
			writeRaw(w, linPage(true, "c1", linNode("ENG-1", "Todo", 1), linNode("ENG-2", "Todo", 1)))
			return
		}
		w.WriteHeader(http.StatusInternalServerError) // page 2 fails
		writeRaw(w, `{"errors":[{"message":"server exploded"}]}`)
	}))
	defer srv.Close()

	imp := New(&fakeUpsertStore{})
	src := newLinearSource("k", "TEAM", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep
	out, err := imp.run(context.Background(), "wsA", "teamA", src)
	if err != nil {
		t.Fatal(err)
	}
	if out.Imported != 2 {
		t.Fatalf("page-1 issues must import: imported=%d, want 2", out.Imported)
	}
	if out.Skipped == 0 {
		t.Fatal("the page-2 fetch failure must be recorded (skipped>0) — a truncated import must be observable")
	}
	if terminalStatus(out) != JobPartial {
		t.Fatalf("status=%s, want partial (a truncated import must NOT look complete/succeeded)", terminalStatus(out))
	}
	if !strings.Contains(strings.Join(out.Errors, " "), "fetch") {
		t.Fatalf("error_summary must name the fetch failure: %v", out.Errors)
	}
}

// cannedPages serves pages[0], pages[1], … in order, then fallback for any further request.
func cannedPages(pages []string, fallback string) http.Handler {
	var i int32
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idx := int(atomic.AddInt32(&i, 1)) - 1
		w.Header().Set("Content-Type", "application/json")
		if idx < len(pages) {
			writeRaw(w, pages[idx])
			return
		}
		writeRaw(w, fallback)
	})
}
