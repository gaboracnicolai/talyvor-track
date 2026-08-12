package importer

import (
	"sort"
	"strings"
	"testing"
)

// csv_custom_fields_test.go — the CI half of the custom-field report. The class is
// csv_dropped_objects.go's, one object wider: a Jira column whose value belongs to a Track object
// this importer never creates. Here the object is a `custom_fields` definition and the
// `issue_field_values` row that would hold the value.
//
// ⚠ THE TEST FOR "IS THIS REPORTABLE" IS WHETHER TRACK SHIPS THE OBJECT — the rule
// csv_unread_refs.go applies to keep `Estimate` out, and the rule that let `comment` in. Track
// ships custom fields end to end: migration 0010, internal/customfield's store and handler, SIX
// mounted routes, model.Issue.FieldValues, and — unlike `comments` — a frontend that calls them
// (frontend/src/api/customFields.ts, frontend/src/hooks/useCustomFields.ts).

func TestJiraCSV_APopulatedCustomFieldIsReported(t *testing.T) {
	const header = "Issue key,Summary,Status,Custom field (Severity)"
	const row = "JRA-1,Ticket one,To Do,Blocker"

	ns := notesFor(mapRow(t, jiraRowMapper, header, row).notes, fieldCustomFieldObj)
	if len(ns) != 1 {
		t.Fatalf("a row carrying a custom-field value produced %d note(s), want 1 — the value "+
			"reached no issue_field_values row and nothing said so", len(ns))
	}
	if ns[0].Via != viaCustomFieldNotCreated {
		t.Errorf("custom-field note arrived via %q, want %q", ns[0].Via, viaCustomFieldNotCreated)
	}
	// The column name as columnIndex holds it: lowercased. Same rule as issueLinkNotes, and for the
	// same two reasons — the spelling is discovered from THIS export's header, and the cell is the
	// tenant's own data.
	if ns[0].Value != "custom field (severity)" {
		t.Errorf("note names %q, want this export's own column %q", ns[0].Value, "custom field (severity)")
	}
	// ⚠ THE CELL IS TENANT DATA. A custom field holds whatever the instance put in it — a customer
	// name, an approver, a free-text incident note — and the job row is readable by every member of
	// the workspace. The note must key on the COLUMN, never the cell.
	if strings.Contains(ns[0].Value, "Blocker") {
		t.Errorf("the note carries the cell value: %q", ns[0].Value)
	}
}

// THE GATE IS THE CELL, NOT THE HEADER. 282 of the 302 genuine real Jira exports carry a populated
// custom field and the other 20 carry the columns with nothing in them; a report that fired on the
// header would be one an operator learns to skip.
func TestCustomFields_AnEmptyCellIsSilent(t *testing.T) {
	const header = "Issue key,Summary,Status,Custom field (Severity),Custom field (Rank)"
	if ns := mapRow(t, jiraRowMapper, header, "JRA-1,Ticket one,To Do,,").notes; len(
		notesFor(ns, fieldCustomFieldObj)) != 0 {
		t.Errorf("a row with the columns present and EMPTY produced custom-field notes: %+v", ns)
	}
	if ns := mapRow(t, jiraRowMapper, "Issue key,Summary,Status", "JRA-1,Ticket one,To Do").notes; len(
		notesFor(ns, fieldCustomFieldObj)) != 0 {
		t.Errorf("an export with no custom-field column produced a custom-field note: %+v", ns)
	}
}

// ⚠ ONE NOTE PER SPELLING, NOT ONE PER ROW — the issueLinkNotes rule, not the droppedObjectNotes
// one. Two spellings of the PARENT are one dropped reference; `Custom field (Severity)` and
// `Custom field (Test level)` are two different fields holding two different values, and collapsing
// them would tell an operator one field was lost when four were. The bound still holds by
// construction: renderWarnings groups on (field, via) and shows at most maxWarningExemplars
// distinct values with a summary line for the rest, so the 345 spellings this corpus carries cannot
// flood a report.
func TestCustomFields_TwoFieldsOnOneRowAreTwoNotes(t *testing.T) {
	const header = "Issue key,Summary,Status,Custom field (Severity),Custom field (Test level)"
	const row = "JRA-1,Ticket one,To Do,Blocker,L2"

	ns := notesFor(mapRow(t, jiraRowMapper, header, row).notes, fieldCustomFieldObj)
	if len(ns) != 2 {
		t.Fatalf("a row carrying TWO custom-field values produced %d note(s), want 2: %+v", len(ns), ns)
	}
	got := []string{ns[0].Value, ns[1].Value}
	sort.Strings(got)
	if got[0] != "custom field (severity)" || got[1] != "custom field (test level)" {
		t.Errorf("notes name %v, want both spellings", got)
	}
}

