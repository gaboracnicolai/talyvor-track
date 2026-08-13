package importer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// provider_context_test.go — THE OTHER HALF OF #124: THE PIPELINE STOPS, THE PROVIDER FETCH DOES NOT.
//
// #124 made run()'s row loop honour its context, so an import stops asking for more rows once the
// process is going down. What it named and did not fix is everything BELOW that loop: both API
// sources called fetchPage with context.Background(), and retryer.wait was a bare time.Sleep. So a
// fetch ALREADY IN FLIGHT when SIGTERM arrives ran to the client's own 20s timeout, and a
// rate-limited page slept up to 30s per attempt on top — all of it uncancellable, all of it AFTER
// the signal.
//
// ⚠⚠ WHY THAT IS NOT COSMETIC: IT REOPENS THE DEFECT #f0445e3 CLOSED. Runner.execute writes the
// terminal status through context.WithoutCancel precisely so a shutdown records what happened —
// but that write only happens if the goroutine REACHES it. A Kubernetes terminationGracePeriod is
// 30 seconds by default; ~80s of uncancellable work after the signal means SIGKILL arrives first,
// no Go code runs, and the job row strands in `running` with finished_at NULL forever. That is the
// exact row runner_shutdown_terminal_state_test.go exists to prevent, reached through a different
// door.
//
// ⚠ THE INSTRUMENT IS THE CLIENT'S OWN RETURN VALUE, AND THE FIRST INSTRUMENT WAS WRONG. The
// obvious probe is a handler that waits on r.Context() and reports whether the client hung up —
// and it does not work: MEASURED here, a cancelled request makes postJSON return
// `context canceled` IMMEDIATELY while the SERVER's handler runs its full delay with
// r.Context() still live. Go's server does not cancel a handler's context the moment a client
// goes away. A probe built on it would have called the fix a failure. What is categorical, and
// what this file asserts, is what the SOURCE returns: an abandoned fetch surfaces
// context.Canceled through SourceRow.Err, and a fetch nothing could stop returns a perfectly good
// row three seconds late. Those are different values, not different durations.

// slowProvider answers correctly, but only after `delay` — long enough that the import can be
// cancelled while the fetch is provably in flight. inFlight closes on the first request, so the
// cancellation lands at a measured moment rather than a guessed one.
type slowProvider struct {
	inFlight chan struct{}
	// release lets the handler return the moment the measurement is IN, so httptest's Close does
	// not block on a sleeping handler. Without it these two tests added six seconds to a package
	// that runs against a 120s CI budget and has been MEASURED at 91% of it on a bad runner draw —
	// a guard that makes the suite likelier to time out is a guard with a cost nobody priced.
	// The delay is still the real ceiling: it is what an UNCANCELLED fetch would take to complete.
	release chan struct{}
	body    string
	delay   time.Duration
}

func newSlowProvider(body string) *slowProvider {
	return &slowProvider{
		inFlight: make(chan struct{}),
		release:  make(chan struct{}),
		body:     body,
		delay:    3 * time.Second,
	}
}

func (p *slowProvider) handler() http.Handler {
	var announced bool
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !announced {
			announced = true
			close(p.inFlight)
		}
		select {
		case <-p.release: // the test has its verdict — stop holding the server open
			return
		case <-time.After(p.delay):
		}
		w.Header().Set("Content-Type", "application/json")
		writeRaw(w, p.body)
	})
}

