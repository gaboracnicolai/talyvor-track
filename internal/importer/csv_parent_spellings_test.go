package importer

import (
	"strings"
	"testing"
)

// csv_parent_spellings_test.go — THE UNREAD-REFERENCE REPORT WAS KEYED ON ONE SPELLING PER TRACK
// REFERENCE, AND JIRA WRITES THE PARENT LINK UNDER MORE THAN ONE.
//
// ⚠⚠ THE FINDING, MEASURED WHOLE-POPULATION OVER THE 302 GENUINE JIRA EXPORTS BEFORE A LINE OF THIS
// FILE EXISTED (~/talyvor-queue/w34-parentspell-census-8c53.py). `jiraUnreadRefs` shipped
// `{"Parent", fieldParentRef}` and `columnIndex` matches a header EXACTLY (lower-cased, trimmed), so
// the report can only ever see a column spelled `Parent`:
//
//	rows carrying a parent reference in ANY spelling   9,096 of 18,807 (48.4%)
//	rows the shipped `Parent` spelling reports             6,641
//	rows carrying one and reported by NOTHING              2,455  ← 27.0% of the population
//
// 116 of the 302 exports carry a resolvable parent reference and NO `Parent` column at all. The
// import writes `parent_id` NULL on every one of those rows and the job says `succeeded imported=N`
// with an empty warnings list — the same shape #116 found for the fifth reference, one spelling down.
//
// ⚠ AND THE LEAD THIS CAME FROM WAS HALF WRONG IN BOTH DIRECTIONS, WHICH IS WHY THE SPELLINGS BELOW
// ARE A MEASUREMENT AND NOT A NAME MATCH.
//
//	(a) `Parent key` (2,926 populated, 42 files) is NOT a coverage gap. It is populated on 2,926 rows
//	    and `Parent` is populated on every one of them — BLIND ROWS: 0. Adding it would emit a SECOND
//	    note about a loss already reported and change coverage by nothing. Same for `Parent summary`
//	    (0 blind of 6,641).
//	(b) `Parent` DOES NOT HOLD AN ISSUE KEY. 6,617 of its 6,641 populated cells are Jira's internal
//	    numeric id (`10402`); `Parent key` holds the `PM-92` shape Track's identifier column holds.
//	    That matters for a future MAPPING, not for this report — a dropped reference is dropped in
//	    either encoding — and it is recorded here so nobody derives a join key from the wrong column.
//
// ⚠ WHAT IS EXCLUDED AND WHY, ALL FOUR REASONS MEASURED RATHER THAN ARGUED:
//
//	· NAME-ONLY columns carry the parent's TEXT, not a reference to it: `Parent summary`,
//	  `Epic Link Summary` (3,485 populated), `Custom field (Epic Name)`. Measured co-occurrence:
//	  `Epic Link Summary` is NEVER populated without `Custom field (Epic Link)` (3,485 of 3,485
//	  together, 0 alone) and `Parent summary` never without `Parent` (0 alone). Excluding them costs
//	  EXACTLY ZERO rows, which is the only reason the exclusion needs no argument.
//	· NON-NATIVE PROVENANCE — this is the `issuekey` trap of the queue's own census one step over.
//	  `Epic Issue Key` (687), `Epic` (687), `Parent Story` (186) and bare `Epic Link` (64) look like
//	  the biggest remaining spellings, and NOT ONE of the 5 files carrying them is a Jira export.
//	  Jira writes custom fields as `Custom field (X)` and always emits the `Issue id` surrogate; over
//	  the 302 files, 282 are native-shaped by that test and those 5 are not — they are hand-built
//	  spreadsheets (`Acceptance Criteria`, `Story Points` unprefixed). The test is not vacuous:
//	  `Parent` itself appears in 117 native and 2 non-native files, so it separates provenance rather
//	  than merely restating the column. 707 rows measured, deliberately not taken.
//	· ISSUE-LINK columns are a different Track object. `Outward issue link (Parent-Child)` (2 blind
//	  rows) belongs with Linear's `Blocked by` (617 blind) and `Related to` (648 blind) against
//	  Track's `issue_relations` table — a second finding with its own population, written up in the
//	  queue rather than smuggled in under fieldParentRef.
//
// ⚠ SO THIS MERGE ADDS TWO SPELLINGS AND MOVES THE REPORT FROM 6,641 TO 8,407 ROWS: +1,766 in 64
// files (+26.6%). `Custom field (Epic Link)` — 3,630 populated in 163 files, 163 of 163
// native-shaped, 1,748 blind — and `Parent id` — 19 populated in 5 files, 5 of 5 native-shaped, 18
// blind. Both are Jira's own export spelling for the parent link; neither needs a join key or a
// policy, because this reports and does not map.

