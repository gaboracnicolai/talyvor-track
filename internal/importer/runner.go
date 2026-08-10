package importer

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// runner.go — T8 live-importer, Build B: the async job runner.
//
// A background goroutine that drains pending import jobs OFF-request, so a bulk import runs past the inline
// 30s HTTP timeout with its state durable in import_jobs. Mirrors the Start(ctx) idiom already used by
// dbresil.Monitor and lensintegration.Syncer.StartSync.

// providerConfig loads a workspace's decrypted provider credentials (C.1's integration store satisfies it).
// A local interface — the importer package does NOT import integrations; main.go injects the concrete store.
type providerConfig interface {
	GetDecrypted(ctx context.Context, workspaceID, provider string) (token, projectKey, baseURL string, err error)
}

type Runner struct {
	jobs    *JobStore
	imp     *Importer
	configs providerConfig // nil ⇒ *_api jobs fail cleanly (integrations disabled)
	// httpClient overrides the provider fetch client. nil ⇒ the SSRF-guarded safehttp client (production).
	// Tests that drive the runner against a loopback mock server inject a plain client here.
	httpClient *http.Client
}

func NewRunner(jobs *JobStore, imp *Importer) *Runner { return &Runner{jobs: jobs, imp: imp} }

// WithProviderConfig wires the credential store so linear_api/jira_api jobs can load their token. Absent ⇒
// those jobs fail with a clear error (never a panic).
func (r *Runner) WithProviderConfig(pc providerConfig) *Runner { r.configs = pc; return r }

// WithHTTPClient overrides the provider fetch client (default: the SSRF-guarded safehttp client). Used by
// end-to-end tests that point a provider integration at a loopback mock, which the guard blocks by design.
func (r *Runner) WithHTTPClient(c *http.Client) *Runner { r.httpClient = c; return r }

// sourceClients returns the injected client as a variadic arg (empty ⇒ provider constructors use safehttp).
func (r *Runner) sourceClients() []*http.Client {
	if r.httpClient != nil {
		return []*http.Client{r.httpClient}
	}
	return nil
}

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
	// THE TERMINAL WRITE RUNS ON A CONTEXT THAT CANNOT BE CANCELLED, AND THAT IS THE WHOLE OF
	// THIS DETAIL. Start is launched as `go importRunner.Start(ctx, 0)` with the PROCESS
	// lifecycle context, so on SIGTERM — a deploy, a scale-down, a restart — ctx is cancelled
	// mid-job. Every Finish below used to run through that same ctx, which makes the UPDATE
	// that records what happened the one write guaranteed to fail exactly when it is needed;
	// its error was discarded, and ClaimNext selects `status = 'pending'` only. MEASURED on
	// f0445e3 against real Postgres: the row stays `running` with started_at set and
	// finished_at NULL, and a second drain claims nothing — for every reader the import is
	// still in progress, forever. Held by
	// TestRunner_ShutdownMidImport_DoesNotLeaveTheJobRunningForever.
	//
	// ⚠ ONLY THE RECORD IS DETACHED. r.imp.run below still takes the cancellable ctx: an import
	// MUST stop when the process is going down, and letting it outlive shutdown would be a
	// different and worse change.
	//
	// ⚠ NO DEADLINE IS INVENTED HERE. Whether this write should carry its own timeout (and what
	// it should be) is an operational judgement with a number in it; pgx's own connect/statement
	// settings still apply. Written up in the queue rather than guessed at.
	finishCtx := context.WithoutCancel(ctx)
	src, err := r.sourceFor(ctx, job)
	if err != nil {
		_ = r.jobs.Finish(finishCtx, job.ID, job.WorkspaceID, JobFailed, 0, 0, 0, err.Error(), nil)
		return
	}
	// workspace_id + team_id are read from the JOB ROW — the only workspace this job can write into.
	out, err := r.imp.run(ctx, job.WorkspaceID, job.TeamID, src)
	if err != nil {
		_ = r.jobs.Finish(finishCtx, job.ID, job.WorkspaceID, JobFailed, 0, 0, 0, err.Error(), nil)
		return
	}
	summary := summarise(out)
	// out.Skipped = rows that FAILED to import → the job's `failed`.
	// out.Refused = rows the importer DECLINED to write because a human owns that identifier
	//   (#71's policy working) → the job's `skipped`, which until this merge had no writer passing
	//   anything but a literal 0 and was structurally zero on every job ever run. It is not a spare
	//   column being filled to tidy up an API: it is where the refusal count belongs, and putting it
	//   there is what stops `failed` counting rows that did not fail.
	//
	// out.Warnings = rows that DID import, degraded. They do NOT change terminalStatus: the import
	// succeeded, and calling it partial would conflate "some rows were rejected" with "every row
	// landed but some fields could not be mapped". The distinction is the point of the column.
	_ = r.jobs.Finish(finishCtx, job.ID, job.WorkspaceID, terminalStatus(out), out.Imported, out.Refused, out.Skipped, summary, out.Warnings)
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
	case "linear_api":
		return r.apiSourceFor(ctx, job, "linear")
	case "jira_api":
		return r.apiSourceFor(ctx, job, "jira")
	default:
		return nil, fmt.Errorf("importer: unsupported source_type %q", job.SourceType)
	}
}

