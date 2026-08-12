package importer

import (
	"strings"
	"testing"
)

// csv_issue_links_test.go — the CI half of the issue-link report. Every shape the corpus census
// measures is pinned HERE as a literal too, so the guard runs on a machine with no corpus (see
// csv_issue_links_corpus_census_test.go for why the census itself skips).
//
// ⚠ THE CLASS IS NOT "ANOTHER UNREAD COLUMN". csv_unread_refs.go reports four columns that would
// have filled a NULLABLE FIELD ON THE ISSUE and a fifth that is overwritten. A link would have
// created a ROW IN ANOTHER TABLE — `issue_relations`, the table Track's RelationsSection,
// DependencyGraph and Kanban BlockerAlert read, and the one issue.Store's blockedChecker asks
// before it sets Issue.IsBlocked. Nothing on the imported issue is empty; the edge is absent.

// ─── the rule ───────────────────────────────────────────────────────────────

// Jira parameterises the column by link TYPE, so the report cannot be a list of literals. This is
// the shape that fact takes as a test: two link types on one row, one note each, and neither type
// named anywhere in the shipped source.
func TestJiraCSV_APopulatedIssueLinkColumnIsReported(t *testing.T) {
	const header = "Issue key,Summary,Status,Priority,Outward issue link (Blocks),Inward issue link (Relates)"
	const row = "JRA-1,Ticket one,To Do,High,JRA-9,JRA-8"

	m := mapRow(t, jiraRowMapper, header, row)

	ns := notesFor(m.notes, fieldIssueLinkRef)
	if len(ns) != 2 {
		t.Fatalf("a row carrying two issue links produced %d note(s), want 2 — the links did not "+
			"reach Track's issue_relations and nothing said so. notes: %+v", len(ns), m.notes)
	}
	got := map[string]bool{}
	for _, n := range ns {
		if n.Via != viaIssueLinkNotRead {
			t.Errorf("issue-link note arrived via %q, want %q", n.Via, viaIssueLinkNotRead)
		}
		got[n.Value] = true
	}
	// The note names the EXPORT's column, because "issue link" is not a word an operator can find
	// anywhere in a Jira export — the column is called `Outward issue link (Blocks)`.
	for _, want := range []string{"outward issue link (blocks)", "inward issue link (relates)"} {
		if !got[want] {
			t.Errorf("no note names %q; the operator cannot find which link was dropped. got %v", want, got)
		}
	}
}

// ⚠ THE VALUE IS THE COLUMN LOWERCASED, AND THAT IS A CONSEQUENCE OF WHERE THE SPELLING COMES FROM
// RATHER THAN A CHOICE ABOUT DISPLAY. A rowMapper receives columnIndex and a row, never the header,
// and columnIndex lowercases every key it holds (csv.go:buildIndex). Every other entry in this
// package names its column with a hand-written literal because that column has ONE fixed spelling;
// this one is discovered from the header at run time, so the lowercased key is the only spelling
// the mapper can see. Pinned so that a later change which starts fabricating capitalisation — the
// only other way to produce a "prettier" name — is caught rather than welcomed.
func TestIssueLinks_TheNoteNamesTheColumnAsTheIndexHoldsIt(t *testing.T) {
	const header = "Issue key,Summary,Status,Outward issue link (Gantt End to Start)"
	const row = "JRA-1,Ticket one,To Do,JRA-9"

	ns := notesFor(mapRow(t, jiraRowMapper, header, row).notes, fieldIssueLinkRef)
	if len(ns) != 1 {
		t.Fatalf("want exactly 1 issue-link note, got %d", len(ns))
	}
	if ns[0].Value != "outward issue link (gantt end to start)" {
		t.Errorf("note names %q; want the header's own column as columnIndex holds it "+
			"(lowercased, trimmed) — anything else is a spelling this code invented", ns[0].Value)
	}
}

