package importer

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/testutil"
)

// api_updated_job_test.go — the issue's LAST-TOUCHED instant on the TWO API TRANSPORTS, driven end
// to end through the async runner onto real Postgres and then read back THROUGH THE QUERY THE
// PRODUCT SORTS ITS MAIN SCREEN BY.
//
// ⚠ WHY THIS EXISTS WHEN #85 ALREADY MERGED `updated_at`. #85 fixed the CSV transport, whose write
// path is issue.Store.Create. The two API transports reach a DIFFERENT STATEMENT —
// UpsertByIdentifier — which #85 deliberately left alone and said so, for the reason #74's C9 and
// #83 both taught: "an un-fed column is untestable and rots", so mapper and statement land
// together. That is this merge. The fix is not inherited and NEITHER IS THE EVIDENCE.
//
// ⚠ THIS IS THE FIFTH COPY OF ONE SEAM AND THE SECOND COLUMN TO TRAVERSE IT. #74 found the
// importer's UPSERT omitting `completed_at`; #78 found the second copy in Create's INSERT for the
// same column; #83 found the third, `created_at` in Create; #84 found the fourth, `created_at` in
// the UPSERT. This is `updated_at` in the UPSERT. Enumerate every copy of a seam — a green guard at
// copy #4 says nothing about copy #5, and this package has now paid for that lesson five times.
//
// ⚠ THE CONSUMERS ARE ENUMERATED, NOT ASSUMED, and that enumeration is why this field is worth a
// merge at all. #83 scoped `updated_at` out with "nothing in Track reads updated_at for a report",
// #84 repeated the sentence while correctly flagging it as unmeasured, and #85 MEASURED IT FALSE —
// five consumers in two languages, the largest of which is not a report:
//
//	frontend/src/components/issue/IssueRow.tsx:58   relativeTime(issue.updated_at) — on EVERY row
//	frontend/src/components/issue/IssueList.tsx:48  sorts the issue list by updated_at DESC
//	internal/issue/store.go:1143                    Search ORDER BY updated_at DESC
//	internal/issue/store.go:648                     updated_at is in the API's sort whitelist
//	internal/analytics/engine.go:416,433,483,508    the AI-cost report's window AND its x-axis
//
// ⚠ A COLUMN ASSERTION ALONE CANNOT SEE THIS DEFECT. `issues.updated_at` is TIMESTAMPTZ DEFAULT
// NOW() and the UPSERT names it only in the conflict arm, so an INSERTed row lands with the import
// instant: always non-null, always looking populated, the wrong value shaped exactly like the right
// one. It is observable only in what the product DOES with it — which is why every case below has
// an ORDERING half as well as a column half.
//
// ⚠ NO NEW WIRE PROBE WAS RUN AND THAT IS DELIBERATE, per the scope #85 left. Jira's `updated`
// rides the SAME `fields` list #74 proved costs no query change, and #84 already measured Linear's
// `updatedAt` as `DateTime!` in its unauthenticated introspection call. What this file does NOT
// inherit is the assertion that the shipped request ASKS for the field — an unknown Jira field name
// is silently ignored with HTTP 200, so a misspelling in jiraFields cannot be caught by a status
// code. TestJiraRequest_AsksForTheUpdatedField / TestLinearQuery_AsksForUpdatedAt pin that at the
// wire, in this package, for this field.

const (
	// The imported issue is opened 300 days ago and last touched 200 days ago. Created < Updated
	// for every real issue, so a fixture that violated it would be testing an impossible provider.
	apiUpdatedCreatedDaysAgo = 300
	apiUpdatedDaysAgo        = 200
)

// apiUpdatedInstants returns the two instants every case below shares. COMPUTED, never written
// down: analytics filters on `created_at > NOW() - INTERVAL '1 day' * $2`, so a hardcoded date ages
// out of the window and the test stops testing anything while staying green (#84's note).
func apiUpdatedInstants() (created, updated time.Time) {
	now := time.Now().UTC()
	created = now.Add(-time.Duration(apiUpdatedCreatedDaysAgo) * 24 * time.Hour).Truncate(time.Second)
	updated = now.Add(-time.Duration(apiUpdatedDaysAgo) * 24 * time.Hour).Truncate(time.Second)
	return created, updated
}

