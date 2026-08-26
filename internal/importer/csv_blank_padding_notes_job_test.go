package importer_test

// csv_blank_padding_notes_job_test.go — A CELL HOLDING ONLY A SPACE IS NOT A POPULATED CELL, AND
// THE ONLY THING THAT MAKES THAT TRUE IS `columnIndex.getAll`'s `strings.TrimSpace`. Before this
// file the whole 39-package suite stayed green with that trim deleted.
//
// This is finding (e) of #189, ridden on its own diff. #189 guarded the two trims that decide what
// LANDS in the issues table (`get`'s cell trim and `buildIndex`'s header trim) and deliberately
// left this one, because its consequence is different in kind: `splitLabels` re-trims and drops
// empties, so Labels are unaffected. What moves is the three gates that ask "does this row POPULATE
// this column?" —
//
//	csv_dropped_objects.go:90   len(ci.getAll(row, col)) == 0    Comment · Time Spent
//	csv_custom_fields.go:171    len(ci.getAll(row, column)) == 0 Custom field (…)
//	csv_issue_links.go:131      len(ci.getAll(row, column)) == 0 Outward/Inward issue link (…)
//
// ⚠ WHY THAT IS A DEFECT AND NOT COSMETICS: THE NOTE CARRIES A COUNT, THE COUNT IS SHOWN TO THE
// CUSTOMER, AND IT IS STORED. Warnings are persisted on the job row (migration 0026) and rendered
// as "N issue(s) carried a %q value this importer does not read". MEASURED end to end through the
// shipped runner on real Postgres, on a three-row Jira export where exactly ONE row carries a real
// comment, one carries " " and one carries nothing:
//
//	trim present:  1 issue(s) carried a "Comment" value …    ← true
//	trim removed:  2 issue(s) carried a "Comment" value …    ← FALSE, and the same for "Time Spent"
//
// The inflated row is a blank padding cell. A Jira "csv-all-fields" export pads every row out to
// the width of the most-commented issue in the result set — 69 `Comment` columns in one measured
// real header — so padding cells are the NORM on this transport, and whether they arrive as "" or
// as " " is the exporter's choice, not the customer's.
//
// ⚠⚠ THE SHAPE OF THE GAP, MEASURED RATHER THAN ASSERTED, AND IT IS THE REUSABLE PART OF THIS
// FILE. Each of the three gates ALREADY HAS a test for this exact question — one per gate, named
// in as many words:
//
//	TestDroppedObjects_AnEmptyCellIsSilent
//	TestCustomFields_AnEmptyCellIsSilent
//	TestIssueLinks_AnEmptyCellIsSilent
//
// They are real guards and they work: control C5 drops the empty-cell FILTER while keeping the
// trim, and all three go red (plus four job-level tests). What NONE of them has is a
// whitespace-only sibling. `""` is guarded three times over; `" "` is guarded nowhere — and `" "`
// is the value a padding cell actually arrives as. THE GUARD WAS WRITTEN FOR THE OBVIOUS EMPTY
// VALUE AND NOT FOR THE ONE THE EXPORT EMITS. That is the question worth carrying to the next
// gate in any repo: not "is the empty case covered" but "is the case this producer really sends
// covered".
//
// ⚠ AND IT CONTRADICTS THE CONSTANT'S OWN DOCUMENTED MEANING. csv_dropped_objects.go:53 defines
// viaObjectNotCreated as "the export CARRIED the column, THE CELL WAS POPULATED, and the Track
// object that value belongs to is never created". Without the trim the second clause is false for
// every note the padding produces: the operator is told Track dropped a comment their export never
// contained, on an import that was otherwise clean.

import (
	"context"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/testutil"
)

// Three rows against every gate at once. Row 1 POPULATES each column for real; row 2 carries a
// single space in each; row 3 carries an empty cell in each. Only row 1 may be counted.
//
// The spellings are the shipped rules, not invented: `Comment` and `Time Spent` are
// jiraObjectColumns' exact names; `Custom field (` is jiraCustomFieldPrefix; `Outward issue link (`
// is one of jiraIssueLinkPrefixes.
const blankPaddingJiraExport = "Issue key,Summary,Status,Comment,Time Spent," +
	"Custom field (Severity),Outward issue link (Blocks)\n" +
	"PROJ-1,One,Done,a real comment,3600,High,PROJ-9\n" +
	"PROJ-2,Two,Done,\" \",\" \",\" \",\" \"\n" +
	"PROJ-3,Three,Done,,,,\n"

