package importer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// jobs.go — T8 live-importer, Build B: the durable import-job record + its store.
//
// import_jobs is the hot, narrow status row (polled during a run); import_job_payloads holds the CSV bytes in
// a separate cold table (see migration 0020). WorkspaceID on the job row is the authorized workspace captured
// at creation under the authz gate — the ONLY workspace the runner will ever write into.

// Job status vocabulary.
const (
	JobPending   = "pending"
	JobRunning   = "running"
	JobSucceeded = "succeeded"
	JobPartial   = "partial"
	JobFailed    = "failed"
)

// Job is a durable import-job record. WorkspaceID/TeamID drive the runner's writes — read from this row,
// never from a param/header/source-row.
type Job struct {
	ID           string     `json:"id"`
	WorkspaceID  string     `json:"workspace_id"`
	TeamID       string     `json:"team_id"`
	SourceType   string     `json:"source_type"`
	Status       string     `json:"status"`
	Imported     int        `json:"imported"`
	Skipped      int        `json:"skipped"`
	Failed       int        `json:"failed"`
	ErrorSummary string     `json:"error_summary,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
}

// JobStore is the persistence for import jobs. Real *pgxpool.Pool in prod and tests.
type JobStore struct{ pool *pgxpool.Pool }

func NewJobStore(pool *pgxpool.Pool) *JobStore { return &JobStore{pool: pool} }

// Create inserts the job row AND its payload in ONE transaction — a file-source job never exists without its
// payload, nor a payload without its job. workspaceID MUST be the server-resolved authorized workspace.
func (s *JobStore) Create(ctx context.Context, workspaceID, teamID, sourceType string, payload []byte) (string, error) {
	if workspaceID == "" || teamID == "" || sourceType == "" {
		return "", errors.New("importer: Create requires workspace_id, team_id, source_type")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var id string
	if err := tx.QueryRow(ctx,
		`INSERT INTO import_jobs (workspace_id, team_id, source_type, status)
		 VALUES ($1, $2, $3, 'pending') RETURNING id`,
		workspaceID, teamID, sourceType).Scan(&id); err != nil {
		return "", fmt.Errorf("importer: insert job: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO import_job_payloads (job_id, payload) VALUES ($1, $2)`, id, payload); err != nil {
		return "", fmt.Errorf("importer: insert payload: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return id, nil
}

// ClaimNext atomically claims the oldest pending job (FOR UPDATE SKIP LOCKED — safe across HA instances),
// marks it running, and returns it (only the fields the runner needs). (nil, nil) when nothing is pending.
func (s *JobStore) ClaimNext(ctx context.Context) (*Job, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var j Job
	err = tx.QueryRow(ctx,
		`SELECT id, workspace_id, team_id, source_type FROM import_jobs
		 WHERE status = 'pending' ORDER BY created_at
		 FOR UPDATE SKIP LOCKED LIMIT 1`).
		Scan(&j.ID, &j.WorkspaceID, &j.TeamID, &j.SourceType)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE import_jobs SET status='running', started_at=NOW() WHERE id=$1`, j.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	j.Status = JobRunning
	return &j, nil
}

// LoadPayload reads the CSV bytes for a job from the COLD payload table. The status poll never calls this —
// the hot import_jobs row stays off the blob.
func (s *JobStore) LoadPayload(ctx context.Context, jobID string) ([]byte, error) {
	var p []byte
	if err := s.pool.QueryRow(ctx, `SELECT payload FROM import_job_payloads WHERE job_id=$1`, jobID).Scan(&p); err != nil {
		return nil, fmt.Errorf("importer: load payload: %w", err)
	}
	return p, nil
}

// Finish records the terminal status + counts.
func (s *JobStore) Finish(ctx context.Context, jobID, status string, imported, skipped, failed int, errSummary string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE import_jobs SET status=$2, imported=$3, skipped=$4, failed=$5,
		        error_summary=NULLIF($6,''), finished_at=NOW() WHERE id=$1`,
		jobID, status, imported, skipped, failed, errSummary)
	if err != nil {
		return fmt.Errorf("importer: finish job: %w", err)
	}
	return nil
}

// Get loads a job's status row (for the status endpoint). Does NOT touch import_job_payloads. (nil, nil)
// when the job doesn't exist.
func (s *JobStore) Get(ctx context.Context, jobID string) (*Job, error) {
	var j Job
	var errSummary *string
	err := s.pool.QueryRow(ctx,
		`SELECT id, workspace_id, team_id, source_type, status, imported, skipped, failed,
		        error_summary, created_at, started_at, finished_at
		 FROM import_jobs WHERE id=$1`, jobID).
		Scan(&j.ID, &j.WorkspaceID, &j.TeamID, &j.SourceType, &j.Status, &j.Imported, &j.Skipped, &j.Failed,
			&errSummary, &j.CreatedAt, &j.StartedAt, &j.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("importer: get job: %w", err)
	}
	if errSummary != nil {
		j.ErrorSummary = *errSummary
	}
	return &j, nil
}