// Linear's three link columns are fixed names, and they exist only in the 34-column export shape.
func TestLinearCSV_APopulatedIssueLinkColumnIsReported(t *testing.T) {
	const header = "ID,Title,Status,Priority,Related to,Blocked by,Duplicate of"
	const row = "ENG-1,Ticket one,Todo,High,ENG-7,ENG-8,ENG-9"

	ns := notesFor(mapRow(t, linearRowMapper, header, row).notes, fieldIssueLinkRef)
	if len(ns) != 3 {
		t.Fatalf("a row carrying all three Linear link columns produced %d note(s), want 3", len(ns))
	}
	got := map[string]bool{}
	for _, n := range ns {
		got[n.Value] = true
		if n.Via != viaIssueLinkNotRead {
			t.Errorf("issue-link note arrived via %q, want %q", n.Via, viaIssueLinkNotRead)
		}
	}
	for _, want := range []string{"related to", "blocked by", "duplicate of"} {
		if !got[want] {
			t.Errorf("no note names %q. got %v", want, got)
		}
	}
}

// THE GATE IS THE CELL, NOT THE HEADER — the same rule csv_unread_refs.go states and for the same
// reason: 14 of the 45 real Linear exports carry no link column at all, and of the 31 that do, most
// rows carry no link. A report that fired on the header would be one an operator learns to skip.
func TestIssueLinks_AnEmptyCellIsSilent(t *testing.T) {
	const linearHeader = "ID,Title,Status,Priority,Related to,Blocked by,Duplicate of"
	if ns := notesFor(mapRow(t, linearRowMapper, linearHeader, "ENG-1,Ticket one,Todo,High,,,").notes,
		fieldIssueLinkRef); len(ns) != 0 {
		t.Errorf("a row with the link columns present and EMPTY produced %d note(s), want 0: %+v", len(ns), ns)
	}
	const jiraHeader = "Issue key,Summary,Status,Outward issue link (Blocks)"
	if ns := notesFor(mapRow(t, jiraRowMapper, jiraHeader, "JRA-1,Ticket one,To Do,").notes,
		fieldIssueLinkRef); len(ns) != 0 {
		t.Errorf("a Jira row with an empty link cell produced %d note(s), want 0: %+v", len(ns), ns)
	}
	// And an export that carries no link column at all — the 30-column Linear shape — says nothing.
	if ns := notesFor(mapRow(t, linearRowMapper, "ID,Title,Status,Priority", "ENG-1,Ticket one,Todo,High").notes,
		fieldIssueLinkRef); len(ns) != 0 {
		t.Errorf("an export with no link column produced %d note(s), want 0: %+v", len(ns), ns)
	}
}

// ⚠⚠ JIRA REPEATS THE COLUMN, ONE OCCURRENCE PER LINK, UP TO 29 TIMES IN ONE REAL HEADER — and
// columnIndex.get NAMES THE FIRST OCCURRENCE (csv.go:422). A rule built on `get` goes silent on a
// row whose first cell is empty and whose second holds the link, which is the shape a multi-link
// export takes. Measured on the corpus this is worth 0 rows TODAY, so this test is the only place
// the choice is visible — and a report that is right for a reason nobody wrote down is one line of
// refactoring away from being wrong.
func TestIssueLinks_ARepeatedColumnIsReportedFromAnyOccurrence(t *testing.T) {
	const header = "Issue key,Summary,Status,Outward issue link (Blocks),Outward issue link (Blocks),Outward issue link (Blocks)"
	const row = "JRA-1,Ticket one,To Do,,,JRA-9"

	ns := notesFor(mapRow(t, jiraRowMapper, header, row).notes, fieldIssueLinkRef)
	if len(ns) != 1 {
		t.Fatalf("a row whose THIRD occurrence of a repeated link column holds the link produced "+
			"%d note(s), want exactly 1 — one dropped column is one line, however many cells it has", len(ns))
	}
	if ns[0].Value != "outward issue link (blocks)" {
		t.Errorf("note names %q, want %q", ns[0].Value, "outward issue link (blocks)")
	}
}

// A COUNT IS NOT A REFERENCE, and this is the measured exclusion rather than an oversight. One real
// export carries `Custom field (Issue Link Count)` on 39 rows, 28 of which carry no link column at
// all; reporting it would tell an operator a link was dropped on rows where the export names none.
// The same reasoning keeps `Epic Link Summary` out of the parent entry — see csv_unread_refs.go.
func TestIssueLinks_ALinkCountColumnIsNotALink(t *testing.T) {
	const header = "Issue key,Summary,Status,Custom field (Issue Link Count)"
	const row = "JRA-1,Ticket one,To Do,3"

	if ns := notesFor(mapRow(t, jiraRowMapper, header, row).notes, fieldIssueLinkRef); len(ns) != 0 {
		t.Errorf("a link COUNT column produced %d issue-link note(s), want 0: %+v", len(ns), ns)
	}
}

