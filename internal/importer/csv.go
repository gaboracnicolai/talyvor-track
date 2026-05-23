// Package importer parses Linear and Jira CSV exports into Track
// issues. It is intentionally tolerant: malformed rows are counted
// and reported but never crash the import, and the partial result
// is always returned so the caller can show progress.
//
// The importer is decoupled from issue.Store via a small local
// interface so unit tests can plug a fake without touching the
// database. main.go injects the real *issue.Store at boot.
package importer

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/talyvor/track/internal/model"
)

// issueCreator is the subset of issue.Store the importer uses. Keeps
// the importer testable and the issue package's public surface
// unchanged.
type issueCreator interface {
	Create(ctx context.Context, i model.Issue) (*model.Issue, error)
}

type Importer struct {
	issues issueCreator
}

func New(issues issueCreator) *Importer { return &Importer{issues: issues} }

// ImportResult is the per-call summary returned to API callers. The
// JSON shape is part of the public API contract — don't rename
// fields without a coordinated client change.
type ImportResult struct {
	Imported int      `json:"imported"`
	Skipped  int      `json:"skipped"`
	Errors   []string `json:"errors"`
}

// columnIndex maps a header name to its index in a CSV row. Built
// once per file so per-row lookup is O(1). Unknown columns are
// silently ignored — exports often carry extra Linear/Jira fields
// (cycle name, estimate, etc.) we don't map yet.
type columnIndex map[string]int

func buildIndex(header []string) columnIndex {
	out := make(columnIndex, len(header))
	for i, h := range header {
		out[strings.TrimSpace(strings.ToLower(h))] = i
	}
	return out
}

// get safely fetches a column by lowercased name. Returns "" if the
// column doesn't exist or the row is too short — that lets row-level
// validation focus on what's required (title) rather than how the
// export was shaped.
func (ci columnIndex) get(row []string, key string) string {
	idx, ok := ci[strings.ToLower(key)]
	if !ok || idx >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[idx])
}

// ─── Linear ─────────────────────────────────────────────────

// ImportLinearCSV parses a Linear "Export data" CSV. Linear's header
// is well-known but column order may shift between exports, so we
// look up each field by name.
//
// Status mapping:
//
//	Backlog → backlog · Todo → todo · In Progress → in_progress
//	Done → done · Cancelled → cancelled
//
// Priority mapping:
//
//	Urgent → 1 · High → 2 · Medium → 3 · Low → 4 · No priority → 0
func (imp *Importer) ImportLinearCSV(ctx context.Context, workspaceID, teamID string, r io.Reader) (*ImportResult, error) {
	return imp.run(ctx, workspaceID, teamID, r, linearRowMapper)
}

func linearRowMapper(ci columnIndex, row []string) (model.Issue, error) {
	title := ci.get(row, "Title")
	if title == "" {
		return model.Issue{}, errEmptyTitle
	}
	return model.Issue{
		Title:       title,
		Description: ci.get(row, "Description"),
		Status:      mapLinearStatus(ci.get(row, "Status")),
		Priority:    mapLinearPriority(ci.get(row, "Priority")),
		Labels:      splitLabels(ci.get(row, "Labels")),
	}, nil
}

func mapLinearStatus(s string) model.IssueStatus {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "backlog":
		return model.StatusBacklog
	case "todo", "to do":
		return model.StatusTodo
	case "in progress", "in_progress":
		return model.StatusInProgress
	case "in review", "in_review":
		return model.StatusInReview
	case "done", "completed":
		return model.StatusDone
	case "cancelled", "canceled":
		return model.StatusCancelled
	default:
		return model.StatusBacklog
	}
}

func mapLinearPriority(p string) model.IssuePriority {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "urgent":
		return model.PriorityUrgent
	case "high":
		return model.PriorityHigh
	case "medium":
		return model.PriorityMedium
	case "low":
		return model.PriorityLow
	default:
		return model.PriorityNone
	}
}

// ─── Jira ───────────────────────────────────────────────────

// ImportJiraCSV parses a Jira CSV export. Jira exports use different
// column names ("Issue Key", "Summary", "Resolved") and a different
// priority vocabulary ("Highest", "Lowest", "Major", "Trivial") that
// we collapse onto the Track 5-level scale.
//
// Status mapping:
//
//	To Do / Backlog → todo/backlog
//	In Progress → in_progress · In Review → in_review
//	Done / Closed / Resolved → done
//
// Priority mapping:
//
//	Highest → 1 (urgent) · High / Major → 2 (high)
//	Medium → 3 · Low → 4 · Lowest / Trivial → 4 · other → 0
func (imp *Importer) ImportJiraCSV(ctx context.Context, workspaceID, teamID string, r io.Reader) (*ImportResult, error) {
	return imp.run(ctx, workspaceID, teamID, r, jiraRowMapper)
}

