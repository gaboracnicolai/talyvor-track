package importer

// run_context_test.go — THE CONTEXT THAT IS SUPPOSED TO STOP AN IMPORT STOPS NOTHING.
//
// runner.go says it in as many words, right above the call:
//
//	⚠ ONLY THE RECORD IS DETACHED. r.imp.run below still takes the cancellable ctx: an import
//	MUST stop when the process is going down, and letting it outlive shutdown would be a
//	different and worse change.
//
// The record IS detached (context.WithoutCancel — #f0445e3's fix, and it holds). What the sentence
// then claims about `run` was never true: run() takes the ctx, hands it to the STORE, and consults
// it NOWHERE ELSE. There is no `ctx.Err()` in the row loop. So on SIGTERM the pipeline does not
// stop — every remaining row is pulled from the source, handed to a store whose every call now
// fails, and counted as a row that FAILED.
//
// TWO CONSEQUENCES, BOTH MEASURED HERE, AND THEY LAND ON DIFFERENT TRANSPORTS:
//
//  1. THE LIVE ONE (CSV — the only transport an operator can reach today, since the *_api pair has
//     never run against a real tenant). A 10,000-row import interrupted at row 3 reports
//     "9,998 row(s) failed" for 9,998 rows that were never attempted. `failed` is the column an
//     operator reads to decide whether to re-run and what to look at; it is being told a data
//     problem where there was a deploy.
//
//  2. THE PROVIDER-FACING ONE (linear_api / jira_api). Row supply on those transports is a paged
//     HTTP fetch, so "keep pulling rows" means "keep asking the provider for pages" — a process
//     that has been told to go down carries on draining a tenant's whole issue set over the
//     network, writing none of it. Both sources fetch on context.Background() as well, so nothing
//     downstream refuses the request either.
//
// ⚠ WHAT THE FIX MUST NOT DO, and this is why the third assertion below exists. Stopping the loop
// is one line; stopping it QUIETLY turns this defect into a worse one. run() breaking out with
// Skipped==0 and Errors empty gives terminalStatus() `unlanded == 0` — SUCCEEDED — so a deploy
// mid-import would record a truncated import as a complete one, and nothing would ever say
// otherwise. A stop must be reported as a stop.
//
// ⚠ THE RESIDUAL IS NAMED RATHER THAN QUIETLY LEFT: the two API sources call fetchPage with
// context.Background() (linear.go, jira.go), so a fetch ALREADY IN FLIGHT when the signal arrives
// still runs to its own 20s timeout, and retryer.wait is a bare time.Sleep of up to 30s per
// rate-limited attempt. This file's fix is at the pipeline, which is the one place that governs
// every source present and future; the per-source context is a separate change with 27 call sites
// and is written up in the queue.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/talyvor/track/internal/model"
)

// cancellingStore is the process going down mid-import: the first write cancels the runner's
// context and then fails the way any pgx call fails once its context is dead — and so does every
// write after it. It implements BOTH issueCreator and issueUpserter so the same stand-in serves the
// CSV path (Create) and the API path (UpsertByIdentifier, taken when a row carries an Identifier).
type cancellingStore struct {
	cancel context.CancelFunc
	calls  int32
}

func (s *cancellingStore) fail(ctx context.Context) error {
	if atomic.AddInt32(&s.calls, 1) == 1 {
		s.cancel()
	}
	return ctx.Err()
}

func (s *cancellingStore) Create(ctx context.Context, _ model.Issue) (*model.Issue, error) {
	return nil, s.fail(ctx)
}

func (s *cancellingStore) UpsertByIdentifier(ctx context.Context, _ model.Issue) (*model.Issue, bool, error) {
	return nil, false, s.fail(ctx)
}

