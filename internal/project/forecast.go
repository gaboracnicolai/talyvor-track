package project

import (
	"context"
	"fmt"
	"time"

	"github.com/talyvor/track/internal/forecast"
)

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
		projectIDs, now, forecast.HistoryWeeks,
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
		f := forecast.Build(id, p.IssueCount-p.CompletedCount, start, ago, now)
		p.Forecast = &f
	}
	return rows.Err()
}
