package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/talyvor/track/internal/analytics"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/testutil"
)

// api_created_job_test.go — the issue's OPENING TIME on the TWO API TRANSPORTS, driven end to end
// through the async runner onto real Postgres and then read back THROUGH THE REPORT THAT CONSUMES IT.
//
// ⚠ WHY THIS EXISTS WHEN #83 ALREADY MERGED `created_at`. #83 fixed the CSV transport, whose write
// path is issue.Store.Create. The two API transports reach a DIFFERENT STATEMENT — UpsertByIdentifier
// — which #83 deliberately left alone and said so: "THE SQL HALF WAS DELIBERATELY NOT LANDED IN THE
// UPSERT WITH NO PRODUCER — an un-fed column is untestable and rots." So the fix is not inherited,
// and neither is the evidence. Both halves, mapper AND upsert, land here together.
//
// ⚠ AND THE SEAM IS THE SAME ONE THIS ITEM HAS NOW PAID FOR FOUR TIMES, arriving a fourth way. #74
// found the importer's UPSERT omitting `completed_at`; #78 found the SECOND copy in Create's INSERT;
// #83 found the THIRD for `created_at` in Create. This is the FOURTH: `created_at` in the UPSERT.
// Enumerate every copy of a seam — a green guard at copy #3 says nothing about copy #4.
//
// ⚠ A COLUMN TEST ALONE CANNOT SEE THIS DEFECT, which is why every case below has an analytics half.
// `issues.created_at` is TIMESTAMPTZ DEFAULT NOW(), so the column is ALWAYS non-null and ALWAYS
// looks populated. The wrong value and the right value have identical shape. The only place they
// differ is the number computed from them:
//
//	analytics.GetTimeToResolution ⇒ EXTRACT(EPOCH FROM completed_at - created_at)/3600
//
// #74 (Jira) and #76/#77 (Linear) deliberately landed `completed_at` FROM THE PROVIDER, so today
// that subtraction is (a past instant) − (the import instant), which is NEGATIVE.
//
// MEASURED AT THE WIRE BEFORE ANY OF THIS WAS WRITTEN, each half negative-controlled first and each
// re-run rather than quoted from a sibling merge (scripts/w34-jira-api-created-probe.py and
// scripts/w34-linear-api-created-probe.py, both committed, both FAIL rather than report if a control
// answers the way a success does):
//
//	JIRA — and ⚠ THIS ONE REACHES THE ENDPOINT THE CODE ACTUALLY CALLS, closing #75's caveat that
//	this package's Jira provenance was v2/Server-DC while the client POSTs v3/Cloud.
//	hibernate.atlassian.net answers POST /rest/api/3/search/jql ANONYMOUSLY:
//	  created arrives as "2026-08-07T16:02:31.638-0700"   ← Cloud v3, offset -0700
//	  the same field on Server v2 is "2026-08-07T12:54:09.000+0000"   ← offset +0000
//	  ⚠ THE DECISIVE CONTROL: asking for fields=["summary"] ALONE returns ONLY summary, so `created`
//	    comes back BECAUSE THE FIELDS LIST ASKED FOR IT. Without that control "created was in the
//	    response" would prove nothing about jiraFields.
//	  ⚠ AND AN UNKNOWN FIELD NAME IS SILENTLY IGNORED (HTTP 200), so a MISSPELLING in jiraFields
//	    cannot be caught at the wire by an error code. Only the value coming back catches it.
//	  On 100 real resolved issues: TRUE cycle time median 88.7h; what Track computes today
//	  median −408.3h; NEGATIVE 100 of 100; correct 0 of 100.
//
//	LINEAR — #76's pre-auth technique RE-RUN for this field, not cited:
//	  an unknown Issue field           ⇒ HTTP 400 GRAPHQL_VALIDATION_FAILED
//	  the shipped query today          ⇒ HTTP 401 AUTHENTICATION_ERROR
//	  the shipped query + createdAt    ⇒ HTTP 401 AUTHENTICATION_ERROR   ⇒ WELL-FORMED
//	  Issue.createdAt is declared `DateTime!` — NON_NULL — where Issue.completedAt is `DateTime`.
//
// The window matters, which is why every instant below is COMPUTED rather than written down: both
// analytics queries filter `created_at > NOW() - INTERVAL '1 day' * $2`, so a hardcoded date would
// age out of the window and the test would stop testing anything while staying green.

