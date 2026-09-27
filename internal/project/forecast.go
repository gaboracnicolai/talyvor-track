package project

import (
	"context"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"slices"
	"time"
)

// Forecast is when a project's open work is likely to finish, projected from the project's own
// finished work. It is a throughput forecast: the weekly count of issues the project finished over
// its recent history is resampled (Monte Carlo) until the open issues run out, and the week in
// which half the trials — and 85% of them — had finished becomes the likely and the safe date.
//
// Status says which of those a caller can show:
//   - "forecast":     Likely and Safe are set.
//   - "nothing_open": no open issues, so there is nothing left to forecast.
//   - "no_history":   open issues, but nothing finished in the history window to project from.
//   - "too_far":      at the current pace the likely date is more than ForecastMaxWeeks away.
type Forecast struct {
	Status            string     `json:"status"`
	Remaining         int        `json:"remaining"`
	HistoryWeeks      int        `json:"history_weeks"`
	FinishedInHistory int        `json:"finished_in_history"`
	Likely            *time.Time `json:"likely,omitempty"`
	Safe              *time.Time `json:"safe,omitempty"`
}

const (
	// ForecastHistoryWeeks is how far back a project's pace is read. A younger project reads
	// only the weeks since its history began, so empty weeks before it existed do not slow it.
	ForecastHistoryWeeks = 12
	// ForecastMaxWeeks caps a trial; a pace that needs longer is reported as "too_far".
	ForecastMaxWeeks = 260
	forecastTrials   = 1000
)

// simulateWeeks resamples weekly (the finished-issue count of each history week) until remaining
// issues are done, forecastTrials times, and returns the 50th and 85th percentile week counts. The
// generator is seeded by the caller so the same project and history give the same answer on every
// page load. A trial that has not finished by ForecastMaxWeeks counts as ForecastMaxWeeks+1.
func simulateWeeks(remaining int, weekly []int, seed uint64) (p50, p85 int) {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	results := make([]int, forecastTrials)
	for t := range results {
		left, weeks := remaining, 0
		for left > 0 && weeks <= ForecastMaxWeeks {
			left -= weekly[rng.IntN(len(weekly))]
			weeks++
		}
		if left > 0 {
			weeks = ForecastMaxWeeks + 1
		}
		results[t] = weeks
	}
	slices.Sort(results)
	return results[forecastTrials*50/100], results[forecastTrials*85/100]
}

// buildForecast turns a project's open count and its completion history into a Forecast.
// weeksAgo holds, for each issue finished in the window, how many whole weeks before now it
// finished (0 = in the last seven days).
func buildForecast(projectID string, remaining int, historyStart *time.Time, weeksAgo []int, now time.Time) Forecast {
	f := Forecast{Remaining: remaining}
	if remaining <= 0 {
		f.Status = "nothing_open"
		return f
	}

	weeks := ForecastHistoryWeeks
	if historyStart != nil {
		age := now.Sub(*historyStart)
		weeks = int(math.Ceil(age.Hours() / (24 * 7)))
		weeks = max(1, min(weeks, ForecastHistoryWeeks))
	}
	for _, w := range weeksAgo {
		if w >= weeks && w < ForecastHistoryWeeks {
			weeks = w + 1
		}
	}
	weekly := make([]int, weeks)
	for _, w := range weeksAgo {
		if w >= 0 && w < weeks {
			weekly[w]++
			f.FinishedInHistory++
		}
	}
	f.HistoryWeeks = weeks
	if f.FinishedInHistory == 0 {
		f.Status = "no_history"
		return f
	}

	h := fnv.New64a()
	fmt.Fprintf(h, "%s|%d|%v", projectID, remaining, weekly)
	p50, p85 := simulateWeeks(remaining, weekly, h.Sum64())
	if p50 > ForecastMaxWeeks {
		f.Status = "too_far"
		return f
	}
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	likely := day.AddDate(0, 0, 7*p50)
	f.Likely = &likely
	if p85 <= ForecastMaxWeeks {
		safe := day.AddDate(0, 0, 7*p85)
		f.Safe = &safe
	}
	f.Status = "forecast"
	return f
}

// attachForecasts reads the completion history of every project in out with one query and sets
// each project's Forecast. A project's history begins at the earlier of its own creation and its
// oldest issue's, so imported work (which keeps its original dates) counts from when it began.
func (s *Store) attachForecasts(ctx context.Context, out []RoadmapProject, projectIDs []string, now time.Time) error {
	rows, err := s.pool.Query(ctx,
		`SELECT p.id,
                LEAST(p.created_at, MIN(i.created_at)) AS history_start,
                COALESCE(ARRAY_AGG(FLOOR(EXTRACT(EPOCH FROM ($2 - i.completed_at)) / 604800)::int)
                    FILTER (WHERE i.status = 'done'
                              AND i.completed_at > $2 - make_interval(weeks => $3)
                              AND i.completed_at <= $2), '{}') AS weeks_ago
            FROM projects p
            LEFT JOIN issues i ON i.project_id = p.id
            WHERE p.id = ANY($1)
            GROUP BY p.id`,
		projectIDs, now, ForecastHistoryWeeks,
	)
	if err != nil {
		return fmt.Errorf("project: roadmap forecast: %w", err)
	}
	defer rows.Close()

	byProject := map[string]int{}
	for i, p := range out {
		byProject[p.ID] = i
	}
	for rows.Next() {
		var (
			id       string
			start    *time.Time
			weeksAgo []int32
		)
		if err := rows.Scan(&id, &start, &weeksAgo); err != nil {
			return err
		}
		idx, ok := byProject[id]
		if !ok {
			continue
		}
		ago := make([]int, len(weeksAgo))
		for i, w := range weeksAgo {
			ago[i] = int(w)
		}
		p := &out[idx]
		f := buildForecast(id, p.IssueCount-p.CompletedCount, start, ago, now)
		p.Forecast = &f
	}
	return rows.Err()
}
