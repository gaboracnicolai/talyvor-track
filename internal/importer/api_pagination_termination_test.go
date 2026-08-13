package importer

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/testutil"
)

// api_pagination_termination_test.go — WHEN THE TWO API SOURCES BELIEVE THEIR PROVIDER HAS RUN OUT
// OF PAGES, and what they report when they are wrong about it.
//
// Both sources buffer a page and refetch on exhaustion. "Exhausted" was decided by ONE field each —
// Jira's `isLast`, Linear's `pageInfo.hasNextPage` — plus one unconditional rule: A PAGE WITH NO
// ROWS ENDS THE SOURCE, whatever the provider said about further pages. Measured on the shipped
// sources before this file existed (probe, canned httptest provider, no credentials):
//
//	jira   {issues:[], isLast:false, nextPageToken:"t1"}  ⇒ 1 HTTP call, 0 rows, CLEAN stop.
//	                                                        Page 2 — which the provider said exists —
//	                                                        is never requested.
//	linear {nodes:[],  pageInfo:{hasNextPage:true}}       ⇒ 0 rows, CLEAN stop, same shape.
//	jira   final page with NO isLast and NO nextPageToken ⇒ NEVER TERMINATES: 3 issues came back as
//	                                                        12 rows in 12 HTTP calls and rising —
//	                                                        fetchPage("") restarts at page 1, so the
//	                                                        import re-reads the whole project forever.
//
// ⚠ WHY A CLEAN STOP IS THE SERIOUS HALF. run() has no counter for a row it never pulled, so an
// abandoned import arrives at terminalStatus as {Imported:0, Skipped:0, Refused:0, stopped:false} —
// `unlanded == 0` — and records itself **succeeded**. That is verbatim the failure mode source.go's
// own context-cancellation comment says must never happen ("reporting that as a complete import is
// the failure mode the ctx check in run() would otherwise create"); the empty-page path recreates it
// one branch below, and nothing in the job row, the warnings array or the error list says so.
//
// ⚠ WHAT IS MEASURED AND WHAT IS INFERRED, kept apart on purpose.
//   - MEASURED, on the wire, anonymously, at the endpoint the client actually POSTs
//     (hibernate.atlassian.net, POST /rest/api/3/search/jql — the instance #83 found): Jira Cloud
//     DOES send `isLast` today, and 40 consecutive pages at maxResults=5 came back FULL. So the
//     empty-non-final page is NOT something this environment has seen a real Jira emit, and this
//     file does not pretend otherwise.
//   - THE CONTRACT, from Atlassian's own published spec (the cached v3 OpenAPI document):
//     `SearchAndReconcileResults` declares NO `required` list at all — control: 371 of the spec's
//     970 schemas DO declare one, so the absence is a signal and not a spec that never uses the
//     keyword — which makes `isLast` an OPTIONAL field, while `nextPageToken` is documented as the
//     terminator: "If this result represents the last or the only page this token will be null."
//     The source terminated on the optional field and never read the documented one.
//   - THE MECHANISM for a short or empty page, from the same operation's own description: issues are
//     included "where the user has Browse projects permission ... issue-level security permission to
//     view the issue" — permission filtering applied to a token-paged window is how a page comes
//     back short, and zero is short. Not observed here; the instance walked has no such filtering.
//
// So the rule this file locks is not "providers do X". It is that TERMINATION MUST COME FROM THE
// FIELD THE PROVIDER DOCUMENTS AS THE TERMINATOR, and that when a source cannot make progress it
// must say so in a way an operator can read — never a silent stop that reports success, never an
// unbounded loop. Every behaviour changed here is one whose current answer is one of those two.

// ── a fake Jira that keys its pages off the token the client sends, like the real one ──
//
// ⚠ NOT cannedPages: that helper serves page N to request N and a FALLBACK to everything after,
// so an over-fetching source is served a tidy `{"issues":[],"isLast":true}` and stops. The
// non-termination above is invisible through it. A token-keyed fake is what makes the extra
// requests observable — and it FAILS the test on an unknown token rather than answering.
func tokenKeyedJira(t *testing.T, pagesByToken map[string]string, calls *int32) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		raw, _ := io.ReadAll(r.Body)
		tok := ""
		if i := strings.Index(string(raw), `"nextPageToken":"`); i >= 0 {
			rest := string(raw)[i+len(`"nextPageToken":"`):]
			tok = rest[:strings.Index(rest, `"`)]
		}
		page, ok := pagesByToken[tok]
		if !ok {
			t.Errorf("fake jira: the source asked for a page with token %q, which this case never handed out", tok)
			page = `{"issues":[],"isLast":true}`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(page))
	})
}