// The note keys on the COLUMN and never on the cell. Two reasons, and both are already this
// package's history: a per-cell value turns a 10,000-row import into 10,000 distinct notes (the
// bound #80 exists to prevent), and a link cell holds another issue's KEY, which would then be
// copied into a job row every member of the workspace can read.
func TestIssueLinks_TheNoteNamesTheColumnNeverTheCell(t *testing.T) {
	const header = "Issue key,Summary,Status,Outward issue link (Blocks)"
	m1 := mapRow(t, jiraRowMapper, header, "JRA-1,Ticket one,To Do,SECRET-9")
	m2 := mapRow(t, jiraRowMapper, header, "JRA-2,Ticket two,To Do,OTHER-4")

	n1 := notesFor(m1.notes, fieldIssueLinkRef)
	n2 := notesFor(m2.notes, fieldIssueLinkRef)
	if len(n1) != 1 || len(n2) != 1 {
		t.Fatalf("premise: want one note per row, got %d and %d", len(n1), len(n2))
	}
	if n1[0] != n2[0] {
		t.Errorf("two rows with DIFFERENT link targets produced different notes (%+v vs %+v) — the "+
			"report is keyed on the cell, so it is unbounded and it leaks the linked issue's key",
			n1[0], n2[0])
	}
	if strings.Contains(n1[0].Value, "SECRET-9") {
		t.Errorf("the note carries the linked issue key %q", n1[0].Value)
	}
}

// ─── the rendered line ──────────────────────────────────────────────────────

// ⚠ THE SENTENCE IS ITS OWN BRANCH BECAUSE THE ONE IT WOULD HAVE SHARED IS FALSE HERE. Rendering an
// issue link through viaColumnNotRead would tell an operator "their Track issue link is left empty";
// there is no such field to be empty. What happened is that a row in `issue_relations` was never
// created — and the consequence that costs something is that an issue the export says is BLOCKED
// imports with Issue.IsBlocked false, because that flag is computed from exactly that table.
func TestIssueLinks_TheRenderedLineNamesTheColumnAndWhatWasNotCreated(t *testing.T) {
	line := FieldNote{Field: fieldIssueLinkRef, Value: "outward issue link (blocks)", Via: viaIssueLinkNotRead}.render(7)

	for _, want := range []string{"7 issue(s)", `"outward issue link (blocks)"`, "issue relation", "blocked"} {
		if !strings.Contains(line, want) {
			t.Errorf("rendered line %q does not contain %q", line, want)
		}
	}
	// The two sentences this line must NOT be. "unrecognised" is the generic fallthrough — nothing
	// here failed to parse — and "left empty" is the unread-COLUMN sentence, which describes a
	// nullable field on the issue rather than a row that was never written.
	if strings.Contains(line, "unrecognised") {
		t.Errorf("the issue-link line fell through to the generic unrecognised-value sentence: %q", line)
	}
	if strings.Contains(line, "left empty") {
		t.Errorf("the issue-link line claims a Track field is left empty; no field on the issue "+
			"holds a link, and the operator sent to look for a null will not find one: %q", line)
	}

	// ⚠⚠ AND THE SAME LINE FROM THE MAPPER'S OWN NOTE, WHICH IS THE HALF THIS TEST WAS MISSING AND
	// A POSITIVE CONTROL FOUND. Everything above builds the FieldNote here and asks the renderer
	// what it makes of it — so it is a test of a format string, and it stayed GREEN under a control
	// that pointed the mapper's note at viaColumnNotRead (control C6,
	// ~/talyvor-queue/w34-issuelinks-controls-c2b7.py). Under that mutation an operator is told
	// their "Track issue link is left empty"; the assertion that can see it has to start at the
	// mapper, because the via is chosen there.
	const header = "Issue key,Summary,Status,Outward issue link (Blocks)"
	ns := notesFor(mapRow(t, jiraRowMapper, header, "JRA-1,Ticket one,To Do,JRA-9").notes, fieldIssueLinkRef)
	if len(ns) != 1 {
		t.Fatalf("premise: want one issue-link note from the mapper, got %d", len(ns))
	}
	fromMapper := ns[0].render(1)
	if !strings.Contains(fromMapper, "no Track issue relation is created") {
		t.Errorf("the line an operator is actually shown for a dropped link is %q — the mapper's "+
			"note does not render the issue-link sentence", fromMapper)
	}
	if strings.Contains(fromMapper, "left empty") || strings.Contains(fromMapper, "unrecognised") {
		t.Errorf("the mapper's own note renders a sentence written for a different loss: %q", fromMapper)
	}
}

