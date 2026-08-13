package importer

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/talyvor/track/internal/model"
)

// api_pagination_stall_test.go — THE NO-PROGRESS GUARD BOTH API SOURCES ALREADY CARRY COULD ONLY
// EVER SEE AN EMPTY PAGE, AND A PAGE WITH ROWS IS THE CASE THAT WALKS FOR EVER.
//
// api_pagination_termination_test.go states the rule this package terminates under: "never a silent
// stop that reports success, never an unbounded loop", and jira.go's own comment above the guard
// says "Both ways that can fail to terminate are reported as an error row ... rather than swallowed
// as a clean stop or walked for ever". BOTH is two, and there are three. In each source the
// repeated-cursor check sits BELOW
//
//	if len(page.issues) > 0 { s.emptyPages = 0; break }
//
// so a provider that returns ROWS while handing back the cursor it was given never reaches it.
//
// MEASURED at 403e32b6 on the shipped sources, canned httptest provider, no credentials — one page
// served unconditionally, `{issues:[PROJ-1], isLast:false, nextPageToken:"stuck"}` and its Linear
// twin `{nodes:[ENG-1], pageInfo:{hasNextPage:true, endCursor:"stuck"}}`:
//
//	jira   ⇒ 40 rows in 40 HTTP calls and rising, all "PROJ-1". No error row. Cap-limited, not ended.
//	linear ⇒ 40 rows in 40 HTTP calls and rising, all "ENG-1".  No error row. Cap-limited, not ended.
//
// ⚠ AND THE PIPELINE DOES NOT STOP IT EITHER, WHICH IS WHERE THE COST IS. run() consults ctx.Err()
// once per row and has no other exit, so the only thing that ends this import is the process dying.
// Measured through the real write pipeline (importer.New(store).run) with a 2-second context as a
// stand-in for the process lifetime:
//
//	48,116 issue writes and 48,117 provider HTTP calls in 2s — for ONE issue that exists once.
//
// That is ~24k upserts/second into Postgres and ~24k requests/second at the provider, and in
// production the context is the PROCESS context: it runs until SIGTERM. runner.drain calls
// execute synchronously in one goroutine, so while it runs NO OTHER IMPORT JOB IN ANY WORKSPACE
// is claimed — one stuck provider wedges import for the whole deployment.
//
// ⚠ WHY A REPEATED CURSOR IS NOT A HYPOTHETICAL PROVIDER BUG. Both cursors are OPAQUE tokens the
// client echoes back verbatim, and both requests are POSTs whose body carries the cursor. Anything
// between this process and the provider that answers a repeated POST with a cached body — a
// corporate egress proxy, a self-hosted Jira Server/DC behind a cache, the `baseURL` this importer
// makes configurable precisely so it can point at one — reproduces it exactly. The source cannot
// tell that from a provider that genuinely stopped advancing, and it does not need to: in both
// cases the next request is one it has already made and the answer is one it has already read.
//
// WHAT IS PINNED HERE, in the shape this file's siblings already use: a page that carries rows and
// does not advance the cursor yields ITS OWN ROWS (they are real — the same reasoning (4) applies
// to an absent endCursor) and then ONE error row naming the lack of progress, and makes no further
// request. The second half of each test is the anti-vacuity control and is not decoration: "refuse
// every multi-page import" satisfies the first half completely, so a well-behaved provider whose
// cursor DOES advance must still drain to completion with no error at all.

// stuckPageProvider serves one body unconditionally, whatever cursor the source sends — the shape a
// cache in front of the provider produces. It counts calls so an unbounded source is observable as
// requests rather than only as rows.
func stuckPageProvider(body string, calls *int32) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(calls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
}

