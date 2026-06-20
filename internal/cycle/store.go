// Package cycle owns sprint-style time-bounded planning windows.
//
// A cycle has an auto-incremented per-team number, a start + end date,
// and a status (upcoming / active / completed). Progress and burndown
// are computed on demand from the issues table — no daily snapshots —
// so the data is always live.
package cycle

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/tenancy"
)

type pgxDB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
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

const cycleColumns = `id, team_id, workspace_id, name, number, status, start_date, end_date, created_at, updated_at`

func scanCycle(s interface{ Scan(...any) error }) (*model.Cycle, error) {
	var c model.Cycle
	if err := s.Scan(
		&c.ID, &c.TeamID, &c.WorkspaceID, &c.Name, &c.Number, &c.Status,
		&c.StartDate, &c.EndDate, &c.CreatedAt, &c.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &c, nil
}

// Create allocates the next cycle number for the team, validates the
// date range, and inserts. Only one active cycle is allowed per team
// at a time — Create itself doesn't enforce that constraint, but
// callers should ensure the new cycle starts as "upcoming" and use
// the activate flow to transition.
// ErrNotFound is returned when a cycle does not exist in the given workspace.
var ErrNotFound = errors.New("cycle: not found")

var validStatuses = map[string]struct{}{"upcoming": {}, "active": {}, "completed": {}}

// CycleUpdate is a partial update; nil fields are left unchanged.
type CycleUpdate struct {
	Name      *string
	Status    *string
	StartDate *time.Time
	EndDate   *time.Time
}

// Update applies a partial update to a cycle, scoped to its workspace. An unknown or
// cross-workspace cycle returns ErrNotFound. Validates the resulting name (non-empty),
// status (upcoming|active|completed), and that end_date stays after start_date.
func (s *Store) Update(ctx context.Context, id, workspaceID string, upd CycleUpdate) (*model.Cycle, error) {
	cur, err := s.GetByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if cur.WorkspaceID != workspaceID {
		return nil, ErrNotFound
	}

	if upd.Name != nil {
		cur.Name = strings.TrimSpace(*upd.Name)
	}
	if upd.Status != nil {
		cur.Status = *upd.Status
	}
	if upd.StartDate != nil {
		cur.StartDate = *upd.StartDate
	}
	if upd.EndDate != nil {
		cur.EndDate = *upd.EndDate
	}

	if cur.Name == "" {
		return nil, errors.New("cycle: name cannot be empty")
	}
	if _, ok := validStatuses[cur.Status]; !ok {
		return nil, fmt.Errorf("cycle: invalid status %q", cur.Status)
	}
	if !cur.EndDate.After(cur.StartDate) {
		return nil, errors.New("cycle: end_date must be after start_date")
	}

	return scanCycle(s.pool.QueryRow(ctx,
		`UPDATE cycles SET name = $2, status = $3, start_date = $4, end_date = $5, updated_at = NOW()
        WHERE id = $1 AND workspace_id = $6 RETURNING `+cycleColumns,
		id, cur.Name, cur.Status, cur.StartDate, cur.EndDate, workspaceID,
	))
}

func (s *Store) Create(ctx context.Context, c model.Cycle) (*model.Cycle, error) {
	if c.WorkspaceID == "" || c.TeamID == "" || c.Name == "" {
		return nil, errors.New("cycle: WorkspaceID, TeamID, and Name required")
	}
	if c.StartDate.IsZero() || c.EndDate.IsZero() {
		return nil, errors.New("cycle: StartDate and EndDate required")
	}
	if !c.EndDate.After(c.StartDate) {
		return nil, errors.New("cycle: EndDate must be after StartDate")
	}
	if c.Status == "" {
		c.Status = "upcoming"
	}
	if err := tenancy.AssertRefInWorkspace(ctx, s.pool, "teams", c.TeamID, c.WorkspaceID); err != nil {
		return nil, err
	}

	var nextNumber int
	if err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(MAX(number), 0) + 1 FROM cycles WHERE team_id = $1`,
		c.TeamID,
	).Scan(&nextNumber); err != nil {
		return nil, fmt.Errorf("cycle: next number: %w", err)
	}
	c.Number = nextNumber

	return scanCycle(s.pool.QueryRow(ctx,
		`INSERT INTO cycles (team_id, workspace_id, name, number, status, start_date, end_date)
        VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING `+cycleColumns,
		c.TeamID, c.WorkspaceID, c.Name, c.Number, c.Status, c.StartDate, c.EndDate,
	))
}

func (s *Store) GetByID(ctx context.Context, id string) (*model.Cycle, error) {
	return scanCycle(s.pool.QueryRow(ctx,
		`SELECT `+cycleColumns+` FROM cycles WHERE id = $1`, id,
	))
}

func (s *Store) ListByTeam(ctx context.Context, teamID string) ([]model.Cycle, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+cycleColumns+` FROM cycles WHERE team_id = $1 ORDER BY number DESC`,
		teamID,
	)
	if err != nil {
		return nil, fmt.Errorf("cycle: list: %w", err)
	}
	defer rows.Close()
	var out []model.Cycle
	for rows.Next() {
		c, err := scanCycle(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// GetActive returns the active cycle for a team, or nil if none. The
// query enforces "currently within the date window" so a misconfigured
// status='active' on a future cycle doesn't accidentally appear.
func (s *Store) GetActive(ctx context.Context, teamID string) (*model.Cycle, error) {
	c, err := scanCycle(s.pool.QueryRow(ctx,
		`SELECT `+cycleColumns+` FROM cycles
        WHERE team_id = $1 AND status = 'active'
          AND NOW() BETWEEN start_date AND end_date
        LIMIT 1`,
		teamID,
	))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return c, nil
}

// Complete marks the cycle done and detaches any remaining incomplete
// issues so the next planning window starts clean. Incomplete issues
// have their cycle_id cleared — they fall back to the team backlog.
func (s *Store) Complete(ctx context.Context, cycleID string) error {
	if _, err := s.pool.Exec(ctx,
		`UPDATE cycles SET status = 'completed', updated_at = NOW() WHERE id = $1`, cycleID,
	); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE issues SET cycle_id = NULL, updated_at = NOW()
        WHERE cycle_id = $1 AND status NOT IN ('done', 'cancelled')`,
		cycleID,
	)
	return err
}

