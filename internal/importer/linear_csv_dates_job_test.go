package importer_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/talyvor/track/internal/analytics"
	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/testutil"
)

// linear_csv_dates_job_test.go — the two Linear CSV date columns driven END TO END through the async
// runner on real Postgres, and then READ BACK THROUGH THE REPORT THAT CONSUMES THEM.
//
// ⚠ WHY THE REPORT AND NOT JUST THE COLUMNS. The two losses fail in OPPOSITE, individually
// invisible ways, and only the computed number sees both:
//
//	Created   unread ⇒ issues.created_at takes its DEFAULT NOW(). The column is never null and
//	                   never looks empty; the wrong value has exactly the shape of the right one.
//	Completed unread ⇒ issues.completed_at stays NULL, and analytics' resolution query selects on
//	                   `completed_at IS NOT NULL`, so the row is not WRONG in the report — it is
//	                   ABSENT from it. A whole Linear history imports and the resolution report
//	                   stays empty.
//
// So a column assertion alone cannot see the first and a report assertion alone cannot distinguish
// the second from "no finished work was imported". Both are here, and the report assertion is
// two-sided on purpose: reading ONLY `Completed` gives (a past instant) − (now) and the median goes
// NEGATIVE — #83's defect, which a one-sided `!= 0` check would have called a pass.
//
// The fixture dates are COMPUTED rather than written down because both analytics queries filter
// `created_at > NOW() - INTERVAL '1 day' * $2` with $2 clamped to 365: a hardcoded date would age
// out of the window and the test would stop testing anything while staying green.
const (
	// The layout the fixture FORMATS with, hardcoded rather than read from the package constant —
	// an assertion that formats with the same constant the code parses with compares the constant
	// to itself and passes for every possible value.
	linearCSVJobDateLayout = "2006-01-02"

	linearCSVCreatedDaysAgo   = 200
	linearCSVCompletedDaysAgo = 100
	linearCSVTrueCycleHours   = float64(linearCSVCreatedDaysAgo-linearCSVCompletedDaysAgo) * 24
)

func linearCSVDatesFixture() (csv string, created, completed time.Time) {
	now := time.Now().UTC()
	// Truncate to the day: the layout carries no time, so a round-trip through it drops one and an
	// equality assertion would fail for a reason that has nothing to do with the finding.
	created = now.Add(-time.Duration(linearCSVCreatedDaysAgo) * 24 * time.Hour).Truncate(24 * time.Hour)
	completed = now.Add(-time.Duration(linearCSVCompletedDaysAgo) * 24 * time.Hour).Truncate(24 * time.Hour)
	csv = "ID,Title,Description,Status,Priority,Assignee,Labels,Created,Completed\n" +
		fmt.Sprintf("LIN-1,Opened long before the import,d,Done,High,,,%s,%s\n",
			created.Format(linearCSVJobDateLayout), completed.Format(linearCSVJobDateLayout))
	return csv, created, completed
}

func runLinearCSVDatesImport(t *testing.T, d *testutil.DB, body string) (wsID string) {
	t.Helper()
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	js := importer.NewJobStore(d.Pool)
	runner := importer.NewRunner(js, importer.New(issue.NewStore(d.Pool)))
	if _, err := js.Create(ctx, ws.ID, team.ID, "linear_csv", []byte(body)); err != nil {
		t.Fatal(err)
	}
	if did, err := runner.RunOnce(ctx); err != nil || !did {
		t.Fatalf("RunOnce did=%v err=%v", did, err)
	}
	return ws.ID
}