// assertFetchIsAbandoned drives one source's Next() in a goroutine, cancels while the fetch is in
// flight, and asserts on WHAT THE SOURCE RETURNED.
func assertFetchIsAbandoned(t *testing.T, p *slowProvider, cancel context.CancelFunc, next func() (SourceRow, bool), transport string) {
	t.Helper()

	type result struct {
		row     SourceRow
		ok      bool
		elapsed time.Duration
	}
	done := make(chan result, 1)
	go func() {
		start := time.Now()
		row, ok := next()
		done <- result{row, ok, time.Since(start)}
	}()

	select {
	case <-p.inFlight:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: the provider was never asked for a page — nothing was in flight to cancel, "+
			"so this test measured nothing", transport)
	}
	cancel()

	var got result
	select {
	case got = <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s: Next() never returned at all", transport)
	}
	close(p.release)
	t.Logf("%s: Next() returned after %v with err=%v", transport, got.elapsed.Round(time.Millisecond), got.row.Err)

	if got.row.Err == nil {
		t.Fatalf("%s: the fetch RAN TO COMPLETION and yielded a clean row %.0fs after the import's "+
			"context was cancelled — the request runs on a context nothing can stop, so a process "+
			"told to shut down holds a provider request open to the client's own 20s timeout and "+
			"can miss its termination grace period entirely",
			transport, got.elapsed.Seconds())
	}
	if !errors.Is(got.row.Err, context.Canceled) {
		t.Fatalf("%s: the fetch failed with %v — that is not the cancellation this test cancelled, "+
			"so the green would be coming from a broken fixture rather than from a stopped fetch",
			transport, got.row.Err)
	}
	// A source that gave up must also STOP, notreport an error and carry on asking.
	if !got.ok {
		t.Fatalf("%s: the abandoned fetch was not surfaced as a row at all — run() records "+
			"SourceRow.Err, and a source that stops SILENTLY is the complete-looking stop this "+
			"package's comments warn about", transport)
	}
}

// TestLinearSource_FetchInFlight_IsAbandonedWhenTheImportIsCancelled and its Jira twin lock BOTH
// sources: the two are separate constructors with separate context fields, and a repair that
// reached only one would leave half the API surface holding a request open through a shutdown.
func TestLinearSource_FetchInFlight_IsAbandonedWhenTheImportIsCancelled(t *testing.T) {
	p := newSlowProvider(linPage(false, "", linNode("ENG-1", "Todo", 1)))
	srv := httptest.NewServer(p.handler())
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := newLinearSource(ctx, "api-key", "TEAM-UUID", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep
	assertFetchIsAbandoned(t, p, cancel, src.Next, "linear")
}

func TestJiraSource_FetchInFlight_IsAbandonedWhenTheImportIsCancelled(t *testing.T) {
	p := newSlowProvider(`{"issues":[{"key":"PROJ-1","fields":{"summary":"t","created":"2026-01-15T09:30:00.000-0700","updated":"2026-01-15T09:30:00.000-0700"}}],"isLast":true}`)
	srv := httptest.NewServer(p.handler())
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := newJiraSource(ctx, "email:token", "PROJ", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep
	assertFetchIsAbandoned(t, p, cancel, src.Next, "jira")
}

// TestRetryerWait_DoesNotSleepThroughAShutdown — the second uncancellable stretch, measured on its
// own because it is reached by a different path: a 429 (Jira) or a RATELIMITED body (Linear) makes
// the client back off BEFORE it retries, and the wait was a bare time.Sleep capped at 30s per
// attempt with up to three attempts per page.
//
// ⚠ THE POSITIVE CONTROL IS IN THE TEST, not only in the control script: an implementation that
// never waits at all would satisfy the first half trivially and would turn every rate-limit
// response into an immediate hot retry. So the second half asserts a LIVE context still waits.
func TestRetryerWait_DoesNotSleepThroughAShutdown(t *testing.T) {
	dead, cancel := context.WithCancel(context.Background())
	cancel()

	// TEN SECONDS RATHER THAN AN HOUR, and the number is a deliberate one: a regression here makes
	// this test WAIT OUT the delay, so the figure is what a broken build costs CI. 10s is far
	// outside the 2s threshold below and far inside the job budget.
	start := time.Now()
	defaultRetryer().wait(dead, 10*time.Second)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("a rate-limit backoff on a cancelled context slept for %v — the process was told "+
			"to shut down and is waiting out a delay the PROVIDER chose the length of", elapsed)
	}

	// … and it must still be a backoff when nothing is going down.
	start = time.Now()
	defaultRetryer().wait(context.Background(), 60*time.Millisecond)
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Fatalf("a backoff on a LIVE context returned after %v — the wait is not waiting, so every "+
			"rate-limit response becomes an immediate retry", elapsed)
	}
}