// drainSource pulls until the source says stop or the cap is hit. The cap is the whole point: a
// source that does not terminate cannot be measured by a loop that trusts it to.
func drainSource(t *testing.T, src IssueSource, cap int) (idents []string, errs []string, hitCap bool) {
	t.Helper()
	for i := 0; i < cap; i++ {
		row, ok := src.Next()
		if !ok {
			return idents, errs, false
		}
		if row.Err != nil {
			errs = append(errs, row.Err.Error())
			continue
		}
		idents = append(idents, row.Issue.Identifier)
	}
	return idents, errs, true
}

// (1) JIRA — the terminator is nextPageToken, not isLast. A final page that omits the optional field
// must end the import; before this fix it restarted the whole project, forever.
func TestJiraSource_TerminatesOnTheDocumentedTerminatorNotOnIsLast(t *testing.T) {
	var calls int32
	pages := map[string]string{
		"":   fmt.Sprintf(`{"issues":[%s],"nextPageToken":"t1"}`, jiraIssueJSON("PROJ-1", "A", "", "To Do", "High")),
		"t1": fmt.Sprintf(`{"issues":[%s],"nextPageToken":"t2"}`, jiraIssueJSON("PROJ-2", "B", "", "To Do", "High")),
		"t2": fmt.Sprintf(`{"issues":[%s]}`, jiraIssueJSON("PROJ-3", "C", "", "Done", "Low")),
	}
	srv := httptest.NewServer(tokenKeyedJira(t, pages, &calls))
	defer srv.Close()
	src := newJiraSource(context.Background(), "me@corp.com:tok", "PROJ", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep

	got, errs, hitCap := drainSource(t, src, 12)
	if hitCap {
		t.Fatalf("source never terminated: %d rows and still going (%v)", len(got), got)
	}
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if want := []string{"PROJ-1", "PROJ-2", "PROJ-3"}; !equalStrings(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	if n := atomic.LoadInt32(&calls); n != 3 {
		t.Fatalf("http calls = %d, want 3 (one per page, none after the terminator)", n)
	}
}

// (2) JIRA — an empty page the provider says is NOT the last must not end the import.
func TestJiraSource_AnEmptyPageThatIsNotTheLastDoesNotEndTheImport(t *testing.T) {
	var calls int32
	pages := map[string]string{
		"": `{"issues":[],"isLast":false,"nextPageToken":"t1"}`,
		"t1": fmt.Sprintf(`{"issues":[%s,%s],"isLast":true}`,
			jiraIssueJSON("PROJ-1", "A", "", "To Do", "High"), jiraIssueJSON("PROJ-2", "B", "", "Done", "Low")),
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
		t.Fatalf("unexpected errors: %v", errs)
	}
	if want := []string{"PROJ-1", "PROJ-2"}; !equalStrings(got, want) {
		t.Fatalf("rows = %v, want %v — the page the provider said existed was never read", got, want)
	}
}

// (3) LINEAR — the mirror. Relay's nodes may be empty while hasNextPage is true.
func TestLinearSource_AnEmptyPageThatIsNotTheLastDoesNotEndTheImport(t *testing.T) {
	page1 := linPage(true, "c1")
	page2 := linPage(false, "", linNode("ENG-1", "Done", 1))
	srv := httptest.NewServer(cannedPages([]string{page1, page2}, linPage(false, "")))
	defer srv.Close()
	src := newLinearSource(context.Background(), "tok", "team-id", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep

	got, errs, hitCap := drainSource(t, src, 12)
	if hitCap {
		t.Fatalf("source never terminated: %v", got)
	}
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if want := []string{"ENG-1"}; !equalStrings(got, want) {
		t.Fatalf("rows = %v, want %v — the page the provider said existed was never read", got, want)
	}
}

// (4) LINEAR — "there is more" with no cursor to ask for it. The next request would carry no
// `after` and restart at page 1, so continuing is a loop and stopping is a silent truncation. The
// third answer is the only honest one: SAY SO, in a row run() records.
func TestLinearSource_HasNextPageWithNoCursorIsReportedNotSwallowed(t *testing.T) {
	page1 := linPage(true, "", linNode("ENG-1", "Done", 1))
	srv := httptest.NewServer(cannedPages([]string{page1}, page1))
	defer srv.Close()
	src := newLinearSource(context.Background(), "tok", "team-id", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep

	got, errs, hitCap := drainSource(t, src, 12)
	if hitCap {
		t.Fatalf("source never terminated (it is re-reading page 1): %v", got)
	}
	if len(errs) != 1 || !strings.Contains(errs[0], "endCursor") {
		t.Fatalf("errors = %v, want exactly one naming endCursor — a truncated import must not look complete", errs)
	}
	if want := []string{"ENG-1"}; !equalStrings(got, want) {
		t.Fatalf("rows = %v, want %v (the rows it DID get are still real)", got, want)
	}
}

// (5) JIRA — a provider that hands back the token it was given is not making progress. Continuing
// is an infinite loop over one empty page; before this fix the empty page ended the import clean.
func TestJiraSource_ARepeatedPageTokenIsReportedNotSwallowed(t *testing.T) {
	var calls int32
	pages := map[string]string{
		"":   `{"issues":[],"isLast":false,"nextPageToken":"t1"}`,
		"t1": `{"issues":[],"isLast":false,"nextPageToken":"t1"}`,
	}
	srv := httptest.NewServer(tokenKeyedJira(t, pages, &calls))
	defer srv.Close()
	src := newJiraSource(context.Background(), "me@corp.com:tok", "PROJ", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep

	got, errs, hitCap := drainSource(t, src, 12)
	if hitCap {
		t.Fatalf("source never terminated: %v", got)
	}
	if len(got) != 0 {
		t.Fatalf("rows = %v, want none", got)
	}
	if len(errs) != 1 || !strings.Contains(errs[0], "no progress") {
		t.Fatalf("errors = %v, want exactly one naming the lack of progress", errs)
	}
}

// (6) JIRA — an unbounded run of empty non-final pages ends as an ERROR, not as a loop and not as a
// clean stop. The bound is a judgement (see maxConsecutiveEmptyPages); what is NOT a judgement is
// that whatever ends the import must be visible in the job row.
func TestJiraSource_EndlessEmptyPagesEndAsAnErrorNotALoop(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		// A fresh token every time: progress by the letter, nothing by the row.
		_, _ = fmt.Fprintf(w, `{"issues":[],"isLast":false,"nextPageToken":"t%d"}`, n)
	}))
	defer srv.Close()
	src := newJiraSource(context.Background(), "me@corp.com:tok", "PROJ", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep

	got, errs, hitCap := drainSource(t, src, 5)
	if hitCap {
		t.Fatalf("source never terminated: %v", got)
	}
	if len(errs) != 1 || !strings.Contains(errs[0], "consecutive pages carried no issues") {
		t.Fatalf("errors = %v, want exactly one naming the empty pages", errs)
	}
	// ⚠ 201 IS WRITTEN DOWN, NOT READ FROM maxConsecutiveEmptyPages. Asserting against the same
	// constant the code counts with compares the constant to itself and passes for EVERY value of
	// it — control C6 raised the bound to 100000 and this case stayed green until the literal
	// replaced it. If the bound moves, this line must move deliberately, which is the point.
	if n := int(atomic.LoadInt32(&calls)); n != 201 {
		t.Fatalf("http calls = %d, want 201 (200 empty pages walked, the 201st is the bound)", n)
	}
	if !strings.Contains(errs[0], "201 consecutive pages") {
		t.Fatalf("error = %q, want it to name the 201 pages it walked", errs[0])
	}
}

// (7) THE OPERATOR'S SENTENCE, on real Postgres through the shipped runner. A jira_api job whose
// first page is empty-but-not-last recorded `succeeded, imported=0` with ZERO rows in `issues` —
// an import that read one page of a project and reported itself complete.
func TestJobRow_JiraAPI_AnEmptyFirstPageIsNotACompletedImport(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	var calls int32
	pages := map[string]string{
		"": `{"issues":[],"isLast":false,"nextPageToken":"t1"}`,
		"t1": fmt.Sprintf(`{"issues":[%s,%s],"isLast":true}`,
			jiraIssueJSON("PROJ-1", "First", "", "To Do", "High"), jiraIssueJSON("PROJ-2", "Second", "", "Done", "Low")),
	}
	srv := httptest.NewServer(tokenKeyedJira(t, pages, &calls))
	defer srv.Close()

	istore := testIntegrationStore(t, d)
	if _, err := istore.Upsert(ctx, ws.ID, "jira", "me@corp.com:api-token", "PROJ", srv.URL); err != nil {
		t.Fatal(err)
	}
	jobID := insertAPIJob(t, d, ws.ID, team.ID, "jira_api")

	runner := NewRunner(NewJobStore(d.Pool), New(issue.NewStore(d.Pool))).
		WithProviderConfig(istore).WithHTTPClient(srv.Client())
	if did, err := runner.RunOnce(ctx); err != nil || !did {
		t.Fatalf("RunOnce did=%v err=%v", did, err)
	}

	job, err := NewJobStore(d.Pool).Get(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM issues WHERE workspace_id = $1`, ws.ID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Fatalf("issues in Postgres = %d, want 2 — job says %q imported=%d", rows, job.Status, job.Imported)
	}
	if job.Status != JobSucceeded || job.Imported != 2 {
		t.Fatalf("job = {status:%q imported:%d}, want {succeeded 2}", job.Status, job.Imported)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
