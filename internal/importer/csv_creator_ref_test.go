package importer

import (
	"strings"
	"testing"

	"github.com/talyvor/track/internal/model"
)

// csv_creator_ref_test.go — the FIFTH reference, and the one whose loss does not look like the
// other four.
//
// ⚠⚠ THE FINDING, MEASURED WITH THE SHIPPED READER OVER BOTH REAL CORPORA BEFORE A LINE OF THIS
// FILE EXISTED. csv_unread_refs.go reports four populated-but-unread reference columns and states
// its set is "exactly the cross-object references model.Issue declares". model.Issue declares
// FIVE: ProjectID · CycleID · AssigneeID · ParentID · **CreatorID**. The fifth was outside the set
// because the sentence conflated two lists — the refs model.Issue DECLARES, and the refs
// issue.Store guards with assertRefInWorkspace. creator_id is in the first and not the second (it
// is server-stamped, never client-supplied), and being un-guarded is what kept it un-reported.
//
// WHOLE POPULATION, both corpora, the same instrument #113 used (encoding/csv under
// newCSVSource's settings — FieldsPerRecord=-1, TrimLeadingSpace=true, BOM stripped):
//
//	Jira    346 files · 31,103 rows   Creator  291 files · 13,828 of 14,154 rows-with-column (97.7%)
//	                                  Reporter 296 files · 15,540 of 15,881 (97.9%)
//	                                  both populated on 13,797 rows, DIFFERENT on 1,187 of them (8.6%)
//	Linear   46 files ·  3,164 rows   Creator   45 files ·  3,056 of 3,099 (98.6%) — no Reporter column
//
// and the PRODUCT's own answer over the same bytes, before this merge: **0 warnings mentioning
// creator or reporter**, across the 314 Jira and 45 Linear files that DO emit warnings. The zero is
// not an instrument that read nothing — the same run counts the warnings those files do carry.
//
// ⚠ IT IS NOT "left empty" AND THAT IS WHY IT IS A SECOND SENTENCE RATHER THAN TWO MORE TABLE ROWS.
// The four reported refs are nullable columns that end up NULL. issues.creator_id is NOT NULL and
// run() stamps model.ImporterCreatorID on every row it writes — so the value does not go missing,
// it is REPLACED, and every imported issue reads as filed by "importer". Rendering the fifth
// through the existing line would ship a false sentence about a money-free but attribution-bearing
// column; TestCreatorRefWarning_DoesNotClaimTheTrackFieldIsEmpty is the assertion that refuses it.
//
// ⚠ TWO JIRA COLUMNS, ONE TRACK FIELD, AND THE REPORT NAMES BOTH RATHER THAN CHOOSING. Jira's
// Creator (who created the issue) and Reporter (who it is filed on behalf of) DIFFER on 8.6% of the
// rows that carry both, so neither subsumes the other, and WHICH of them should become Track's
// creator_id is a product decision with two defensible answers — the same reason this whole class
// reports rather than maps. Telling the operator both columns arrived and neither was used needs no
// decision at all.

// ─── the fifth reference, both providers ────────────────────────────────────

func TestLinearCSV_APopulatedCreatorColumnIsReported(t *testing.T) {
	// The Linear export's own spelling, from the 30-column shape all 45 real exports carry.
	const header = "ID,Title,Description,Status,Priority,Creator,Assignee"
	const row = "ENG-1,Ticket one,a body,Todo,High,ada@example.com,grace@example.com"

	m := mapRow(t, linearRowMapper, header, row)

	ns := notesFor(m.notes, fieldCreatorRef)
	if len(ns) != 1 {
		t.Fatalf("a populated Creator column produced %d notes, want exactly 1 — the person who "+
			"filed the issue did not reach Track and nothing said so", len(ns))
	}
	if ns[0].Value != "Creator" {
		t.Errorf("the note names source column %q, want %q — an operator told \"creator\" must be "+
			"able to find that word in their export", ns[0].Value, "Creator")
	}
	if ns[0].Via != viaColumnNotReadStamped {
		t.Errorf("creator note arrived via %q, want %q — the stamped path is what makes its "+
			"sentence differ from the four that are left empty", ns[0].Via, viaColumnNotReadStamped)
	}
}

func TestJiraCSV_BothPersonWhoFiledColumnsAreReported(t *testing.T) {
	const header = "Issue key,Summary,Status,Priority,Assignee,Reporter,Creator"
	const row = "JRA-1,Ticket one,To Do,High,ada@example.com,grace@example.com,alan@example.com"

	m := mapRow(t, jiraRowMapper, header, row)

	ns := notesFor(m.notes, fieldCreatorRef)
	if len(ns) != 2 {
		t.Fatalf("a row carrying BOTH Jira person-who-filed columns produced %d creator notes, "+
			"want exactly 2 — they disagree on 8.6%% of the corpus rows that carry both, so one "+
			"line cannot stand for the other", len(ns))
	}
	got := map[string]string{}
	for _, n := range ns {
		got[n.Value] = n.Via
	}
	for _, col := range []string{"Creator", "Reporter"} {
		via, ok := got[col]
		if !ok {
			t.Errorf("no note names the %q column; the operator cannot find what to look at", col)
			continue
		}
		if via != viaColumnNotReadStamped {
			t.Errorf("%s note arrived via %q, want %q", col, via, viaColumnNotReadStamped)
		}
	}
}

