package analytics

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

func newMockEngine(t *testing.T) (*Engine, pgxmock.PgxPoolIface) {
	t.Helper()
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	t.Cleanup(pool.Close)
	return newEngine(pool), pool
}

func TestGetVelocity_ReturnsCompletionRates(t *testing.T) {
	engine, pool := newMockEngine(t)
	now := time.Now().UTC()
	pool.ExpectQuery(`FROM cycles c\s+WHERE c.team_id`).
		WithArgs("team-1", "ws-1", 3).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "name", "start_date", "end_date", "total", "completed", "ai_cost",
		}).
			AddRow("c-3", "Sprint 3", now, now.Add(7*24*time.Hour), 10, 8, 4.50).
			AddRow("c-2", "Sprint 2", now, now.Add(7*24*time.Hour), 8, 8, 2.10).
			AddRow("c-1", "Sprint 1", now, now.Add(7*24*time.Hour), 12, 5, 0.0))

	out, err := engine.GetVelocity(context.Background(), "team-1", "ws-1", 3)
	if err != nil {
		t.Fatalf("GetVelocity: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("got %d cycles, want 3", len(out))
	}
	// Sprint 2 is 8/8 = 100% complete.
	if out[1].CompletionRate < 0.99 || out[1].CompletionRate > 1.01 {
		t.Errorf("Sprint 2 completion = %v, want ~1.0", out[1].CompletionRate)
	}
	// Sprint 1 is 5/12 ≈ 41.7%.
	if out[2].CompletionRate < 0.41 || out[2].CompletionRate > 0.42 {
		t.Errorf("Sprint 1 completion = %v, want ~0.417", out[2].CompletionRate)
	}
}

func TestGetVelocity_IncludesAICostPerCycle(t *testing.T) {
	engine, pool := newMockEngine(t)
	now := time.Now().UTC()
	pool.ExpectQuery(`FROM cycles c\s+WHERE c.team_id`).
		WithArgs("team-1", "ws-1", 5).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "name", "start_date", "end_date", "total", "completed", "ai_cost",
		}).AddRow("c-1", "Sprint 1", now, now, 10, 8, 12.34))

	out, err := engine.GetVelocity(context.Background(), "team-1", "ws-1", 5)
	if err != nil {
		t.Fatalf("GetVelocity: %v", err)
	}
	if out[0].AICostUSD != 12.34 {
		t.Errorf("AICostUSD = %v, want 12.34", out[0].AICostUSD)
	}
}

func TestGetDistribution_GroupsByStatus(t *testing.T) {
	engine, pool := newMockEngine(t)
	pool.ExpectQuery(`GROUP BY status`).
		WithArgs("ws-1", 30).
		WillReturnRows(pgxmock.NewRows([]string{"label", "count", "ai_cost"}).
			AddRow("backlog", 15, 0.0).
			AddRow("in_progress", 8, 3.20).
			AddRow("done", 25, 12.50))

	out, err := engine.GetDistribution(context.Background(), "ws-1", "status", 30)
	if err != nil {
		t.Fatalf("GetDistribution: %v", err)
	}
	total := 0
	for _, b := range out {
		total += b.Count
	}
	if total != 48 {
		t.Errorf("total count = %d, want 48", total)
	}
	// "done" should be 25/48 ≈ 52.1%.
	for _, b := range out {
		if b.Label == "done" {
			if b.Pct < 0.51 || b.Pct > 0.53 {
				t.Errorf("done pct = %v, want ~0.521", b.Pct)
			}
		}
	}
}

func TestGetDistribution_GroupsByPriority(t *testing.T) {
	engine, pool := newMockEngine(t)
	pool.ExpectQuery(`GROUP BY priority`).
		WithArgs("ws-1", 30).
		WillReturnRows(pgxmock.NewRows([]string{"label", "count", "ai_cost"}).
			AddRow("1", 3, 5.50).
			AddRow("2", 12, 2.10).
			AddRow("3", 30, 1.20).
			AddRow("4", 5, 0.0))

	out, err := engine.GetDistribution(context.Background(), "ws-1", "priority", 30)
	if err != nil {
		t.Fatalf("GetDistribution: %v", err)
	}
	if len(out) != 4 {
		t.Fatalf("got %d buckets, want 4", len(out))
	}
}

func TestGetDistribution_RejectsUnknownGroupBy(t *testing.T) {
	engine, _ := newMockEngine(t)
	if _, err := engine.GetDistribution(context.Background(), "ws-1", "haxxor; DROP TABLE issues;--", 30); err == nil {
		t.Error("unknown group_by must produce an error to prevent SQL injection")
	}
}

func TestGetTimeToResolution_CalculatesMedianCorrectly(t *testing.T) {
	engine, pool := newMockEngine(t)
	// Global stats row.
	pool.ExpectQuery(`PERCENTILE_CONT\(0\.5\).*PERCENTILE_CONT\(0\.75\).*PERCENTILE_CONT\(0\.95\)`).
		WithArgs("ws-1", 30).
		WillReturnRows(pgxmock.NewRows([]string{"avg", "p50", "p75", "p95"}).
			AddRow(48.5, 24.0, 36.0, 96.0))
	// Per-priority breakdown.
	pool.ExpectQuery(`GROUP BY priority`).
		WithArgs("ws-1", 30).
		WillReturnRows(pgxmock.NewRows([]string{"priority", "median"}).
			AddRow("1", 4.0).
			AddRow("3", 36.0))

	out, err := engine.GetTimeToResolution(context.Background(), "ws-1", "", 30)
	if err != nil {
		t.Fatalf("GetTimeToResolution: %v", err)
	}
	if out.MedianHours != 24.0 {
		t.Errorf("Median = %v, want 24.0", out.MedianHours)
	}
	if out.P95Hours != 96.0 {
		t.Errorf("P95 = %v, want 96.0", out.P95Hours)
	}
	if out.ByPriority["1"] != 4.0 {
		t.Errorf("by-priority urgent = %v, want 4.0", out.ByPriority["1"])
	}
}

