package importer

import (
	"strings"
	"testing"
)

// csv_dropped_objects_test.go — the CI half of the dropped-object report: a Jira column whose value
// belongs to a Track object (a row in `comments`, a row in `time_entries`) that this importer never
// creates. csv_issue_links.go opened this class for `issue_relations`; these are the other two
// tables a real Jira export carries values for.
//
// ⚠ THE TEST FOR "IS THIS REPORTABLE" IS WHETHER TRACK SHIPS THE OBJECT, AND IT IS THE ONE
// csv_unread_refs.go APPLIES TO KEEP `Estimate` OUT. Jira `Story Points` and Linear `Estimate` stay
// unreported because model.Issue has no estimate field and time_entries has no estimate column —
// there is no Track object for the value to have failed to become. `comments` and `time_entries`
// are tables Track creates, reads, guards for tenancy and (for time) renders.

func TestJiraCSV_APopulatedCommentColumnIsReported(t *testing.T) {
	const header = "Issue key,Summary,Status,Comment"
	const row = "JRA-1,Ticket one,To Do,12/Mar/24;ada;looks good to me"

	ns := notesFor(mapRow(t, jiraRowMapper, header, row).notes, fieldCommentObj)
	if len(ns) != 1 {
		t.Fatalf("a row carrying a comment produced %d note(s), want 1 — the comment did not reach "+
			"Track's comments table and nothing said so", len(ns))
	}
	if ns[0].Via != viaObjectNotCreated {
		t.Errorf("comment note arrived via %q, want %q", ns[0].Via, viaObjectNotCreated)
	}
	if ns[0].Value != "Comment" {
		t.Errorf("note names %q, want the export's own column %q", ns[0].Value, "Comment")
	}
	// ⚠ THE CELL IS A COMMENT BODY. It is another person's words, and the job row is readable by
	// every member of the workspace — so the note must key on the column, exactly as the unread
	// reference notes do, and for a stronger reason than the note bound alone.
	if strings.Contains(ns[0].Value, "looks good") || strings.Contains(ns[0].Value, "ada") {
		t.Errorf("the note carries the comment body: %q", ns[0].Value)
	}
}

func TestJiraCSV_APopulatedTimeSpentColumnIsReported(t *testing.T) {
	const header = "Issue key,Summary,Status,Time Spent"
	const row = "JRA-1,Ticket one,To Do,3600"

	ns := notesFor(mapRow(t, jiraRowMapper, header, row).notes, fieldLoggedTimeObj)
	if len(ns) != 1 {
		t.Fatalf("a row carrying logged work produced %d note(s), want 1", len(ns))
	}
	if ns[0].Via != viaObjectNotCreated || ns[0].Value != "Time Spent" {
		t.Errorf("logged-time note = %+v, want via %q value %q", ns[0], viaObjectNotCreated, "Time Spent")
	}
}

// THE GATE IS THE CELL, NOT THE HEADER — 165 of the 302 real exports carry a `Comment` column and
// most of their rows have no comment; a report that fired on the header would be one an operator
// learns to skip.
func TestDroppedObjects_AnEmptyCellIsSilent(t *testing.T) {
	const header = "Issue key,Summary,Status,Comment,Time Spent"
	if ns := mapRow(t, jiraRowMapper, header, "JRA-1,Ticket one,To Do,,").notes; len(notesFor(ns, fieldCommentObj))+
		len(notesFor(ns, fieldLoggedTimeObj)) != 0 {
		t.Errorf("a row with the columns present and EMPTY produced dropped-object notes: %+v", ns)
	}
	if ns := mapRow(t, jiraRowMapper, "Issue key,Summary,Status", "JRA-1,Ticket one,To Do").notes; len(
		notesFor(ns, fieldCommentObj)) != 0 {
		t.Errorf("an export with no Comment column produced a comment note: %+v", ns)
	}
}

// ⚠⚠ JIRA REPEATS `Comment` ONCE PER COMMENT — 69 OCCURRENCES IN ONE REAL HEADER — and
// columnIndex.get names the FIRST (csv.go:422). One dropped object class, one line per row however
// many cells carry it, and a row whose first comment cell is empty still reports.
func TestDroppedObjects_ARepeatedColumnIsOneNoteFromAnyOccurrence(t *testing.T) {
	const header = "Issue key,Summary,Status,Comment,Comment,Comment"
	const row = "JRA-1,Ticket one,To Do,,,12/Mar/24;ada;the third cell holds it"

	ns := notesFor(mapRow(t, jiraRowMapper, header, row).notes, fieldCommentObj)
	if len(ns) != 1 {
		t.Fatalf("a row whose THIRD Comment cell holds the comment produced %d note(s), want exactly 1", len(ns))
	}
}