const (
	apiCreatedDaysAgo  = 200
	apiResolvedDaysAgo = 100
	apiTrueCycleHours  = float64(apiCreatedDaysAgo-apiResolvedDaysAgo) * 24

	// The MEASURED Cloud v3 serialisation, HARDCODED rather than taken from jiraTimeLayouts. #75's
	// C6: a fixture formatted with the same constant the code parses with compares the constant to
	// itself and passes for every possible value, including "".
	jiraAPICreatedTestLayout = "2006-01-02T15:04:05.000-0700"
	// Linear's fixtures in this package use the Z form; kept identical so this test does not
	// quietly become a second serialisation claim it has no evidence for.
	linearAPICreatedTestLayout = "2006-01-02T15:04:05.000Z"
)

// apiCreatedInstants returns the two instants every case below shares.
//
// ⚠ THE JIRA ONE IS DELIBERATELY NOT UTC. The measured Cloud bytes carry `-0700`, and a fixture
// written in UTC would render `+0000` and silently test the Server-DC shape instead of the shipped
// one. Formatting in a −07:00 zone is what makes this fixture the measured bytes.
func apiCreatedInstants() (created, resolved time.Time) {
	now := time.Now().UTC()
	created = now.Add(-time.Duration(apiCreatedDaysAgo) * 24 * time.Hour).Truncate(time.Second)
	resolved = now.Add(-time.Duration(apiResolvedDaysAgo) * 24 * time.Hour).Truncate(time.Second)
	return created, resolved
}

func jiraAPICreatedFixtureTime(t time.Time) string {
	return t.In(time.FixedZone("measured", -7*3600)).Format(jiraAPICreatedTestLayout)
}

// jiraIssueAPICreatedJSON shapes a v3 issue carrying `created` exactly as the real Cloud instance
// serialises it. An empty created omits the key entirely — a response that carries no such field.
func jiraIssueAPICreatedJSON(key, summary, status, created, resolution string) string {
	field := func(name, v string) string {
		if v == "" {
			return ""
		}
		return fmt.Sprintf(`,%q:%q`, name, v)
	}
	return fmt.Sprintf(`{"key":%q,"fields":{"summary":%q,"description":null,"status":{"name":%q},`+
		`"priority":{"name":"Medium"},"labels":[]%s%s}}`,
		key, summary, status, field("created", created), field("resolutiondate", resolution))
}

// linNodeCreated is linNodeDated plus `createdAt`. An empty createdAt sends JSON null — which for a
// DateTime! field means the transport changed, and is a state the report must be able to name.
// linNodeCreated overrides the opening time linNodeDated supplies by default. An empty created
// sends JSON null — which, for a field Linear declares NON_NULL, means the transport changed.
//
// ⚠ IT PANICS RATHER THAN RETURNING AN UNCHANGED NODE. A string substitution that matches nothing
// edits zero bytes and is byte-indistinguishable from a fixture that works — #71 lost two positive
// controls to exactly that, and #83 lost another to a replacement that applied and meant nothing.
// If linNodeDated's shape moves, this stops the suite instead of quietly testing the default value.
func linNodeCreated(id, stateName, stateType, completed, created string) string {
	node := linNodeDated(id, stateName, stateType, "", completed, 1)
	oldField := fmt.Sprintf(`,"createdAt":%q`, fixtureLinearCreated)
	if strings.Count(node, oldField) != 1 {
		panic("linNodeCreated: linNodeDated no longer emits exactly one default createdAt — " +
			"this fixture would silently test the default instead of the case it names")
	}
	newField := `,"createdAt":null`
	if created != "" {
		newField = fmt.Sprintf(`,"createdAt":%q`, created)
	}
	return strings.Replace(node, oldField, newField, 1)
}