// (1) JIRA — rows plus a repeated nextPageToken. The twin of (5) in the termination file, which
// covers the same provider behaviour on an EMPTY page and is the only half that was ever guarded.
func TestJiraSource_ARepeatedPageTokenWithRowsIsReportedNotWalkedForEver(t *testing.T) {
	var calls int32
	body := fmt.Sprintf(`{"issues":[%s],"isLast":false,"nextPageToken":"stuck"}`,
		jiraIssueJSON("PROJ-1", "A", "", "To Do", "High"))
	srv := httptest.NewServer(stuckPageProvider(body, &calls))
	defer srv.Close()
	src := newJiraSource(context.Background(), "me@corp.com:tok", "PROJ", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep

	got, errs, hitCap := drainSource(t, src, 40)
	if hitCap {
		t.Fatalf("source never terminated: %d rows and still going (%v) in %d HTTP calls — a page that "+
			"does not advance the token is an infinite import, not a page", len(got), got, atomic.LoadInt32(&calls))
	}
	// ⚠ TWO ROWS, NOT ONE, AND THE SECOND IS NOT AN OVERSIGHT. A stall is only detectable BY making
	// the second request and comparing the token it returns with the one it was given, so page two
	// has already been fetched and has already carried its rows by the time anything is known. Those
	// rows are YIELDED rather than dropped: "the same token must mean the same page" is an inference
	// about a provider that is by now demonstrably misbehaving, and this package's standing rule is
	// that a source never silently discards rows a provider actually sent — the empty-endCursor
	// branch yields its page for the same reason. The duplicate is idempotent downstream for every
	// row that carries an identifier, which source.go records is now every transport.
	if want := []string{"PROJ-1", "PROJ-1"}; !equalStrings(got, want) {
		t.Fatalf("rows = %v, want %v — page two's rows are real whatever the token says", got, want)
	}
	if len(errs) != 1 || !strings.Contains(errs[0], "no progress") {
		t.Fatalf("errors = %v, want exactly one naming the lack of progress — a truncated import must "+
			"not look complete", errs)
	}
	// Two calls, not one: the first page is legitimately fetched, and the SECOND fetch is the one
	// that reveals the token did not move. What must not happen is a third.
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Fatalf("http calls = %d, want 2 — the source must stop requesting once the token repeats", n)
	}
}

