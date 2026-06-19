// Package timetracking implements per-issue, per-member time entries.
//
// One running timer per member at a time: StartTimer atomically
// closes any open entry for the same member before opening a new
// one. StopTimer returns nil-nil when there's nothing to stop —
// callers branch on `entry != nil`, not on errors, so a stop call
// against a fresh session is silent rather than noisy.
//
// Time entries are kept after issue soft-delete. The ON DELETE
// CASCADE on issues(id) only fires on a literal SQL DELETE, which
// the issue store never performs (Delete sets status='cancelled').
// That keeps the billing trail intact for cancelled work.
package timetracking

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ─── types ──────────────────────────────────────────────────

type TimeEntry struct {
	ID          string     `json:"id"`
	IssueID     string     `json:"issue_id"`
	WorkspaceID string     `json:"workspace_id"`
	MemberID    string     `json:"member_id"`
	Description string     `json:"description"`
	StartedAt   time.Time  `json:"started_at"`
	StoppedAt   *time.Time `json:"stopped_at,omitempty"`
	DurationSec int        `json:"duration_sec"`
	Billable    bool       `json:"billable"`
	CreatedAt   time.Time  `json:"created_at"`
}

type TimerState struct {
	Running    bool       `json:"running"`
	IssueID    string     `json:"issue_id,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	ElapsedSec int        `json:"elapsed_sec"`
}

type TimeSummary struct {
	IssueID     string `json:"issue_id"`
	TotalSec    int    `json:"total_sec"`
	BillableSec int    `json:"billable_sec"`
	MemberCount int    `json:"member_count"`
	EntryCount  int    `json:"entry_count"`
}

type MemberTime struct {
	MemberID    string `json:"member_id"`
	Name        string `json:"name"`
	TotalSec    int    `json:"total_sec"`
	BillableSec int    `json:"billable_sec"`
}

type ProjectTime struct {
	ProjectID   string `json:"project_id"`
	Name        string `json:"name"`
	TotalSec    int    `json:"total_sec"`
	BillableSec int    `json:"billable_sec"`
}

type WorkspaceSummary struct {
	TotalSec    int           `json:"total_sec"`
	BillableSec int           `json:"billable_sec"`
	ByMember    []MemberTime  `json:"by_member"`
	ByProject   []ProjectTime `json:"by_project"`
}

// ─── store ──────────────────────────────────────────────────

type pgxDB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

type Store struct{ pool pgxDB }

func NewStore(pool *pgxpool.Pool) *Store {
	var db pgxDB
	if pool != nil {
		db = pool
	}
	return newStore(db)
}

func newStore(db pgxDB) *Store { return &Store{pool: db} }

const entryColumns = `id, issue_id, workspace_id, member_id, description,
    started_at, stopped_at, duration_sec, billable, created_at`

func scanEntry(s interface{ Scan(...any) error }) (*TimeEntry, error) {
	var e TimeEntry
	if err := s.Scan(
		&e.ID, &e.IssueID, &e.WorkspaceID, &e.MemberID, &e.Description,
		&e.StartedAt, &e.StoppedAt, &e.DurationSec, &e.Billable, &e.CreatedAt,
	); err != nil {
		return nil, err
	}
	return &e, nil
}

// ─── StartTimer ────────────────────────────────────────────

// StartTimer opens a new running entry for the member and atomically
// closes any pre-existing running entry. Wrapped in a transaction so
// the "only one running timer per member" invariant holds even if a
// caller races a stop + start.
// assertIssueInWorkspace refuses unless issueID belongs to workspaceID. Object-graph
// integrity: a time entry must reference an issue in its own workspace — the FKs
// prove issue/workspace exist independently, not that they belong together.
func (s *Store) assertIssueInWorkspace(ctx context.Context, issueID, workspaceID string) error {
	var ok bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM issues WHERE id = $1 AND workspace_id = $2)`,
		issueID, workspaceID,
	).Scan(&ok); err != nil {
		return fmt.Errorf("timetracking: validate issue: %w", err)
	}
	if !ok {
		return errors.New("timetracking: issue is not in this workspace")
	}
	return nil
}

