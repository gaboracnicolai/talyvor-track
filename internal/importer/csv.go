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
// Refused covers rows the importer DECLINED to write, by policy, with nothing wrong: the provider
// key collided with an issue a human created and #71's upsert predicate protected it. That is a
// THIRD outcome, distinct from Skipped (a row that could not land) and from Warnings (a row that
// landed degraded) — and until it was split out it was counted in Skipped, which the runner writes
// to the job's `failed` column. MEASURED at dcfbaa3: an import that correctly protected three
// human-written issues reported {status:"failed", imported:0, skipped:0, failed:3}. Correct
// behaviour reported as failure.
//
// ⚠ THE NAMES CROSS OVER AND THAT IS PRE-EXISTING, said rather than quietly re-litigated:
// ImportResult.Skipped (json "skipped") means ROWS THAT FAILED and the runner writes it to the
// job's `failed`; import_jobs.skipped now carries Refused. Renaming ImportResult.Skipped → Failed
// is the tidy-up that would end the confusion, and it changes a shipped JSON key — this struct's
// own comment above forbids doing that without a coordinated client change, so it is reported in
// the queue rather than smuggled into a fidelity fix.
type ImportResult struct {
	Imported int      `json:"imported"`
	Skipped  int      `json:"skipped"`
	Refused  int      `json:"refused"`
	Errors   []string `json:"errors"`
	Warnings []string `json:"warnings"`
}

// FieldNote records ONE provider value a mapper could not place. The pipeline COUNTS these
// (map[FieldNote]int) rather than accumulating a string per row: a 10,000-row import of one
// unknown status must produce one warning, not ten thousand.
type FieldNote struct {
	Field  string // "status" | "priority" | fieldDueDate | fieldResolutionDate
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
	//	            | viaUnparseableDate — a date arrived in a shape no pinned layout accepts
	//	            | viaStatusNotDone   — a resolution date arrived on an issue that is not done
	//	ViaResolved true iff ViaValue is what produced Mapped
	//
	// The last two are the same argument applied to a value that ARRIVED AND WAS REFUSED: a due date
	// dropped for being unparseable and a Jira with no due dates are the same empty column, and only
	// the warning tells them apart.
	Via         string
	ViaValue    string
	ViaResolved bool
}

const (
	viaCategory   = "statusCategory"
	viaNoCategory = "no-statusCategory"

	// Linear's twin of the two above. They are SEPARATE constants rather than one shared pair
	// because the rendered line names the provider's own field, and a warning that told a Linear
	// operator to go look at a "statusCategory" would send them to a field their API does not have.
	viaStateType   = "state.type"
	viaNoStateType = "no-state.type"

	// The two ways a DATE the provider sent does not reach its column. Both are deliberate and both
	// are reported, for the reason the Via fields exist at all: a value that arrived and vanished
	// must not look like a value that never arrived.
	viaUnparseableDate = "unparseable-date" // no pinned layout accepts the shape
	viaStatusNotDone   = "status-not-done"  // a resolution date on an issue that did not import as done
)

