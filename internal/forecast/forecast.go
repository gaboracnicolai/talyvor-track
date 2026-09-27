// Package forecast projects when a body of open work is likely to finish from the pace at which
// the same people finished work before. The Roadmap forecasts a project from its own history; a
// cycle is forecast from its team's.
package forecast

import (
	"fmt"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"slices"
	"time"
)

// Forecast is when open work is likely to finish. It is a throughput forecast: the weekly count
// of issues finished over the recent history is resampled (Monte Carlo) until the open issues run
// out, and the week in which half the trials — and 85% of them — had finished becomes the likely
// and the safe date.
//
// Status says which of those a caller can show:
//   - "forecast":     Likely and Safe are set.
//   - "nothing_open": no open issues, so there is nothing left to forecast.
//   - "no_history":   open issues, but nothing finished in the history window to project from.
//   - "too_far":      at the current pace the likely date is more than MaxWeeks away.
type Forecast struct {
	Status            string     `json:"status"`
	Remaining         int        `json:"remaining"`
	HistoryWeeks      int        `json:"history_weeks"`
	FinishedInHistory int        `json:"finished_in_history"`
	Likely            *time.Time `json:"likely,omitempty"`
	Safe              *time.Time `json:"safe,omitempty"`
}

const (
	// HistoryWeeks is how far back a pace is read. Younger work reads only the weeks since its
	// history began, so empty weeks before it existed do not slow it.
	HistoryWeeks = 12
	// MaxWeeks caps a trial; a pace that needs longer is reported as "too_far".
	MaxWeeks       = 260
	forecastTrials = 1000
)

// simulateWeeks resamples weekly (the finished-issue count of each history week) until remaining
// issues are done, forecastTrials times, and returns the 50th and 85th percentile week counts. The
// generator is seeded by the caller so the same work and history give the same answer on every
// page load. A trial that has not finished by MaxWeeks counts as MaxWeeks+1.
func simulateWeeks(remaining int, weekly []int, seed uint64) (p50, p85 int) {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	results := make([]int, forecastTrials)
	for t := range results {
		left, weeks := remaining, 0
		for left > 0 && weeks <= MaxWeeks {
			left -= weekly[rng.IntN(len(weekly))]
			weeks++
		}
		if left > 0 {
			weeks = MaxWeeks + 1
		}
		results[t] = weeks
	}
	slices.Sort(results)
	return results[forecastTrials*50/100], results[forecastTrials*85/100]
}

// Build turns an open-issue count and the completion history behind it into a Forecast. key
// names the work (a project or cycle ID) and seeds the simulation. weeksAgo holds, for each issue
// finished in the window, how many whole weeks before now it finished (0 = in the last seven
// days); historyStart is when that history began, nil meaning the whole window.
func Build(key string, remaining int, historyStart *time.Time, weeksAgo []int, now time.Time) Forecast {
	f := Forecast{Remaining: remaining}
	if remaining <= 0 {
		f.Status = "nothing_open"
		return f
	}

	weeks := HistoryWeeks
	if historyStart != nil {
		age := now.Sub(*historyStart)
		weeks = int(math.Ceil(age.Hours() / (24 * 7)))
		weeks = max(1, min(weeks, HistoryWeeks))
	}
	for _, w := range weeksAgo {
		if w >= weeks && w < HistoryWeeks {
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
	fmt.Fprintf(h, "%s|%d|%v", key, remaining, weekly)
	p50, p85 := simulateWeeks(remaining, weekly, h.Sum64())
	if p50 > MaxWeeks {
		f.Status = "too_far"
		return f
	}
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	likely := day.AddDate(0, 0, 7*p50)
	f.Likely = &likely
	if p85 <= MaxWeeks {
		safe := day.AddDate(0, 0, 7*p85)
		f.Safe = &safe
	}
	f.Status = "forecast"
	return f
}