// THE PREMISE THE SENTENCE RESTS ON, asserted rather than assumed. If a transport ever maps one of
// these columns onto issue.CreatorID, the report describes a loss that no longer happens.
func TestCreatorRef_TheMappersStillFillNoCreator(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mapper rowMapper
		header string
		row    string
	}{
		{"linear", linearRowMapper, "ID,Title,Status,Creator", "ENG-1,Ticket one,Todo,ada@example.com"},
		{"jira", jiraRowMapper, "Issue key,Summary,Status,Creator,Reporter", "JRA-1,Ticket one,To Do,ada@example.com,grace@example.com"},
	} {
		m := mapRow(t, tc.mapper, tc.header, tc.row)
		if m.issue.CreatorID != "" {
			t.Fatalf("%s: THE MAPPER NOW FILLS CreatorID (%q). That is the fix the queue asks for — "+
				"remove the Creator/Reporter entries from csv_unread_refs.go WITH the change rather "+
				"than leaving a warning about a loss that no longer occurs.", tc.name, m.issue.CreatorID)
		}
	}
}

// ─── the silence is per-cell, not per-column (the fifth, same rule as the four) ───

func TestCreatorRef_AnEmptyCellAndAnAbsentColumnAreSilent(t *testing.T) {
	// The column EXISTS and the cell is empty: the provider sent nothing, so nothing was lost.
	empty := mapRow(t, jiraRowMapper,
		"Issue key,Summary,Status,Creator,Reporter", "JRA-1,Ticket one,To Do,,")
	if ns := notesFor(empty.notes, fieldCreatorRef); len(ns) != 0 {
		t.Errorf("an EMPTY creator/reporter cell was reported as a loss (%d note(s))", len(ns))
	}
	// The export does not carry the columns at all — a different condition with its own vocabulary
	// in this package, and not "a value arrived and was dropped".
	absent := mapRow(t, linearRowMapper, "ID,Title,Status", "ENG-1,Ticket one,Todo")
	if ns := notesFor(absent.notes, fieldCreatorRef); len(ns) != 0 {
		t.Errorf("an ABSENT creator column was reported as a dropped value (%d note(s))", len(ns))
	}
}

// ─── the rendered sentence ──────────────────────────────────────────────────

// THE ASSERTION THAT REFUSES THE OBVIOUS IMPLEMENTATION. Adding the fifth reference to the existing
// table with the existing Via is a two-line change that renders "their Track creator is left
// empty" — and issues.creator_id is NOT NULL and holds "importer" on every imported row, so that
// sentence is false on the one column of the five where the value is replaced rather than dropped.
func TestCreatorRefWarning_DoesNotClaimTheTrackFieldIsEmpty(t *testing.T) {
	n := FieldNote{Field: fieldCreatorRef, Value: "Creator", Via: viaColumnNotReadStamped}
	got := n.render(3056)

	for _, want := range []string{"3056", `"Creator"`, fieldCreatorRef, model.ImporterCreatorID} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered line %q does not carry %q — the operator needs the count, their own "+
				"column name, the Track field, and what Track recorded instead", got, want)
		}
	}
	if strings.Contains(got, "left empty") {
		t.Errorf("the creator line claims the Track field is left empty: %q. It is NOT NULL and "+
			"holds %q on every imported row — the value was replaced, not dropped.",
			got, model.ImporterCreatorID)
	}
	if strings.Contains(got, "unrecognised") {
		t.Errorf("the creator line fell through to the unrecognised-value sentence: %q — nothing "+
			"was unrecognised, the column was never read", got)
	}
}

// The four that ARE left empty must keep saying so. Without this, one shared sentence could be
// edited to fit the fifth and quietly stop describing the other four.
func TestUnreadRefWarning_TheNullableFourStillSayLeftEmpty(t *testing.T) {
	n := FieldNote{Field: fieldAssigneeRef, Value: "Assignee", Via: viaColumnNotRead}
	if got := n.render(1363); !strings.Contains(got, "left empty") {
		t.Errorf("the assignee line no longer says the Track field is left empty: %q", got)
	}
}

// ─── the tables themselves ──────────────────────────────────────────────────

// Every entry must name a Via the renderer HAS A BRANCH FOR. An entry whose via is "" (or any
// value render does not know) falls through to the generic "unrecognised <field> <value>"
// sentence, which is a true-looking line about a column that was never read — the exact confusion
// viaColumnNotRead was split out to prevent.
func TestUnreadRefTables_EveryEntryNamesAViaTheRendererKnows(t *testing.T) {
	for _, tc := range []struct {
		name string
		refs []unreadRef
	}{
		{"linear", linearUnreadRefs},
		{"jira", jiraUnreadRefs},
	} {
		for _, r := range tc.refs {
			if r.via == "" {
				t.Errorf("%s table entry %q names no via", tc.name, r.column)
				continue
			}
			line := FieldNote{Field: r.field, Value: r.column, Via: r.via}.render(7)
			if strings.Contains(line, "unrecognised") {
				t.Errorf("%s table entry %q (via %q) renders through the unrecognised-value "+
					"fallback: %q", tc.name, r.column, r.via, line)
			}
			if !strings.Contains(line, "does not read") {
				t.Errorf("%s table entry %q (via %q) renders a line that does not say the column "+
					"was never read: %q", tc.name, r.column, r.via, line)
			}
		}
	}
}