// apiSourceFor builds a provider IssueSource for an *_api job. There is NO payload row — the provider config
// (token, project/team key, base URL) is loaded from the credential store BY THE JOB'S workspace_id (the
// Build-B tenancy re-enforcement: workspace comes only from the job row). No integration / no key ⇒ a clean
// error → the job fails observably, never a panic.
func (r *Runner) apiSourceFor(ctx context.Context, job *Job, provider string) (IssueSource, error) {
	if r.configs == nil {
		return nil, fmt.Errorf("importer: %s_api import unavailable — integrations not configured", provider)
	}
	token, projectKey, baseURL, err := r.configs.GetDecrypted(ctx, job.WorkspaceID, provider)
	if err != nil {
		return nil, fmt.Errorf("importer: load %s integration: %w", provider, err)
	}
	switch provider {
	case "linear":
		return newLinearSource(token, projectKey, baseURL, r.sourceClients()...), nil
	case "jira":
		return newJiraSource(token, projectKey, baseURL, r.sourceClients()...), nil
	default:
		return nil, fmt.Errorf("importer: unknown provider %q", provider)
	}
}

func (r *Runner) csvSourceFor(ctx context.Context, jobID string, mapper rowMapper) (IssueSource, error) {
	payload, err := r.jobs.LoadPayload(ctx, jobID)
	if err != nil {
		return nil, err
	}
	return newCSVSource(bytes.NewReader(payload), mapper)
}

// summarise renders error_summary. A row that FAILED and a row that was REFUSED are named
// separately, because "3 row(s) failed" for three issues the importer correctly protected is a
// sentence that sends someone to debug a working system.
//
// ⚠ BYTE-IDENTICAL TO THE PREVIOUS WORDING WHEN NOTHING WAS REFUSED — the failure-only case still
// renders exactly "%d row(s) failed; first: %s". That is what keeps this from re-litigating a
// sentence #72/#74 pinned by test, and TestRunner_PartialImport_Observable is the check.
//
// The "first:" clause carries the first per-row message either way, so no import is quieter about
// what happened than it was before: only the counting sentence changed.
func summarise(out *ImportResult) string {
	if len(out.Errors) == 0 {
		return ""
	}
	parts := make([]string, 0, 2)
	if out.Skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d row(s) failed", out.Skipped))
	}
	// THE TWO REFUSALS ARE NAMED APART. Both are the policy working and both go in the same
	// counter; they send an operator to two different places. "was not created by an import" is a
	// true sentence about a human's issue and a FALSE one about a row the operator's own earlier
	// import created in another team, where the thing to act on is the team, not the duplicate.
	//
	// ⚠ BYTE-IDENTICAL WHEN NO ROW HIT THE SECOND CASE — the arithmetic is written so the
	// native-only wording renders exactly as it did before the split, which is what keeps
	// TestJobRow_AllRowsRefused and TestRunner_PartialImport_Observable pinning what they pinned.
	if native := out.Refused - out.refusedOtherTeam; native > 0 {
		parts = append(parts, fmt.Sprintf(
			"%d row(s) refused: an issue with that identifier already exists and was not created by an import",
			native))
	}
	if out.refusedOtherTeam > 0 {
		parts = append(parts, fmt.Sprintf(
			"%d row(s) refused: that identifier is already imported into another team of this workspace",
			out.refusedOtherTeam))
	}
	return strings.Join(parts, "; ") + "; first: " + out.Errors[0]
}

// terminalStatus maps an ImportResult to a job status: succeeded = every row landed; partial = some
// imported + some did not; failed = nothing imported and something did not land.
//
// ⚠ REFUSALS COUNT HERE EXACTLY AS THEY DID BEFORE THE COUNTERS WERE SPLIT, AND THAT IS DELIBERATE.
// It would be easy to read "a refusal is not a failure" as "an all-refused import succeeded" — but
// an import that landed NOTHING must not report itself clean, which is the shape this item has
// found eight times in the other direction. Whether all-refused should be succeeded / partial /
// failed is a product judgement with three defensible answers and is NOT this merge's to make; it
// is written up in the queue. So `unlanded` is the same quantity the old `out.Skipped` was, and
// every status this function returns is byte-identical to what it returned at dcfbaa3.
// TestJobRow_AllRowsRefused pins that, and control C4 flips it to `out.Skipped` alone and reds.
func terminalStatus(out *ImportResult) string {
	unlanded := out.Skipped + out.Refused
	switch {
	case unlanded == 0:
		return JobSucceeded
	case out.Imported > 0:
		return JobPartial
	default:
		return JobFailed
	}
}