// TestRun_Cancelled_StopsPullingRowsAndSaysSo — consequence (1), on the transport that runs today.
//
// The vacuity to watch: a source with one row would stop "correctly" for the trivial reason that
// there was nothing left to pull. So the fixture is deliberately long and the test asserts the
// store was reached, that the cancellation really happened, and that MOST of the source went
// untouched — a green here cannot come from an import that had nothing left to do.
func TestRun_Cancelled_StopsPullingRowsAndSaysSo(t *testing.T) {
	const rows = 50

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &cancellingStore{cancel: cancel}
	imp := New(store)

	src := &sliceSource{}
	for i := 1; i <= rows; i++ {
		src.rows = append(src.rows, SourceRow{Issue: model.Issue{Title: fmt.Sprintf("row-%d", i)}, RowNum: i})
	}

	out, err := imp.run(ctx, "ws-x", "team-x", src)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// ── the premises. Without these a green says nothing about shutdown. ──────────────
	if atomic.LoadInt32(&store.calls) == 0 {
		t.Fatal("the store was never called, so the context was never cancelled — this test " +
			"measured an uninterrupted import")
	}
	if ctx.Err() == nil {
		t.Fatal("the run context is still live — the shutdown this test exists for did not happen")
	}
	if len(src.rows) < 10 {
		t.Fatalf("the fixture holds %d rows — too few for 'it stopped early' to mean anything", len(src.rows))
	}

	// ── the finding ──────────────────────────────────────────────────────────────────
	// src.i is how many rows the pipeline PULLED. Every row past the cancellation was handed to a
	// store that could not write it and was then counted in `failed`.
	if src.i > 2 {
		t.Fatalf("the pipeline pulled %d of %d rows after its context was cancelled and counted "+
			"%d of them as failed — an import interrupted by a deploy reports rows that were "+
			"never attempted as rows that failed to import",
			src.i, rows, out.Skipped)
	}

	// The status is NOT asserted here, deliberately: this fixture's first row was attempted and
	// failed, so Skipped is 1 and terminalStatus reaches `failed` through the counters whether or
	// not it knows the run was cut short. An assertion that passes for the wrong reason is worth
	// nothing — the status is measured where it is armed, one test down.
}

// countingStore SUCCEEDS on every write and cancels the run context after n of them. It is the
// other half of shutdown, and the half the counters cannot see: the signal arrives BETWEEN rows,
// so no write ever fails, and the rows that were never pulled land in no counter at all.
type countingStore struct {
	cancel   context.CancelFunc
	cancelAt int
	calls    int32
}

func (s *countingStore) Create(_ context.Context, i model.Issue) (*model.Issue, error) {
	if int(atomic.AddInt32(&s.calls, 1)) == s.cancelAt {
		s.cancel()
	}
	return &i, nil
}

// TestRun_Cancelled_ATruncatedImportIsNeverReportedComplete — the trap the fix creates, and the
// reason the fix is more than one line.
//
// Stopping the loop is the easy half. Stopping it QUIETLY is a worse defect than the one being
// fixed: the rows past the cancellation are never pulled, so they are in NO counter, and
// terminalStatus's `unlanded == 0` is satisfied by an import that read three rows of fifty. The
// job row would then say `succeeded`, the payload row would be dropped, and nothing anywhere
// would record that forty-seven issues never arrived.
func TestRun_Cancelled_ATruncatedImportIsNeverReportedComplete(t *testing.T) {
	const rows, cancelAt = 50, 3

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &countingStore{cancel: cancel, cancelAt: cancelAt}
	imp := New(store)

	src := &sliceSource{}
	for i := 1; i <= rows; i++ {
		src.rows = append(src.rows, SourceRow{Issue: model.Issue{Title: fmt.Sprintf("row-%d", i)}, RowNum: i})
	}

	out, err := imp.run(ctx, "ws-x", "team-x", src)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// ── the premises ─────────────────────────────────────────────────────────────────
	if ctx.Err() == nil {
		t.Fatal("the run context is still live — the shutdown this test exists for did not happen")
	}
	if out.Imported != cancelAt {
		t.Fatalf("Imported=%d, want %d — this fixture's writes all SUCCEED, so anything else means "+
			"the import stopped for some reason other than the cancellation", out.Imported, cancelAt)
	}
	// THE ONE THAT ARMS EVERYTHING BELOW. If a write had failed, the counters would reach a
	// non-succeeded status on their own and the assertions would pass without reading `stopped`
	// at all — which is exactly how a guard ends up green for the wrong reason.
	if out.Skipped != 0 || out.Refused != 0 {
		t.Fatalf("Skipped=%d Refused=%d, want 0/0 — with a row in a counter this test can no "+
			"longer tell a status that knows the run was cut short from one that counted a failure",
			out.Skipped, out.Refused)
	}
	if src.i >= rows {
		t.Fatalf("the pipeline pulled all %d rows, so nothing was truncated and there is nothing "+
			"here to measure", rows)
	}

	// ── the finding ──────────────────────────────────────────────────────────────────
	if got := terminalStatus(out); got == JobSucceeded {
		t.Fatalf("an import that read %d of %d rows before the process shut down records itself "+
			"as %q — every unread row is in no counter, so `unlanded == 0` is true of a truncated "+
			"import and the job row claims a complete one", src.i, rows, got)
	}
	if got := terminalStatus(out); got != JobPartial {
		t.Fatalf("terminalStatus = %q, want %q — %d rows DID land, so the import was not a failure "+
			"either", got, JobPartial, out.Imported)
	}
	summary := summarise(out)
	if summary == "" {
		t.Fatal("the import stopped mid-source and rendered an EMPTY error_summary — the job row " +
			"is non-terminal-looking to an operator who is told nothing at all about why")
	}
	if !strings.Contains(summary, "stopped") {
		t.Fatalf("error_summary = %q — it does not say the import was STOPPED, so an operator "+
			"reads an interruption as a data problem", summary)
	}
	// AND IT MUST BE A SENTENCE, NOT A DANGLING CLAUSE. summarise joins its counting clauses and
	// then appends "; first: <the first per-row message>". A stopped run is the first case in this
	// package's life where the error list is non-empty and EVERY counter is zero, so without a
	// clause of its own the whole summary renders as `; first: stopped after row 3: …` — leading
	// separator and all. This is what tells a clause in summarise from the word merely appearing
	// in the row message it borrowed.
	if strings.HasPrefix(summary, ";") {
		t.Fatalf("error_summary = %q — it opens with the clause separator, so summarise rendered "+
			"no counting clause at all and the operator's first line is a fragment", summary)
	}
}

