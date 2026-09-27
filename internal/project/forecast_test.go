package project

import (
	"testing"
	"time"
)

// A project that has finished exactly five issues every week for twelve weeks, with twenty open,
// finishes in four weeks in every trial — so the likely and the safe date are both four weeks out.
// A steady pace is the one history whose answer is known without the simulation, which is what
// lets this pin the arithmetic (weeks → date) rather than a random draw.
func TestForecast_ASteadyPaceFinishesOnTheArithmeticDate(t *testing.T) {
	now := time.Date(2026, 9, 28, 15, 0, 0, 0, time.UTC)
	start := now.AddDate(0, -6, 0)
	var weeksAgo []int
	for w := 0; w < ForecastHistoryWeeks; w++ {
		for range 5 {
			weeksAgo = append(weeksAgo, w)
		}
	}

	f := buildForecast("p1", 20, &start, weeksAgo, now)

	if f.Status != "forecast" || f.HistoryWeeks != 12 || f.FinishedInHistory != 60 {
		t.Fatalf("got status=%q history_weeks=%d finished=%d, want forecast/12/60", f.Status, f.HistoryWeeks, f.FinishedInHistory)
	}
	want := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	if f.Likely == nil || !f.Likely.Equal(want) || f.Safe == nil || !f.Safe.Equal(want) {
		t.Fatalf("likely=%v safe=%v, want both %v", f.Likely, f.Safe, want)
	}
}

// An uneven pace spreads the trials, so the safe (85%) date must land on or after the likely one.
func TestForecast_AnUnevenPaceGivesASafeDateNoEarlierThanTheLikelyOne(t *testing.T) {
	now := time.Date(2026, 9, 28, 15, 0, 0, 0, time.UTC)
	start := now.AddDate(0, -6, 0)
	weeksAgo := []int{0, 0, 0, 0, 0, 0, 2, 5, 5, 9}

	f := buildForecast("p2", 30, &start, weeksAgo, now)

	if f.Status != "forecast" || f.Likely == nil || f.Safe == nil {
		t.Fatalf("got %+v, want a forecast with both dates", f)
	}
	if f.Safe.Before(*f.Likely) || !f.Likely.After(now) {
		t.Fatalf("likely=%v safe=%v: want now < likely <= safe", f.Likely, f.Safe)
	}
}
