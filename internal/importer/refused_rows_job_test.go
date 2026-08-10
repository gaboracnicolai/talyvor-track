package importer

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/testutil"
)

// refused_rows_job_test.go — W3.4: A ROW THE IMPORTER PROTECTIVELY REFUSED IS NOT A ROW THAT FAILED.
//
// #71 built the refusal deliberately: UpsertByIdentifier's `DO UPDATE ... WHERE creator_id =
// ImporterCreatorID` means a provider may update rows the IMPORTER created and may NOT overwrite a
// human's. store.go's own comment calls the outcome "a Skipped row ... the difference between 'we did
// not import one issue' and a silent overwrite".
//
// MEASURED at dcfbaa3, driven through the ASYNC RUNNER to the JOB ROW (which no test had done — #71's
// tests stop at ImportResult, #74's C9 lesson one seam over):
//
//	1 of 3 rows refused  ⇒  {status:partial, imported:2, skipped:0, failed:1}
//	                        error_summary "1 row(s) failed; first: ... refusing to overwrite it"
//	3 of 3 rows refused  ⇒  {status:failed,  imported:0, skipped:0, failed:3}
//	                        error_summary "3 row(s) failed; ..."
//
// The second line is a CORRECT import: three human-written issues were protected, the database holds
// exactly what it should, nothing went wrong — and the job says it FAILED, three times over. That is
// this item's "data loss reported as success" shape INVERTED: correct behaviour reported as failure.
//
// ⚠ WHAT THIS FILE DOES NOT CHANGE, AND IT IS PINNED BELOW RATHER THAN LEFT TO DRIFT: the job's
// TERMINAL STATUS. Whether an import whose every row was refused is "succeeded", "partial" or
// "failed" is a product judgement with three defensible answers — and making it `succeeded` would
// let an import that landed nothing read as clean, which is the exact shape this item has found
// eight times. terminalStatus therefore still counts refusals, and TestJobRow_AllRowsRefused pins
// that it STILL reports `failed`. Nothing here becomes quieter than it is today; only the counters
// and the sentence stop misnaming what happened.

// refusalTeam seeds a team whose identifier is EXACTLY id, so the Track-derived key and the provider
// key collide in the one un-namespaced column — #71's premise, restated at the job level.
func refusalTeam(t *testing.T, d *testutil.DB, workspaceID, id string) *model.Team {
	t.Helper()
	return teamWithIdentifier(t, d, workspaceID, id)
}

// runJiraAPIJob enqueues a jira_api job whose provider answers with the given issue JSON blobs and
// drains it through the real async runner. Returns the finished job row.
func runJiraAPIJob(t *testing.T, d *testutil.DB, ws, teamID string, issuesJSON ...string) *Job {
	t.Helper()
	ctx := context.Background()
	srv := httptest.NewServer(cannedPages([]string{jiraAPIPage(issuesJSON...)}, jiraAPIPage()))
	t.Cleanup(srv.Close)

	istore := testIntegrationStore(t, d)
	if _, err := istore.Upsert(ctx, ws, "jira", "me@corp.com:api-token", "ENG", srv.URL); err != nil {
		t.Fatalf("seed integration: %v", err)
	}
	jobID := insertAPIJob(t, d, ws, teamID, "jira_api")

	runner := NewRunner(NewJobStore(d.Pool), New(issue.NewStore(d.Pool))).
		WithProviderConfig(istore).WithHTTPClient(srv.Client())
	if did, err := runner.RunOnce(ctx); err != nil || !did {
		t.Fatalf("RunOnce did=%v err=%v", did, err)
	}
	j, err := NewJobStore(d.Pool).Get(ctx, jobID)
	if err != nil {
		t.Fatalf("read job row: %v", err)
	}
	return j
}

// seedNative writes n issues as a HUMAN (creator "user-1"), taking identifiers ENG-1..ENG-n.
func seedNative(t *testing.T, d *testutil.DB, ws, teamID string, n int) {
	t.Helper()
	ctx := context.Background()
	store := issue.NewStore(d.Pool)
	for i := 0; i < n; i++ {
		if _, err := store.Create(ctx, model.Issue{
			WorkspaceID: ws, TeamID: teamID,
			Title: "a human wrote this", Description: "and this", CreatorID: "user-1",
		}); err != nil {
			t.Fatalf("seed native issue %d: %v", i+1, err)
		}
	}
}