// TestRun_Cancelled_StopsAskingTheProviderForPages — consequence (2), measured at the wire.
//
// The provider is the instrument: it counts the page requests that arrive AFTER the cancellation.
// A source that stopped asking is the only way that count is zero.
func TestRun_Cancelled_StopsAskingTheProviderForPages(t *testing.T) {
	const pages = 8

	var served, servedAfterCancel int32
	cancelled := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&served, 1)
		select {
		case <-cancelled:
			atomic.AddInt32(&servedAfterCancel, 1)
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		// One issue per page so the cancellation lands on page 1 and every later page is a page
		// the process asked for after it was told to stop.
		writeRaw(w, linPage(int(n) < pages, fmt.Sprintf("c%d", n),
			linNode(fmt.Sprintf("ENG-%d", n), "Todo", 1)))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &cancellingStore{cancel: func() { cancel(); close(cancelled) }}
	imp := New(store)

	src := newLinearSource("api-key", "TEAM-UUID", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep

	if _, err := imp.run(ctx, "ws-x", "team-x", src); err != nil {
		t.Fatalf("run: %v", err)
	}

	// ── the premises ─────────────────────────────────────────────────────────────────
	if atomic.LoadInt32(&store.calls) == 0 {
		t.Fatal("the store was never called, so the context was never cancelled — this test " +
			"measured an uninterrupted import")
	}
	if ctx.Err() == nil {
		t.Fatal("the run context is still live — the shutdown this test exists for did not happen")
	}
	// THE ANTI-VACUITY THAT MATTERS. "No pages after cancellation" is trivially true of a fixture
	// with one page. This asserts the provider really had more to give: page 1 said hasNextPage.
	if atomic.LoadInt32(&served) == 0 {
		t.Fatal("the provider was never asked for a page — the source never ran")
	}
	if pages < 2 {
		t.Fatal("the fixture serves a single page, so 'it stopped fetching' cannot be measured")
	}

	// ── the finding ──────────────────────────────────────────────────────────────────
	if n := atomic.LoadInt32(&servedAfterCancel); n != 0 {
		t.Fatalf("the provider was asked for %d further page(s) AFTER the import's context was "+
			"cancelled (%d requests in total, of a %d-page tenant) — a process that has been told "+
			"to shut down carries on draining the tenant over the network and writes none of it",
			n, atomic.LoadInt32(&served), pages)
	}
}