func (s *Store) StartTimer(ctx context.Context, issueID, workspaceID, memberID, description string) (*TimeEntry, error) {
	if s.pool == nil {
		return nil, errors.New("timetracking: store has no pool")
	}
	if issueID == "" || memberID == "" || workspaceID == "" {
		return nil, errors.New("timetracking: issue_id, workspace_id, member_id required")
	}
	if err := s.assertIssueInWorkspace(ctx, issueID, workspaceID); err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("timetracking: start begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`UPDATE time_entries SET stopped_at = NOW(),
            duration_sec = EXTRACT(EPOCH FROM (NOW() - started_at))::int
        WHERE member_id = $1 AND workspace_id = $2 AND stopped_at IS NULL`,
		memberID, workspaceID,
	); err != nil {
		return nil, fmt.Errorf("timetracking: stop previous: %w", err)
	}

	out, err := scanEntry(tx.QueryRow(ctx,
		`INSERT INTO time_entries (issue_id, workspace_id, member_id, description, started_at, billable)
        VALUES ($1, $2, $3, $4, NOW(), true) RETURNING `+entryColumns,
		issueID, workspaceID, memberID, description,
	))
	if err != nil {
		return nil, fmt.Errorf("timetracking: insert: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("timetracking: start commit: %w", err)
	}
	return out, nil
}

// ─── StopTimer ─────────────────────────────────────────────

// StopTimer closes the open entry for the member. Returns nil-nil
// when nothing was running — letting the caller distinguish "no
// timer" from an error without a sentinel.
func (s *Store) StopTimer(ctx context.Context, memberID, workspaceID string) (*TimeEntry, error) {
	if s.pool == nil {
		return nil, errors.New("timetracking: store has no pool")
	}
	row := s.pool.QueryRow(ctx,
		`UPDATE time_entries SET stopped_at = NOW(),
            duration_sec = EXTRACT(EPOCH FROM (NOW() - started_at))::int
        WHERE member_id = $1 AND workspace_id = $2 AND stopped_at IS NULL
        RETURNING `+entryColumns,
		memberID, workspaceID,
	)
	out, err := scanEntry(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("timetracking: stop: %w", err)
	}
	return out, nil
}

// ─── GetRunningTimer ───────────────────────────────────────

// GetRunningTimer returns the live elapsed seconds for the member's
// open entry, or a non-running TimerState if none. We compute the
// elapsed here (clock at the server) so the frontend's 1-second
// counter only needs to re-render visually — it never has to re-sync
// against the wall clock between API calls.
func (s *Store) GetRunningTimer(ctx context.Context, memberID, workspaceID string) (*TimerState, error) {
	if s.pool == nil {
		return &TimerState{Running: false}, nil
	}
	var (
		issueID   string
		startedAt time.Time
	)
	err := s.pool.QueryRow(ctx,
		`SELECT issue_id, started_at FROM time_entries
        WHERE member_id = $1 AND workspace_id = $2 AND stopped_at IS NULL
        LIMIT 1`,
		memberID, workspaceID,
	).Scan(&issueID, &startedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return &TimerState{Running: false}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("timetracking: running: %w", err)
	}
	elapsed := int(time.Since(startedAt).Seconds())
	return &TimerState{
		Running:    true,
		IssueID:    issueID,
		StartedAt:  &startedAt,
		ElapsedSec: elapsed,
	}, nil
}

// ─── LogTime ───────────────────────────────────────────────

// LogTime inserts a manual entry — both StartedAt and StoppedAt must
// be present. The store computes duration_sec from the timestamps
// (round to nearest second) so callers never have to keep their
// duration math in sync with the wall-clock format.
func (s *Store) LogTime(ctx context.Context, e TimeEntry) (*TimeEntry, error) {
	if s.pool == nil {
		return nil, errors.New("timetracking: store has no pool")
	}
	if e.IssueID == "" || e.MemberID == "" || e.WorkspaceID == "" {
		return nil, errors.New("timetracking: issue_id, workspace_id, member_id required")
	}
	if e.StoppedAt == nil {
		return nil, errors.New("timetracking: stopped_at required for manual log")
	}
	duration := int(e.StoppedAt.Sub(e.StartedAt).Seconds())
	if duration < 0 {
		return nil, errors.New("timetracking: stopped_at must be after started_at")
	}
	if err := s.assertIssueInWorkspace(ctx, e.IssueID, e.WorkspaceID); err != nil {
		return nil, err
	}
	return scanEntry(s.pool.QueryRow(ctx,
		`INSERT INTO time_entries (issue_id, workspace_id, member_id, description,
            started_at, stopped_at, duration_sec, billable)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING `+entryColumns,
		e.IssueID, e.WorkspaceID, e.MemberID, e.Description,
		e.StartedAt, e.StoppedAt, duration, e.Billable,
	))
}

// ─── ListByIssue ───────────────────────────────────────────

func (s *Store) ListByIssue(ctx context.Context, issueID string) ([]TimeEntry, error) {
	if s.pool == nil {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+entryColumns+` FROM time_entries WHERE issue_id = $1 ORDER BY started_at DESC`,
		issueID,
	)
	if err != nil {
		return nil, fmt.Errorf("timetracking: list issue: %w", err)
	}
	defer rows.Close()
	var out []TimeEntry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// ─── ListByMember ──────────────────────────────────────────

func (s *Store) ListByMember(ctx context.Context, memberID, workspaceID string, since time.Time) ([]TimeEntry, error) {
	if s.pool == nil {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+entryColumns+` FROM time_entries
        WHERE member_id = $1 AND workspace_id = $2 AND started_at >= $3
        ORDER BY started_at DESC`,
		memberID, workspaceID, since,
	)
	if err != nil {
		return nil, fmt.Errorf("timetracking: list member: %w", err)
	}
	defer rows.Close()
	var out []TimeEntry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// ─── GetIssueSummary ───────────────────────────────────────

// GetIssueSummary aggregates time across all entries on an issue.
// COUNT(DISTINCT member_id) drives the team-size display in the
// IssueDetail header.
func (s *Store) GetIssueSummary(ctx context.Context, issueID string) (*TimeSummary, error) {
	if s.pool == nil {
		return &TimeSummary{IssueID: issueID}, nil
	}
	out := &TimeSummary{IssueID: issueID}
	var total, billable, members, entries int64
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(duration_sec), 0)                                 AS total_sec,
                COALESCE(SUM(duration_sec) FILTER (WHERE billable), 0)          AS billable_sec,
                COUNT(DISTINCT member_id)                                       AS member_count,
                COUNT(*)                                                        AS entry_count
            FROM time_entries WHERE issue_id = $1`,
		issueID,
	).Scan(&total, &billable, &members, &entries)
	if err != nil {
		return nil, fmt.Errorf("timetracking: issue summary: %w", err)
	}
	out.TotalSec = int(total)
	out.BillableSec = int(billable)
	out.MemberCount = int(members)
	out.EntryCount = int(entries)
	return out, nil
}