type CycleProgress struct {
	CycleID           string  `json:"cycle_id"`
	TotalIssues       int     `json:"total_issues"`
	Completed         int     `json:"completed"`
	InProgress        int     `json:"in_progress"`
	NotStarted        int     `json:"not_started"`
	CompletionPct     float64 `json:"completion_pct"`
	TotalAICostUSD    float64 `json:"total_ai_cost_usd"`
	AvgAICostPerIssue float64 `json:"avg_ai_cost_per_issue"`
}

// GetProgress aggregates issue counts + AI spend for the cycle. One
// query: GROUP BY status bucket using FILTER, plus SUM(ai_cost_usd).
func (s *Store) GetProgress(ctx context.Context, cycleID string) (*CycleProgress, error) {
	p := &CycleProgress{CycleID: cycleID}
	err := s.pool.QueryRow(ctx,
		`SELECT
            COUNT(*) AS total,
            COUNT(*) FILTER (WHERE status = 'done')                              AS completed,
            COUNT(*) FILTER (WHERE status IN ('in_progress', 'in_review'))       AS in_progress,
            COUNT(*) FILTER (WHERE status IN ('backlog', 'todo'))                 AS not_started,
            COALESCE(SUM(ai_cost_usd), 0)                                         AS total_ai_cost
        FROM issues WHERE cycle_id = $1`,
		cycleID,
	).Scan(&p.TotalIssues, &p.Completed, &p.InProgress, &p.NotStarted, &p.TotalAICostUSD)
	if err != nil {
		return nil, fmt.Errorf("cycle: progress: %w", err)
	}
	if p.TotalIssues > 0 {
		p.CompletionPct = float64(p.Completed) / float64(p.TotalIssues)
		p.AvgAICostPerIssue = p.TotalAICostUSD / float64(p.TotalIssues)
	}
	return p, nil
}

type BurndownPoint struct {
	Date      time.Time `json:"date"`
	Remaining int       `json:"remaining"`
	Ideal     int       `json:"ideal"`
}

// GetBurndown returns one data point per day in the cycle window.
// Remaining is the count of issues NOT done as of end-of-day for that
// date; Ideal is the linear interpolation from start to end. Computed
// on the fly so the chart always reflects the latest data.
func (s *Store) GetBurndown(ctx context.Context, cycleID string) ([]BurndownPoint, error) {
	c, err := s.GetByID(ctx, cycleID)
	if err != nil {
		return nil, err
	}
	var totalIssues int
	if err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM issues WHERE cycle_id = $1`, cycleID,
	).Scan(&totalIssues); err != nil {
		return nil, err
	}

	days := int(c.EndDate.Sub(c.StartDate).Hours()/24) + 1
	if days < 1 {
		days = 1
	}

	out := make([]BurndownPoint, 0, days)
	for i := 0; i < days; i++ {
		day := c.StartDate.AddDate(0, 0, i)
		eod := time.Date(day.Year(), day.Month(), day.Day(), 23, 59, 59, 0, day.Location())

		var completed int
		if err := s.pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM issues
            WHERE cycle_id = $1 AND completed_at IS NOT NULL AND completed_at <= $2`,
			cycleID, eod,
		).Scan(&completed); err != nil {
			return nil, fmt.Errorf("cycle: burndown day %s: %w", day.Format("2006-01-02"), err)
		}

		// Ideal line: linear from total to 0 over `days-1` intervals.
		ideal := totalIssues
		if days > 1 {
			ideal = totalIssues - (totalIssues*i)/(days-1)
		} else if i > 0 {
			ideal = 0
		}

		out = append(out, BurndownPoint{
			Date:      day,
			Remaining: totalIssues - completed,
			Ideal:     ideal,
		})
	}
	return out, nil
}
