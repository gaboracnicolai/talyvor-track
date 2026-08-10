package importer_test

// runner_shutdown_terminal_state_test.go — AN IMPORT INTERRUPTED BY A NORMAL SHUTDOWN WAS
// RECORDED AS RUNNING FOREVER.
//
// The runner is started as `go importRunner.Start(ctx, 0)` (cmd/track/main.go), so the ctx it
// executes a job under is the PROCESS lifecycle context: on SIGTERM — a deploy, a scale-down,
// a restart — it is cancelled mid-job. Runner.execute then wrote the terminal status through
// THAT SAME cancelled context:
//
//	_ = r.jobs.Finish(ctx, job.ID, …)
//
// so the UPDATE that says what happened is the one write guaranteed to fail exactly when it is
// needed. The error was discarded (`_ =`), and `ClaimNext` selects `status = 'pending'` only —
// so the row stays `running` with started_at set and finished_at NULL, and NOTHING will ever
// look at it again. It is not "the import failed": it is a job that, for every reader, is
// still in progress.
//
// MEASURED on f0445e3 against real Postgres (this test, before the fix): status `running`,
// finished_at NULL, and a second RunOnce on a healthy context claims NOTHING — the job is
// unreachable while its payload row still sits in import_job_payloads.
//
// THE FIX IS THE TERMINAL WRITE'S CONTEXT AND NOTHING ELSE. `imp.run` still runs under the
// cancelled ctx — an import MUST stop when the process is going down, and making it survive
// shutdown would be a different and worse change. Only the record of what happened is detached,
// with context.WithoutCancel.
//
// ⚠ WHAT THIS DOES NOT DO, deliberately: it does not requeue, retry or reap anything. A job
// killed by SIGKILL (no Go code runs at all) still strands in `running`, and whether a stranded
// job should be re-claimed after some interval is a policy question with a number in it. This
// merge makes the ORDERLY case record itself honestly; the disorderly one is written up in the
// queue as a decision, not guessed at here.

import (
	"context"
	"testing"

	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/testutil"
)

// shutdownCreator is the process going down WHILE a row is being written: the first Create call
// cancels the runner's context and then fails the way any pgx call fails once its context is
// dead. It is the smallest faithful stand-in for SIGTERM arriving mid-import — the runner's ctx
// is cancelled from outside, at a moment when a job is claimed and running.
type shutdownCreator struct {
	cancel context.CancelFunc
	calls  int
}

func (s *shutdownCreator) Create(ctx context.Context, _ model.Issue) (*model.Issue, error) {
	s.calls++
	s.cancel()
	return nil, ctx.Err()
}

const shutdownCSV = "Title,Description,Status,Priority,Labels\n" +
	"Interrupted Issue,a description,Todo,High,bug\n"

// TestRunner_ShutdownMidImport_DoesNotLeaveTheJobRunningForever — the finding, with the two
// checks that stop it being vacuous.
//
// The dangerous vacuity here is the opposite of a byte-count guard's: if the fixture yielded NO
// rows, Create would never run, the context would never be cancelled, and the job would finish
// cleanly as `succeeded` — so the assertion below would fail rather than pass. The test still
// asserts BOTH that Create was reached and that the context really was cancelled, because a
// terminal status written under a LIVE context proves nothing about the case this exists for.
func TestRunner_ShutdownMidImport_DoesNotLeaveTheJobRunningForever(t *testing.T) {
	d := testutil.New(t)
	setup := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	jobs := importer.NewJobStore(d.Pool)
	jobID, err := jobs.Create(setup, ws.ID, team.ID, "linear_csv", []byte(shutdownCSV))
	if err != nil {
		t.Fatal(err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sc := &shutdownCreator{cancel: cancel}
	runner := importer.NewRunner(jobs, importer.New(sc))

	did, err := runner.RunOnce(runCtx)
	if err != nil {
		t.Fatalf("RunOnce returned an error before it could claim: %v", err)
	}
	if !did {
		t.Fatal("RunOnce claimed nothing — the job was never executed, so nothing below is a measurement")
	}
	// The two premises. Without them a green run says nothing about shutdown.
	if sc.calls == 0 {
		t.Fatal("the fixture yielded no rows, so the import never reached a write and the " +
			"context was never cancelled — this test measured an uninterrupted import")
	}
	if runCtx.Err() == nil {
		t.Fatal("the runner's context is still live — the shutdown this test exists for did not happen")
	}

	// ── the finding ──────────────────────────────────────────────────────────────────
	// Read on a HEALTHY context: what a status poll after the restart would see.
	job, err := jobs.Get(setup, jobID)
	if err != nil || job == nil {
		t.Fatalf("Get: job=%v err=%v", job, err)
	}
	if job.Status == importer.JobRunning {
		t.Fatalf("a job interrupted by shutdown is recorded as %q with finished_at=%v — the "+
			"terminal write went through the cancelled context and failed, and ClaimNext "+
			"selects 'pending' only, so nothing will ever look at this row again",
			job.Status, job.FinishedAt)
	}
	if job.FinishedAt == nil {
		t.Fatalf("job status is %q but finished_at is NULL — the terminal write did not land", job.Status)
	}
	// Nothing landed, so the honest terminal status is `failed`: every row was refused by a
	// dying process. `partial` would claim some of the import survived and `succeeded` would
	// claim all of it did.
	if job.Status != importer.JobFailed {
		t.Fatalf("job status = %q, want %q — no row landed, so the job must not report otherwise",
			job.Status, importer.JobFailed)
	}
	if job.ErrorSummary == "" {
		t.Fatal("the job recorded a terminal status with no error_summary — an operator is told " +
			"the import failed and nothing about why")
	}

	// ── the consequence, measured rather than argued ─────────────────────────────────
	// A second drain on a healthy context must find nothing to claim. This is what "forever"
	// means: before the fix the row is `running` and unreachable; after it, it is terminal and
	// equally unreachable — the difference is that a terminal row SAYS what happened.
	again, err := runner.RunOnce(setup)
	if err != nil {
		t.Fatalf("second RunOnce: %v", err)
	}
	if again {
		t.Fatal("a finished job was claimed a second time — ClaimNext is not selecting on status")
	}
}