// TestJobRow_LinearCSV_ImportedIssueKeepsTheDatesLinearRecorded is the column half of both fields.
func TestJobRow_LinearCSV_ImportedIssueKeepsTheDatesLinearRecorded(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	body, created, completed := linearCSVDatesFixture()
	wsID := runLinearCSVDatesImport(t, d, body)

	var gotCreated time.Time
	var gotCompleted *time.Time
	if err := d.Pool.QueryRow(ctx,
		`SELECT created_at, completed_at FROM issues WHERE workspace_id = $1`, wsID,
	).Scan(&gotCreated, &gotCompleted); err != nil {
		t.Fatalf("read dates: %v", err)
	}
	if delta := gotCreated.UTC().Sub(created); delta > time.Minute || delta < -time.Minute {
		t.Errorf("created_at = %s, want %s (the Created column) — off by %s.\n"+
			"A defaulted created_at is the import instant, so every imported issue reads as opened today.",
			gotCreated.UTC().Format(time.RFC3339), created.Format(time.RFC3339), delta)
	}
	if gotCompleted == nil {
		t.Fatalf("completed_at is NULL for a Done row whose Completed column said %s.\n"+
			"analytics selects on completed_at IS NOT NULL, so this issue is absent from every "+
			"resolution and throughput report.", completed.Format(time.RFC3339))
	}
	if delta := gotCompleted.UTC().Sub(completed); delta > time.Minute || delta < -time.Minute {
		t.Errorf("completed_at = %s, want %s (the Completed column) — off by %s",
			gotCompleted.UTC().Format(time.RFC3339), completed.Format(time.RFC3339), delta)
	}
}

// TestJobRow_LinearCSV_CycleTimeOfAnImportedIssueIsRealAndPositive is the half a column read cannot
// do. Both bounds are load-bearing: MedianHours == 0 is what an unread `Completed` produces (no row
// satisfies the report's `IS NOT NULL` predicate at all), and a NEGATIVE median is what reading
// only `Completed` produces.
func TestJobRow_LinearCSV_CycleTimeOfAnImportedIssueIsRealAndPositive(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	body, _, _ := linearCSVDatesFixture()
	wsID := runLinearCSVDatesImport(t, d, body)

	stats, err := analytics.New(d.Pool).GetTimeToResolution(ctx, wsID, "", 365)
	if err != nil {
		t.Fatalf("resolution stats: %v", err)
	}
	if stats.MedianHours == 0 {
		t.Fatalf("median time to resolution = 0 for a workspace whose only issue Linear finished %d "+
			"days ago.\nNo row satisfies `completed_at IS NOT NULL`: the import landed a whole "+
			"finished history that the resolution report cannot see.", linearCSVCompletedDaysAgo)
	}
	if stats.MedianHours < 0 {
		t.Fatalf("median time to resolution = %.1f hours — NEGATIVE. That is completed_at (past) "+
			"minus created_at (the import instant).", stats.MedianHours)
	}
	if delta := stats.MedianHours - linearCSVTrueCycleHours; delta > 24 || delta < -24 {
		t.Errorf("median time to resolution = %.1f hours, want ≈ %.0f (Completed − Created)",
			stats.MedianHours, linearCSVTrueCycleHours)
	}
}

// TestJobRow_LinearCSV_ADatelessExportSaysSoInTheJobROW is the structural-zero line, asserted where
// an operator actually reads it: import_jobs.warnings, not the in-process ImportResult. #80's
// column is the only channel this reaches, and a clean-looking job row is exactly what made every
// earlier instance of this defect invisible.
func TestJobRow_LinearCSV_ADatelessExportSaysSoInTheJobROW(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	wsID := runLinearCSVDatesImport(t, d,
		"ID,Title,Description,Status,Priority,Assignee,Labels\nLIN-1,No dates at all,d,Done,High,,\n")

	var status string
	var imported int
	var warnings []string
	if err := d.Pool.QueryRow(ctx,
		`SELECT status, imported, warnings FROM import_jobs WHERE workspace_id = $1`, wsID,
	).Scan(&status, &imported, &warnings); err != nil {
		t.Fatalf("read job row: %v", err)
	}
	if imported != 1 {
		t.Fatalf("imported = %d, want 1 — the fixture is not exercising the report", imported)
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w, `no "Created" column in this export`) {
			found = true
		}
	}
	if !found {
		t.Errorf("job row for a dateless Linear export: status=%q imported=%d warnings=%v\n"+
			"Want a warning naming the absent Created column. Without it the row reads "+
			"{succeeded, imported:1, warnings:[]} while every issue records as opened at import time.",
			status, imported, warnings)
	}
}
