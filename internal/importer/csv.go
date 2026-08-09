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
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/talyvor/track/internal/model"
)

// issueCreator is the subset of issue.Store the importer uses. Keeps
// the importer testable and the issue package's public surface
// unchanged.
type issueCreator interface {
	Create(ctx context.Context, i model.Issue) (*model.Issue, error)
}

// issueUpserter is the C.2 re-import write path. A source that supplies an Identifier (the API providers,
// C.3) routes through it; the CSV path (no Identifier) still uses Create. Optional — a store that lacks it
// (the CSV-only fake) simply never triggers the upsert branch.
type issueUpserter interface {
	UpsertByIdentifier(ctx context.Context, i model.Issue) (*model.Issue, bool, error)
}

type Importer struct {
	issues   issueCreator
	upserter issueUpserter // set iff the backing store supports upsert; drives the API re-import path
}

// New keeps its issueCreator-only signature (CSV callers + tests unchanged) and auto-detects upsert support:
// the real issue.Store implements UpsertByIdentifier, a CSV-only fake does not.
func New(issues issueCreator) *Importer {
	imp := &Importer{issues: issues}
	if up, ok := issues.(issueUpserter); ok {
		imp.upserter = up
	}
	return imp
}

// ImportResult is the per-call summary returned to API callers. The
// JSON shape is part of the public API contract — don't rename
// fields without a coordinated client change.
//
// Warnings covers rows that DID import but with a field the mapper could not place on Track's
// scale. That is a different outcome from Skipped (a row that never landed) and it used to be
// invisible: an issue in a status Track does not know became `backlog` and the caller was told
// {imported:N, skipped:0, errors:[]}. Measured on 014b6e2 — 11 of 22 realistic Jira statuses and
// 7 of 13 Linear states fell through, "Deployed" and Linear's own default "Duplicate" among them.
type ImportResult struct {
	Imported int      `json:"imported"`
	Skipped  int      `json:"skipped"`
	Errors   []string `json:"errors"`
	Warnings []string `json:"warnings"`
}

// FieldNote records ONE provider value a mapper could not place. The pipeline COUNTS these
// (map[FieldNote]int) rather than accumulating a string per row: a 10,000-row import of one
// unknown status must produce one warning, not ten thousand.
type FieldNote struct {
	Field  string // "status" | "priority"
	Value  string // the provider's value, verbatim — "" means the source supplied none
	Mapped string // the Track value it was given instead, so the warning is self-describing

	// Via / ViaValue / ViaResolved record HOW Mapped was reached once the provider's NAME turned
	// out to be unrecognised. They are not decoration and they are not for debugging: a Jira tenant
	// that never sends a statusCategory and a Jira tenant whose categories resolved every row would
	// otherwise produce the IDENTICAL warning, and nobody could ever tell from a production import
	// whether the category read runs at all. That is the structural-zero class, one field down.
	//
	//	Via         "" (no fallback exists for this transport — every CSV path)
	//	            | viaCategory        — a statusCategory arrived; ViaValue is its key, verbatim
	//	            | viaNoCategory      — none arrived
	//	ViaResolved true iff ViaValue is what produced Mapped
	Via         string
	ViaValue    string
	ViaResolved bool
}

const (
	viaCategory   = "statusCategory"
	viaNoCategory = "no-statusCategory"
)

// render turns one note and its count into a single self-describing line. The three Via shapes are
// three DIFFERENT sentences on purpose — see FieldNote.
func (n FieldNote) render(count int) string {
	subject := fmt.Sprintf("unrecognised %s %q on %d issue(s)", n.Field, n.Value, count)
	if n.Value == "" {
		// An absent value is a different report from an unknown one: it usually means we looked in
		// the wrong place (a CSV status column named "State"), not that the provider is exotic.
		subject = fmt.Sprintf("no %s value on %d issue(s)", n.Field, count)
	}
	switch {
	case n.Via == viaCategory && n.ViaResolved:
		return fmt.Sprintf("%s — resolved via statusCategory %q as %q", subject, n.ViaValue, n.Mapped)
	case n.Via == viaCategory:
		return fmt.Sprintf("%s — statusCategory %q carries no Track status, imported as %q", subject, n.ViaValue, n.Mapped)
	case n.Via == viaNoCategory:
		return fmt.Sprintf("%s — no statusCategory present, imported as %q", subject, n.Mapped)
	default:
		return fmt.Sprintf("%s — imported as %q", subject, n.Mapped)
	}
}