// ─── the spelling the report could not see ──────────────────────────────────

// The whole finding in one case: 163 exports carry the Epic Link and 116 exports carry NO `Parent`
// column, so on 1,748 real rows the parent link arrived, landed nowhere, and nothing said so.
func TestJiraCSV_TheEpicLinkSpellingOfTheParentReferenceIsReported(t *testing.T) {
	// Column order and spellings verbatim from corpus file b16ab58dd255afa7cd0fffcaa706d8f9,
	// trimmed to the columns this question needs. There is no `Parent` column — that is the shape
	// 116 of the 302 exports have.
	const header = "Summary,Issue key,Status,Priority,Assignee,Custom field (Epic Link)"
	const row = "Ticket one,JRA-1,To Do,High,ada@example.com,PENT004039-3"

	m := mapRow(t, jiraRowMapper, header, row)

	ns := notesFor(m.notes, fieldParentRef)
	if len(ns) != 1 {
		t.Fatalf("a populated %q column produced %d %s notes, want exactly 1 — the parent link "+
			"arrived, parent_id is NULL, and the import reports itself clean on 1,748 real rows",
			"Custom field (Epic Link)", len(ns), fieldParentRef)
	}
	if ns[0].Via != viaColumnNotRead {
		t.Errorf("%s note arrived via %q, want %q", fieldParentRef, ns[0].Via, viaColumnNotRead)
	}
	// The note must name the EXPORT'S OWN spelling. An operator told to look at "Parent" cannot find
	// that word anywhere in an export whose column is `Custom field (Epic Link)` — the whole reason
	// FieldNote.Value carries the column rather than the field.
	if ns[0].Value != "Custom field (Epic Link)" {
		t.Errorf("the note names column %q; the export's column is %q and that is the only string "+
			"the operator can search for", ns[0].Value, "Custom field (Epic Link)")
	}
	// The report must never be mistaken for the fix.
	if m.issue.ParentID != nil {
		t.Fatalf("A TRANSPORT NOW FILLS parent_id from %q (%v) — that is the mapping the queue "+
			"defers for want of a no-match policy, and its entry must leave the report WITH the change",
			"Custom field (Epic Link)", m.issue.ParentID)
	}
}

// The second shape, and the one a header-level fix would miss: the export CARRIES `Parent` and the
// cell is EMPTY on this row while the Epic Link holds the link. 1,882 corpus rows populate both and
// 1,748 populate only the Epic Link, so the two columns are not interchangeable per row.
func TestJiraCSV_AnEmptyParentCellDoesNotHideAPopulatedEpicLink(t *testing.T) {
	const header = "Summary,Issue key,Status,Parent,Parent key,Custom field (Epic Link)"
	const row = "Ticket one,JRA-1,To Do,,,PENT034001-15"

	m := mapRow(t, jiraRowMapper, header, row)

	ns := notesFor(m.notes, fieldParentRef)
	if len(ns) != 1 {
		t.Fatalf("with `Parent` present-but-empty and %q populated, the row produced %d %s notes, "+
			"want exactly 1 — a report gated on the FIRST spelling's cell goes silent here",
			"Custom field (Epic Link)", len(ns), fieldParentRef)
	}
	if ns[0].Value != "Custom field (Epic Link)" {
		t.Errorf("the note names %q; the populated column on this row is %q", ns[0].Value, "Custom field (Epic Link)")
	}
}