// jiraIssueAPIUpdatedJSON shapes a v3 issue carrying BOTH `created` and `updated` as the real Cloud
// instance serialises them. An empty value omits the key entirely — a response with no such field.
func jiraIssueAPIUpdatedJSON(key, summary, status, created, updated string) string {
	field := func(name, v string) string {
		if v == "" {
			return ""
		}
		return fmt.Sprintf(`,%q:%q`, name, v)
	}
	return fmt.Sprintf(`{"key":%q,"fields":{"summary":%q,"description":null,"status":{"name":%q},`+
		`"priority":{"name":"Medium"},"labels":[]%s%s}}`,
		key, summary, status, field("created", created), field("updated", updated))
}

// linNodeUpdatedAt adds `updatedAt` to a node that already carries `createdAt`.
//
// ⚠ IT PANICS RATHER THAN RETURNING AN UNCHANGED NODE, for the reason linNodeCreated states: a
// string substitution that matches nothing edits zero bytes and is byte-indistinguishable from a
// fixture that works. #71 lost two positive controls to exactly that and #83 lost a third.
func linNodeUpdatedAt(node, updated string) string {
	// ⚠ IT REPLACES THE DEFAULT, IT DOES NOT PREPEND ONE. The first draft inserted a second
	// `updatedAt` key ahead of the default linNodeDated already emits; encoding/json takes the LAST
	// duplicate key, so every case silently asserted against fixtureLinearUpdated instead of its own
	// instant. CAUGHT BY THE TEST FAILING WITH THE FIXTURE CONSTANT IN THE MESSAGE, not by reading —
	// which is the whole reason the delta is printed rather than just "mismatch".
	old := fmt.Sprintf(`,"updatedAt":%q`, fixtureLinearUpdated)
	if strings.Count(node, old) != 1 {
		panic("linNodeUpdatedAt: linNodeDated no longer emits exactly one default updatedAt — " +
			"this fixture would silently test the default instead of the case it names")
	}
	field := `,"updatedAt":null`
	if updated != "" {
		field = fmt.Sprintf(`,"updatedAt":%q`, updated)
	}
	return strings.Replace(node, old, field, 1)
}

// retitleLinearNode renames a node so the ordering search can find it. It PANICS on a miss for the
// same reason linNodeUpdatedAt does: linNodeDated builds the title as "T-<id>", and a replacement
// that silently matched nothing would leave the node unfindable by Search and turn the ordering
// assertion's `len(got) != 2` guard into the only thing that ever fired.
func retitleLinearNode(node, id, title string) string {
	old := fmt.Sprintf(`"title":"T-%s"`, id)
	if strings.Count(node, old) != 1 {
		panic("retitleLinearNode: linNodeDated no longer emits exactly one " + old +
			" — the ordering fixture would be unfindable by Search")
	}
	return strings.Replace(node, old, fmt.Sprintf(`"title":%q`, title), 1)
}

// runJiraAPICreatedImportInto / runLinearAPICreatedImportInto are runJiraAPICreatedImport and
// runLinearAPICreatedImport against a workspace and team THE CALLER ALREADY MADE. The ordering
// cases need the native issue to exist in the SAME workspace and to be created BEFORE the import,
// which the existing drivers cannot express because they mint the workspace themselves.
func runJiraAPICreatedImportInto(t *testing.T, d *testutil.DB, wsID, teamID string, issues ...string) string {
	t.Helper()
	ctx := context.Background()
	srv := httptest.NewServer(cannedPages([]string{jiraAPIPage(issues...)}, jiraAPIPage()))
	defer srv.Close()

	istore := testIntegrationStore(t, d)
	if _, err := istore.Upsert(ctx, wsID, "jira", "me@corp.com:api-token", "PROJ", srv.URL); err != nil {
		t.Fatal(err)
	}
	jobID := insertAPIJob(t, d, wsID, teamID, "jira_api")

	runner := NewRunner(NewJobStore(d.Pool), New(issue.NewStore(d.Pool))).
		WithProviderConfig(istore).WithHTTPClient(srv.Client())
	if did, err := runner.RunOnce(ctx); err != nil || !did {
		t.Fatalf("RunOnce did=%v err=%v", did, err)
	}
	return jobID
}