// The date fields a note can be about. Named so the rendered line reads as a sentence.
const (
	fieldDueDate        = "due date"
	fieldResolutionDate = "resolution date"
	// Linear names this field `completedAt`, not `resolutiondate`. Separate for the same reason
	// viaStateType is separate from viaCategory: a warning must name the provider's own field, or it
	// sends the operator to look for something their API does not have.
	fieldCompletionTime = "completion time"
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
	case n.Via == viaUnparseableDate:
		// The layouts are pinned by hand from a real Jira's responses. A tenant whose serialisation
		// differs from all of them learns it here, on its first import, instead of receiving a
		// column of nulls that reads as "we have no due dates".
		return fmt.Sprintf("%s %q on %d issue(s) is not a date shape this importer recognises — not recorded",
			n.Field, n.Value, count)
	case n.Via == viaNoCreatedColumn:
		// The structural-zero line for a field whose failure is otherwise invisible: created_at is
		// never null, so without this an operator cannot tell "Track read your Created column" from
		// "Track recorded every one of these as opened today".
		return fmt.Sprintf("no %q column in this export — %d issue(s) recorded as created at import time, "+
			"which makes their time-to-resolution meaningless", jiraCSVCreatedColumn, count)
	case n.Via == viaNoLinearCreatedColumn:
		// The Linear twin of the line above. It is a SEPARATE branch, not a shared one, because it
		// names THIS provider's column constant: a Linear operator sent to a constant called
		// jiraCSVCreatedColumn is one rename away from being sent to the wrong column entirely.
		return fmt.Sprintf("no %q column in this export — %d issue(s) recorded as created at import time, "+
			"which makes their time-to-resolution meaningless", linearCSVCreatedColumn, count)
	case n.Via == viaNoCreatedValue:
		return fmt.Sprintf("empty %s on %d issue(s) — recorded as created at import time", n.Field, count)
	case n.Via == viaNoUpdatedColumn:
		// The structural-zero line for Updated. It names a DIFFERENT consequence from the Created
		// one on purpose: created_at corrupts a number on an analytics page, updated_at reorders
		// the issue list and relabels every row, which is what an operator will actually see.
		return fmt.Sprintf("no %q column in this export — %d issue(s) recorded as last updated at "+
			"import time, so they sort above current work and every one reads as just updated",
			jiraCSVUpdatedColumn, count)
	case n.Via == viaNoUpdatedValue:
		return fmt.Sprintf("empty %s on %d issue(s) — recorded as last updated at import time", n.Field, count)
	case n.Via == viaNoCreatedField:
		// The API twin of viaNoCreatedColumn, and a SEPARATE sentence because it points somewhere
		// else: there is no export to go and re-make, so it names the `fields` list the client sends.
		// ⚠ IT IS REACHABLE: a real Jira Cloud IGNORES an unknown field name (HTTP 200, key absent),
		// so a rename or a typo in jiraFields lands here and nowhere else.
		return fmt.Sprintf("the provider response carried no %q field — %d issue(s) recorded as created "+
			"at import time, which makes their time-to-resolution meaningless", jiraAPICreatedField, count)
	case n.Via == viaNullCreatedAt:
		// Linear declares this field NON_NULL, so a null is a statement about the RESPONSE rather than
		// about the issue. Saying "this issue has no creation time" would be the wrong sentence.
		return fmt.Sprintf("%s arrived null on %d issue(s) — Linear declares %s non-null, so the response "+
			"does not match the schema this importer reads; recorded as created at import time",
			linearAPICreatedField, count, linearAPICreatedField)
	case n.Via == viaStatusNotDone:
		return fmt.Sprintf("%s %q on %d issue(s) not recorded — the issue imported as %q, and Track records a completion time only on %q",
			n.Field, n.Value, count, n.Mapped, model.StatusDone)
	case n.Via == viaResolutionCancelled:
		// The one line in this file that reports a mapping being OVERTURNED rather than a value
		// being dropped, so it names both the old answer and the new one.
		return fmt.Sprintf("%s %q on %d issue(s) — Track reads that word as %q, so the issue imported as %q rather than %q and carries no completion time",
			n.Field, n.Value, count, n.Mapped, n.Mapped, model.StatusDone)
	case n.Via == viaNoResolutionField:
		// The API twin of "no Resolution column", and a SEPARATE sentence because it points
		// somewhere else: there is no export to go and re-make, so it names the `fields` list the
		// client sends. ⚠ IT IS REACHABLE: a real Jira Cloud answers HTTP 200 and omits a field the
		// request did not ask for or misspelled, so a rename or a typo in jiraFields lands here and
		// nowhere else — and the rows it describes are byte-identical to correctly-imported ones.
		return fmt.Sprintf("the provider response carried no %q field — %d issue(s) closed in Jira were "+
			"imported as delivered work without checking whether they were finished or abandoned",
			jiraAPIResolutionField, count)
	case n.Via == viaADFNodeDropped:
		// The only line in this file about a DOCUMENT rather than a scalar, so it names the node
		// type — an operator who is told "media" can find it in their editor, and "something in
		// your description" would send them nowhere. It names the search index too, because that is
		// the consequence a display-only reading of "the description imported without it" misses.
		return fmt.Sprintf("the %s on %d issue(s) contains a Jira %q node, which carries no text — "+
			"it imported without that node, and Track's search reads only the text",
			n.Field, count, n.Value)
	case n.Via == viaResolutionUnreadable:
		return fmt.Sprintf("%s %q on %d issue(s) — Track cannot read that word as finished-or-abandoned, so the issue imported as %q unchanged",
			n.Field, n.Value, count, n.Mapped)
	case n.Via == viaCategory && n.ViaResolved:
		return fmt.Sprintf("%s — resolved via statusCategory %q as %q", subject, n.ViaValue, n.Mapped)
	case n.Via == viaCategory:
		return fmt.Sprintf("%s — statusCategory %q carries no Track status, imported as %q", subject, n.ViaValue, n.Mapped)
	case n.Via == viaNoCategory:
		return fmt.Sprintf("%s — no statusCategory present, imported as %q", subject, n.Mapped)
	case n.Via == viaStateType && n.ViaResolved:
		return fmt.Sprintf("%s — resolved via state.type %q as %q", subject, n.ViaValue, n.Mapped)
	case n.Via == viaStateType:
		return fmt.Sprintf("%s — state.type %q carries no Track status, imported as %q", subject, n.ViaValue, n.Mapped)
	case n.Via == viaNoStateType:
		return fmt.Sprintf("%s — no state.type present, imported as %q", subject, n.Mapped)
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

// columnIndex maps a header name to EVERY index it occupies in a CSV row, in header order. Built
// once per file so per-row lookup is O(1). Unknown columns are silently ignored — exports often
// carry extra Linear/Jira fields (cycle name, estimate, etc.) we don't map yet.
//
// ⚠ IT IS A SLICE BECAUSE A REAL EXPORT REPEATS A HEADER, AND IT USED TO BE AN int. A Jira
// "csv-all-fields" export emits one column PER VALUE for a multi-value field, all under the same
// name, padding every row out to the width of the most-valued issue in the result set. Measured
// against a real instance (see jira_csv_labels_test.go for the run and its negative controls): the
// same view answered 15 × "Labels" for one result set, 2 for another and 1 for a third, alongside
// 19 × "Comment" and 8 × "Affects Version/s".
//
// With `map[string]int` the assignment below overwrote, so the LAST occurrence won — which on the
// measured export is the padding, empty for every issue that is not the widest. 25 label values
// present, ONE imported, five of six issues importing none while carrying two each, and the caller
// told {imported:6, skipped:0, warnings:[]}.
type columnIndex map[string][]int

func buildIndex(header []string) columnIndex {
	out := make(columnIndex, len(header))
	for i, h := range header {
		k := strings.TrimSpace(strings.ToLower(h))
		out[k] = append(out[k], i)
	}
	return out
}

// get safely fetches a SINGLE-valued column by lowercased name. Returns "" if the column doesn't
// exist or the row is too short — that lets row-level validation focus on what's required (title)
// rather than how the export was shaped.
//
// ⚠ IT NAMES THE FIRST OCCURRENCE. Every column the mappers read through it is single-occurrence on
// the measured export (Summary · Status · Priority · Description · Due Date · Resolved), so this is
// a no-op there and is pinned as one. For a repeated header it is a deliberate choice replacing an
// accident: last-occurrence was never decided, it was the map assignment overwriting.
func (ci columnIndex) get(row []string, key string) string {
	idxs := ci[strings.ToLower(key)]
	if len(idxs) == 0 || idxs[0] >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[idxs[0]])
}