func TestGetAICostTrends_ReturnsDailyCostsAndProjection(t *testing.T) {
	engine, pool := newMockEngine(t)
	now := time.Now().UTC()
	// 1. totals
	pool.ExpectQuery(`SELECT COALESCE\(SUM\(ai_cost_usd\), 0\), COUNT\(\*\)`).
		WithArgs("ws-1", 30).
		WillReturnRows(pgxmock.NewRows([]string{"total", "count"}).AddRow(90.0, 30))
	// 2. daily series
	pool.ExpectQuery(`date_trunc\('day'`).
		WithArgs("ws-1", 30).
		WillReturnRows(pgxmock.NewRows([]string{"day", "cost", "issues"}).
			AddRow(now.AddDate(0, 0, -2), 1.50, 2).
			AddRow(now.AddDate(0, 0, -1), 2.10, 3).
			AddRow(now, 0.80, 1))
	// 3. top issues
	pool.ExpectQuery(`ORDER BY ai_cost_usd DESC LIMIT 10`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"id", "identifier", "title", "cost", "tokens"}).
			AddRow("i-1", "ENG-1", "expensive", 12.0, 8000))
	// 4. by team
	pool.ExpectQuery(`JOIN teams t ON t.id = i.team_id`).
		WithArgs("ws-1", 30).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "cost"}).
			AddRow("team-1", "Engineering", 75.0))
	// 5. by label
	pool.ExpectQuery(`UNNEST\(labels\)`).
		WithArgs("ws-1", 30).
		WillReturnRows(pgxmock.NewRows([]string{"label", "cost"}).
			AddRow("bug", 50.0).
			AddRow("feature", 25.0))

	out, err := engine.GetAICostTrends(context.Background(), "ws-1", 30)
	if err != nil {
		t.Fatalf("GetAICostTrends: %v", err)
	}
	if out.TotalCostUSD != 90.0 {
		t.Errorf("TotalCostUSD = %v, want 90.0", out.TotalCostUSD)
	}
	// 30 USD over 30 days × 30 → 90. AvgCostPerIssue = 90/30 = 3.0.
	if out.AvgCostPerIssue != 3.0 {
		t.Errorf("AvgCostPerIssue = %v, want 3.0", out.AvgCostPerIssue)
	}
	// ProjectedMonthly = (total/days)*30 = (90/30)*30 = 90.
	if out.ProjectedMonthly != 90.0 {
		t.Errorf("ProjectedMonthly = %v, want 90.0", out.ProjectedMonthly)
	}
	if len(out.DailyCosts) != 3 {
		t.Errorf("daily costs = %d, want 3", len(out.DailyCosts))
	}
}

func TestGetWorkload_CountsOpenAndOverdueCorrectly(t *testing.T) {
	engine, pool := newMockEngine(t)
	pool.ExpectQuery(`JOIN members m ON m.id = i.assignee_id`).
		WithArgs("ws-1", "team-1").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "name", "avatar_url", "open", "in_progress", "overdue", "cost",
		}).
			AddRow("alice", "Alice", "", 7, 3, 2, 1.50).
			AddRow("bob", "Bob", "", 4, 1, 0, 0.0))

	out, err := engine.GetWorkload(context.Background(), "ws-1", "team-1")
	if err != nil {
		t.Fatalf("GetWorkload: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d members, want 2", len(out))
	}
	if out[0].MemberID != "alice" || out[0].Overdue != 2 {
		t.Errorf("alice = %+v", out[0])
	}
	if out[1].Overdue != 0 {
		t.Errorf("bob overdue = %d, want 0", out[1].Overdue)
	}
}

func TestExportVelocityCSV_ProducesValidCSV(t *testing.T) {
	engine, pool := newMockEngine(t)
	now := time.Now().UTC()
	pool.ExpectQuery(`FROM cycles c\s+WHERE c.team_id`).
		WithArgs("team-1", "ws-1", 2).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "name", "start_date", "end_date", "total", "completed", "ai_cost",
		}).
			AddRow("c-2", "Sprint, 2", now, now, 10, 9, 1.234).
			AddRow("c-1", "Sprint 1", now, now, 8, 8, 0.5))

	var buf bytes.Buffer
	if err := engine.ExportVelocityCSV(context.Background(), "team-1", "ws-1", 2, &buf); err != nil {
		t.Fatalf("ExportVelocityCSV: %v", err)
	}
	body := buf.String()
	// Header row + 2 data rows = 3 lines minimum.
	if lines := strings.Count(body, "\n"); lines < 3 {
		t.Errorf("got %d lines, want >= 3", lines)
	}
	// CSV must properly quote the comma-containing name.
	if !strings.Contains(body, `"Sprint, 2"`) {
		t.Errorf("comma-in-name not quoted; body=%s", body)
	}
	// Money formatted to 2dp: 1.234 → 1.23.
	if !strings.Contains(body, "1.23") {
		t.Errorf("ai_cost_usd not formatted to 2dp; body=%s", body)
	}
}

func TestClampDays_BoundsRespected(t *testing.T) {
	if got := clampDays(0); got != defaultWindowDays {
		t.Errorf("0 → %d, want %d (default)", got, defaultWindowDays)
	}
	if got := clampDays(99999); got != maxWindowDays {
		t.Errorf("99999 → %d, want %d (cap)", got, maxWindowDays)
	}
	if got := clampDays(60); got != 60 {
		t.Errorf("60 → %d, want 60 (passthrough)", got)
	}
}