// The old Server/DC spelling. Small (19 cells, 5 files) and kept because all 5 are native-shaped
// exports and it is the same field in the same numeric encoding as `Parent`.
func TestJiraCSV_TheParentIdSpellingOfTheParentReferenceIsReported(t *testing.T) {
	// Verbatim column order from corpus file 2fae5e75030cbe00f25c63f8019371ca, trimmed. Note
	// `Issue id` sits beside `Parent id`: the index matches a header EXACTLY, so the two cannot be
	// confused, and this case is what proves it.
	const header = "Summary,Issue key,Issue id,Parent id,Status"
	const row = "Ticket one,JRA-1,5125200,5125217,To Do"

	m := mapRow(t, jiraRowMapper, header, row)

	ns := notesFor(m.notes, fieldParentRef)
	if len(ns) != 1 {
		t.Fatalf("a populated `Parent id` column produced %d %s notes, want exactly 1", len(ns), fieldParentRef)
	}
	if ns[0].Value != "Parent id" {
		t.Errorf("the note names %q, want %q", ns[0].Value, "Parent id")
	}
}

// ─── one loss is one line ───────────────────────────────────────────────────

// ⚠ THIS PASSED FROM THE START AND IS THE MUST-STAY-GREEN COMPANION. The tally in run() keys on the
// whole FieldNote, so a report that fired once per matching SPELLING would tell an operator two
// things about one dropped reference — "carried a \"Parent\" value" and "carried a \"Parent key\"
// value" — on each of the 2,926 corpus rows that populate both. Exactly one note, naming the FIRST
// declared spelling, which keeps today's output byte-identical on every row `Parent` already covered.
func TestJiraCSV_TwoSpellingsOfTheParentReferenceProduceOneNote(t *testing.T) {
	const header = "Summary,Issue key,Status,Parent,Parent key,Custom field (Epic Link)"
	const row = "Ticket one,JRA-1,To Do,10402,PM-92,PENT004039-3"

	m := mapRow(t, jiraRowMapper, header, row)

	ns := notesFor(m.notes, fieldParentRef)
	if len(ns) != 1 {
		t.Fatalf("a row populating THREE spellings of the parent reference produced %d %s notes, "+
			"want exactly 1 — one dropped reference is one line", len(ns), fieldParentRef)
	}
	if ns[0].Value != "Parent" {
		t.Errorf("the note names %q; the first declared spelling is %q, and preferring it is what "+
			"keeps this report's output unchanged on the 6,641 rows it already covered", ns[0].Value, "Parent")
	}
}

// ─── the gates the new spellings must not widen ─────────────────────────────

// The per-CELL gate, on the new spellings. An export carrying an Epic Link column and no epic links
// has lost nothing; a warning on every such import is one an operator learns to skip. 2,788 of the
// 6,418 rows that carry `Custom field (Epic Link)` have an empty cell.
func TestJiraCSV_AnEmptyEpicLinkCellIsNotReported(t *testing.T) {
	m := mapRow(t, jiraRowMapper,
		"Summary,Issue key,Status,Parent,Parent id,Custom field (Epic Link)",
		"Ticket one,JRA-1,To Do,,,")
	if ns := notesFor(m.notes, fieldParentRef); len(ns) != 0 {
		t.Errorf("every parent spelling empty produced %d %s note(s) — the provider sent nothing",
			len(ns), fieldParentRef)
	}
}