// ─── GetWorkspaceSummary ───────────────────────────────────

// GetWorkspaceSummary produces the dashboard payload: a single
// workspace total plus a per-member and per-project breakdown. Three
// queries (totals, by-member, by-project) instead of one big GROUP BY
// because the result shape doesn't sit cleanly in one resultset.
func (s *Store) GetWorkspaceSummary(ctx context.Context, workspaceID string, since time.Time) (*WorkspaceSummary, error) {
	if s.pool == nil {
		return &WorkspaceSummary{ByMember: []MemberTime{}, ByProject: []ProjectTime{}}, nil
	}
	out := &WorkspaceSummary{ByMember: []MemberTime{}, ByProject: []ProjectTime{}}

	var total, billable int64
	if err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(duration_sec), 0),
                COALESCE(SUM(duration_sec) FILTER (WHERE billable), 0)
            FROM time_entries WHERE workspace_id = $1 AND started_at >= $2`,
		workspaceID, since,
	).Scan(&total, &billable); err != nil {
		return nil, fmt.Errorf("timetracking: workspace totals: %w", err)
	}
	out.TotalSec = int(total)
	out.BillableSec = int(billable)

	mrows, err := s.pool.Query(ctx,
		`SELECT m.id, m.name,
                COALESCE(SUM(t.duration_sec), 0),
                COALESCE(SUM(t.duration_sec) FILTER (WHERE t.billable), 0)
            FROM time_entries t
            JOIN members m ON m.id = t.member_id
            WHERE t.workspace_id = $1 AND t.started_at >= $2
            GROUP BY m.id, m.name
            ORDER BY SUM(t.duration_sec) DESC NULLS LAST`,
		workspaceID, since,
	)
	if err != nil {
		return nil, fmt.Errorf("timetracking: by member: %w", err)
	}
	for mrows.Next() {
		var (
			mt        MemberTime
			tot, bill int64
		)
		if err := mrows.Scan(&mt.MemberID, &mt.Name, &tot, &bill); err != nil {
			mrows.Close()
			return nil, err
		}
		mt.TotalSec = int(tot)
		mt.BillableSec = int(bill)
		out.ByMember = append(out.ByMember, mt)
	}
	mrows.Close()

	prows, err := s.pool.Query(ctx,
		`SELECT COALESCE(p.id, '')                                   AS project_id,
                COALESCE(p.name, 'No project')                       AS name,
                COALESCE(SUM(t.duration_sec), 0)                      AS total_sec,
                COALESCE(SUM(t.duration_sec) FILTER (WHERE t.billable), 0) AS billable_sec
            FROM time_entries t
            JOIN issues i ON i.id = t.issue_id
            LEFT JOIN projects p ON p.id = i.project_id
            WHERE t.workspace_id = $1 AND t.started_at >= $2
            GROUP BY p.id, p.name
            ORDER BY SUM(t.duration_sec) DESC NULLS LAST`,
		workspaceID, since,
	)
	if err != nil {
		return nil, fmt.Errorf("timetracking: by project: %w", err)
	}
	for prows.Next() {
		var (
			pt        ProjectTime
			tot, bill int64
		)
		if err := prows.Scan(&pt.ProjectID, &pt.Name, &tot, &bill); err != nil {
			prows.Close()
			return nil, err
		}
		pt.TotalSec = int(tot)
		pt.BillableSec = int(bill)
		out.ByProject = append(out.ByProject, pt)
	}
	prows.Close()

	return out, nil
}

// IssueTotalSec is a thin convenience for the issue store: only the
// total-seconds field is needed to fill model.Issue.TimeTracked,
// and a tiny COUNT-style query is cheaper than the full summary.
func (s *Store) IssueTotalSec(ctx context.Context, issueID string) (int, error) {
	if s.pool == nil {
		return 0, nil
	}
	var total int64
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(duration_sec), 0) FROM time_entries WHERE issue_id = $1`,
		issueID,
	).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("timetracking: total: %w", err)
	}
	return int(total), nil
}

// ─── Delete ────────────────────────────────────────────────

// Delete is a hard delete on the entry. Used by the API to support
// "remove this row from billing" without a corresponding soft-delete
// status — entries are immutable bookkeeping.
func (s *Store) Delete(ctx context.Context, id string) error {
	if s.pool == nil {
		return errors.New("timetracking: store has no pool")
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM time_entries WHERE id = $1`, id)
	return err
}
