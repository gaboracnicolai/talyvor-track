package importer

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"time"
)

// runner.go — T8 live-importer, Build B: the async job runner.
//
// A background goroutine that drains pending import jobs OFF-request, so a bulk import runs past the inline
// 30s HTTP timeout with its state durable in import_jobs. Mirrors the Start(ctx) idiom already used by
// dbresil.Monitor and lensintegration.Syncer.StartSync.

type Runner struct {
	jobs *JobStore
	imp  *Importer
}

func NewRunner(jobs *JobStore, imp *Importer) *Runner { return &Runner{jobs: jobs, imp: imp} }

const defaultRunnerInterval = 2 * time.Second

// Start polls for pending jobs on a ticker until ctx is done. BLOCKING — run via `go runner.Start(ctx, 0)`,
// composing with the process lifecycle like the other Start(ctx) goroutines.
func (r *Runner) Start(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = defaultRunnerInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.drain(ctx)
		}
	}
}

// drain runs pending jobs until none remain (or ctx is done).
func (r *Runner) drain(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		did, err := r.RunOnce(ctx)
		if err != nil {
			slog.Warn("importer: runner claim failed", slog.String("err", err.Error()))
			return
		}
		if !did {
			return
		}
	}
}

// RunOnce claims one pending job and executes it. (false, nil) when nothing is pending. Exposed for
// deterministic tests (no ticker wait).
func (r *Runner) RunOnce(ctx context.Context) (bool, error) {
	job, err := r.jobs.ClaimNext(ctx)
	if err != nil {
		return false, err
	}
	if job == nil {
		return false, nil
	}
	r.execute(ctx, job)
	return true, nil
}

// execute drains a claimed job's source through run() and records the terminal status.
//
// THE TENANCY RE-ENFORCEMENT: the workspace comes ONLY from job.WorkspaceID (loaded from the job row at
// claim). execute takes the *Job and reads NO workspace from any other place — not a parameter, not an HTTP
// header (there is none here — this is off-request), not the source rows (the CSV mapper maps no workspace).
// So a job's writes land in exactly one workspace: the one persisted at creation under the authz gate.
func (r *Runner) execute(ctx context.Context, job *Job) {
	src, err := r.sourceFor(ctx, job)
	if err != nil {
		_ = r.jobs.Finish(ctx, job.ID, JobFailed, 0, 0, 0, err.Error())
		return
	}
	// workspace_id + team_id are read from the JOB ROW — the only workspace this job can write into.
	out, err := r.imp.run(ctx, job.WorkspaceID, job.TeamID, src)
	if err != nil {
		_ = r.jobs.Finish(ctx, job.ID, JobFailed, 0, 0, 0, err.Error())
		return
	}
	summary := ""
	if len(out.Errors) > 0 {
		summary = fmt.Sprintf("%d row(s) failed; first: %s", out.Skipped, out.Errors[0])
	}
	// out.Skipped = rows that failed to import → the job's `failed`; `skipped` is reserved (0 for now).
	_ = r.jobs.Finish(ctx, job.ID, terminalStatus(out), out.Imported, 0, out.Skipped, summary)
}

// sourceFor dispatches on source_type → (IssueSource). A '*_csv' job reads its payload from the cold table
// and wraps it in the existing csvSource with the matching mapper. 'linear_api'/'jira_api' are Build C. An
// unknown source_type fails the job cleanly (never a panic, never a silent no-op — the caller sets failed).
func (r *Runner) sourceFor(ctx context.Context, job *Job) (IssueSource, error) {
	switch job.SourceType {
	case "linear_csv":
		return r.csvSourceFor(ctx, job.ID, linearRowMapper)
	case "jira_csv":
		return r.csvSourceFor(ctx, job.ID, jiraRowMapper)
	default:
		return nil, fmt.Errorf("importer: unsupported source_type %q", job.SourceType)
	}
}

func (r *Runner) csvSourceFor(ctx context.Context, jobID string, mapper rowMapper) (IssueSource, error) {
	payload, err := r.jobs.LoadPayload(ctx, jobID)
	if err != nil {
		return nil, err
	}
	return newCSVSource(bytes.NewReader(payload), mapper)
}

// terminalStatus maps an ImportResult to a job status: succeeded = nothing failed; partial = some imported +
// some failed; failed = failures with nothing imported.
func terminalStatus(out *ImportResult) string {
	switch {
	case out.Skipped == 0:
		return JobSucceeded
	case out.Imported > 0:
		return JobPartial
	default:
		return JobFailed
	}
}