// ── the shared drivers ────────────────────────────────────────────────────────────────────────

func runJiraAPICreatedImport(t *testing.T, d *testutil.DB, issues ...string) (wsID, jobID string) {
	t.Helper()
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	srv := httptest.NewServer(cannedPages([]string{jiraAPIPage(issues...)}, jiraAPIPage()))
	defer srv.Close()

	istore := testIntegrationStore(t, d)
	if _, err := istore.Upsert(ctx, ws.ID, "jira", "me@corp.com:api-token", "PROJ", srv.URL); err != nil {
		t.Fatal(err)
	}
	jobID = insertAPIJob(t, d, ws.ID, team.ID, "jira_api")

	runner := NewRunner(NewJobStore(d.Pool), New(issue.NewStore(d.Pool))).
		WithProviderConfig(istore).WithHTTPClient(srv.Client())
	if did, err := runner.RunOnce(ctx); err != nil || !did {
		t.Fatalf("RunOnce did=%v err=%v", did, err)
	}
	return ws.ID, jobID
}

func runLinearAPICreatedImport(t *testing.T, d *testutil.DB, nodes ...string) (wsID, jobID string) {
	t.Helper()
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	srv := httptest.NewServer(cannedPages([]string{linearAPIPage(nodes...)}, linearAPIPage()))
	defer srv.Close()

	istore := testIntegrationStore(t, d)
	if _, err := istore.Upsert(ctx, ws.ID, "linear", "api-token", "LINEAR-TEAM-KEY", srv.URL); err != nil {
		t.Fatal(err)
	}
	jobID = insertAPIJob(t, d, ws.ID, team.ID, "linear_api")

	runner := NewRunner(NewJobStore(d.Pool), New(issue.NewStore(d.Pool))).
		WithProviderConfig(istore).WithHTTPClient(srv.Client())
	if did, err := runner.RunOnce(ctx); err != nil || !did {
		t.Fatalf("RunOnce did=%v err=%v", did, err)
	}
	return ws.ID, jobID
}