// The ABSENT-column gate. 139 of the 302 exports carry no parent spelling at all, and that is a
// narrower export rather than a dropped value — this package reports absent columns through its own
// viaNo*Column vocabulary and must not conflate the two.
func TestJiraCSV_AnExportWithNoParentSpellingAtAllIsSilent(t *testing.T) {
	m := mapRow(t, jiraRowMapper, "Summary,Issue key,Status", "Ticket one,JRA-1,To Do")
	if ns := notesFor(m.notes, fieldParentRef); len(ns) != 0 {
		t.Errorf("an export carrying NO parent spelling produced %d %s note(s)", len(ns), fieldParentRef)
	}
}

// ⚠ THE EXCLUSION, ASSERTED IN THE INVERSE DIRECTION SO IT STAYS A DECISION. A column holding the
// parent's NAME is not a reference to the parent, and the three name-only spellings are measured to
// add exactly zero rows (see the file header). If someone reads `Epic Link Summary` as a parent link
// because the words match, this reds — and it is the one direction the corpus census cannot check,
// because a census only ever asks about columns that ARE in the table.
func TestJiraCSV_AParentNameColumnIsNotAParentReference(t *testing.T) {
	const header = "Summary,Issue key,Status,Epic Link Summary,Custom field (Epic Name),Parent summary"
	const row = "Ticket one,JRA-1,To Do,Hardware,Profile Management,Create chassis"

	m := mapRow(t, jiraRowMapper, header, row)

	if ns := notesFor(m.notes, fieldParentRef); len(ns) != 0 {
		t.Errorf("a row whose only populated parent-ish columns hold the parent's NAME produced %d "+
			"%s note(s) naming %q — a name is not a reference, and all three of these spellings are "+
			"measured never to be populated without their key twin (0 rows gained)",
			len(ns), fieldParentRef, ns[0].Value)
	}
}

// ─── the table's own floors ─────────────────────────────────────────────────

// A spelling that serves TWO entries would report one dropped value twice, and a spelling repeated
// inside one entry would make the preference order meaningless. Neither is reachable by reading the
// table — both are one map away.
func TestUnreadRefTables_NoSpellingIsDeclaredTwice(t *testing.T) {
	for _, tc := range []struct {
		name string
		refs []unreadRef
	}{
		{"linear", linearUnreadRefs},
		{"jira", jiraUnreadRefs},
	} {
		seen := map[string]string{} // lowercased spelling -> the field that already claimed it
		for _, r := range tc.refs {
			if len(r.columns) == 0 {
				t.Errorf("%s table has an entry for %q with NO spellings — it can never fire", tc.name, r.field)
				continue
			}
			for _, c := range r.columns {
				k := strings.ToLower(strings.TrimSpace(c))
				if k == "" {
					t.Errorf("%s table entry %q declares an empty spelling", tc.name, r.field)
					continue
				}
				if prev, dup := seen[k]; dup {
					t.Errorf("%s table declares spelling %q for BOTH %q and %q — one populated cell "+
						"would produce two warnings about one dropped value", tc.name, c, prev, r.field)
					continue
				}
				seen[k] = r.field
			}
		}
	}
}

