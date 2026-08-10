package importer

import (
	"context"
	"fmt"
	"math"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/talyvor/track/internal/analytics"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/testutil"
)

// api_resolution_job_test.go — the Jira API transport's resolution rule driven END TO END through
// the async runner onto real Postgres, and then read back THROUGH THE REPORT THAT MISCOUNTS IT.
//
// ⚠ THIS IS NOT THE UNIT TEST TWICE, AND THIS PACKAGE HAS PAID FOR THAT SIX TIMES. The mapper and
// the SQL each hold part of this invariant and only a database read proves they agree. The API
// transport writes through issue.Store.UpsertByIdentifier, NOT Create — and the two statements do
// NOT gate identically: Create nils `completed_at` itself when the status is not done
// (store.go:239), while the UPSERT passes `issue.CompletedAt` THROUGH UNGATED. So on this transport
// the mapper's gate is the ONLY gate, which is exactly the difference #74/#78/#83/#84/#86 kept
// finding between the two write paths, and it is why the mapper must run the resolution rule BEFORE
// jiraCompletedAt rather than after it. A mapper-only test cannot see that ordering; this can.
//
// ⚠ AND THE CONSUMER HALF IS WHAT MAKES THIS A PRODUCT DEFECT RATHER THAN A WRONG COLUMN.
// analytics.GetTimeToResolution selects on `completed_at IS NOT NULL` with NO status predicate, so
// every abandoned issue that lands with a completion time enters the throughput and cycle-time
// numbers as delivered work. The two fixtures below are shaped so the report gives a DIFFERENT
// ANSWER depending on whether the abandoned row is counted — a column assertion alone would pass on
// a fix that never reached the statement.

// jiraIssueResolutionDatedJSON is jiraIssueResolutionJSON with the two instants supplied, because
// this file's assertion is about a NUMBER the report computes from them.
func jiraIssueResolutionDatedJSON(key, summary, status, resolutionJSON, created, resolved string) string {
	res := ""
	if resolutionJSON != "" {
		res = fmt.Sprintf(`,"resolution":%s`, resolutionJSON)
	}
	return fmt.Sprintf(`{"key":%q,"fields":{"summary":%q,"description":null,"status":{"name":%q},`+
		`"priority":{"name":"Medium"},"labels":[],"resolutiondate":%q,"created":%q,"updated":%q%s}}`,
		key, summary, status, resolved, created, resolved, res)
}

