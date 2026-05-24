package project

import (
	"context"
	"fmt"
	"time"

	"github.com/talyvor/track/internal/milestone"
	"github.com/talyvor/track/internal/model"
)

// RoadmapProject is one row on the Roadmap timeline: the project
// metadata, its team name, milestone rollup, and the AI cost
// aggregate that's the entire reason a CFO might pick Track over
// Linear for product planning.
type RoadmapProject struct {
	model.Project
	TeamName       string             `json:"team_name"`
	Milestones     []RoadmapMilestone `json:"milestones"`
	IssueCount     int                `json:"issue_count"`
	CompletedCount int                `json:"completed_count"`
	CompletionPct  float64            `json:"completion_pct"`
	AICostUSD      float64            `json:"ai_cost_usd"`
}

// RoadmapMilestone is one diamond on the timeline. Embeds the
// canonical Milestone (defined in internal/milestone) so any field
// added there flows here automatically.
type RoadmapMilestone struct {
	milestone.Milestone
	IssueCount     int     `json:"issue_count"`
	CompletedCount int     `json:"completed_count"`
	AICostUSD      float64 `json:"ai_cost_usd"`
}

// GetRoadmap returns every project whose [start_date, target_date]
// overlaps [start, end] (or has no dates set — see "unscheduled"
// fallback below) along with its milestones. Two queries total: one
// for project rollups, one for milestones across every returned
// project. No N+1 even at large workspace sizes.
//
// teamID nil means "any team in the workspace"; a non-nil value adds
// the AND p.team_id = $4 clause and shifts the args list.
func (s *Store) GetRoadmap(ctx context.Context, workspaceID string, teamID *string, start, end time.Time) ([]RoadmapProject, error) {
	if s.pool == nil {
		return nil, nil
	}

	// Date-range overlap predicate: a project is in range when its
	// target_date is on/after the window start OR its start_date is
	// on/before the window end. Projects with both dates NULL match
	// neither half — they appear in the dedicated "unscheduled"
	// section the frontend renders separately.
	args := []any{workspaceID, start, end}
	teamClause := ""
	if teamID != nil {
		teamClause = " AND p.team_id = $4"
		args = append(args, *teamID)
	}

	sql := `SELECT p.id, p.workspace_id, p.team_id, p.name, p.identifier, p.description,
                p.status, p.priority, p.start_date, p.target_date, p.created_at, p.updated_at,
                t.name AS team_name,
                COUNT(i.id)                                                AS issue_count,
                COUNT(i.id) FILTER (WHERE i.status IN ('done','cancelled')) AS completed_count,
                COALESCE(SUM(i.ai_cost_usd), 0)                            AS ai_cost_usd
            FROM projects p
            JOIN teams t ON t.id = p.team_id
            LEFT JOIN issues i ON i.project_id = p.id
            WHERE p.workspace_id = $1
              AND (p.target_date >= $2 OR p.start_date <= $3 OR (p.start_date IS NULL AND p.target_date IS NULL))` +
		teamClause + `
            GROUP BY p.id, t.name
            ORDER BY p.start_date ASC NULLS LAST, p.created_at ASC`

	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("project: roadmap: %w", err)
	}
	defer rows.Close()

	var (
		out        []RoadmapProject
		projectIDs []string
	)
	for rows.Next() {
		var (
			rp        RoadmapProject
			issues    int64
			completed int64
		)
		if err := rows.Scan(
			&rp.ID, &rp.WorkspaceID, &rp.TeamID, &rp.Name, &rp.Identifier, &rp.Description,
			&rp.Status, &rp.Priority, &rp.StartDate, &rp.TargetDate, &rp.CreatedAt, &rp.UpdatedAt,
			&rp.TeamName, &issues, &completed, &rp.AICostUSD,
		); err != nil {
			return nil, err
		}
		rp.IssueCount = int(issues)
		rp.CompletedCount = int(completed)
		if rp.IssueCount > 0 {
			rp.CompletionPct = (float64(rp.CompletedCount) / float64(rp.IssueCount)) * 100.0
		}
		rp.Milestones = []RoadmapMilestone{}
		out = append(out, rp)
		projectIDs = append(projectIDs, rp.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(projectIDs) == 0 {
		return []RoadmapProject{}, nil
	}

	// Milestone rollup. Same shape as the project query — counts and
	// AI spend joined from the issues table via milestone_id. One
	// pass through the result set; we bucket into the right project
	// using a map keyed by project_id.
	mrows, err := s.pool.Query(ctx,
		`SELECT m.id, m.workspace_id, m.project_id, m.name, m.description, m.status,
                m.target_date, m.completed_at, m.created_at, m.updated_at,
                COUNT(i.id)                                                AS issue_count,
                COUNT(i.id) FILTER (WHERE i.status IN ('done','cancelled')) AS completed_count,
                COALESCE(SUM(i.ai_cost_usd), 0)                            AS ai_cost_usd
            FROM milestones m
            LEFT JOIN issues i ON i.milestone_id = m.id
            WHERE m.project_id = ANY($1)
            GROUP BY m.id
            ORDER BY m.target_date ASC NULLS LAST, m.created_at ASC`,
		projectIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("project: roadmap milestones: %w", err)
	}
	defer mrows.Close()

	byProject := map[string]int{}
	for i, p := range out {
		byProject[p.ID] = i
	}
	for mrows.Next() {
		var (
			rm        RoadmapMilestone
			issues    int64
			completed int64
		)
		if err := mrows.Scan(
			&rm.ID, &rm.WorkspaceID, &rm.ProjectID, &rm.Name, &rm.Description, &rm.Status,
			&rm.TargetDate, &rm.CompletedAt, &rm.CreatedAt, &rm.UpdatedAt,
			&issues, &completed, &rm.AICostUSD,
		); err != nil {
			return nil, err
		}
		rm.IssueCount = int(issues)
		rm.CompletedCount = int(completed)
		if idx, ok := byProject[rm.ProjectID]; ok {
			out[idx].Milestones = append(out[idx].Milestones, rm)
		}
	}
	return out, mrows.Err()
}