// getAll fetches EVERY column of that name, in header order, dropping the empties an export pads
// short rows with. This is the accessor a multi-value field must use; `get` cannot express one.
func (ci columnIndex) getAll(row []string, key string) []string {
	out := []string{}
	for _, idx := range ci[strings.ToLower(key)] {
		if idx >= len(row) {
			continue
		}
		if v := strings.TrimSpace(row[idx]); v != "" {
			out = append(out, v)
		}
	}
	return out
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
	// The two date columns this mapper carried in its own fixtures for the whole life of the
	// package and never read. WHEN THE ISSUE WAS OPENED is not an empty column anybody can see —
	// issues.created_at DEFAULTs to NOW(), so an unread Created makes every imported issue read as
	// opened at import time; WHEN IT WAS FINISHED is the column analytics selects on, so an unread
	// Completed makes a whole imported history invisible to the resolution and throughput reports
	// rather than wrong in them. See linear_csv_dates.go for both measurements and for the stop
	// reason they overturn.
	created, createdNotes := linearCSVCreated(ci, row)
	completed, completedNotes := linearCSVCompleted(ci.get(row, linearCSVCompletedColumn), status)
	return mappedIssue{
		issue: model.Issue{
			Title:       title,
			Description: ci.get(row, "Description"),
			Status:      status,
			Priority:    prio,
			Labels:      splitLabelColumns(ci.getAll(row, "Labels")),
			CompletedAt: completed,
			CreatedAt:   created,
		},
		notes: append(collectNotes(rawStatus, status, statusOK, statusFallback{}, rawPrio, prio, prioOK),
			concatNotes(createdNotes, completedNotes)...),
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

// mapLinearStateType places Linear's CANONICAL state category — WorkflowState.type, the value a team
// cannot rename however it renames the state. It is the second chance an unrecognised state NAME gets,
// and only the Linear API transport can offer it (a CSV export carries no such column).
//
// ⚠ THIS FIELD WAS DEFERRED FOR FOUR MERGES ON A PREMISE THAT TURNED OUT TO BE FALSE. W3.4 recorded,
// repeatedly, that reading it "needs a GraphQL query change that 400s the WHOLE query if wrong, and
// the test fake accepts any query, so no CI test in this repo can catch it — it needs one real call
// against a real tenant". Measured 2026-08-09 against api.linear.app/graphql, negative-controlled
// first (fabricated host ⇒ curl exit 6; fabricated path on the real host ⇒ 404):
//
//	an invalid field ⇒ HTTP 400 GRAPHQL_VALIDATION_FAILED
//	this query       ⇒ HTTP 401 AUTHENTICATION_ERROR
//
// Linear validates the DOCUMENT BEFORE it authenticates, so a malformed query and a well-formed one
// are distinguishable with no credentials at all. scripts/w34-linear-schema-probe.py re-runs it.
//
// ⚠ THE VOCABULARY IS NOT AN ENUM, unlike Jira's four-value /rest/api/2/statuscategory. Linear models
// this as `String!`; measured across 1,132 schema types and 115 enums, NOT ONE carries the issue-state
// vocabulary. It comes from the field's own description, read by unauthenticated introspection:
//
//	One of "triage", "backlog", "unstarted", "started", "completed", "canceled", "duplicate".
//
// That is SEVEN. Linear's public docs list six — `duplicate` is the one an implementation written
// from memory drops.
//
// ⚠ `triage` AND `duplicate` ARE NOT RESOLUTIONS, and that is #73's `undefined` rule, not timidity.
// Triage is Linear's pre-workflow inbox and Track has no such state; duplicate is close to cancelled
// and is not the same claim. Both fall through and are REPORTED as arrived-and-unusable, which is a
// distinct line from "no type present" — so a first real import says out loud whether this code ran.
//
// ⚠ `started` IS COARSER THAN THE NAME MAPPING AND THAT IS ON THE REPORT, NOT HIDDEN: Linear files
// both "In Progress" and "In Review" under `started`, so a custom review state whose NAME the mapper
// does not know resolves to in_progress, not in_review. This is where Linear differs from #73's Jira
// measurement, which found the name mapping and the category disagreeing zero times — and it is
// exactly why the name still goes first. Nothing that already imported correctly can change.
func mapLinearStateType(t string) (model.IssueStatus, bool) {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "backlog":
		return model.StatusBacklog, true
	case "unstarted":
		return model.StatusTodo, true
	case "started":
		return model.StatusInProgress, true
	case "completed":
		return model.StatusDone, true
	case "canceled":
		return model.StatusCancelled, true
	case "triage", "duplicate":
		// Measured, named, and deliberately not answered — see the header above.
		return model.StatusBacklog, false
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
	// The Resolution column says whether resolved work was FINISHED or ABANDONED, and it runs
	// BEFORE the date mapping below because that mapping gates on the status this line can change.
	// It can only ever move done → cancelled, and it invents no vocabulary — see
	// jira_csv_resolution.go for the measurement and the refusal.
	status, resolutionNotes := applyJiraResolution(ci.get(row, jiraCSVResolutionColumn), status)
	// The two date columns a real export carries and this mapper read for six merges as if they
	// were not there. Both are measured — column spelling and serialisation — in
	// jira_csv_dates.go; both REPORT a value they cannot place rather than nil'ing it.
	due, dueNotes := jiraCSVDueDate(ci.get(row, jiraCSVDueDateColumn))
	completed, completedNotes := jiraCSVResolved(ci.get(row, jiraCSVResolvedColumn), status)
	// WHEN THE ISSUE WAS OPENED. Unlike the two above, a miss here is not an empty column anybody
	// can see — issues.created_at DEFAULTs to NOW(), so an unread Created makes every imported issue
	// read as opened at import time and turns the time-to-resolution report NEGATIVE. See
	// jira_csv_created.go for the measurement and for why the two absent-cases are reported apart.
	created, createdNotes := jiraCSVCreated(ci, row)
	// WHEN THE ISSUE WAS LAST TOUCHED. Same invisible-failure shape as Created — issues.updated_at
	// DEFAULTs to NOW() too — but it surfaces on the product's MAIN SCREEN rather than on an
	// analytics page: the issue list sorts by updated_at DESC and every row prints "updated <n>
	// ago". An unread Updated puts a backlog measured in YEARS at the top of today's list. See
	// jira_csv_updated.go for the measurement and for the stop reason it overturns.
	updated, updatedNotes := jiraCSVUpdated(ci, row)
	return mappedIssue{
		issue: model.Issue{
			Title:       title,
			Description: ci.get(row, "Description"),
			Status:      status,
			Priority:    prio,
			Labels:      splitLabelColumns(ci.getAll(row, "Labels")),
			DueDate:     due,
			CompletedAt: completed,
			CreatedAt:   created,
			UpdatedAt:   updated,
		},
		notes: append(collectNotes(rawStatus, status, statusOK, statusFallback{}, rawPrio, prio, prioOK),
			concatNotes(resolutionNotes, dueNotes, completedNotes, createdNotes, updatedNotes)...),
	}, nil
}

// concatNotes joins the per-field note lists into one. It exists because the nested `append(a,
// append(b, c...)...)` this replaced APPENDS INTO ITS FIRST ARGUMENT'S BACKING ARRAY, and with a
// third list to join that is a real aliasing hazard rather than a style preference.
func concatNotes(lists ...[]FieldNote) []FieldNote {
	var out []FieldNote
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
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

// splitLabelColumns turns the values of EVERY column named "Labels" into one ordered list. The two
// shapes an export can use are BOTH handled and neither is guessed at: a repeated header (measured
// on a real Jira export) contributes one value per column, and a single comma-joined cell (the only
// shape ever measured for a Linear export, pinned since the first CSV merge) still splits on commas.
// With one column the result is byte-identical to the old splitLabels(ci.get(...)) call.
//
// ⚠ NO DEDUPLICATION, said rather than left to be discovered: if an export ever listed one label in
// two columns it would import twice. Nothing in the measured exports does, and collapsing duplicates
// would be inventing a rule for a shape nobody has seen.
func splitLabelColumns(cells []string) []string {
	out := []string{}
	for _, c := range cells {
		out = append(out, splitLabels(c)...)
	}
	return out
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
