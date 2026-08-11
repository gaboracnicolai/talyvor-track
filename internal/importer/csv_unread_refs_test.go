package importer

import (
	"strings"
	"testing"
)

// csv_unread_refs_test.go — the CI half of the unread-object-reference report. Every number the
// corpus census measures is pinned HERE as a literal too, so the guard runs on a machine with no
// corpus (see csv_unread_refs_corpus_census_test.go for why the census itself skips).

// mapRow is the shipped mapper driven the way csvSource drives it: buildIndex over the header, then
// the mapper over one data row. It is deliberately NOT a fixture struct — a fixture that supplies
// the columns by name cannot tell a mapper that reads a column from one that does not.
func mapRow(t *testing.T, mapper rowMapper, header, row string) mappedIssue {
	t.Helper()
	src, err := newCSVSource(strings.NewReader(header+"\n"+row+"\n"), mapper)
	if err != nil {
		t.Fatalf("newCSVSource: %v", err)
	}
	got, ok := src.Next()
	if !ok {
		t.Fatal("source yielded no row")
	}
	if got.Err != nil {
		t.Fatalf("row refused: %v", got.Err)
	}
	return mappedIssue{issue: got.Issue, notes: got.Notes}
}

// ─── the four references, both providers ────────────────────────────────────

// THE FOUR ARE NOT A TASTE LIST. They are exactly the cross-object references model.Issue declares
// and issue.Store guards with assertRefInWorkspace (project_id · cycle_id · assignee_id ·
// parent_id) — the set Track's own object graph defines. Every one of them is NULL on every
// imported issue, on every transport, and until this file nothing said so.
func TestLinearCSV_APopulatedObjectReferenceColumnIsReported(t *testing.T) {
	// A row whose four reference columns are all populated. The header is the real 30-column
	// Linear export shape's spellings for them; the rest of the export is not needed to ask
	// this question and its absence is not what is being measured.
	const header = "ID,Title,Description,Status,Priority,Labels,Assignee,Project,Cycle Name,Parent issue"
	const row = "ENG-1,Ticket one,a body,Todo,High,alpha,ada@example.com,Platform,Cycle 12,ENG-9"

	m := mapRow(t, linearRowMapper, header, row)

	for _, field := range []string{fieldAssigneeRef, fieldProjectRef, fieldCycleRef, fieldParentRef} {
		ns := notesFor(m.notes, field)
		if len(ns) != 1 {
			t.Errorf("a populated %s column produced %d notes, want exactly 1 — "+
				"the value did not reach Track and nothing said so", field, len(ns))
			continue
		}
		if ns[0].Via != viaColumnNotRead {
			t.Errorf("%s note arrived via %q, want %q", field, ns[0].Via, viaColumnNotRead)
		}
		// The note NAMES THE PROVIDER'S COLUMN, because an operator sent to "cycle" cannot find
		// that word anywhere in a Linear export — the column is called "Cycle Name".
		if ns[0].Value == "" {
			t.Errorf("%s note names no source column; the operator cannot find what to look at", field)
		}
	}

	// And the reference itself is still empty — the report must never be mistaken for the fix.
	if m.issue.AssigneeID != nil || m.issue.ProjectID != nil || m.issue.CycleID != nil || m.issue.ParentID != nil {
		t.Fatalf("A TRANSPORT NOW FILLS ONE OF THE FOUR REFERENCES: assignee=%v project=%v cycle=%v parent=%v. "+
			"That is the fix the queue asks for — the report in csv_unread_refs.go now describes something "+
			"that no longer happens for that field, and its entry must be removed WITH the change.",
			m.issue.AssigneeID, m.issue.ProjectID, m.issue.CycleID, m.issue.ParentID)
	}
}

func TestJiraCSV_APopulatedObjectReferenceColumnIsReported(t *testing.T) {
	const header = "Issue key,Summary,Description,Status,Priority,Labels,Assignee,Sprint,Parent"
	const row = "JRA-1,Ticket one,a body,To Do,High,alpha,ada@example.com,Sprint 4,JRA-9"

	m := mapRow(t, jiraRowMapper, header, row)

	for _, field := range []string{fieldAssigneeRef, fieldCycleRef, fieldParentRef} {
		ns := notesFor(m.notes, field)
		if len(ns) != 1 {
			t.Errorf("a populated %s column produced %d notes, want exactly 1", field, len(ns))
			continue
		}
		if ns[0].Via != viaColumnNotRead {
			t.Errorf("%s note arrived via %q, want %q", field, ns[0].Via, viaColumnNotRead)
		}
	}

	// ⚠ THE JIRA PROJECT IS NOT ONE OF THEM, AND THE EXCLUSION IS THE MEASUREMENT. A Jira project
	// is what this import maps to a Track TEAM — the operator supplies team_id on the job — so
	// reporting it as a lost `project_id` would report a loss that did not happen. 16,020 of the
	// corpus's 16,284 rows carry a `Project key`; if that column were in the table this assertion
	// would fail, which is what keeps the exclusion a decision rather than an oversight.
	withProject := mapRow(t, jiraRowMapper,
		"Issue key,Summary,Status,Project key,Project name",
		"JRA-1,Ticket one,To Do,JRA,Jira Reference Application")
	if ns := notesFor(withProject.notes, fieldProjectRef); len(ns) != 0 {
		t.Errorf("a Jira `Project` column produced a lost-project report (%d note(s)); "+
			"a Jira project is the Track TEAM this job already targets, not a Track project", len(ns))
	}
}