func runLinearAPICreatedImportInto(t *testing.T, d *testutil.DB, wsID, teamID string, nodes ...string) string {
	t.Helper()
	ctx := context.Background()
	srv := httptest.NewServer(cannedPages([]string{linearAPIPage(nodes...)}, linearAPIPage()))
	defer srv.Close()

	istore := testIntegrationStore(t, d)
	if _, err := istore.Upsert(ctx, wsID, "linear", "api-token", "LINEAR-TEAM-KEY", srv.URL); err != nil {
		t.Fatal(err)
	}
	jobID := insertAPIJob(t, d, wsID, teamID, "linear_api")

	runner := NewRunner(NewJobStore(d.Pool), New(issue.NewStore(d.Pool))).
		WithProviderConfig(istore).WithHTTPClient(srv.Client())
	if did, err := runner.RunOnce(ctx); err != nil || !did {
		t.Fatalf("RunOnce did=%v err=%v", did, err)
	}
	return jobID
}

func readUpdatedAt(t *testing.T, d *testutil.DB, wsID, ident string) time.Time {
	t.Helper()
	var got time.Time
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT updated_at FROM issues WHERE workspace_id=$1 AND identifier=$2`, wsID, ident).Scan(&got); err != nil {
		t.Fatalf("read updated_at for %s: %v", ident, err)
	}
	return got.UTC()
}

// assertStaleImportDoesNotOutrankTodaysWork is the CONSUMER half, and it is the half that makes
// this field a product defect rather than a missing timestamp.
//
// A native issue edited during the test must outrank a provider issue untouched for 200 days in the
// query the product lists by recency (issue.Store.Search, ORDER BY updated_at DESC). The native
// issue is created FIRST and the import runs SECOND, so with a defaulted updated_at the imported
// row's timestamp is strictly LATER and this fails deterministically — not a tie whose order varies.
func assertStaleImportDoesNotOutrankTodaysWork(t *testing.T, d *testutil.DB, ws, nativeID, nativeTitle, transport string) {
	t.Helper()
	got, err := issue.NewStore(d.Pool).Search(context.Background(), ws, "widget", 10)
	if err != nil {
		t.Fatalf("%s: search: %v", transport, err)
	}
	if len(got) != 2 {
		t.Fatalf("%s: search returned %d issues, want 2 (the native one and the imported one) — the "+
			"ordering assertion is meaningless unless both rows are present", transport, len(got))
	}
	if got[0].ID != nativeID {
		t.Errorf("%s: most-recently-updated issue is %q, want %q.\n"+
			"A provider issue nobody has touched in %d days outranks work edited today in the query "+
			"the product sorts the issue list by (issue/store.go:1143, ORDER BY updated_at DESC), "+
			"and IssueRow.tsx prints it as updated just now. That is a defaulted updated_at on the "+
			"UPSERT path, not a fact about the backlog.",
			transport, got[0].Title, nativeTitle, apiUpdatedDaysAgo)
	}
}

// createTodaysWork inserts the human-authored row the ordering assertion compares against.
// CreatorID is a HUMAN on purpose, never model.ImporterCreatorID: this row stands for a person's
// current work and the upsert's import-ownership predicate must never reach it.
func createTodaysWork(t *testing.T, d *testutil.DB, wsID, teamID, creator string) *model.Issue {
	t.Helper()
	native, err := issue.NewStore(d.Pool).Create(context.Background(), model.Issue{
		WorkspaceID: wsID, TeamID: teamID,
		Title:       "widget alpha edited today",
		Description: "current work",
		CreatorID:   creator,
		Status:      model.StatusTodo, Priority: model.PriorityMedium,
	})
	if err != nil {
		t.Fatalf("create native issue: %v", err)
	}
	return native
}

// ── THE WIRE: the shipped request must ASK for the field ──────────────────────────────────────
//
// ⚠ THIS IS NOT CEREMONY. Jira answers HTTP 200 for an unknown field name and simply omits it, so
// a misspelling in jiraFields produces a perfectly successful import in which every row is silently
// defaulted — the exact failure this whole item keeps finding, arriving through the request instead
// of the mapper. A status code cannot catch it; only asking what was sent can.

func TestJiraRequest_AsksForTheUpdatedField(t *testing.T) {
	for _, f := range jiraFields {
		if f == jiraAPIUpdatedField {
			return
		}
	}
	t.Errorf("jiraFields = %v does not request %q. Jira returns HTTP 200 and omits an unknown "+
		"field, so every imported issue would carry a defaulted updated_at and the import would "+
		"report complete success.", jiraFields, jiraAPIUpdatedField)
}

// ⚠ THE LITERAL IS HARDCODED AND THE CONSTANT IS CHECKED AGAINST IT — both, deliberately, and they
// are two different assertions. #75's C6: a test written against the same constant the code uses
// compares the constant to itself and passes for EVERY value, including "". So the query is
// searched for the MEASURED wire name as a literal. But `linearAPIUpdatedField` has no production
// use — the GraphQL query is one literal string, where jiraAPIUpdatedField rides jiraFields — and a
// constant nothing reads is a constant that can drift away from the wire silently. Pinning it to
// the same literal is what keeps it honest without inventing a use for it.
func TestLinearQuery_AsksForUpdatedAt(t *testing.T) {
	if !strings.Contains(linearIssuesQuery, "updatedAt") {
		t.Errorf("the shipped Linear GraphQL query does not select `updatedAt`, so the mapper can "+
			"only ever see the zero value:\n%s", linearIssuesQuery)
	}
	if linearAPIUpdatedField != "updatedAt" {
		t.Errorf("linearAPIUpdatedField = %q, want %q — the constant names Linear's field in every "+
			"warning this package emits, and it has drifted from the name the query actually asks for.",
			linearAPIUpdatedField, "updatedAt")
	}
}

// ── JIRA API ──────────────────────────────────────────────────────────────────────────────────

func TestJobRow_JiraAPI_ImportedIssueKeepsTheDateJiraLastUpdatedIt(t *testing.T) {
	d := testutil.New(t)
	created, updated := apiUpdatedInstants()
	wsID, _ := runJiraAPICreatedImport(t, d, jiraIssueAPIUpdatedJSON("PROJ-1", "widget alpha untouched for months",
		"To Do", jiraAPICreatedFixtureTime(created), jiraAPICreatedFixtureTime(updated)))

	got := readUpdatedAt(t, d, wsID, "PROJ-1")
	if delta := got.Sub(updated); delta > time.Minute || delta < -time.Minute {
		t.Errorf("updated_at = %s, want %s (Jira's `updated`) — off by %s.\n"+
			"A defaulted updated_at is the instant the IMPORT ran, so every imported issue reads as "+
			"touched just now and the screen the team works from is ordered by import order.",
			got.Format(time.RFC3339), updated.Format(time.RFC3339), delta)
	}
}

func TestJobRow_JiraAPI_AStaleImportDoesNotOutrankTodaysWork(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	native := createTodaysWork(t, d, ws.ID, team.ID, "creator-native-w34-api-updated-jira")

	created, updated := apiUpdatedInstants()
	runJiraAPICreatedImportInto(t, d, ws.ID, team.ID,
		jiraIssueAPIUpdatedJSON("PROJ-1", "widget beta untouched for months", "To Do",
			jiraAPICreatedFixtureTime(created), jiraAPICreatedFixtureTime(updated)))

	assertStaleImportDoesNotOutrankTodaysWork(t, d, ws.ID, native.ID, native.Title, "jira_api")
}

func TestJobRow_JiraAPI_MissingUpdatedIsReportedNotDefaulted(t *testing.T) {
	d := testutil.New(t)
	created, _ := apiUpdatedInstants()
	_, jobID := runJiraAPICreatedImport(t, d,
		jiraIssueAPIUpdatedJSON("PROJ-1", "No updated at all", "To Do", jiraAPICreatedFixtureTime(created), ""),
		jiraIssueAPIUpdatedJSON("PROJ-2", "Unparseable updated", "To Do", jiraAPICreatedFixtureTime(created), "next tuesday"))

	report := strings.Join(jobWarnings(t, d, jobID), " | ")
	if report == "" {
		t.Fatal("the job reported NOTHING for two issues whose last-touched instant did not land. " +
			"Both rows still carry a plausible updated_at (the DEFAULT), so silence here is the only " +
			"way a reader could ever find out.")
	}
	// ⚠ THE LITERAL, NOT `fieldUpdated`, AND THE OLD PREDICATE IS WHY THIS FILE COULD NOT SEE #141.
	// `fieldUpdated` is Track's DISPLAY name ("last-updated time"), and the only sentence that ever
	// contained it for viaNoUpdatedField was the `default:` arm — `"%s — imported as %q"`, which
	// interpolates n.Field. So this assertion was satisfied by, and ONLY by, the fallthrough that
	// meant the via had no sentence of its own. It is now the Created twin's convention verbatim
	// (api_created_job_test.go): a HARDCODED provider literal, for #75's C6 reason — an assertion
	// written against the same constant the code sends compares the constant to itself.
	if !strings.Contains(report, "updated") {
		t.Errorf("job report %q does not name the field that did not land", report)
	}
	// ⚠ TWO DISTINGUISHABLE LINES, NOT ONE — #74's rule at this door. "the response carried no
	// `updated` key" and "it carried one no pinned layout accepts" are different provider facts,
	// and collapsing them is how a serialisation change gets read as a missing field.
	//
	// ⚠ THE LINES ARE COUNTED, NOT FILTERED-THEN-COUNTED, for the same reason as above: the filter
	// was `Contains(w, fieldUpdated)`, so a via with a sentence of its own DROPPED OUT of the
	// count — the better the report got, the fewer lines this saw. The Created twin counts
	// `len(jobWarnings(...))` and that is the shape a distinctness check should have.
	if lines := jobWarnings(t, d, jobID); len(lines) < 2 {
		t.Errorf("an ABSENT updated and an UNPARSEABLE updated produced %d line(s): %q. "+
			"They are different provider facts and must not collapse into one.",
			len(lines), report)
	}
}

// ── LINEAR API ────────────────────────────────────────────────────────────────────────────────

func TestJobRow_LinearAPI_ImportedIssueKeepsTheDateLinearLastUpdatedIt(t *testing.T) {
	d := testutil.New(t)
	created, updated := apiUpdatedInstants()
	node := linNodeUpdatedAt(linNodeCreated("ENG-1", "Todo", "unstarted", "",
		created.Format(linearAPICreatedTestLayout)), updated.Format(linearAPICreatedTestLayout))
	wsID, _ := runLinearAPICreatedImport(t, d, node)

	got := readUpdatedAt(t, d, wsID, "ENG-1")
	if delta := got.Sub(updated); delta > time.Minute || delta < -time.Minute {
		t.Errorf("updated_at = %s, want %s (Linear's `updatedAt`) — off by %s.\n"+
			"A defaulted updated_at is the instant the IMPORT ran.",
			got.Format(time.RFC3339), updated.Format(time.RFC3339), delta)
	}
}

func TestJobRow_LinearAPI_AStaleImportDoesNotOutrankTodaysWork(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	native := createTodaysWork(t, d, ws.ID, team.ID, "creator-native-w34-api-updated-linear")

	created, updated := apiUpdatedInstants()
	node := linNodeUpdatedAt(linNodeCreated("ENG-1", "Todo", "unstarted", "",
		created.Format(linearAPICreatedTestLayout)), updated.Format(linearAPICreatedTestLayout))
	node = retitleLinearNode(node, "ENG-1", "widget beta untouched for months")
	runLinearAPICreatedImportInto(t, d, ws.ID, team.ID, node)

	assertStaleImportDoesNotOutrankTodaysWork(t, d, ws.ID, native.ID, native.Title, "linear_api")
}

func TestJobRow_LinearAPI_NullUpdatedAtIsReportedNotDefaulted(t *testing.T) {
	d := testutil.New(t)
	created, _ := apiUpdatedInstants()
	node := linNodeUpdatedAt(linNodeCreated("ENG-1", "Todo", "unstarted", "",
		created.Format(linearAPICreatedTestLayout)), "")
	_, jobID := runLinearAPICreatedImport(t, d, node)

	report := strings.Join(jobWarnings(t, d, jobID), " | ")
	// The empty-report case its Created twin asserts, which this one never did — and without it a
	// silent import satisfies nothing below, because "" contains no literal either way.
	if report == "" {
		t.Fatal("a field Linear declares NON_NULL (Issue.updatedAt: DateTime!) arrived null and the " +
			"job said nothing. The row still carries a defaulted updated_at, so nothing else can say so.")
	}
	// The literal, not `fieldUpdated` — see the Jira sibling above for why that constant made this
	// assertion a test of the default branch rather than of this via's own sentence.
	if !strings.Contains(report, "updated") {
		t.Errorf("Linear declares Issue.updatedAt as DateTime! (NON_NULL). A null arriving there "+
			"means the transport changed, and the job report must name it. Got %q", report)
	}
}