// (2) LINEAR — the mirror: rows plus a repeated endCursor.
func TestLinearSource_ARepeatedCursorWithRowsIsReportedNotWalkedForEver(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(stuckPageProvider(linPage(true, "stuck", linNode("ENG-1", "Done", 1)), &calls))
	defer srv.Close()
	src := newLinearSource(context.Background(), "tok", "team-id", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep

	got, errs, hitCap := drainSource(t, src, 40)
	if hitCap {
		t.Fatalf("source never terminated: %d rows and still going (%v) in %d HTTP calls", len(got), got,
			atomic.LoadInt32(&calls))
	}
	if want := []string{"ENG-1", "ENG-1"}; !equalStrings(got, want) {
		t.Fatalf("rows = %v, want %v — page two's rows are real whatever the cursor says (see (1))", got, want)
	}
	if len(errs) != 1 || !strings.Contains(errs[0], "no progress") {
		t.Fatalf("errors = %v, want exactly one naming the lack of progress", errs)
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Fatalf("http calls = %d, want 2 — the source must stop requesting once the cursor repeats", n)
	}
}

// (3) ANTI-VACUITY, JIRA. "Stop when the token repeats" is satisfied completely by "stop after the
// second page, always". A three-page import whose token DOES advance must still deliver all three
// pages with no error — this is what fails if the guard is keyed on anything but the repeat.
func TestJiraSource_AnAdvancingTokenStillDrainsEveryPage(t *testing.T) {
	var calls int32
	pages := map[string]string{
		"":   fmt.Sprintf(`{"issues":[%s],"isLast":false,"nextPageToken":"t1"}`, jiraIssueJSON("PROJ-1", "A", "", "To Do", "High")),
		"t1": fmt.Sprintf(`{"issues":[%s],"isLast":false,"nextPageToken":"t2"}`, jiraIssueJSON("PROJ-2", "B", "", "To Do", "High")),
		"t2": fmt.Sprintf(`{"issues":[%s],"isLast":true}`, jiraIssueJSON("PROJ-3", "C", "", "Done", "Low")),
	}
	srv := httptest.NewServer(tokenKeyedJira(t, pages, &calls))
	defer srv.Close()
	src := newJiraSource(context.Background(), "me@corp.com:tok", "PROJ", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep

	got, errs, hitCap := drainSource(t, src, 12)
	if hitCap {
		t.Fatalf("source never terminated: %v", got)
	}
	if len(errs) != 0 {
		t.Fatalf("unexpected errors on a well-behaved provider: %v — the guard must fire on a REPEATED "+
			"token, not on paging", errs)
	}
	if want := []string{"PROJ-1", "PROJ-2", "PROJ-3"}; !equalStrings(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
}

// (4) ANTI-VACUITY, LINEAR. Same control on the Relay side.
func TestLinearSource_AnAdvancingCursorStillDrainsEveryPage(t *testing.T) {
	pages := []string{
		linPage(true, "c1", linNode("ENG-1", "Done", 1)),
		linPage(true, "c2", linNode("ENG-2", "Done", 1)),
		linPage(false, "", linNode("ENG-3", "Done", 1)),
	}
	srv := httptest.NewServer(cannedPages(pages, linPage(false, "")))
	defer srv.Close()
	src := newLinearSource(context.Background(), "tok", "team-id", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep

	got, errs, hitCap := drainSource(t, src, 12)
	if hitCap {
		t.Fatalf("source never terminated: %v", got)
	}
	if len(errs) != 0 {
		t.Fatalf("unexpected errors on a well-behaved provider: %v", errs)
	}
	if want := []string{"ENG-1", "ENG-2", "ENG-3"}; !equalStrings(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
}

// stallCounter counts issue writes so the pipeline test can report the cost in the unit that
// matters — rows written into a workspace for an issue that exists once.
type stallCounter struct{ n int32 }

func (c *stallCounter) Create(_ context.Context, _ model.Issue) (*model.Issue, error) {
	atomic.AddInt32(&c.n, 1)
	return &model.Issue{}, nil
}

// (5) THE PIPELINE, which is where the cost actually lands. run() has exactly one exit other than
// the source saying stop — ctx.Err() — so before the fix this measured 48,116 writes in the two
// seconds the context was given. The assertion is deliberately not a number: it is that run()
// RETURNS ON ITS OWN, with the context still alive and unspent.
func TestImporterRun_AStalledSourceEndsTheJobRatherThanTheProcess(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(stuckPageProvider(linPage(true, "stuck", linNode("ENG-1", "Done", 1)), &calls))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	src := newLinearSource(ctx, "tok", "team-id", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep

	store := &stallCounter{}
	done := make(chan *ImportResult, 1)
	go func() {
		res, err := New(store).run(ctx, "ws-1", "team-1", src)
		if err != nil {
			t.Errorf("run returned an error: %v", err)
		}
		done <- res
	}()

	var res *ImportResult
	select {
	case res = <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("run() did not return in 5s: %d writes, %d provider calls — the import is bounded only "+
			"by the process lifetime", atomic.LoadInt32(&store.n), atomic.LoadInt32(&calls))
	}
	if ctx.Err() != nil {
		t.Fatalf("run() returned only because the context expired — that is the pre-fix behaviour")
	}
	if res.stopped {
		t.Fatalf("result reports stopped=true, i.e. cancellation ended it rather than the source")
	}
	// TWO writes — the two pages the source fetched before it could know the second was a re-read
	// (see (1)) — against 48,116 before the fix. The assertion that matters is that this is a small
	// CONSTANT rather than a function of how long the process stays up.
	if got := atomic.LoadInt32(&store.n); got != 2 {
		t.Fatalf("issue writes = %d, want 2 — one per fetched page, bounded by the stall guard", got)
	}
	// The stall is a Skipped error row, which is what run() counts an unlanded row as and what keeps
	// terminalStatus from reporting this truncated import as succeeded.
	if res.Skipped != 1 {
		t.Fatalf("skipped = %d, want 1 (the error row) — an incomplete import that counts nothing "+
			"unlanded is one terminalStatus reports as succeeded", res.Skipped)
	}
}