func doneIssue(key, summary string) string {
	return jiraIssueWithCategoryJSON(key, summary, "Done", "done", "Done")
}

// TestJobRow_RefusedRowIsCountedAsRefusedNotFailed — the mixed case. Two provider rows land; the
// third is refused because a human owns that identifier. The refusal must reach the column that
// exists for it and must NOT be counted as a failure.
func TestJobRow_RefusedRowIsCountedAsRefusedNotFailed(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	tm := refusalTeam(t, d, ws.ID, "ENG")
	seedNative(t, d, ws.ID, tm.ID, 1) // ENG-1 belongs to a human

	j := runJiraAPIJob(t, d, ws.ID, tm.ID,
		doneIssue("ENG-1", "from the provider"), // refused
		doneIssue("ENG-2", "second"),            // lands
		doneIssue("ENG-3", "third"),             // lands
	)

	if j.Imported != 2 {
		t.Errorf("Imported = %d, want 2", j.Imported)
	}
	if j.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1 — a protective refusal is exactly what this column is for; "+
			"it has been literal 0 on every job ever run", j.Skipped)
	}
	if j.Failed != 0 {
		t.Errorf("Failed = %d, want 0 — nothing failed: the importer declined to overwrite a "+
			"human's issue, which is #71 working as designed", j.Failed)
	}
	if j.Status != JobPartial {
		t.Errorf("Status = %q, want %q", j.Status, JobPartial)
	}
	if strings.Contains(j.ErrorSummary, "row(s) failed") {
		t.Errorf("error_summary calls a refusal a failure: %q", j.ErrorSummary)
	}
	if !strings.Contains(j.ErrorSummary, "refused") {
		t.Errorf("error_summary must say what actually happened; got %q", j.ErrorSummary)
	}
	// Nothing may become quieter: the per-row message is still carried.
	if !strings.Contains(j.ErrorSummary, "refusing to overwrite it") {
		t.Errorf("the per-row reason must survive in the summary; got %q", j.ErrorSummary)
	}
	// And the human's issue is still the human's.
	var title string
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT title FROM issues WHERE workspace_id=$1 AND identifier=$2`, ws.ID, "ENG-1").Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "a human wrote this" {
		t.Errorf("ENG-1 title = %q — the import overwrote a native issue", title)
	}
}

// TestJobRow_AllRowsRefused — every row refused. THE TERMINAL STATUS IS DELIBERATELY UNCHANGED: an
// import that landed nothing must not start reading as clean just because the counters got honest.
// This test is the guard on the half I did NOT decide.
func TestJobRow_AllRowsRefused(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	tm := refusalTeam(t, d, ws.ID, "ENG")
	seedNative(t, d, ws.ID, tm.ID, 3) // ENG-1..ENG-3 all belong to humans

	j := runJiraAPIJob(t, d, ws.ID, tm.ID,
		doneIssue("ENG-1", "p1"), doneIssue("ENG-2", "p2"), doneIssue("ENG-3", "p3"))

	if j.Imported != 0 || j.Skipped != 3 || j.Failed != 0 {
		t.Errorf("counts = {imported:%d skipped:%d failed:%d}, want {0 3 0}", j.Imported, j.Skipped, j.Failed)
	}
	if j.Status != JobFailed {
		t.Errorf("Status = %q, want %q — whether an all-refused import is succeeded/partial/failed is "+
			"a product decision this merge deliberately does NOT make; it must stay as loud as it is today",
			j.Status, JobFailed)
	}
}

// erroringUpsertStore implements both halves of the write path and fails every upsert with a
// caller-supplied error, so the CLASSIFIER ITSELF can be tested at its branch rather than only
// through the one error real Postgres happens to produce.
type erroringUpsertStore struct{ err error }

func (e *erroringUpsertStore) Create(_ context.Context, i model.Issue) (*model.Issue, error) {
	return &i, nil
}

func (e *erroringUpsertStore) UpsertByIdentifier(_ context.Context, _ model.Issue) (*model.Issue, bool, error) {
	return nil, false, e.err
}

// TestRun_UpsertErrorClassification — BOTH DIRECTIONS AT THE BRANCH.
//
// ⚠ THIS TEST EXISTS BECAUSE A POSITIVE CONTROL FOUND MY GUARD BLIND. C2 blinds the classifier so
// that EVERY upsert error counts as a refusal, and the whole suite stayed green: every other test
// that reaches the upsert error path does so with a genuine collision, so "classify collisions as
// refusals" and "classify everything as refusals" were indistinguishable. A transport or tenancy
// failure would then have been laundered into `skipped` with `failed` reading 0 — the same
// misreport this merge fixes, pointing the other way, introduced by the fix.
//
// The real Postgres tests above cannot close this: the collision is the only upsert error that
// store can be made to produce on demand. A fake at the seam can produce the other one.
func TestRun_UpsertErrorClassification(t *testing.T) {
	ctx := context.Background()
	row := []SourceRow{{Issue: model.Issue{Identifier: "ENG-1", Title: "t"}, RowNum: 1}}

	t.Run("the collision sentinel is a REFUSAL", func(t *testing.T) {
		st := &erroringUpsertStore{err: fmt.Errorf("issue: %q already exists: %w",
			"ENG-1", model.ErrIdentifierNotImportOwned)}
		out, err := New(st).run(ctx, "ws", "team", &sliceSource{rows: row})
		if err != nil {
			t.Fatal(err)
		}
		if out.Refused != 1 || out.Skipped != 0 {
			t.Errorf("Refused=%d Skipped=%d, want 1/0", out.Refused, out.Skipped)
		}
	})

	t.Run("ANY OTHER upsert error is a FAILURE", func(t *testing.T) {
		st := &erroringUpsertStore{err: errors.New("dial tcp: connection refused")}
		out, err := New(st).run(ctx, "ws", "team", &sliceSource{rows: row})
		if err != nil {
			t.Fatal(err)
		}
		if out.Skipped != 1 || out.Refused != 0 {
			t.Errorf("Skipped=%d Refused=%d, want 1/0 — a transport failure is not a policy "+
				"refusal, and counting it as one would under-report `failed` exactly as the "+
				"pre-merge code over-reported it", out.Skipped, out.Refused)
		}
	})
}

// TestJobRow_GenuineFailureStaysInFailed — the isolation direction, and the reason the two counters
// are worth separating at all. A row this importer cannot write is a REAL failure: it must stay in
// `failed` and must NOT be laundered into `skipped`. This passes before the change as well as
// after, so it is positive-controlled rather than trusted (see
// scripts/w34-refused-rows-controls.py, C5/C6).
//
// ⚠ THE FIXTURE WAS RETARGETED AND THE SUBJECT WAS NOT. It used to be a raggedly-short row
// ("bad-short-row" against a five-column header), on the premise that a short row is a genuine
// failure. That premise stopped being true: a row narrower than its header is now imported and
// REPORTED rather than refused, because 73 of 3,099 rows across 45 real Linear exports are in that
// state with every column this importer reads present and well-formed (see source.go's Next and
// linear_csv_short_row_test.go). What this test is ABOUT — that a genuine per-row failure lands in
// `failed` and never in `refused` — is unchanged, so the fixture moved to a row that still genuinely
// fails: an empty title, refused by the mapper's own errEmptyTitle. Nothing here was relaxed; the
// example was swapped for one that is still an example.
func TestJobRow_GenuineFailureStaysInFailed(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	tm := d.Team(t, ws.ID)

	jobs := NewJobStore(d.Pool)
	mixed := "Title,Description,Status,Priority,Labels\n" +
		"Good A,d,Todo,High,bug\n" +
		",d,Todo,High,bug\n" + // empty TITLE — the genuine per-row failure this test is about
		"Good B,d,Done,Low,ui\n"
	jobID, err := jobs.Create(ctx, ws.ID, tm.ID, "jira_csv", []byte(mixed))
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(jobs, New(issue.NewStore(d.Pool)))
	if _, err := runner.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	j, err := jobs.Get(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if j.Imported != 2 || j.Failed != 1 || j.Skipped != 0 {
		t.Errorf("counts = {imported:%d skipped:%d failed:%d}, want {2 0 1} — a malformed row is a "+
			"genuine failure and must not be laundered into the refusal count",
			j.Imported, j.Skipped, j.Failed)
	}
	if !strings.Contains(j.ErrorSummary, "row(s) failed") {
		t.Errorf("a genuine failure must still say so; got %q", j.ErrorSummary)
	}
	if strings.Contains(j.ErrorSummary, "refused") {
		t.Errorf("a malformed row is not a refusal; got %q", j.ErrorSummary)
	}
}