// ⚠ THE FORWARD RULE, AND THE ONE THAT MADE THIS MERGE PROVE ITS OWN SPELLINGS. Every spelling in
// every entry must appear in a VERBATIM header line from a real export. Without it, a spelling can
// be added from a plausible-sounding name and the report for that reference silently never fires —
// which is a defect shaped exactly like the one this file fixes, pointing the other way.
//
// This is deliberately a floor on EXISTENCE and not a count: a count pinned here goes red when the
// corpus grows, with nothing wrong.
func TestUnreadRefTables_EverySpellingAppearsInARealExportHeader(t *testing.T) {
	// Verbatim header lines from /tmp/w34-jira-corpus and /tmp/w34-linear-corpus-cache, each named
	// by the corpus file it came from so the provenance is checkable rather than asserted.
	const (
		// 30-column Linear export shape.
		linearHeader30 = "ID,Team,Title,Description,Status,Estimate,Priority,Project ID,Project,Creator,Assignee,Labels,Cycle Number,Cycle Name,Cycle Start,Cycle End,Created,Updated,Started,Triaged,Completed,Canceled,Archived,Due Date,Parent issue,Initiatives,Project Milestone ID,Project Milestone,SLA Status,Roadmaps"
		// 9aa6d1aeb71c057f7921d14ce5261661 — the modern Jira Cloud shape, and the only one of these
		// two that carries `Parent`.
		jiraHeaderParent = "Summary,Issue key,Issue id,Issue Type,Status,Project key,Project name,Project type,Project lead,Project lead id,Priority,Reporter,Creator,Created,Updated,Sprint,Parent,Parent key,Parent summary,Status Category,Status Category Changed"
		// 2fae5e75030cbe00f25c63f8019371ca — carries BOTH new spellings and NO `Parent` column, which
		// is the shape 116 of the 302 exports have. Trimmed at `Sprint`; the tail is 60 more
		// Comment/Attachment/Watchers columns.
		//
		// ⚠ THERE WAS A THIRD LITERAL HERE AND THE FLOOR BELOW DELETED IT. b16ab58dd255afa7cd0fffcaa706d8f9
		// was included as a typical Epic Link export — one of the 163 — and the load-bearing check
		// found it justified no spelling this header does not already carry. Three literals where two
		// suffice is not extra safety: it reads as provenance from a wider sample than was actually
		// consulted. BREADTH is the census's job (163 files for the Epic Link, 5 for `Parent id`);
		// EXISTENCE IN A REAL HEADER is this floor's, and one real header settles that.
		jiraHeaderParentID = "Summary,Issue key,Issue id,Parent id,Issue Type,Status,Project key,Resolution,Assignee,Reporter,Creator,Created,Updated,Resolved,Fix Version/s,Component/s,Labels,Description,Watchers,Outward issue link (Blocks),Outward issue link (Parent-Child),Attachment,Custom field (Acceptance Criteria),Custom field (Epic Colour),Custom field (Epic Link),Custom field (Epic Name),Custom field (Epic Status),Sprint"
	)

	check := func(name string, headers []string, refs []unreadRef) {
		have := map[string]bool{}
		for _, header := range headers {
			for _, h := range strings.Split(header, ",") {
				have[strings.ToLower(strings.TrimSpace(h))] = true
			}
		}
		for _, r := range refs {
			for _, c := range r.columns {
				if !have[strings.ToLower(strings.TrimSpace(c))] {
					t.Errorf("%s table names spelling %q, which is in no real export header here — "+
						"that spelling of the %s report can never fire", name, c, r.field)
				}
			}
		}
		if len(refs) == 0 {
			t.Errorf("%s table is empty — the report is structurally silent", name)
		}
	}
	check("linear", []string{linearHeader30}, linearUnreadRefs)
	check("jira", []string{jiraHeaderParent, jiraHeaderParentID}, jiraUnreadRefs)

	// ⚠ AND THE FLOOR ON THE FLOOR. Every header literal above must be LOAD-BEARING: if one of them
	// contributes no spelling that the others do not already carry, it is decoration, and the next
	// person to add a spelling will believe it was checked against a wider corpus than it was.
	jira := []string{jiraHeaderParent, jiraHeaderParentID}
	declared := map[string]bool{}
	for _, r := range jiraUnreadRefs {
		for _, c := range r.columns {
			declared[strings.ToLower(strings.TrimSpace(c))] = true
		}
	}
	for i := range jira {
		others := map[string]bool{}
		for j, h := range jira {
			if j == i {
				continue
			}
			for _, c := range strings.Split(h, ",") {
				others[strings.ToLower(strings.TrimSpace(c))] = true
			}
		}
		unique := 0
		for _, c := range strings.Split(jira[i], ",") {
			k := strings.ToLower(strings.TrimSpace(c))
			if declared[k] && !others[k] {
				unique++
			}
		}
		if unique == 0 {
			t.Errorf("jira header literal %d justifies no spelling the other headers do not already "+
				"carry — it reads as extra provenance and supplies none", i)
		}
	}
}
