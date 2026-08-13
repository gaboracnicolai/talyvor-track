package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/testutil"
)

// terminal_write_failure_test.go — W3.4: THE ONE WRITE THAT RECORDS WHAT AN IMPORT DID HAD ITS
// ERROR DISCARDED, AND NOTHING ELSE SAID SO.
//
// runner.go's execute ends every path with `_ = r.jobs.Finish(...)`. The paragraph above that call
// already reasons about this exact outcome — "the row stays `running` with started_at set and
// finished_at NULL, and a second drain claims nothing — for every reader the import is still in
// progress, forever" — and closed ONE cause of it (the process-lifecycle ctx, via
// context.WithoutCancel). The DISCARD it names in the same sentence ("its error was discarded") was
// left in place, so every OTHER cause of a failed terminal write still produces that outcome with
// no signal at all.
//
// MEASURED at 5057c3d against real Postgres, through the shipped async runner, with the terminal
// UPDATE refused by a trigger (any refusal will do — a statement timeout, a dropped connection, a
// constraint):
//
//	issues actually written : 2
//	job row                 : status="running", finished_at=NULL   (forever)
//	second RunOnce          : did=false        — ClaimNext selects status='pending' only
//	RunOnce's own return    : did=true, err=nil — the runner reports it did the work
//	PROCESS LOG OUTPUT      : 0 bytes
//
// Two issues are in the database, the operator polling /v1/import/jobs/{id} sees `running` for
// ever, and there is not one line anywhere saying an import finished. ⚠ THE ASYMMETRY IS THE
// ARGUMENT: drain() logs `importer: runner claim failed` when CLAIMING a job errors, so the cheap
// half of the runner is observable and the consequential half is mute.
//
// ⚠ WHAT THIS FILE DOES NOT FIX, STATED NOT IMPLIED: the row still stays `running` and is still
// never re-claimed. Recovering it needs a reaper with a lease age — a NUMBER, i.e. a judgement
// about how long a legitimately-long import may run before it is presumed dead — and inventing one
// here would be exactly the guess this item refuses. The residual is pinned by
// TestRunner_TerminalWriteFailure_LeavesTheRowRunning below and written up in the queue.

// refuseTerminalWrite makes the job row's TERMINAL update fail and leaves every other statement
// alone: the trigger fires only WHEN NEW.finished_at IS NOT NULL, which ClaimNext's
// `status='running', started_at=NOW()` update does not set. So the job is claimed normally, the
// rows import normally, and only Finish is refused — the seam under test and nothing else.
const refuseTerminalWrite = `
CREATE OR REPLACE FUNCTION refuse_terminal_write() RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION 'terminal write refused (test fixture)';
END; $$ LANGUAGE plpgsql;
CREATE TRIGGER refuse_terminal_write BEFORE UPDATE ON import_jobs
  FOR EACH ROW WHEN (NEW.finished_at IS NOT NULL)
  EXECUTE FUNCTION refuse_terminal_write();
`

// captureImporterLogs swaps slog.Default for a JSON logger over a buffer, restored on cleanup.
// Mirrors internal/member/audit_test.go's captureLogs. No test in this package calls t.Parallel(),
// so the swap is not shared with a concurrently running test.
func captureImporterLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(orig) })
	return &buf
}

// logRecord is one decoded slog JSON line. Attributes land at the top level, so the map carries
// both the built-ins (level/msg) and every slog.String/slog.Int attr.
type logRecord map[string]any

func decodeLogRecords(t *testing.T, buf *bytes.Buffer) []logRecord {
	t.Helper()
	out := []logRecord{}
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec logRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line is not JSON (%v): %q", err, line)
		}
		out = append(out, rec)
	}
	return out
}

// findJobRecord returns the log record naming this job id, or nil.
func findJobRecord(recs []logRecord, jobID string) logRecord {
	for _, r := range recs {
		if id, _ := r["job_id"].(string); id == jobID {
			return r
		}
	}
	return nil
}

// runWithRefusedTerminalWrite seeds a workspace/team, enqueues a job of sourceType, installs the
// trigger, and drains it once with the log captured. Returns the job id and the records.
func runWithRefusedTerminalWrite(t *testing.T, sourceType, body string) (string, []logRecord, *testutil.DB, string) {
	t.Helper()
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	tm := d.Team(t, ws.ID)

	jobs := NewJobStore(d.Pool)
	jobID, err := jobs.Create(ctx, ws.ID, tm.ID, sourceType, []byte(body))
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := d.Pool.Exec(ctx, refuseTerminalWrite); err != nil {
		t.Fatalf("install trigger: %v", err)
	}

	buf := captureImporterLogs(t)
	runner := NewRunner(jobs, New(issue.NewStore(d.Pool)))
	if did, err := runner.RunOnce(ctx); err != nil || !did {
		t.Fatalf("RunOnce did=%v err=%v — the job must still be claimed and run", did, err)
	}
	return jobID, decodeLogRecords(t, buf), d, ws.ID
}

const terminalWriteCSV = "Title,Description,Status,Priority,Labels\n" +
	"Good A,d,Todo,High,bug\n" +
	"Good B,d,Done,Low,ui\n"

