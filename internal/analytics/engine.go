// Package analytics implements Track's reporting engine.
//
// Every report is a pure read against the issues / cycles / members
// tables. No analytics state is persisted — recomputing is cheap
// enough at the typical issue volume, and the freshness story is
// trivial (no cache invalidation). The composite indexes in
// migrations/0009_analytics.sql carry the hot query shapes.
package analytics

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// maxWindowDays caps every report at 365 days. Wider windows are
// rare in practice and would push the cost of the full-scan queries
// above what the indexes can absorb.
const (
	maxWindowDays     = 365
	defaultWindowDays = 30
)

type pgxDB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Engine struct{ pool pgxDB }

func New(pool *pgxpool.Pool) *Engine {
	var db pgxDB
	if pool != nil {
		db = pool
	}
	return newEngine(db)
}

func newEngine(db pgxDB) *Engine { return &Engine{pool: db} }

func clampDays(days int) int {
	if days <= 0 {
		return defaultWindowDays
	}
	if days > maxWindowDays {
		return maxWindowDays
	}
	return days
}

// ─────────────────────────────────────────────────────────
// Report 1: Velocity
// ─────────────────────────────────────────────────────────

type CycleVelocity struct {
	CycleID        string    `json:"cycle_id"`
	CycleName      string    `json:"cycle_name"`
	StartDate      time.Time `json:"start_date"`
	EndDate        time.Time `json:"end_date"`
	Completed      int       `json:"completed"`
	Total          int       `json:"total"`
	CompletionRate float64   `json:"completion_rate"`
	AICostUSD      float64   `json:"ai_cost_usd"`
}