// renderedColumnNames — THE SPELLINGS THE OPERATOR ACTUALLY SEES, WHICH ARE NOT THE HEADER'S.
//
// ⚠ THIS LIST WAS FIRST WRITTEN AS THE HEADER SPELLINGS AND THE TEST WENT RED ON THE LAST TWO,
// WHICH IS THE `-1` BRANCH OF countedIn EARNING ITS PLACE: it separates "no warning names this
// column at all" from "the count is wrong", and only the first answer could have told me the
// spelling was different rather than the gate silent. Measured off the stored job row:
//
//	"Comment"                      droppedObjectNotes takes the value from jiraObjectColumns,
//	"Time Spent"                   a Go literal, so the header's own casing survives
//	"custom field (severity)"      customFieldNotes and issueLinkNotes derive the value from
//	"outward issue link (blocks)"  columnIndex's KEYS, which buildIndex lowercases
//
// Both are deliberate and documented in their own files (csv_issue_links.go:125 says so in as many
// words). The INCONSISTENCY between the two families is real — one warning list shows the customer
// `"Comment"` and `"custom field (severity)"` side by side — and is recorded, not fixed here.
var renderedColumnNames = []string{
	"Comment", "Time Spent", "custom field (severity)", "outward issue link (blocks)",
}

// countedIn pulls the leading integer out of the one stored warning naming `column`. Returns -1
// when no warning names it at all, which is a DIFFERENT answer from 0 and is asserted as such.
func countedIn(warnings []string, column string) int {
	for _, w := range warnings {
		if !strings.Contains(w, `"`+column+`"`) {
			continue
		}
		n := 0
		for i := 0; i < len(w) && w[i] >= '0' && w[i] <= '9'; i++ {
			n = n*10 + int(w[i]-'0')
		}
		return n
	}
	return -1
}

func TestBlankPaddingCell_IsNotCountedAsAPopulatedColumn(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	js := importer.NewJobStore(d.Pool)
	runner := importer.NewRunner(js, importer.New(issue.NewStore(d.Pool)))
	id, err := js.Create(ctx, ws.ID, team.ID, "jira_csv", []byte(blankPaddingJiraExport))
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if did, err := runner.RunOnce(ctx); err != nil || !did {
		t.Fatalf("RunOnce did=%v err=%v", did, err)
	}
	job, err := js.Get(ctx, id)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}

	// PREMISE, asserted rather than assumed: all three rows imported. A fixture that imported one
	// row would satisfy "the count is 1" for the worst possible reason.
	if job.Status != importer.JobSucceeded || job.Imported != 3 {
		t.Fatalf("premise: job = %s imported=%d failed=%d %q, want succeeded/3",
			job.Status, job.Imported, job.Failed, job.ErrorSummary)
	}

	// ⚠ THE WARNINGS ARE READ BACK OFF THE JOB ROW, not off the mapper's return value: they are
	// persisted (migration 0026) and this is the shape the operator actually sees.
	for _, column := range renderedColumnNames {
		got := countedIn(job.Warnings, column)
		switch got {
		case 1:
			// exactly the one row that carries a value
		case -1:
			t.Errorf("no stored warning names %q at all.\n"+
				"Row 1 genuinely populates it, so the note MUST fire — this test cannot tell a "+
				"correct count from a silent gate unless it does.\nwarnings: %q", column, job.Warnings)
		default:
			t.Errorf("the stored warning for %q counts %d issue(s); exactly ONE row of this export "+
				"carries a value there. The others hold \" \" and \"\".\n"+
				"columnIndex.getAll trims each cell and drops the empties; without that trim a blank "+
				"padding cell reads as populated, and the customer is told Track dropped %d of their "+
				"objects when it dropped %d. viaObjectNotCreated's own definition says the cell was "+
				"POPULATED.\nwarnings: %q", column, got, got, 1, job.Warnings)
		}
	}
}

// TestBlankPaddingCell_TheGoodRowIsStillReported is the other direction, and it is a separate test
// on purpose: a gate that reported NOTHING would satisfy every "not 2" assertion above. A trim that
// swallowed real values would be a worse bug than the one this file guards.
func TestBlankPaddingCell_TheGoodRowIsStillReported(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	js := importer.NewJobStore(d.Pool)
	runner := importer.NewRunner(js, importer.New(issue.NewStore(d.Pool)))
	// The SAME export with row 1 alone — the row that really carries every value.
	only := "Issue key,Summary,Status,Comment,Time Spent," +
		"Custom field (Severity),Outward issue link (Blocks)\n" +
		"PROJ-1,One,Done,a real comment,3600,High,PROJ-9\n"
	id, err := js.Create(ctx, ws.ID, team.ID, "jira_csv", []byte(only))
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if did, err := runner.RunOnce(ctx); err != nil || !did {
		t.Fatalf("RunOnce did=%v err=%v", did, err)
	}
	job, err := js.Get(ctx, id)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.Status != importer.JobSucceeded || job.Imported != 1 {
		t.Fatalf("premise: job = %s imported=%d %q, want succeeded/1",
			job.Status, job.Imported, job.ErrorSummary)
	}
	for _, column := range renderedColumnNames {
		if got := countedIn(job.Warnings, column); got != 1 {
			t.Errorf("a row that genuinely populates %q is counted %d, want 1 — the gate has "+
				"stopped reporting real dropped objects, which loses the operator more than the "+
				"padding ever cost them.\nwarnings: %q", column, got, job.Warnings)
		}
	}
}