// (1) THE MAIN PATH: an import that RAN and whose result could not be recorded must say so, and the
// line must carry what the row would have carried — the log is the only remaining record of it.
func TestRunner_TerminalWriteFailure_IsReported(t *testing.T) {
	jobID, recs, d, wsID := runWithRefusedTerminalWrite(t, "jira_csv", terminalWriteCSV)

	// The premise: the rows really did land. Without this the assertions below could be satisfied
	// by an import that did nothing at all.
	var rows int
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM issues WHERE workspace_id=$1`, wsID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Fatalf("fixture: want 2 imported issues, got %d — the refusal must hit the JOB row only", rows)
	}

	rec := findJobRecord(recs, jobID)
	if rec == nil {
		t.Fatalf("an import wrote %d issues and its terminal state could not be recorded, and NOTHING "+
			"was logged: %d record(s), none naming job %s. The row stays 'running' for ever and no "+
			"later drain re-claims it, so this line is the only surface that can say the import ran.",
			rows, len(recs), jobID)
	}
	if lvl, _ := rec["level"].(string); lvl != "ERROR" {
		t.Errorf("level = %q, want ERROR — an unrecorded import is not a warning: the job row is "+
			"permanently wrong and nothing retries it", lvl)
	}
	// The counts, because the log line REPLACES the row nobody could write. A line that says only
	// "finish failed" tells an operator that something happened and not what.
	if got := jsonInt(rec["imported"]); got != 2 {
		t.Errorf("imported = %v, want 2 — the record must carry the counts the row could not", rec["imported"])
	}
	if got, _ := rec["intended_status"].(string); got != JobSucceeded {
		t.Errorf("intended_status = %q, want %q", got, JobSucceeded)
	}
	if got, _ := rec["err"].(string); !strings.Contains(got, "terminal write refused") {
		t.Errorf("err = %q, want the database's own refusal quoted", got)
	}
}

// (2) THE OTHER REACHABLE CALL SITE. execute records a terminal state from THREE places; a fix that
// only touches the last one leaves the others mute. sourceFor's failure path is reached by a job row
// whose source_type the runner does not implement (JobStore.Create validates only that it is
// non-empty — the vocabulary check lives in the HTTP handler, so a row can carry anything).
//
// The third call site — run() returning an error — is guarded by a precondition (empty
// workspace/team) that a claimed job row cannot satisfy, so it is fixed the same way and is NOT
// asserted here rather than being covered by a test that cannot fail.
func TestRunner_TerminalWriteFailure_IsReportedForAFailedSource(t *testing.T) {
	jobID, recs, _, _ := runWithRefusedTerminalWrite(t, "not_a_real_source", terminalWriteCSV)

	rec := findJobRecord(recs, jobID)
	if rec == nil {
		t.Fatalf("a job that failed BEFORE its source opened also could not record that, and nothing "+
			"was logged: %d record(s), none naming job %s", len(recs), jobID)
	}
	if got, _ := rec["intended_status"].(string); got != JobFailed {
		t.Errorf("intended_status = %q, want %q", got, JobFailed)
	}
}

// (3) THE DISCRIMINATION TEST. A report that fires on every import is not a report. An import whose
// terminal write SUCCEEDS must emit no such record — without this, logging unconditionally would
// satisfy (1) and (2).
func TestRunner_SuccessfulFinish_ReportsNothing(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	tm := d.Team(t, ws.ID)

	jobs := NewJobStore(d.Pool)
	jobID, err := jobs.Create(ctx, ws.ID, tm.ID, "jira_csv", []byte(terminalWriteCSV))
	if err != nil {
		t.Fatal(err)
	}
	buf := captureImporterLogs(t)
	runner := NewRunner(jobs, New(issue.NewStore(d.Pool)))
	if did, err := runner.RunOnce(ctx); err != nil || !did {
		t.Fatalf("RunOnce did=%v err=%v", did, err)
	}

	j, err := jobs.Get(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if j.Status != JobSucceeded {
		t.Fatalf("fixture: job status = %q, want succeeded — this test is about a run that RECORDED itself", j.Status)
	}
	if rec := findJobRecord(decodeLogRecords(t, buf), jobID); rec != nil {
		t.Errorf("a job that recorded its terminal state cleanly logged a failure record anyway: %v — "+
			"a report that fires on every import cannot be read as a report of anything", rec)
	}
}

// (4) THE RESIDUAL, PINNED RATHER THAN LEFT TO DRIFT. This is NOT a guard on the change above: it
// passes before and after it, deliberately. It records what is STILL true — the row stays `running`
// and no later drain re-claims it — so that whoever adds a reaper does it on purpose and this file
// tells them what it changes. Control C6 (ClaimNext also claiming 'running' rows) reds it, which is
// what makes it a falsifiable pin rather than a decoration.
func TestRunner_TerminalWriteFailure_LeavesTheRowRunning(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	tm := d.Team(t, ws.ID)

	jobs := NewJobStore(d.Pool)
	jobID, err := jobs.Create(ctx, ws.ID, tm.ID, "jira_csv", []byte(terminalWriteCSV))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool.Exec(ctx, refuseTerminalWrite); err != nil {
		t.Fatalf("install trigger: %v", err)
	}
	runner := NewRunner(jobs, New(issue.NewStore(d.Pool)))
	if did, err := runner.RunOnce(ctx); err != nil || !did {
		t.Fatalf("RunOnce did=%v err=%v", did, err)
	}

	j, err := jobs.Get(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if j.Status != JobRunning || j.FinishedAt != nil {
		t.Errorf("job row = {status:%q finished_at:%v}, want {running <nil>} — if this changed, a "+
			"recovery path was added and the queue's write-up of the residual is now stale",
			j.Status, j.FinishedAt)
	}
	if did, err := runner.RunOnce(ctx); err != nil || did {
		t.Errorf("second drain did=%v err=%v, want did=false — ClaimNext selects status='pending' "+
			"only, so a row stuck at 'running' is never retried", did, err)
	}
}

// jsonInt reads a JSON number attribute as an int (encoding/json decodes every number as float64).
func jsonInt(v any) int {
	f, ok := v.(float64)
	if !ok {
		return -1
	}
	return int(f)
}
