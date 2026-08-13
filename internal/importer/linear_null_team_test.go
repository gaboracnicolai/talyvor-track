package importer

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/testutil"
)

// linear_null_team_test.go — WHEN LINEAR ANSWERS THAT THE TEAM DOES NOT RESOLVE, AND THE IMPORT
// REPORTS ITSELF CLEAN.
//
// `linearIssuesQuery` is `team(id: $teamId) { issues(...) }`. `team` is a NULLABLE field: when the
// argument names nothing the credential can resolve, GraphQL's answer is `{"data":{"team":null}}`
// with NO `errors[]` — a 200, a well-formed document, and a null where the connection should be.
// That is not an error the transport can see:
//
//   - status is 200, so the `status != http.StatusOK` arm does not fire;
//   - `parsed.Errors` is empty, so the "a 200 with errors[] is NOT a silent success" arm does not
//     fire either;
//   - `linearResp.Data.Team` is a VALUE struct, and encoding/json unmarshalling `null` into a
//     struct is a documented no-op — so Team keeps its zero value: `nodes` nil, `hasNextPage`
//     false. fetchPage returns a page that is empty AND final.
//
// The source then takes its `exhausted` arm on the first refill and stops CLEANLY. run() pulled no
// rows, so {Imported:0, Skipped:0, Refused:0, stopped:false} reaches terminalStatus, `unlanded == 0`
// is true, and the job records **succeeded, imported=0, failed=0, warnings=[], error_summary=""**.
// Every surface an operator can read says the import worked and the team was empty.
//
// ⚠ THIS IS THE SAME DEFECT api_pagination_termination_test.go LOCKED, ONE BRANCH EARLIER, AND ITS
// HEADER ALREADY NAMES THE RULE: "when a source cannot make progress it must say so in a way an
// operator can read — never a silent stop that reports success". That file fixed the empty-page and
// the unfetchable-cursor cases. A null `team` is the third way to make no progress and the only one
// where the provider has told us, in the response body, that the thing being imported does not
// exist for this credential.
//
// ⚠ WHAT THIS FILE DOES NOT CLAIM, BECAUSE IT IS NOT PROVABLE HERE. W3.4 records an open question:
// whether `team(id:)` accepts a team KEY (`ENG`, which is what `integrations.project_or_team_key`
// stores for BOTH providers and what runner.apiSourceFor passes verbatim) or only a UUID. If it is
// UUID-only then EVERY linear_api import in existence takes this path. That question needs a real
// tenant and is still open — and the defect here does NOT depend on the answer. A team that was
// deleted, renamed, moved to another workspace, or that this token simply cannot see produces the
// same null through the same branch, and the reporting is wrong in all of them.
//
// ⚠ THE LOAD-BEARING PAIR IS (1) AND (2). A team that genuinely holds no issues is a LEGITIMATE
// zero and must stay clean — an import of an empty team is a successful import of an empty team.
// A guard that cannot tell those two apart would be an alarm on every empty team, which is worse
// than the silence it replaces. (2) is what makes (1) a measurement rather than a blanket refusal.

// linearNullTeam is the exact document a nullable field resolving to null produces: data present,
// team null, NO errors array. Written literally rather than through linPage, because linPage can
// only build the non-null shape and the whole point is the shape it cannot build.
const linearNullTeam = `{"data":{"team":null}}`

// linearEmptyTeam is the CONTROL shape: the team resolved, and it holds no issues.
const linearEmptyTeam = `{"data":{"team":{"issues":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}}`