// statusFallback describes the second chance a transport gave an unrecognised status NAME. Its zero
// value means "this transport has none", which is every CSV path — a Jira CSV export carries no
// category column, so those warnings keep their existing wording exactly.
type statusFallback struct {
	via      string
	value    string
	resolved bool
}

// mappedIssue pairs a mapped issue with the notes its mapping produced. The API sources map a
// whole page at a time, so the notes have to travel next to the issue rather than be returned
// separately — otherwise they cannot be attributed to a row.
type mappedIssue struct {
	issue model.Issue
	notes []FieldNote
}

// statusNote / priorityNote build the note for a value a mapper rejected. Kept next to
// ImportResult so the vocabulary ("status", "priority") has one definition.
func statusNote(raw string, mapped model.IssueStatus) FieldNote {
	return FieldNote{Field: "status", Value: raw, Mapped: string(mapped)}
}

func priorityNote(raw string, mapped model.IssuePriority) FieldNote {
	return FieldNote{Field: "priority", Value: raw, Mapped: strconv.Itoa(int(mapped))}
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
	src, err := newCSVSource(r, linearRowMapper)
	if err != nil {
		return nil, err
	}
	return imp.run(ctx, workspaceID, teamID, src)
}

func linearRowMapper(ci columnIndex, row []string) (mappedIssue, error) {
	title := ci.get(row, "Title")
	if title == "" {
		return mappedIssue{}, errEmptyTitle
	}
	rawStatus, rawPrio := ci.get(row, "Status"), ci.get(row, "Priority")
	status, statusOK := mapLinearStatus(rawStatus)
	prio, prioOK := mapLinearPriority(rawPrio)
	return mappedIssue{
		issue: model.Issue{
			Title:       title,
			Description: ci.get(row, "Description"),
			Status:      status,
			Priority:    prio,
			Labels:      splitLabels(ci.get(row, "Labels")),
		},
		notes: collectNotes(rawStatus, status, statusOK, statusFallback{}, rawPrio, prio, prioOK),
	}, nil
}

// collectNotes is the one place a (value, recognised) pair becomes a reportable note, so the two
// providers and the two transports cannot drift on what counts as degraded.
func collectNotes(rawStatus string, status model.IssueStatus, statusOK bool, fb statusFallback, rawPrio string, prio model.IssuePriority, prioOK bool) []FieldNote {
	var notes []FieldNote
	if !statusOK {
		n := statusNote(rawStatus, status)
		n.Via, n.ViaValue, n.ViaResolved = fb.via, fb.value, fb.resolved
		notes = append(notes, n)
	}
	if !prioOK {
		notes = append(notes, priorityNote(rawPrio, prio))
	}
	return notes
}

// The mappers below return (value, recognised). The VALUE is unchanged from before this change —
// an unknown status is still imported as backlog, because inventing a meaning for "Deployed"
// needs the provider's canonical state category and that is a separate, riskier change. What is
// new is that they no longer claim the fallback was a mapping.
//
// ⚠ THE EMPTY STRING IS ASYMMETRIC, ON PURPOSE. An absent PRIORITY is a real value: both
// providers model "no priority", so "" ⇒ PriorityNone is a mapping, not a failure. An absent
// STATUS is not: every Linear and Jira issue has one, so an empty one means we did not find it —
// which is exactly what happens when a CSV's status column is named something else, silently
// importing every row as backlog.
func mapLinearStatus(s string) (model.IssueStatus, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "backlog":
		return model.StatusBacklog, true
	case "todo", "to do":
		return model.StatusTodo, true
	case "in progress", "in_progress":
		return model.StatusInProgress, true
	case "in review", "in_review":
		return model.StatusInReview, true
	case "done", "completed":
		return model.StatusDone, true
	case "cancelled", "canceled":
		return model.StatusCancelled, true
	default:
		return model.StatusBacklog, false
	}
}

func mapLinearPriority(p string) (model.IssuePriority, bool) {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "urgent":
		return model.PriorityUrgent, true
	case "high":
		return model.PriorityHigh, true
	case "medium":
		return model.PriorityMedium, true
	case "low":
		return model.PriorityLow, true
	case "", "none", "no priority":
		return model.PriorityNone, true
	default:
		return model.PriorityNone, false
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
	src, err := newCSVSource(r, jiraRowMapper)
	if err != nil {
		return nil, err
	}
	return imp.run(ctx, workspaceID, teamID, src)
}