// ─── the silence is per-cell, not per-column ────────────────────────────────

// An EMPTY reference cell is not a loss and must not be reported: the provider had nothing to send.
// Without this, an export carrying an Assignee column and no assignees would warn on every row —
// the report would be about the header, and an operator would learn to ignore it within a week.
func TestCSV_AnEmptyObjectReferenceCellIsNotReported(t *testing.T) {
	m := mapRow(t, linearRowMapper,
		"ID,Title,Status,Assignee,Project,Cycle Name,Parent issue",
		"ENG-1,Ticket one,Todo,,,,")
	for _, field := range []string{fieldAssigneeRef, fieldProjectRef, fieldCycleRef, fieldParentRef} {
		if ns := notesFor(m.notes, field); len(ns) != 0 {
			t.Errorf("an EMPTY %s cell was reported as a loss (%d note(s)) — the provider sent nothing", field, len(ns))
		}
	}
}

// An export that does NOT CARRY the column is also silent here. That absence is a real and
// different condition (the operator exported a narrower shape), but it is not "a value arrived and
// was dropped", and this package already has a separate vocabulary for absent columns
// (viaNo*Column). Conflating them would put a sentence about a value in front of an operator who
// sent none.
func TestCSV_AnAbsentObjectReferenceColumnIsNotReported(t *testing.T) {
	m := mapRow(t, jiraRowMapper, "Issue key,Summary,Status", "JRA-1,Ticket one,To Do")
	for _, field := range []string{fieldAssigneeRef, fieldProjectRef, fieldCycleRef, fieldParentRef} {
		if ns := notesFor(m.notes, field); len(ns) != 0 {
			t.Errorf("an ABSENT %s column was reported as a dropped value (%d note(s))", field, len(ns))
		}
	}
}

// ─── the rendered sentence ──────────────────────────────────────────────────

// The report an operator actually reads. It must carry the count, the provider's own column name,
// and the Track field that stayed empty — one line for the whole import, not one per row.
func TestUnreadRefWarning_RendersOneLineNamingBothEnds(t *testing.T) {
	n := FieldNote{Field: fieldAssigneeRef, Value: "Assignee", Via: viaColumnNotRead}
	got := n.render(1363)
	for _, want := range []string{"1363", `"Assignee"`, fieldAssigneeRef} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered line %q does not carry %q", got, want)
		}
	}
	// It must NOT be rendered by the generic "unrecognised <field> <value>" fallback: nothing was
	// unrecognised — the column was never read.
	if strings.Contains(got, "unrecognised") {
		t.Errorf("the unread-column line fell through to the unrecognised-value sentence: %q", got)
	}
}

// ─── whole-population, pinned as literals ───────────────────────────────────

// The corpus numbers, written down where they run. csv_unread_refs_corpus_census_test.go recomputes
// them from the real exports when the corpus is present; this asserts the SHAPE the numbers
// describe — that every column named in the tables is one a real export actually carries — using
// the real header rows from that corpus rather than a hand-written one.
//
// ⚠ THIS IS THE FLOOR, AND IT IS DELIBERATELY NOT A COUNT. A count pinned here would go red when
// the corpus grows; what must not happen is a table entry naming a column no real export has.
func TestUnreadRefTables_EveryColumnAppearsInARealExportHeader(t *testing.T) {
	// Verbatim header lines from the corpus: the 30-column and 34-column Linear shapes, and the
	// two Jira export shapes that carry Sprint and Parent.
	const linearHeader30 = "ID,Team,Title,Description,Status,Estimate,Priority,Project ID,Project,Creator,Assignee,Labels,Cycle Number,Cycle Name,Cycle Start,Cycle End,Created,Updated,Started,Triaged,Completed,Canceled,Archived,Due Date,Parent issue,Initiatives,Project Milestone ID,Project Milestone,SLA Status,Roadmaps"
	const jiraHeaderWide = "Issue key,Issue id,Summary,Issue Type,Status,Project key,Project name,Priority,Resolution,Assignee,Reporter,Created,Updated,Last Viewed,Resolved,Due Date,Sprint,Parent,Parent summary,Labels"

	check := func(name, header string, refs []unreadRef) {
		have := map[string]bool{}
		for _, h := range strings.Split(header, ",") {
			have[strings.ToLower(strings.TrimSpace(h))] = true
		}
		for _, r := range refs {
			if !have[strings.ToLower(r.column)] {
				t.Errorf("%s table names column %q, which is in no real export header — "+
					"the report for %s can never fire", name, r.column, r.field)
			}
		}
		if len(refs) == 0 {
			t.Errorf("%s table is empty — the report is structurally silent", name)
		}
	}
	check("linear", linearHeader30, linearUnreadRefs)
	check("jira", jiraHeaderWide, jiraUnreadRefs)

	// The union of the two tables is exactly the four Track references, no more and no fewer. A
	// fifth entry here would be a claim about a Track column that does not exist; a missing one is
	// a reference that went back to being silent.
	seen := map[string]bool{}
	for _, r := range append(append([]unreadRef{}, linearUnreadRefs...), jiraUnreadRefs...) {
		seen[r.field] = true
	}
	for _, want := range []string{fieldAssigneeRef, fieldProjectRef, fieldCycleRef, fieldParentRef} {
		if !seen[want] {
			t.Errorf("no transport reports a lost %s", want)
		}
		delete(seen, want)
	}
	for extra := range seen {
		t.Errorf("a table reports %q, which is not one of Track's four issue object references", extra)
	}
}