// jiraCloudInstant serialises an instant the way the measured Cloud site does — three fractional
// digits and a NUMERIC offset. Not time.RFC3339: #74 pinned these layouts from real bytes and
// RFC3339 refuses this shape, so a fixture written with the obvious constant would test a value the
// importer never accepts.
func jiraCloudInstant(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000-0700") }

// ⚠ THE THREE CYCLE TIMES ARE ALL DIFFERENT, AND MY FIRST DRAFT'S WERE NOT — THE CONSUMER
// ASSERTION WAS INERT AND IT PASSED ON THE FIRST RUN. With the finished and the unreadable row both
// at 9 days, PERCENTILE_CONT over {216, 216, 4776} is 216 and over {216, 216} is also 216: the
// median is IDENTICAL whether or not the abandoned row is counted, so the one assertion that makes
// this a product defect rather than a wrong column could never have failed. Found by running it —
// every other assertion in the test reddened and that one did not. Three distinct durations, chosen
// so that dropping the abandoned row MOVES the answer:
//
//	counted today   {216, 1176, 4776} ⇒ median 1176
//	counted after   {216, 1176}       ⇒ median  696
const (
	resolutionFinishedOpenedDaysAgo   = 10  // ⇒  9 days ⇒  216 h
	resolutionUnreadableOpenedDaysAgo = 50  // ⇒ 49 days ⇒ 1176 h
	resolutionAbandonedOpenedDaysAgo  = 200 // ⇒ 199 days ⇒ 4776 h
	resolutionResolvedDaysAgo         = 1

	// PERCENTILE_CONT(0.5) over the two rows that really were delivered.
	resolutionWantMedianHours = float64(
		((resolutionFinishedOpenedDaysAgo-resolutionResolvedDaysAgo)*24 +
			(resolutionUnreadableOpenedDaysAgo-resolutionResolvedDaysAgo)*24) / 2)
)

func readResolutionRows(t *testing.T, d *testutil.DB, wsID string) map[string]importedResolutionRow {
	t.Helper()
	rows, err := d.Pool.Query(context.Background(),
		`SELECT identifier, status, completed_at IS NOT NULL FROM issues WHERE workspace_id = $1`, wsID)
	if err != nil {
		t.Fatalf("read back issues: %v", err)
	}
	defer rows.Close()
	out := map[string]importedResolutionRow{}
	for rows.Next() {
		var ident, status string
		var hasComplete bool
		if err := rows.Scan(&ident, &status, &hasComplete); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[ident] = importedResolutionRow{status, hasComplete}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	return out
}

type importedResolutionRow struct {
	status      string
	hasComplete bool
}

func TestJobRow_JiraAPI_AbandonedWorkLandsCancelledAndUncountedInPostgres(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	now := time.Now().UTC()
	resolved := jiraCloudInstant(now.Add(-resolutionResolvedDaysAgo * 24 * time.Hour))
	finishedOpened := jiraCloudInstant(now.Add(-resolutionFinishedOpenedDaysAgo * 24 * time.Hour))
	unreadableOpened := jiraCloudInstant(now.Add(-resolutionUnreadableOpenedDaysAgo * 24 * time.Hour))
	abandonedOpened := jiraCloudInstant(now.Add(-resolutionAbandonedOpenedDaysAgo * 24 * time.Hour))

	srv := httptest.NewServer(cannedPages([]string{jiraAPIPage(
		jiraIssueResolutionDatedJSON("PROJ-1", "abandoned in postgres", "Closed",
			`{"id":"1","name":"Won't Fix"}`, abandonedOpened, resolved),
		jiraIssueResolutionDatedJSON("PROJ-2", "finished in postgres", "Closed",
			`{"id":"10","name":"Done"}`, finishedOpened, resolved),
		jiraIssueResolutionDatedJSON("PROJ-3", "unreadable in postgres", "Closed",
			`{"id":"5","name":"Rejected"}`, unreadableOpened, resolved),
	)}, jiraAPIPage()))
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

	j, err := NewJobStore(d.Pool).Get(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	// Every row LANDS. A reclassification is not a failure and must not be counted as one — #81's
	// lesson, one field over.
	if j.Status != JobSucceeded || j.Imported != 3 || j.Failed != 0 || j.Skipped != 0 {
		t.Fatalf("job row = {status:%q imported:%d failed:%d skipped:%d}, want {succeeded 3 0 0}",
			j.Status, j.Imported, j.Failed, j.Skipped)
	}

	got := readResolutionRows(t, d, ws.ID)
	for _, tc := range []struct {
		ident       string
		wantStatus  string
		wantHasDate bool
		why         string
	}{
		{"PROJ-1", string(model.StatusCancelled), false,
			`Jira resolved it "Won't Fix"; a completion time here is counted as delivered work by resolution-stats`},
		{"PROJ-2", string(model.StatusDone), true, `Jira resolved it "Done" — nothing about this row may change`},
		{"PROJ-3", string(model.StatusDone), true, `Jira resolved it "Rejected", which Track refuses to interpret — nothing may change`},
	} {
		row, ok := got[tc.ident]
		if !ok {
			t.Errorf("%s is not in the issues table at all", tc.ident)
			continue
		}
		if row.status != tc.wantStatus {
			t.Errorf("%s: status column = %q, want %q — %s", tc.ident, row.status, tc.wantStatus, tc.why)
		}
		if row.hasComplete != tc.wantHasDate {
			t.Errorf("%s: completed_at IS NOT NULL = %v, want %v — %s", tc.ident, row.hasComplete, tc.wantHasDate, tc.why)
		}
	}

	// ⚠ THE CONSUMER. With the abandoned row counted the median is the midpoint of 9 days and 199
	// days; with it excluded the report says 9 days, which is how long the delivered work took.
	stats, err := analytics.New(d.Pool).GetTimeToResolution(ctx, ws.ID, "", 365)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(stats.MedianHours-resolutionWantMedianHours) > 0.01 {
		t.Errorf("analytics median time to resolution = %.1f h, want %.1f h.\n"+
			"An issue Jira resolved \"Won't Fix\" is carrying a completion time, and "+
			"GetTimeToResolution selects on `completed_at IS NOT NULL` with no status predicate — so "+
			"abandoned work is being reported as delivered throughput and is dragging the cycle time "+
			"with it.", stats.MedianHours, resolutionWantMedianHours)
	}

	// The warnings reach the JOB ROW's TEXT[], not just ImportResult — 0026's channel is the one a
	// real import is read through, and a report that stops at the struct is inert exactly there.
	var sawOverride, sawRefusal bool
	for _, w := range j.Warnings {
		if strings.Contains(w, `resolution "Won't Fix"`) && strings.Contains(w, `"cancelled"`) {
			sawOverride = true
		}
		if strings.Contains(w, `resolution "Rejected"`) {
			sawRefusal = true
		}
	}
	if !sawOverride || !sawRefusal {
		t.Errorf("job warnings do not carry both the override and the refusal: %#v", j.Warnings)
	}
}

// ⚠ THE STRUCTURAL ZERO, END TO END. A response with no `resolution` key at all is what a typo or a
// rename in jiraFields produces — measured on the shipped endpoint, an unknown field name is
// answered HTTP 200 with the key simply absent. The rows then land EXACTLY as they do today, which
// is the point: the only thing that distinguishes "Track read your resolutions" from "Track recorded
// every abandoned issue as delivered" is this line in the job's warnings.
func TestJobRow_JiraAPI_AResponseWithNoResolutionFieldSaysSoInTheJobRow(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	now := time.Now().UTC()
	srv := httptest.NewServer(cannedPages([]string{jiraAPIPage(
		jiraIssueResolutionDatedJSON("PROJ-1", "no resolution key", "Closed", "",
			jiraCloudInstant(now.Add(-10*24*time.Hour)), jiraCloudInstant(now.Add(-24*time.Hour))),
	)}, jiraAPIPage()))
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
	j, err := NewJobStore(d.Pool).Get(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if j.Imported != 1 {
		t.Fatalf("job row imported = %d, want 1 — an absent resolution must not stop a row landing", j.Imported)
	}
	// ⚠ IT ASSERTS THE STRUCTURAL-ZERO SENTENCE, NOT MERELY THAT THE WORD "resolution" APPEARS —
	// AND THE FIRST DRAFT DID THE LATTER AND A CONTROL WALKED THROUGH IT. Blinding the absent-key
	// branch makes the empty bytes fall through to the decoder, which reports them as an
	// UNREADABLE resolution; that line also contains "resolution" and "1 issue(s)", so the loose
	// assertion stayed GREEN on a mutation that removed the very thing this test exists for. The
	// two sentences point an operator at different places — one at their `fields` list, one at
	// their resolution vocabulary — so only one of them is the right answer here.
	var said bool
	for _, w := range j.Warnings {
		if strings.Contains(w, `carried no "resolution" field`) && strings.Contains(w, "1 issue(s)") {
			said = true
		}
	}
	if !said {
		t.Errorf("job warnings say nothing about a response that carried no resolution field: %#v.\n"+
			"That is the one state in which this whole rule is silently inert, and it is reachable "+
			"from a single typo in jiraFields.", j.Warnings)
	}
}