// ─── the spellings are real ─────────────────────────────────────────────────

// The literals below are REAL EXPORT HEADERS, carried here so the CI half can ask whether the
// shipped spellings match what providers emit without needing the corpus. Provenance is the file's
// md5 in the cached corpus.
const (
	// 4f4aea9e1a5840d16b5c5e8969293279 — Linear's 34-column shape, the ONLY one of its two published
	// shapes that carries link columns; the 30-column shape (see csv_parent_spellings_test.go) has
	// none, which is why 14 of the 45 real exports can lose no link.
	linearHeader34WithLinks = "ID,Team,Title,Description,Status,Estimate,Priority,Project ID,Project,Creator,Assignee,Labels,Cycle Number,Cycle Name,Cycle Start,Cycle End,Created,Updated,Started,Triaged,Completed,Canceled,Archived,Due Date,Parent issue,Initiatives,Project Milestone ID,Project Milestone,SLA Status,UUID,Time in status (minutes),Related to,Blocked by,Duplicate of"
	// 76e487073d7a66fd50d68560dd8de84b — a real Jira export carrying a link column, and note the
	// TYPE in the parentheses: `Relates`, not one of the five types Track models.
	jiraHeaderWithLink = "Summary,Issue key,Issue id,Issue Type,Status,Project key,Project name,Project type,Project lead,Project lead id,Project description,Priority,Reporter Id,Creator Id,Created,Updated,Last Viewed,Resolved,Affects versions,Fix versions,Due date,Votes,Description,Environment,Watchers Id,Original estimate,Remaining Estimate,Time Spent,Work Ratio,Inward issue link (Relates),Parent,Parent summary,Status Category"
)

func TestIssueLinkColumns_AppearInARealExportHeader(t *testing.T) {
	linear := buildIndex(strings.Split(linearHeader34WithLinks, ","))
	for _, col := range linearIssueLinkColumns {
		if !linear.has(col) {
			t.Errorf("linearIssueLinkColumns names %q, which is in NO real Linear export header — "+
				"a spelling no export emits reports nothing", col)
		}
	}
	jira := buildIndex(strings.Split(jiraHeaderWithLink, ","))
	if got := jiraIssueLinkSpellings(jira); len(got) != 1 || got[0] != "inward issue link (relates)" {
		t.Errorf("the Jira prefix rule found %v in a real export header, want exactly "+
			"[inward issue link (relates)]", got)
	}
	// And the rule must not claim a header that carries no link column at all.
	if got := jiraIssueLinkSpellings(buildIndex(strings.Split(linearHeader34WithLinks, ","))); len(got) != 0 {
		t.Errorf("the Jira prefix rule matched %v in a Linear header, which carries no Jira link column", got)
	}
}

// ⚠ THE PREFIX RULE IS NOT A CONVENIENCE. 60 DISTINCT link-column spellings appear across the real
// Jira corpus — the link TYPE inside the parentheses is configured per instance — so a list of
// literals is complete only for the instances that were sampled. This is the same defect #117 fixed
// for the parent link, and shipping the list shape here would have re-created it deliberately.
func TestJiraIssueLinkSpellings_MatchesAnUnseenLinkType(t *testing.T) {
	// A type that appears NOWHERE in this repo or in the cached corpus.
	ci := buildIndex([]string{"Issue key", "Summary", "Outward issue link (Supersedes)"})
	got := jiraIssueLinkSpellings(ci)
	if len(got) != 1 || got[0] != "outward issue link (supersedes)" {
		t.Errorf("the rule found %v for a link type it has never seen — an instance whose "+
			"administrator named a link type reports nothing, which is the exact-list defect", got)
	}
	// The prefix is anchored: a column that merely MENTIONS a link is not one.
	for _, col := range []string{"Custom field (Issue Link Count)", "Linked Issues Summary", "Outward issue links"} {
		if got := jiraIssueLinkSpellings(buildIndex([]string{"Issue key", col})); len(got) != 0 {
			t.Errorf("%q matched the link rule (%v); it is not a link column", col, got)
		}
	}
}