// ⚠⚠ A JIRA CSV-ALL-FIELDS EXPORT REPEATS A CUSTOM FIELD ONCE PER VALUE — `Custom field (External
// issue ID)` reaches 40 occurrences in one real header — and columnIndex.get names the FIRST
// (csv.go:442). The gate is therefore ANY occurrence, exactly as it is for `Comment` and for the
// link columns: a row whose first cell is empty and whose third holds the value still reports.
func TestCustomFields_ARepeatedColumnIsOneNoteFromAnyOccurrence(t *testing.T) {
	const header = "Issue key,Summary,Status,Custom field (Sprint marker),Custom field (Sprint marker),Custom field (Sprint marker)"
	const row = "JRA-1,Ticket one,To Do,,,the third cell holds it"

	ns := notesFor(mapRow(t, jiraRowMapper, header, row).notes, fieldCustomFieldObj)
	if len(ns) != 1 {
		t.Fatalf("a row whose THIRD cell of one repeated custom field holds the value produced "+
			"%d note(s), want exactly 1: %+v", len(ns), ns)
	}
	if ns[0].Value != "custom field (sprint marker)" {
		t.Errorf("note names %q, want the single spelling once", ns[0].Value)
	}
}

// ⚠ `Custom field (Epic Link)` IS ALREADY REPORTED, AS A PARENT REFERENCE (csv_unread_refs.go), and
// one dropped value must not produce two sentences in two vocabularies. It is the ONLY custom-field
// spelling any other entry in this package claims — measured, not assumed: see
// TestCustomFields_TheExclusionListIsExactlyWhatAnotherEntryClaims.
func TestCustomFields_EpicLinkIsReportedOnceAsAParentNotTwice(t *testing.T) {
	const header = "Issue key,Summary,Status,Custom field (Epic Link)"
	const row = "JRA-1,Ticket one,To Do,JRA-100"

	all := mapRow(t, jiraRowMapper, header, row).notes
	if ns := notesFor(all, fieldCustomFieldObj); len(ns) != 0 {
		t.Errorf("the epic link produced a custom-field note as well as its parent note: %+v", ns)
	}
	if ns := notesFor(all, fieldParentRef); len(ns) != 1 {
		t.Fatalf("the epic link produced %d parent note(s), want 1 — the exclusion must not have "+
			"silenced the report that already existed", len(ns))
	}
}

// The exclusion list is derived from the tables, never hand-copied: an entry that starts claiming
// another custom-field spelling tomorrow must not silently double-report.
func TestCustomFields_TheExclusionListIsExactlyWhatAnotherEntryClaims(t *testing.T) {
	claimed := map[string]bool{}
	for _, r := range jiraUnreadRefs {
		for _, c := range r.columns {
			if strings.HasPrefix(strings.ToLower(c), jiraCustomFieldPrefix) {
				claimed[strings.ToLower(c)] = true
			}
		}
	}
	for _, o := range jiraObjectColumns {
		for _, c := range o.columns {
			if strings.HasPrefix(strings.ToLower(c), jiraCustomFieldPrefix) {
				claimed[strings.ToLower(c)] = true
			}
		}
	}
	if len(claimed) != len(jiraCustomFieldsReportedElsewhere) {
		t.Fatalf("the exclusion list holds %d spelling(s) and the tables claim %d: %v vs %v",
			len(jiraCustomFieldsReportedElsewhere), len(claimed),
			jiraCustomFieldsReportedElsewhere, claimed)
	}
	for c := range claimed {
		if !jiraCustomFieldsReportedElsewhere[c] {
			t.Errorf("%q is claimed by another entry and is NOT excluded — it will be reported twice", c)
		}
	}
}

