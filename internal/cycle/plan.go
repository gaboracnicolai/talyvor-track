package cycle

import (
	"context"
	"fmt"
	"time"

	"github.com/talyvor/track/internal/forecast"
)

// Plan is a cycle priced and dated before it ends: when its open work is likely to finish, at its
// team's recent pace, and what AI spend the whole cycle is likely to cost, from what comparable
// finished work was actually charged.
type Plan struct {
	CycleID  string            `json:"cycle_id"`
	EndDate  time.Time         `json:"end_date"`
	Forecast forecast.Forecast `json:"forecast"`
	Cost     PlanCost          `json:"cost"`
}

// PlanCost is the estimated AI cost of a cycle's planned work. SpentUSD is what its issues have
// already been charged (by Lens, at its real prices); each open issue is then priced at the
// average charge of comparable work — the team's issues finished in the last forecast.HistoryWeeks
// weeks that share one of its labels when there are at least minLabelComparables of them,
// otherwise all of them — less what that issue has already been charged, never below zero.
type PlanCost struct {
	SpentUSD          float64 `json:"spent_usd"`
	EstimatedOpenUSD  float64 `json:"estimated_open_usd"`
	EstimatedTotalUSD float64 `json:"estimated_total_usd"`
	OpenIssues        int     `json:"open_issues"`
	ComparableIssues  int     `json:"comparable_issues"`
	Priced            bool    `json:"priced"`
}

// minLabelComparables is how many finished issues sharing a label it takes to price an open
// issue from that label alone rather than from the whole team.
const minLabelComparables = 3

type finishedIssue struct {
	labels    []string
	cost      float64
	completed time.Time
}

// GetPlan forecasts and prices cycleID. The cycle's own issues give the open count and the spend so
// far; the team's issues finished in the last forecast.HistoryWeeks weeks give both the pace and
// the per-issue prices.
func (s *Store) GetPlan(ctx context.Context, cycleID, workspaceID string, now time.Time) (*Plan, error) {
	if err := s.assertInWorkspace(ctx, cycleID, workspaceID); err != nil {
		return nil, err
	}
	c, err := s.GetByID(ctx, cycleID)
	if err != nil {
		return nil, fmt.Errorf("cycle: plan: %w", err)
	}
	plan := &Plan{CycleID: c.ID, EndDate: c.EndDate}

	// The team's finished work in the window, and when the team's history began.
	var historyStart *time.Time
	if err := s.pool.QueryRow(ctx,
		`SELECT MIN(created_at) FROM issues WHERE team_id = $1`, c.TeamID,
	).Scan(&historyStart); err != nil {
		return nil, fmt.Errorf("cycle: plan history: %w", err)
	}
	rows, err := s.pool.Query(ctx,
		`SELECT labels, ai_cost_usd, completed_at FROM issues
          WHERE team_id = $1 AND status = 'done'
            AND completed_at > $2::timestamptz - make_interval(weeks => $3) AND completed_at <= $2::timestamptz`,
		c.TeamID, now, forecast.HistoryWeeks,
	)
	if err != nil {
		return nil, fmt.Errorf("cycle: plan history: %w", err)
	}
	var finished []finishedIssue
	for rows.Next() {
		var f finishedIssue
		if err := rows.Scan(&f.labels, &f.cost, &f.completed); err != nil {
			rows.Close()
			return nil, err
		}
		finished = append(finished, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// The cycle's own issues: spend so far, and the open ones to price and forecast.
	rows, err = s.pool.Query(ctx,
		`SELECT status IN ('done', 'cancelled'), labels, ai_cost_usd FROM issues WHERE cycle_id = $1`,
		cycleID,
	)
	if err != nil {
		return nil, fmt.Errorf("cycle: plan issues: %w", err)
	}
	defer rows.Close()
	type openIssue struct {
		labels []string
		spent  float64
	}
	var open []openIssue
	for rows.Next() {
		var (
			closed bool
			o      openIssue
		)
		if err := rows.Scan(&closed, &o.labels, &o.spent); err != nil {
			return nil, err
		}
		plan.Cost.SpentUSD += o.spent
		if !closed {
			open = append(open, o)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	weeksAgo := make([]int, len(finished))
	for i, f := range finished {
		weeksAgo[i] = int(now.Sub(f.completed) / (7 * 24 * time.Hour))
	}
	plan.Forecast = forecast.Build(cycleID, len(open), historyStart, weeksAgo, now)

	plan.Cost.OpenIssues = len(open)
	plan.Cost.ComparableIssues = len(finished)
	plan.Cost.Priced = len(open) == 0 || len(finished) > 0
	if len(finished) > 0 {
		teamAvg := averageCost(finished, nil)
		for _, o := range open {
			est := teamAvg
			if byLabel, n := labelAverage(finished, o.labels); n >= minLabelComparables {
				est = byLabel
			}
			plan.Cost.EstimatedOpenUSD += max(0, est-o.spent)
		}
	}
	plan.Cost.EstimatedTotalUSD = plan.Cost.SpentUSD + plan.Cost.EstimatedOpenUSD
	return plan, nil
}

// averageCost is the mean charge of the finished issues keep accepts (all of them when keep is nil).
func averageCost(finished []finishedIssue, keep func(finishedIssue) bool) float64 {
	sum, n := 0.0, 0
	for _, f := range finished {
		if keep == nil || keep(f) {
			sum += f.cost
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// labelAverage is the mean charge of the finished issues sharing at least one of labels, and how
// many there were.
func labelAverage(finished []finishedIssue, labels []string) (float64, int) {
	if len(labels) == 0 {
		return 0, 0
	}
	want := make(map[string]struct{}, len(labels))
	for _, l := range labels {
		want[l] = struct{}{}
	}
	shares := func(f finishedIssue) bool {
		for _, l := range f.labels {
			if _, ok := want[l]; ok {
				return true
			}
		}
		return false
	}
	n := 0
	for _, f := range finished {
		if shares(f) {
			n++
		}
	}
	return averageCost(finished, shares), n
}