// (1) THE DEFECT. A null team must reach run() as an error row, not as a clean end-of-source.
func TestLinearSource_ANullTeamIsReportedNotImportedAsAnEmptyOne(t *testing.T) {
	srv := httptest.NewServer(cannedPages([]string{linearNullTeam}, linearNullTeam))
	defer srv.Close()
	src := newLinearSource(context.Background(), "tok", "ENG", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep

	got, errs, hitCap := drainSource(t, src, 12)
	if hitCap {
		t.Fatalf("source never terminated: %v", got)
	}
	if len(got) != 0 {
		t.Fatalf("rows = %v, want none — a null team carries no issues", got)
	}
	if len(errs) != 1 {
		t.Fatalf("errors = %v, want exactly one — a team that did not resolve must not read as an "+
			"empty team, or the job records `succeeded imported=0` and nothing says the import "+
			"never had a team to read", errs)
	}
	// The sentence must name the TEAM and the KEY the operator supplied: "0 issues imported" sends
	// them to look at their backlog, and the thing to act on is the team argument.
	if !strings.Contains(errs[0], "team") {
		t.Fatalf("error = %q, want it to name the team — an operator reading this must be sent to "+
			"the team argument, not to their issue list", errs[0])
	}
	if !strings.Contains(errs[0], "ENG") {
		t.Fatalf("error = %q, want it to quote the key that did not resolve (ENG) — the key is the "+
			"one thing the operator can change", errs[0])
	}
}

// (2) THE CONTROL, AND IT IS THE HALF THAT MAKES (1) WORTH ANYTHING. A team that resolved and holds
// no issues is a clean, successful, empty import. It must produce ZERO error rows.
func TestLinearSource_ATeamThatResolvedAndIsEmptyStaysClean(t *testing.T) {
	srv := httptest.NewServer(cannedPages([]string{linearEmptyTeam}, linearEmptyTeam))
	defer srv.Close()
	src := newLinearSource(context.Background(), "tok", "ENG", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep

	got, errs, hitCap := drainSource(t, src, 12)
	if hitCap {
		t.Fatalf("source never terminated: %v", got)
	}
	if len(got) != 0 {
		t.Fatalf("rows = %v, want none", got)
	}
	if len(errs) != 0 {
		t.Fatalf("errors = %v, want none — an empty team is a successful import of an empty team, "+
			"and warning about it is an alarm on a working system", errs)
	}
}

// (3) THE NULL MUST BE SEEN ON A LATER PAGE TOO. A credential whose access to the team is revoked
// mid-walk, or a team deleted between pages, answers null on page 2 — and the rows page 1 yielded
// are real. The import is INCOMPLETE, which is the one thing that must not be reported as done.
func TestLinearSource_ANullTeamOnALaterPageDoesNotSilentlyTruncate(t *testing.T) {
	page1 := linPage(true, "c1", linNode("ENG-1", "Done", 1))
	srv := httptest.NewServer(cannedPages([]string{page1, linearNullTeam}, linearNullTeam))
	defer srv.Close()
	src := newLinearSource(context.Background(), "tok", "ENG", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep

	got, errs, hitCap := drainSource(t, src, 12)
	if hitCap {
		t.Fatalf("source never terminated: %v", got)
	}
	if want := []string{"ENG-1"}; !equalStrings(got, want) {
		t.Fatalf("rows = %v, want %v — the rows page 1 yielded are real whatever page 2 said", got, want)
	}
	if len(errs) != 1 {
		t.Fatalf("errors = %v, want exactly one — an import cut short by a team that stopped "+
			"resolving must not report itself complete", errs)
	}
}

// (4) THE ORDER OF THE TWO REFUSALS, AND IT IS HERE BECAUSE A CONTROL SAID NOTHING HELD IT.
// Control C4 in scripts/w34-linear-null-team-controls.py moves the team check ABOVE the errors[]
// arm and was GREEN on the first run — the ordering was a comment's claim and nothing more, so a
// 200 that carried a real GraphQL error could have been reported as "the team did not resolve",
// sending an operator to change a team key over an authentication or rate-limit fault. Linear
// answers a raised error with `data: {"team": null}` ALONGSIDE errors[], so the two arms really do
// see the same document and only their order decides the sentence.
func TestLinearSource_AGraphQLErrorBesideANullTeamKeepsTheErrorSentence(t *testing.T) {
	const errWithNullTeam = `{"data":{"team":null},"errors":[{"message":"Authentication required"}]}`
	srv := httptest.NewServer(cannedPages([]string{errWithNullTeam}, errWithNullTeam))
	defer srv.Close()
	src := newLinearSource(context.Background(), "tok", "ENG", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep

	_, errs, hitCap := drainSource(t, src, 12)
	if hitCap {
		t.Fatalf("source never terminated")
	}
	if len(errs) != 1 {
		t.Fatalf("errors = %v, want exactly one", errs)
	}
	if !strings.Contains(errs[0], "Authentication required") {
		t.Fatalf("error = %q, want the provider's own message — the more specific arm must answer "+
			"first, or an auth failure reads as a missing team and the operator changes the wrong "+
			"thing", errs[0])
	}
}

// ── THE JOB ROW: the surface an operator actually reads ───────────────────────────────────────
//
// The three cases above are about the SOURCE. This one is about what `import_jobs` says afterwards,
// through the shipped async runner on real Postgres — because a source that returns an error row is
// only half the fix. run() must count it, terminalStatus must refuse to call the job succeeded, and
// summarise must put the sentence somewhere an operator can find it.
//
// ⚠ MEASURED BEFORE THE FIX, THROUGH THIS EXACT HARNESS: status `succeeded`, imported 0, skipped 0,
// failed 0, error_summary "", warnings []. Six fields, and not one of them different from a genuine
// import of a genuinely empty team.

// runLinearNullTeamImport drives a full linear_api job whose provider answers `data.team: null`.
func runLinearNullTeamImport(t *testing.T, d *testutil.DB, teamKey, page string) string {
	t.Helper()
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	srv := httptest.NewServer(cannedPages([]string{page}, page))
	defer srv.Close()

	istore := testIntegrationStore(t, d)
	if _, err := istore.Upsert(ctx, ws.ID, "linear", "api-token", teamKey, srv.URL); err != nil {
		t.Fatal(err)
	}
	jobID := insertAPIJob(t, d, ws.ID, team.ID, "linear_api")

	runner := NewRunner(NewJobStore(d.Pool), New(issue.NewStore(d.Pool))).
		WithProviderConfig(istore).WithHTTPClient(srv.Client())
	if did, err := runner.RunOnce(ctx); err != nil || !did {
		t.Fatalf("RunOnce did=%v err=%v", did, err)
	}
	return jobID
}

func TestJobRow_LinearAPI_ANullTeamDoesNotRecordASucceededImport(t *testing.T) {
	d := testutil.New(t)
	jobID := runLinearNullTeamImport(t, d, "ENG", linearNullTeam)

	j, err := NewJobStore(d.Pool).Get(context.Background(), jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if j.Status == JobSucceeded {
		t.Errorf("status = %q for an import whose team did not resolve.\n"+
			"imported=%d skipped=%d failed=%d error_summary=%q warnings=%v\n"+
			"Every one of those fields is byte-identical to a real import of a real empty team, so "+
			"there is no field an operator could read to tell the two apart.",
			j.Status, j.Imported, j.Skipped, j.Failed, j.ErrorSummary, j.Warnings)
	}
	if j.Imported != 0 {
		t.Errorf("imported = %d, want 0 — a null team carries no issues", j.Imported)
	}
	if !strings.Contains(j.ErrorSummary, "ENG") {
		t.Errorf("error_summary = %q, want it to quote the team key that did not resolve — the job "+
			"row is the only place this reaches an operator", j.ErrorSummary)
	}
}

// THE CONTROL AT THE JOB LEVEL, and it is the one that decides whether this change is a report or
// an alarm. A team that resolved and holds nothing must still record a clean, successful import.
func TestJobRow_LinearAPI_AnEmptyTeamStillRecordsASucceededImport(t *testing.T) {
	d := testutil.New(t)
	jobID := runLinearNullTeamImport(t, d, "ENG", linearEmptyTeam)

	j, err := NewJobStore(d.Pool).Get(context.Background(), jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if j.Status != JobSucceeded {
		t.Errorf("status = %q for a team that resolved and holds no issues, want %q — importing an "+
			"empty team is a successful import of an empty team, and failing it would be an alarm "+
			"on a working system.\nerror_summary=%q", j.Status, JobSucceeded, j.ErrorSummary)
	}
	if j.ErrorSummary != "" {
		t.Errorf("error_summary = %q, want empty", j.ErrorSummary)
	}
	if len(j.Warnings) != 0 {
		t.Errorf("warnings = %v, want none", j.Warnings)
	}
}