// ⚠ THE PREFIX IS JIRA'S OWN AND THE RULE IS A PREFIX, NOT A CONTAINS. `Custom field (Time Spent)`
// exists in this corpus on 20 rows and holds a DATE, not logged work — csv_dropped_objects.go
// already says so, and it is why THAT entry lists exact spellings. Here the direction is reversed:
// the custom-field lens must claim `Custom field (Time Spent)` and must NOT claim the bare
// `Time Spent` the object table owns.
func TestCustomFields_TheBareSpellingBelongsToTheObjectTableNotHere(t *testing.T) {
	const header = "Issue key,Summary,Status,Time Spent,Custom field (Time Spent)"
	const row = "JRA-1,Ticket one,To Do,3600,12/Mar/24 9:41 AM"

	all := mapRow(t, jiraRowMapper, header, row).notes
	cf := notesFor(all, fieldCustomFieldObj)
	if len(cf) != 1 || cf[0].Value != "custom field (time spent)" {
		t.Fatalf("custom-field notes = %+v, want exactly the parenthesised spelling", cf)
	}
	if ns := notesFor(all, fieldLoggedTimeObj); len(ns) != 1 || ns[0].Value != "Time Spent" {
		t.Errorf("logged-time notes = %+v, want the bare column still reported by its own entry", ns)
	}
}

// ⚠ JIRA ONLY, AND MEASURED: zero of the 46 cached real Linear exports carry a column starting
// `Custom field (` — Linear's CSV export has no custom-field columns at all. A Linear table here
// could only ever fire on a fabricated header, which is the same argument csv_dropped_objects.go
// makes for comments and logged time.
func TestCustomFields_TheLinearMapperDoesNotReportThem(t *testing.T) {
	const header = "ID,Title,Status,Custom field (Severity)"
	const row = "ENG-1,Ticket one,Todo,Blocker"

	if ns := notesFor(mapRow(t, linearRowMapper, header, row).notes, fieldCustomFieldObj); len(ns) != 0 {
		t.Errorf("the Linear mapper reported a custom field on a header no real Linear export "+
			"carries: %+v", ns)
	}
}

// The spellings are discovered from the header, so their ORDER is a map's unless it is sorted —
// and two runs of one import whose warnings differ only in order cannot be diffed, which is the
// property renderWarnings sorts for in the first place. Same pin as jiraIssueLinkSpellings.
func TestCustomFields_SpellingsAreSortedAndLowercased(t *testing.T) {
	ci := buildIndex([]string{"Issue key", "Custom field (Zebra)", "Custom field (Alpha)", "Summary"})
	got := jiraCustomFieldSpellings(ci)
	want := []string{"custom field (alpha)", "custom field (zebra)"}
	if len(got) != len(want) {
		t.Fatalf("spellings = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("spellings = %v, want %v", got, want)
		}
	}
}

// The rendered line must name BOTH ends — the export's own column spelling and the Track object it
// never became — for the reason viaColumnNotRead's line does: either half alone sends the reader to
// the wrong place. And it must not claim a MAPPING was attempted; nothing was unrecognised here.
func TestCustomFields_TheRenderedLineNamesTheColumnAndTheTrackObject(t *testing.T) {
	line := FieldNote{
		Field: fieldCustomFieldObj,
		Value: "custom field (severity)",
		Via:   viaCustomFieldNotCreated,
	}.render(7)

	// ⚠ THE OBJECT CLAUSE IS ASSERTED WHOLE, AND THAT IS NOT FUSSINESS — IT IS THIS TEST'S OWN
	// POSITIVE CONTROL FINDING. The first version of this assertion looked for the substring
	// "custom field" and control C10 (rename fieldCustomFieldObj to "widget") LEFT IT GREEN: the
	// rendered line still contained "custom field" because the COLUMN NAME supplies it in the other
	// half of the sentence. The assertion could not fail on the thing it claimed to check. Naming
	// the whole clause is what makes the object half falsifiable independently of the column half.
	for _, want := range []string{
		`7 issue(s)`,
		`"custom field (severity)"`,
		"no Track custom field is created for it and no custom field value is stored",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("rendered line %q does not name %q", line, want)
		}
	}
	if strings.Contains(line, "unrecognised") {
		t.Errorf("rendered line claims a value was unrecognised: %q", line)
	}
}