// ⚠ THE ROLL-UP IS NOT THE ISSUE'S OWN LOGGED WORK, AND EXCLUDING IT IS MEASURED RATHER THAN
// FORGOTTEN. Jira emits `Σ Time Spent` — the sum over an issue AND its subtasks — on 838 rows of the
// corpus against `Time Spent`'s 283. Reporting it would tell an operator that a parent issue lost
// work that was logged on its children and is already counted under them.
func TestDroppedObjects_TheSigmaRollUpIsNotTheIssuesOwnLoggedTime(t *testing.T) {
	const header = "Issue key,Summary,Status,Σ Time Spent,Σ Original Estimate"
	const row = "JRA-1,Ticket one,To Do,43200,3600"

	if ns := notesFor(mapRow(t, jiraRowMapper, header, row).notes, fieldLoggedTimeObj); len(ns) != 0 {
		t.Errorf("the Σ roll-up produced %d logged-time note(s), want 0: %+v", len(ns), ns)
	}
}

// ⚠ AND THE VALUE THAT HAS NO TRACK OBJECT AT ALL STAYS SILENT. `Original estimate` (848 rows) and
// `Story Points` have nowhere to land: time_entries has no estimate column and model.Issue has no
// estimate field. A warning about them names a Track capability that does not exist — the same rule
// csv_unread_refs.go states for Estimate, kept true here where the neighbouring column IS reported.
func TestDroppedObjects_AValueWithNoTrackObjectIsNotReported(t *testing.T) {
	const header = "Issue key,Summary,Status,Original estimate,Remaining Estimate,Custom field (Story Points),Work Ratio"
	const row = "JRA-1,Ticket one,To Do,3600,1800,5.0,50%"

	for _, n := range mapRow(t, jiraRowMapper, header, row).notes {
		if n.Via == viaObjectNotCreated {
			t.Errorf("a value with no Track object produced a dropped-object note: %+v", n)
		}
	}
}

// The rendered line, and the line the MAPPER's own note renders — the half a positive control found
// missing on the issue-link class (control C6). A shared sentence with the unread-COLUMN class
// would tell an operator "their Track comment is left empty"; there is no such field.
func TestDroppedObjects_TheRenderedLineNamesWhatWasNotCreated(t *testing.T) {
	line := FieldNote{Field: fieldCommentObj, Value: "Comment", Via: viaObjectNotCreated}.render(9)
	for _, want := range []string{"9 issue(s)", `"Comment"`, "no Track comment"} {
		if !strings.Contains(line, want) {
			t.Errorf("rendered line %q does not contain %q", line, want)
		}
	}
	if strings.Contains(line, "unrecognised") || strings.Contains(line, "left empty") {
		t.Errorf("the dropped-object line reuses a sentence written for another loss: %q", line)
	}

	ns := notesFor(mapRow(t, jiraRowMapper, "Issue key,Summary,Status,Time Spent",
		"JRA-1,Ticket one,To Do,3600").notes, fieldLoggedTimeObj)
	if len(ns) != 1 {
		t.Fatalf("premise: want one logged-time note from the mapper, got %d", len(ns))
	}
	fromMapper := ns[0].render(1)
	if !strings.Contains(fromMapper, "no Track logged time") || strings.Contains(fromMapper, "left empty") {
		t.Errorf("the line an operator is actually shown for dropped logged work is %q", fromMapper)
	}
}

// LINEAR IS NOT WIRED TO THIS TABLE, AND THAT IS A MEASUREMENT: no real Linear export carries a
// comment or a time column at all (its `Estimate` is story points, which have no Track object).
// Wiring it would add a table that can never fire.
func TestDroppedObjects_LinearHasNoSuchColumns(t *testing.T) {
	const header = "ID,Title,Status,Priority,Estimate,Comment,Time Spent"
	const row = "ENG-1,Ticket one,Todo,High,5,a body,3600"
	for _, n := range mapRow(t, linearRowMapper, header, row).notes {
		if n.Via == viaObjectNotCreated {
			t.Errorf("the Linear mapper reports a dropped object; no real Linear export carries "+
				"one of these columns, so this table can only fire on a fabricated header: %+v", n)
		}
	}
}