func jiraRowMapper(ci columnIndex, row []string) (model.Issue, error) {
	title := ci.get(row, "Summary")
	if title == "" {
		// Some Jira exports use "Title" as the summary column header
		// — fall back so we don't reject otherwise-valid rows.
		title = ci.get(row, "Title")
	}
	if title == "" {
		return model.Issue{}, errEmptyTitle
	}
	return model.Issue{
		Title:       title,
		Description: ci.get(row, "Description"),
		Status:      mapJiraStatus(ci.get(row, "Status")),
		Priority:    mapJiraPriority(ci.get(row, "Priority")),
		Labels:      splitLabels(ci.get(row, "Labels")),
	}, nil
}

func mapJiraStatus(s string) model.IssueStatus {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "backlog":
		return model.StatusBacklog
	case "to do", "todo", "open", "reopened":
		return model.StatusTodo
	case "in progress":
		return model.StatusInProgress
	case "in review", "code review":
		return model.StatusInReview
	case "done", "closed", "resolved":
		return model.StatusDone
	case "cancelled", "canceled", "won't do", "won't fix":
		return model.StatusCancelled
	default:
		return model.StatusBacklog
	}
}

func mapJiraPriority(p string) model.IssuePriority {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "highest", "blocker", "critical":
		return model.PriorityUrgent
	case "high", "major":
		return model.PriorityHigh
	case "medium":
		return model.PriorityMedium
	case "low":
		return model.PriorityLow
	case "lowest", "trivial", "minor":
		return model.PriorityLow
	default:
		return model.PriorityNone
	}
}

// ─── shared driver ──────────────────────────────────────────

var errEmptyTitle = errors.New("row has no title; skipping")

type rowMapper func(columnIndex, []string) (model.Issue, error)

// run is the per-file pipeline shared by both Linear and Jira import.
// One CSV reader with FieldsPerRecord=-1 (allow variable column count)
// — that way a single malformed mid-stream row is counted in Skipped
// instead of aborting the whole batch.
func (imp *Importer) run(ctx context.Context, workspaceID, teamID string, r io.Reader, mapper rowMapper) (*ImportResult, error) {
	if workspaceID == "" || teamID == "" {
		return nil, errors.New("importer: workspace_id and team_id are required")
	}

	rd := csv.NewReader(r)
	rd.FieldsPerRecord = -1
	rd.TrimLeadingSpace = true

	header, err := rd.Read()
	if errors.Is(err, io.EOF) {
		return &ImportResult{Errors: []string{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("importer: read header: %w", err)
	}
	ci := buildIndex(header)
	expectedCols := len(header)

	out := &ImportResult{Errors: []string{}}
	rowNum := 1 // header was row 1
	for {
		row, err := rd.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		rowNum++
		if err != nil {
			out.Skipped++
			out.Errors = append(out.Errors, fmt.Sprintf("row %d: %v", rowNum, err))
			continue
		}
		// Catch raggedly-short rows that csv.Read tolerates because of
		// FieldsPerRecord=-1. The expected column count is fixed by
		// the header; anything shorter is malformed.
		if len(row) < expectedCols {
			out.Skipped++
			out.Errors = append(out.Errors, fmt.Sprintf("row %d: expected %d columns, got %d", rowNum, expectedCols, len(row)))
			continue
		}

		issueModel, err := mapper(ci, row)
		if err != nil {
			out.Skipped++
			out.Errors = append(out.Errors, fmt.Sprintf("row %d: %v", rowNum, err))
			continue
		}
		issueModel.WorkspaceID = workspaceID
		issueModel.TeamID = teamID
		issueModel.CreatorID = "importer"

		if _, err := imp.issues.Create(ctx, issueModel); err != nil {
			out.Skipped++
			out.Errors = append(out.Errors, fmt.Sprintf("row %d: create: %v", rowNum, err))
			continue
		}
		out.Imported++
	}
	return out, nil
}

// splitLabels turns Linear/Jira's comma-separated label string into a
// trimmed slice. Returns an empty (non-nil) slice for empty input so
// downstream JSON encodes `[]`.
func splitLabels(s string) []string {
	if s == "" {
		return []string{}
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		t := strings.TrimSpace(p)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}