// jobWarnings reads the job's report out of the `warnings` TEXT[] column (migration 0026's channel),
// which is where a row that DID import with a field the mapper could not place is named.
func jobWarnings(t *testing.T, d *testutil.DB, jobID string) []string {
	t.Helper()
	j, err := NewJobStore(d.Pool).Get(context.Background(), jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	return j.Warnings
}

func readCreatedAt(t *testing.T, d *testutil.DB, wsID, ident string) time.Time {
	t.Helper()
	var got time.Time
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT created_at FROM issues WHERE workspace_id=$1 AND identifier=$2`, wsID, ident).Scan(&got); err != nil {
		t.Fatalf("read created_at for %s: %v", ident, err)
	}
	return got.UTC()
}

func assertCycleTimeIsTrue(t *testing.T, d *testutil.DB, wsID, transport string) {
	t.Helper()
	stats, err := analytics.New(d.Pool).GetTimeToResolution(context.Background(), wsID, "", 365)
	if err != nil {
		t.Fatalf("resolution stats: %v", err)
	}
	if stats.MedianHours <= 0 {
		t.Errorf("%s: median time to resolution = %.1f hours for an issue the provider opened %d days "+
			"ago and finished %d days ago.\nA negative cycle time is completed_at (past) minus "+
			"created_at (the import instant) — the column is DEFAULTed, so it never looks empty.",
			transport, stats.MedianHours, apiCreatedDaysAgo, apiResolvedDaysAgo)
	}
	if delta := stats.MedianHours - apiTrueCycleHours; delta > 24 || delta < -24 {
		t.Errorf("%s: median time to resolution = %.1f hours, want ≈ %.0f (completed − created)",
			transport, stats.MedianHours, apiTrueCycleHours)
	}
}

// ── JIRA API ──────────────────────────────────────────────────────────────────────────────────

func TestJobRow_JiraAPI_ImportedIssueKeepsTheDateJiraOpenedIt(t *testing.T) {
	d := testutil.New(t)
	created, resolved := apiCreatedInstants()
	wsID, _ := runJiraAPICreatedImport(t, d, jiraIssueAPICreatedJSON("PROJ-1", "Opened long before the import",
		"Done", jiraAPICreatedFixtureTime(created), jiraAPICreatedFixtureTime(resolved)))

	got := readCreatedAt(t, d, wsID, "PROJ-1")
	if delta := got.Sub(created); delta > time.Minute || delta < -time.Minute {
		t.Errorf("created_at = %s, want %s (Jira's `created`) — off by %s.\n"+
			"A defaulted created_at is the instant the IMPORT ran, so every imported issue reads as "+
			"opened today and every cycle-time number computed from it is wrong.",
			got.Format(time.RFC3339), created.Format(time.RFC3339), delta)
	}
}

func TestJobRow_JiraAPI_CycleTimeOfAnImportedIssueIsNotNegative(t *testing.T) {
	d := testutil.New(t)
	created, resolved := apiCreatedInstants()
	wsID, _ := runJiraAPICreatedImport(t, d, jiraIssueAPICreatedJSON("PROJ-1", "Opened long before the import",
		"Done", jiraAPICreatedFixtureTime(created), jiraAPICreatedFixtureTime(resolved)))
	assertCycleTimeIsTrue(t, d, wsID, "jira_api")
}

// TestJobRow_JiraAPI_MissingCreatedIsReportedNotDefaulted is #74's REPORTED-NEVER-DEFAULTED rule at
// the door where it carries the most weight. Without it, "Track read your opening times" and "Track
// recorded every one of these as opened today" are BYTE-IDENTICAL in the report — because the
// column is defaulted, there is no null anywhere for anyone to notice.
func TestJobRow_JiraAPI_MissingCreatedIsReportedNotDefaulted(t *testing.T) {
	d := testutil.New(t)
	_, resolved := apiCreatedInstants()
	_, jobID := runJiraAPICreatedImport(t, d,
		jiraIssueAPICreatedJSON("PROJ-1", "No created at all", "Done", "", jiraAPICreatedFixtureTime(resolved)),
		jiraIssueAPICreatedJSON("PROJ-2", "Unparseable created", "Done", "next tuesday", ""))

	report := strings.Join(jobWarnings(t, d, jobID), " | ")
	if report == "" {
		t.Fatal("the job reported NOTHING for two issues whose opening time did not land. " +
			"Both rows still carry a plausible created_at (the DEFAULT), so silence here is the only " +
			"way a reader could ever find out.")
	}
	if !strings.Contains(report, "created") {
		t.Errorf("job report %q does not name the field that did not land", report)
	}
	// ⚠ TWO DISTINGUISHABLE LINES, NOT ONE. "the response carried no `created` key" and "it carried
	// one no pinned layout accepts" are different facts about the provider, and collapsing them is
	// how a serialisation change gets read as a missing field. #74's rule, at this door.
	if len(jobWarnings(t, d, jobID)) < 2 {
		t.Errorf("an ABSENT created and an UNPARSEABLE created produced %d line(s): %q. "+
			"They are different provider facts and must not collapse into one.",
			len(jobWarnings(t, d, jobID)), report)
	}
}

// ── LINEAR API ────────────────────────────────────────────────────────────────────────────────

func TestJobRow_LinearAPI_ImportedIssueKeepsTheDateLinearOpenedIt(t *testing.T) {
	d := testutil.New(t)
	created, resolved := apiCreatedInstants()
	wsID, _ := runLinearAPICreatedImport(t, d, linNodeCreated("ENG-1", "Done", "completed",
		resolved.Format(linearAPICreatedTestLayout), created.Format(linearAPICreatedTestLayout)))

	got := readCreatedAt(t, d, wsID, "ENG-1")
	if delta := got.Sub(created); delta > time.Minute || delta < -time.Minute {
		t.Errorf("created_at = %s, want %s (Linear's `createdAt`) — off by %s.",
			got.Format(time.RFC3339), created.Format(time.RFC3339), delta)
	}
}

func TestJobRow_LinearAPI_CycleTimeOfAnImportedIssueIsNotNegative(t *testing.T) {
	d := testutil.New(t)
	created, resolved := apiCreatedInstants()
	wsID, _ := runLinearAPICreatedImport(t, d, linNodeCreated("ENG-1", "Done", "completed",
		resolved.Format(linearAPICreatedTestLayout), created.Format(linearAPICreatedTestLayout)))
	assertCycleTimeIsTrue(t, d, wsID, "linear_api")
}

// TestJobRow_LinearAPI_NullCreatedAtIsReported — Issue.createdAt is DateTime! (measured, NON_NULL),
// so a null here does not mean "this issue has no opening time"; it means the transport changed.
// That is a different sentence from Jira's absent-column case and is reported as one.
func TestJobRow_LinearAPI_NullCreatedAtIsReported(t *testing.T) {
	d := testutil.New(t)
	_, resolved := apiCreatedInstants()
	_, jobID := runLinearAPICreatedImport(t, d, linNodeCreated("ENG-1", "Done", "completed",
		resolved.Format(linearAPICreatedTestLayout), ""))

	report := strings.Join(jobWarnings(t, d, jobID), " | ")
	if report == "" {
		t.Fatal("a field Linear declares NON_NULL (Issue.createdAt: DateTime!) arrived null and the " +
			"job said nothing. The row still carries a defaulted created_at, so nothing else can say so.")
	}
	if !strings.Contains(report, "created") {
		t.Errorf("job report %q does not name the field", report)
	}
}

// ── THE WIRE HALF ─────────────────────────────────────────────────────────────────────────────
//
// ⚠ EVERY TEST ABOVE WOULD STAY GREEN IF THE REQUEST STOPPED ASKING FOR THE FIELD. The canned
// servers in this package answer ANY body with the same page, so a fixture supplies `created`
// whether or not jiraFields names it — a control that narrows the request is CAUGHT BY NOTHING
// above. #74 hit this for the date fields and #75 for the endpoint itself; it is the same hole,
// one field over, and it is only visible by reading the bytes the client actually sends.
//
// The literals are HARDCODED, never `jiraAPICreatedField`. #75's C6: an assertion written against
// the same constant the code sends compares the constant to itself and passes for every value.

func TestJiraRequest_AsksForTheCreationTime(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issues":[],"isLast":true}`))
	}))
	defer srv.Close()

	src := newJiraSource(context.Background(), "e:t", "PROJ", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep
	src.Next()

	var sent struct {
		Fields []string `json:"fields"`
	}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("decode outgoing body %q: %v", body, err)
	}
	if !containsString(sent.Fields, "created") {
		t.Errorf("outgoing fields %v does not ask for \"created\" — the field would never arrive, "+
			"and because created_at is DEFAULT NOW() the rows would still look populated.", sent.Fields)
	}
}

func TestLinearRequest_AsksForTheCreationTime(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"team":{"issues":{"pageInfo":{"hasNextPage":false},"nodes":[]}}}}`))
	}))
	defer srv.Close()

	src := newLinearSource(context.Background(), "tok", "TEAM", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep
	src.Next()

	var sent struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("decode outgoing body %q: %v", body, err)
	}
	if !strings.Contains(sent.Query, "createdAt") {
		t.Errorf("the outgoing GraphQL document does not select createdAt:\n%s", sent.Query)
	}
	// ⚠ AND IT MUST BE IN THE ISSUE NODE SELECTION, not merely somewhere in the document. A
	// `createdAt` selected on `team` would satisfy a bare Contains and land nothing on any issue.
	nodes := strings.Index(sent.Query, "nodes {")
	if nodes < 0 || !strings.Contains(sent.Query[nodes:], "createdAt") {
		t.Errorf("createdAt is not inside the issue node selection:\n%s", sent.Query)
	}
}