func jiraRowMapper(ci columnIndex, row []string) (mappedIssue, error) {
	title := ci.get(row, "Summary")
	if title == "" {
		// Some Jira exports use "Title" as the summary column header
		// — fall back so we don't reject otherwise-valid rows.
		title = ci.get(row, "Title")
	}
	if title == "" {
		return mappedIssue{}, errEmptyTitle
	}
	rawStatus, rawPrio := ci.get(row, "Status"), ci.get(row, "Priority")
	status, statusOK := mapJiraStatus(rawStatus)
	prio, prioOK := mapJiraPriority(rawPrio)
	return mappedIssue{
		issue: model.Issue{
			Title:       title,
			Description: ci.get(row, "Description"),
			Status:      status,
			Priority:    prio,
			Labels:      splitLabels(ci.get(row, "Labels")),
		},
		notes: collectNotes(rawStatus, status, statusOK, statusFallback{}, rawPrio, prio, prioOK),
	}, nil
}

func mapJiraStatus(s string) (model.IssueStatus, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "backlog":
		return model.StatusBacklog, true
	case "to do", "todo", "open", "reopened":
		return model.StatusTodo, true
	case "in progress":
		return model.StatusInProgress, true
	case "in review", "code review":
		return model.StatusInReview, true
	case "done", "closed", "resolved":
		return model.StatusDone, true
	case "cancelled", "canceled", "won't do", "won't fix":
		return model.StatusCancelled, true
	default:
		return model.StatusBacklog, false
	}
}

// mapJiraStatusCategory places Jira's CANONICAL, non-renameable state category. It is the second
// chance an unrecognised status NAME gets, and only the Jira API transport can offer it.
//
// MEASURED against a real Jira (jira.atlassian.com, anonymous REST, 2026-08-09), controlled first
// against a fabricated host that answered 404:
//
//	GET /rest/api/2/statuscategory ⇒ EXACTLY FOUR, which is the whole vocabulary below:
//	  id=1 key="undefined" (No Category) · id=2 "new" · id=4 "indeterminate" · id=3 "done"
//	GET /rest/api/2/search?fields=status ⇒ the category is NESTED INSIDE the status object, so it
//	  arrives with the field jiraFields already asks for: no query change, nothing that can 400.
//
// That instance defines 46 statuses; mapJiraStatus knows 9. The other 37 all import as `backlog`
// today — 13 in `indeterminate` (in flight) and 4 in `done` (finished). This resolves 37 of 37, and
// disagrees with the 9 known names ZERO times, which is why the name mapping still runs first.
//
// ⚠ `undefined` IS NOT A RESOLUTION. It is Jira's literal "No Category" — Jira saying it does not
// know either. Giving it a Track status would invent precisely the meaning this whole change exists
// to stop inventing, so it falls through and is REPORTED as arrived-and-unusable.
//
// ⚠ `done` IS COARSER THAN THE NAME MAPPING AND THAT IS NOT HIDDEN: Jira files "Won't Do" under
// `done` too, so an unknown cancellation-shaped name resolves to done, not cancelled. Every
// resolution carries a warning naming the category it came from, so the coarsening is on the report.
func mapJiraStatusCategory(key string) (model.IssueStatus, bool) {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "new":
		return model.StatusTodo, true
	case "indeterminate":
		return model.StatusInProgress, true
	case "done":
		return model.StatusDone, true
	default:
		return model.StatusBacklog, false
	}
}

func mapJiraPriority(p string) (model.IssuePriority, bool) {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "highest", "blocker", "critical":
		return model.PriorityUrgent, true
	case "high", "major":
		return model.PriorityHigh, true
	case "medium":
		return model.PriorityMedium, true
	case "low":
		return model.PriorityLow, true
	case "lowest", "trivial", "minor":
		return model.PriorityLow, true
	case "", "none":
		return model.PriorityNone, true
	default:
		return model.PriorityNone, false
	}
}

// ─── shared driver ──────────────────────────────────────────

var errEmptyTitle = errors.New("row has no title; skipping")

type rowMapper func(columnIndex, []string) (mappedIssue, error)

// The per-provider CSV parse + the shared write pipeline now live behind the IssueSource seam in source.go
// (csvSource + run). ImportLinearCSV / ImportJiraCSV above build a csvSource and feed it to run — behaviour
// unchanged; the extraction lets Build C plug a paginated API source into the same run + tenancy path.

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
