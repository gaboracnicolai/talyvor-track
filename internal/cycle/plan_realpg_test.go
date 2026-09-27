package cycle_test

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/talyvor/track/internal/cycle"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/testutil"
)

// A cycle is dated and priced from its team's recent finished work. The team has finished two
// issues a week for eight weeks: four labelled "ml" charged $5.00 each and twelve unlabelled
// charged $0.50 each ($1.625 a finished issue overall). The cycle holds four open issues and one
// cancelled one charged $0.25:
//
//	"ml", charged $1.00 so far  → priced from its label: $5.00 − $1.00 = $4.00 to go
//	unlabelled, nothing charged → the team average:                      $1.625
//	"docs", no finished "docs"  → too few label comparables, team average: $1.625
//	unlabelled, charged $3.00   → already past the average:               $0
//
// So $4.25 is spent, $7.25 is estimated to go, $11.50 in all — and four open issues at two a week
// finish in exactly two weeks.
func TestCyclePlan_ForecastsAndPricesTheCycleFromItsTeamsFinishedWork_RealPG(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := testutil.New(t)

	ws := db.Workspace(t)
	tm := db.Team(t, ws.ID)
	issues := issue.NewStore(db.Pool)
	cycles := cycle.NewStore(db.Pool)

	now := time.Now().UTC()
	cyc, err := cycles.Create(ctx, model.Cycle{
		WorkspaceID: ws.ID, TeamID: tm.ID, Name: "Cycle 9", Number: 9, Status: "active",
		StartDate: now.AddDate(0, 0, -3), EndDate: now.AddDate(0, 0, 14),
	})
	if err != nil {
		t.Fatalf("seed cycle: %v", err)
	}

	historyStart := now.Add(-56*24*time.Hour + time.Hour)
	n := 0
	seed := func(status model.IssueStatus, labels []string, completedAt *time.Time, cycleID *string, usd float64) {
		t.Helper()
		n++
		feature := fmt.Sprintf("plan-fixture-%d", n)
		if _, err := issues.Create(ctx, model.Issue{
			WorkspaceID: ws.ID, TeamID: tm.ID, CycleID: cycleID, Title: "work",
			CreatorID: model.ImporterCreatorID, Status: status, Labels: labels,
			CreatedAt: historyStart, CompletedAt: completedAt, LensFeature: feature,
		}); err != nil {
			t.Fatalf("seed issue: %v", err)
		}
		if usd > 0 {
			if m, err := issues.RecordSpendEvent(ctx, "evt-"+feature, feature, usd, 10, ws.ID, "test"); err != nil || m != 1 {
				t.Fatalf("seed spend: matched %d, err=%v", m, err)
			}
		}
	}
	for w := 0; w < 8; w++ {
		for i := 0; i < 2; i++ {
			done := now.Add(-time.Duration(w*7*24+84) * time.Hour)
			if w < 2 {
				seed(model.StatusDone, []string{"ml"}, &done, nil, 5.00)
			} else {
				seed(model.StatusDone, nil, &done, nil, 0.50)
			}
		}
	}
	seed(model.StatusTodo, []string{"ml"}, nil, &cyc.ID, 1.00)
	seed(model.StatusTodo, nil, nil, &cyc.ID, 0)
	seed(model.StatusTodo, []string{"docs"}, nil, &cyc.ID, 0)
	seed(model.StatusInProgress, nil, nil, &cyc.ID, 3.00)
	seed(model.StatusCancelled, nil, nil, &cyc.ID, 0.25)

	plan, err := cycles.GetPlan(ctx, cyc.ID, ws.ID, now)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}

	c := plan.Cost
	near := func(got, want float64) bool { return math.Abs(got-want) < 1e-9 }
	if !c.Priced || c.OpenIssues != 4 || c.ComparableIssues != 16 ||
		!near(c.SpentUSD, 4.25) || !near(c.EstimatedOpenUSD, 7.25) || !near(c.EstimatedTotalUSD, 11.50) {
		t.Fatalf("cost = %+v, want priced, 4 open, 16 comparable, $4.25 spent, $7.25 to go, $11.50 total", c)
	}
	f := plan.Forecast
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	want := today.AddDate(0, 0, 14)
	if f.Status != "forecast" || f.Remaining != 4 || f.HistoryWeeks != 8 || f.FinishedInHistory != 16 ||
		f.Likely == nil || !f.Likely.Equal(want) || f.Safe == nil || !f.Safe.Equal(want) {
		t.Fatalf("forecast = %+v (likely %v, safe %v), want 4 open over 8 weeks of 16 finished, both dates %v",
			f, f.Likely, f.Safe, want)
	}

	// Another workspace's caller cannot read the plan.
	other := db.Workspace(t)
	if _, err := cycles.GetPlan(ctx, cyc.ID, other.ID, now); err != cycle.ErrNotFound {
		t.Fatalf("cross-workspace GetPlan err = %v, want ErrNotFound", err)
	}
}