// GetVelocity returns the last N cycles for a team, each annotated
// with completion rate + AI cost. One query joins the cycles table
// with two correlated subqueries against issues — fewer round-trips
// than fetching cycle metadata then aggregating per cycle.
func (e *Engine) GetVelocity(ctx context.Context, teamID string, cycles int) ([]CycleVelocity, error) {
	if cycles <= 0 {
		cycles = 5
	}
	if cycles > 50 {
		cycles = 50
	}
	rows, err := e.pool.Query(ctx, `
        SELECT c.id, c.name, c.start_date, c.end_date,
            COALESCE((SELECT COUNT(*) FROM issues WHERE cycle_id = c.id), 0) AS total,
            COALESCE((SELECT COUNT(*) FROM issues WHERE cycle_id = c.id
                AND status IN ('done','cancelled')), 0) AS completed,
            COALESCE((SELECT SUM(ai_cost_usd) FROM issues WHERE cycle_id = c.id), 0) AS ai_cost
        FROM cycles c
        WHERE c.team_id = $1
        ORDER BY c.number DESC
        LIMIT $2`, teamID, cycles)
	if err != nil {
		return nil, fmt.Errorf("analytics: velocity: %w", err)
	}
	defer rows.Close()
	var out []CycleVelocity
	for rows.Next() {
		var v CycleVelocity
		if err := rows.Scan(&v.CycleID, &v.CycleName, &v.StartDate, &v.EndDate,
			&v.Total, &v.Completed, &v.AICostUSD); err != nil {
			return nil, err
		}
		if v.Total > 0 {
			v.CompletionRate = float64(v.Completed) / float64(v.Total)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────
// Report 2: Burndown (wraps cycle data with on-track + projection)
// ─────────────────────────────────────────────────────────

type BurndownPoint struct {
	Date      time.Time `json:"date"`
	Remaining int       `json:"remaining"`
	Ideal     int       `json:"ideal"`
}

type BurndownReport struct {
	CycleID      string          `json:"cycle_id"`
	CycleName    string          `json:"cycle_name"`
	StartDate    time.Time       `json:"start_date"`
	EndDate      time.Time       `json:"end_date"`
	Points       []BurndownPoint `json:"points"`
	IsOnTrack    bool            `json:"is_on_track"`
	ProjectedEnd *time.Time      `json:"projected_end,omitempty"`
}

// GetBurndown reads the cycle window, samples remaining issues per
// day, and computes the on-track / projected-end metadata. Same
// approach as cycle.Store.GetBurndown but with the analytics-layer
// enrichments.
func (e *Engine) GetBurndown(ctx context.Context, cycleID string) (*BurndownReport, error) {
	var (
		name             string
		start, end       time.Time
		total            int
	)
	if err := e.pool.QueryRow(ctx,
		`SELECT c.name, c.start_date, c.end_date,
            (SELECT COUNT(*) FROM issues WHERE cycle_id = c.id)
        FROM cycles c WHERE c.id = $1`,
		cycleID,
	).Scan(&name, &start, &end, &total); err != nil {
		return nil, fmt.Errorf("analytics: burndown cycle: %w", err)
	}

	days := int(end.Sub(start).Hours()/24) + 1
	if days < 1 {
		days = 1
	}
	report := &BurndownReport{
		CycleID: cycleID, CycleName: name,
		StartDate: start, EndDate: end,
		Points: make([]BurndownPoint, 0, days),
	}
	now := time.Now().UTC()
	currentRemaining := total
	for i := 0; i < days; i++ {
		day := start.AddDate(0, 0, i)
		eod := time.Date(day.Year(), day.Month(), day.Day(), 23, 59, 59, 0, day.Location())
		var completed int
		if err := e.pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM issues
            WHERE cycle_id = $1 AND completed_at IS NOT NULL AND completed_at <= $2`,
			cycleID, eod,
		).Scan(&completed); err != nil {
			return nil, fmt.Errorf("analytics: burndown day %s: %w", day.Format("2006-01-02"), err)
		}
		ideal := total
		if days > 1 {
			ideal = total - (total*i)/(days-1)
		} else if i > 0 {
			ideal = 0
		}
		remaining := total - completed
		report.Points = append(report.Points, BurndownPoint{Date: day, Remaining: remaining, Ideal: ideal})
		if !day.After(now) {
			currentRemaining = remaining
			// On-track means actual remaining is at or below the
			// ideal line for the current date.
			report.IsOnTrack = remaining <= ideal
		}
	}

	// Projected end: extrapolate from the current completion rate.
	// daysElapsed = (now - start) in days; rate = (total - remaining) / daysElapsed.
	daysElapsed := now.Sub(start).Hours() / 24
	if daysElapsed > 0.5 && currentRemaining > 0 {
		rate := float64(total-currentRemaining) / daysElapsed
		if rate > 0 {
			daysRemaining := float64(currentRemaining) / rate
			projected := now.Add(time.Duration(daysRemaining * 24 * float64(time.Hour)))
			report.ProjectedEnd = &projected
		}
	}
	return report, nil
}

// ─────────────────────────────────────────────────────────
// Report 3: Distribution
// ─────────────────────────────────────────────────────────

type DistributionBucket struct {
	Label     string  `json:"label"`
	Count     int     `json:"count"`
	Pct       float64 `json:"pct"`
	AICostUSD float64 `json:"ai_cost_usd"`
}

// allowedGroupBy gates the groupBy column at query-build time so SQL
// composition is safe — caller's groupBy never reaches the SQL string
// unless it's in this set.
var allowedGroupBy = map[string]string{
	"status":   "status",
	"priority": "priority::text",
	"assignee": "COALESCE(assignee_id, 'unassigned')",
	"team":     "team_id",
}

// GetDistribution aggregates issues by the requested column over the
// window. Labels are handled as a special case (UNNEST). Returns
// buckets sorted by count desc.
func (e *Engine) GetDistribution(ctx context.Context, workspaceID, groupBy string, days int) ([]DistributionBucket, error) {
	days = clampDays(days)
	if groupBy == "label" {
		return e.distributionByLabel(ctx, workspaceID, days)
	}
	col, ok := allowedGroupBy[groupBy]
	if !ok {
		return nil, fmt.Errorf("analytics: unsupported group_by %q", groupBy)
	}
	sql := fmt.Sprintf(`SELECT %s::text, COUNT(*), COALESCE(SUM(ai_cost_usd), 0)
        FROM issues
        WHERE workspace_id = $1
          AND created_at > NOW() - (INTERVAL '1 day' * $2::int)
        GROUP BY %s
        ORDER BY COUNT(*) DESC`, col, col)
	return e.scanDistribution(ctx, sql, workspaceID, days)
}

func (e *Engine) distributionByLabel(ctx context.Context, workspaceID string, days int) ([]DistributionBucket, error) {
	return e.scanDistribution(ctx,
		`SELECT label, COUNT(*), COALESCE(SUM(ai_cost_usd), 0)
        FROM (
            SELECT UNNEST(labels) AS label, ai_cost_usd
            FROM issues
            WHERE workspace_id = $1
              AND created_at > NOW() - (INTERVAL '1 day' * $2::int)
        ) t
        GROUP BY label
        ORDER BY COUNT(*) DESC`,
		workspaceID, days,
	)
}

func (e *Engine) scanDistribution(ctx context.Context, sql string, args ...any) ([]DistributionBucket, error) {
	rows, err := e.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("analytics: distribution: %w", err)
	}
	defer rows.Close()
	var (
		buckets []DistributionBucket
		total   int
	)
	for rows.Next() {
		var b DistributionBucket
		if err := rows.Scan(&b.Label, &b.Count, &b.AICostUSD); err != nil {
			return nil, err
		}
		buckets = append(buckets, b)
		total += b.Count
	}
	for i := range buckets {
		if total > 0 {
			buckets[i].Pct = float64(buckets[i].Count) / float64(total)
		}
	}
	return buckets, rows.Err()
}

// ─────────────────────────────────────────────────────────
// Report 4: Time to Resolution (percentile-based)
// ─────────────────────────────────────────────────────────

type ResolutionStats struct {
	AvgHours    float64            `json:"avg_hours"`
	MedianHours float64            `json:"median_hours"`
	P75Hours    float64            `json:"p75_hours"`
	P95Hours    float64            `json:"p95_hours"`
	ByPriority  map[string]float64 `json:"by_priority"`
}

// GetTimeToResolution uses PERCENTILE_CONT in SQL — application-level
// approximation would diverge from what the Postgres histograms /
// dashboards show. One query for the global stats, a second for the
// per-priority breakdown.
func (e *Engine) GetTimeToResolution(ctx context.Context, workspaceID, teamID string, days int) (*ResolutionStats, error) {
	days = clampDays(days)

	var (
		args   = []any{workspaceID, days}
		teamSQL = ""
	)
	if teamID != "" {
		args = append(args, teamID)
		teamSQL = " AND team_id = $3"
	}
	var stats ResolutionStats
	row := e.pool.QueryRow(ctx, fmt.Sprintf(`
        SELECT
            COALESCE(AVG(EXTRACT(EPOCH FROM completed_at - created_at)/3600), 0),
            COALESCE(PERCENTILE_CONT(0.5)  WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM completed_at - created_at)/3600), 0),
            COALESCE(PERCENTILE_CONT(0.75) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM completed_at - created_at)/3600), 0),
            COALESCE(PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM completed_at - created_at)/3600), 0)
        FROM issues
        WHERE workspace_id = $1
          AND completed_at IS NOT NULL
          AND created_at > NOW() - (INTERVAL '1 day' * $2::int)%s`, teamSQL),
		args...)
	if err := row.Scan(&stats.AvgHours, &stats.MedianHours, &stats.P75Hours, &stats.P95Hours); err != nil {
		return nil, fmt.Errorf("analytics: resolution stats: %w", err)
	}

	// Per-priority breakdown — median only, keeps the surface narrow.
	prioRows, err := e.pool.Query(ctx, fmt.Sprintf(`
        SELECT priority::text,
            COALESCE(PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM completed_at - created_at)/3600), 0)
        FROM issues
        WHERE workspace_id = $1
          AND completed_at IS NOT NULL
          AND created_at > NOW() - (INTERVAL '1 day' * $2::int)%s
        GROUP BY priority`, teamSQL),
		args...)
	if err != nil {
		return nil, fmt.Errorf("analytics: resolution by priority: %w", err)
	}
	defer prioRows.Close()
	stats.ByPriority = make(map[string]float64)
	for prioRows.Next() {
		var (
			prio   string
			median float64
		)
		if err := prioRows.Scan(&prio, &median); err != nil {
			return nil, err
		}
		stats.ByPriority[prio] = median
	}
	return &stats, prioRows.Err()
}

// ─────────────────────────────────────────────────────────
// Report 5: AI Cost Trends (Talyvor's unique report)
// ─────────────────────────────────────────────────────────

type DailyCost struct {
	Date    time.Time `json:"date"`
	CostUSD float64   `json:"cost_usd"`
	Issues  int       `json:"issues_worked"`
}

type IssueCost struct {
	IssueID    string  `json:"issue_id"`
	Identifier string  `json:"identifier"`
	Title      string  `json:"title"`
	CostUSD    float64 `json:"cost_usd"`
	Tokens     int     `json:"tokens"`
}

type TeamCost struct {
	TeamID  string  `json:"team_id"`
	Name    string  `json:"name"`
	CostUSD float64 `json:"cost_usd"`
}

type LabelCost struct {
	Label   string  `json:"label"`
	CostUSD float64 `json:"cost_usd"`
}

type AICostTrends struct {
	TotalCostUSD     float64     `json:"total_cost_usd"`
	DailyCosts       []DailyCost `json:"daily_costs"`
	TopCostIssues    []IssueCost `json:"top_cost_issues"`
	CostByTeam       []TeamCost  `json:"cost_by_team"`
	CostByLabel      []LabelCost `json:"cost_by_label"`
	ProjectedMonthly float64     `json:"projected_monthly_usd"`
	AvgCostPerIssue  float64     `json:"avg_cost_per_issue"`
}

// GetAICostTrends runs the four sub-queries the AI cost dashboard
// needs and assembles the response. Daily costs feeds the line
// chart; top issues feeds the leaderboard; team + label breakdowns
// feed the pie / bar charts.
func (e *Engine) GetAICostTrends(ctx context.Context, workspaceID string, days int) (*AICostTrends, error) {
	days = clampDays(days)
	out := &AICostTrends{}

	// Totals + averages — one row scan.
	var (
		total   float64
		count   int
	)
	if err := e.pool.QueryRow(ctx, `
        SELECT COALESCE(SUM(ai_cost_usd), 0), COUNT(*) FILTER (WHERE ai_cost_usd > 0)
        FROM issues
        WHERE workspace_id = $1
          AND updated_at > NOW() - (INTERVAL '1 day' * $2::int)`,
		workspaceID, days,
	).Scan(&total, &count); err != nil {
		return nil, fmt.Errorf("analytics: cost totals: %w", err)
	}
	out.TotalCostUSD = total
	if count > 0 {
		out.AvgCostPerIssue = total / float64(count)
	}
	// Projection extrapolates the per-day rate to 30 days. Safer
	// than scaling to a calendar month because months vary.
	if days > 0 {
		out.ProjectedMonthly = (total / float64(days)) * 30
	}

	// Daily series.
	rows, err := e.pool.Query(ctx, `
        SELECT date_trunc('day', updated_at) AS day,
            COALESCE(SUM(ai_cost_usd), 0),
            COUNT(*) FILTER (WHERE ai_cost_usd > 0)
        FROM issues
        WHERE workspace_id = $1
          AND updated_at > NOW() - (INTERVAL '1 day' * $2::int)
        GROUP BY day
        ORDER BY day ASC`,
		workspaceID, days,
	)
	if err != nil {
		return nil, fmt.Errorf("analytics: daily costs: %w", err)
	}
	for rows.Next() {
		var d DailyCost
		if err := rows.Scan(&d.Date, &d.CostUSD, &d.Issues); err != nil {
			rows.Close()
			return nil, err
		}
		out.DailyCosts = append(out.DailyCosts, d)
	}
	rows.Close()

	// Top-cost issues.
	rows, err = e.pool.Query(ctx, `
        SELECT id, identifier, title, ai_cost_usd, ai_tokens
        FROM issues
        WHERE workspace_id = $1 AND ai_cost_usd > 0
        ORDER BY ai_cost_usd DESC LIMIT 10`,
		workspaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("analytics: top issues: %w", err)
	}
	for rows.Next() {
		var ic IssueCost
		if err := rows.Scan(&ic.IssueID, &ic.Identifier, &ic.Title, &ic.CostUSD, &ic.Tokens); err != nil {
			rows.Close()
			return nil, err
		}
		out.TopCostIssues = append(out.TopCostIssues, ic)
	}
	rows.Close()

	// Cost by team — JOIN issues.team_id to teams for the display name.
	rows, err = e.pool.Query(ctx, `
        SELECT t.id, t.name, COALESCE(SUM(i.ai_cost_usd), 0)
        FROM issues i
        JOIN teams t ON t.id = i.team_id
        WHERE i.workspace_id = $1
          AND i.updated_at > NOW() - (INTERVAL '1 day' * $2::int)
        GROUP BY t.id, t.name
        ORDER BY SUM(i.ai_cost_usd) DESC NULLS LAST`,
		workspaceID, days,
	)
	if err != nil {
		return nil, fmt.Errorf("analytics: cost by team: %w", err)
	}
	for rows.Next() {
		var tc TeamCost
		if err := rows.Scan(&tc.TeamID, &tc.Name, &tc.CostUSD); err != nil {
			rows.Close()
			return nil, err
		}
		out.CostByTeam = append(out.CostByTeam, tc)
	}
	rows.Close()

	// Cost by label — UNNEST the labels array.
	rows, err = e.pool.Query(ctx, `
        SELECT label, COALESCE(SUM(ai_cost_usd), 0)
        FROM (
            SELECT UNNEST(labels) AS label, ai_cost_usd
            FROM issues
            WHERE workspace_id = $1
              AND updated_at > NOW() - (INTERVAL '1 day' * $2::int)
        ) t
        GROUP BY label
        ORDER BY SUM(ai_cost_usd) DESC LIMIT 20`,
		workspaceID, days,
	)
	if err != nil {
		return nil, fmt.Errorf("analytics: cost by label: %w", err)
	}
	for rows.Next() {
		var lc LabelCost
		if err := rows.Scan(&lc.Label, &lc.CostUSD); err != nil {
			rows.Close()
			return nil, err
		}
		out.CostByLabel = append(out.CostByLabel, lc)
	}
	rows.Close()

	return out, nil
}

// ─────────────────────────────────────────────────────────
// Report 6: Workload Distribution
// ─────────────────────────────────────────────────────────

type MemberWorkload struct {
	MemberID   string  `json:"member_id"`
	Name       string  `json:"name"`
	AvatarURL  string  `json:"avatar_url"`
	OpenIssues int     `json:"open_issues"`
	InProgress int     `json:"in_progress"`
	Overdue    int     `json:"overdue"`
	AICostUSD  float64 `json:"ai_cost_usd"`
}

// GetWorkload groups open issues by assignee and joins to members
// for display. Overdue counts issues past their due_date that aren't
// done or cancelled.
func (e *Engine) GetWorkload(ctx context.Context, workspaceID, teamID string) ([]MemberWorkload, error) {
	args := []any{workspaceID}
	teamSQL := ""
	if teamID != "" {
		args = append(args, teamID)
		teamSQL = " AND i.team_id = $2"
	}
	rows, err := e.pool.Query(ctx, fmt.Sprintf(`
        SELECT m.id, m.name, m.avatar_url,
            COUNT(*) FILTER (WHERE i.status NOT IN ('done','cancelled')) AS open_issues,
            COUNT(*) FILTER (WHERE i.status IN ('in_progress','in_review')) AS in_progress,
            COUNT(*) FILTER (
                WHERE i.due_date IS NOT NULL
                  AND i.due_date < NOW()
                  AND i.status NOT IN ('done','cancelled')
            ) AS overdue,
            COALESCE(SUM(i.ai_cost_usd), 0) AS ai_cost_usd
        FROM issues i
        JOIN members m ON m.id = i.assignee_id
        WHERE i.workspace_id = $1%s
        GROUP BY m.id, m.name, m.avatar_url
        ORDER BY open_issues DESC`, teamSQL),
		args...)
	if err != nil {
		return nil, fmt.Errorf("analytics: workload: %w", err)
	}
	defer rows.Close()
	var out []MemberWorkload
	for rows.Next() {
		var w MemberWorkload
		if err := rows.Scan(&w.MemberID, &w.Name, &w.AvatarURL,
			&w.OpenIssues, &w.InProgress, &w.Overdue, &w.AICostUSD); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────
// CSV export — writes directly to an io.Writer without buffering
// ─────────────────────────────────────────────────────────

// ExportVelocityCSV streams the velocity report as CSV. Money is
// formatted to 2dp per the constraint.
func (e *Engine) ExportVelocityCSV(ctx context.Context, teamID string, cycles int, w io.Writer) error {
	rows, err := e.GetVelocity(ctx, teamID, cycles)
	if err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"cycle_id", "cycle_name", "start_date", "end_date", "completed", "total", "completion_rate", "ai_cost_usd"}); err != nil {
		return err
	}
	for _, r := range rows {
		_ = cw.Write([]string{
			r.CycleID, r.CycleName,
			r.StartDate.Format(time.RFC3339), r.EndDate.Format(time.RFC3339),
			strconv.Itoa(r.Completed), strconv.Itoa(r.Total),
			strconv.FormatFloat(r.CompletionRate, 'f', 4, 64),
			strconv.FormatFloat(r.AICostUSD, 'f', 2, 64),
		})
	}
	cw.Flush()
	return cw.Error()
}

// ExportAICostTrendsCSV streams the daily-cost series as CSV. The
// full AI trends struct has nested arrays (top issues, by team, by
// label); for the CSV export we project to the time-series so the
// output is one row per day.
func (e *Engine) ExportAICostTrendsCSV(ctx context.Context, workspaceID string, days int, w io.Writer) error {
	rep, err := e.GetAICostTrends(ctx, workspaceID, days)
	if err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"date", "cost_usd", "issues_worked"}); err != nil {
		return err
	}
	for _, d := range rep.DailyCosts {
		_ = cw.Write([]string{
			d.Date.Format("2006-01-02"),
			strconv.FormatFloat(d.CostUSD, 'f', 2, 64),
			strconv.Itoa(d.Issues),
		})
	}
	cw.Flush()
	return cw.Error()
}

// ExportDistributionCSV streams the distribution buckets.
func (e *Engine) ExportDistributionCSV(ctx context.Context, workspaceID, groupBy string, days int, w io.Writer) error {
	buckets, err := e.GetDistribution(ctx, workspaceID, groupBy, days)
	if err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{groupBy, "count", "pct", "ai_cost_usd"}); err != nil {
		return err
	}
	for _, b := range buckets {
		_ = cw.Write([]string{
			b.Label,
			strconv.Itoa(b.Count),
			strconv.FormatFloat(b.Pct, 'f', 4, 64),
			strconv.FormatFloat(b.AICostUSD, 'f', 2, 64),
		})
	}
	cw.Flush()
	return cw.Error()
}

var _ = errors.New
