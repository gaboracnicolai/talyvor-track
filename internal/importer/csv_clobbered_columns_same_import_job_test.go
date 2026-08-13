package importer_test

// csv_clobbered_columns_same_import_job_test.go — THE CLOBBERED-COLUMN REPORT ON AN EXPORT THAT
// NAMES ONE ISSUE TWICE, driven END TO END on real Postgres through the shipped async runner.
//
// ⚠⚠ THE FINDING, MEASURED THROUGH THIS RUNNER BEFORE THE FIX EXISTED. `SourceRow.NotesIfUpdated`
// carries its own contract in its doc comment — "reported ONLY IF the write turned out to overwrite
// an issue that ALREADY EXISTED" — and run() implements it as `if overwroteExisting`, which is the
// UPDATE branch of UpsertByIdentifier. Those two are not the same predicate. A second row naming an
// identifier THIS SAME JOB inserted seconds earlier also takes the UPDATE branch, so on a
// FIRST-EVER import into an EMPTY workspace the job reported:
//
//	Issue key,Summary,Status,Priority          (no Description column, no Labels column)
//	JRASERVER-64802,Ticket one,To Do,High
//	JRASERVER-64802,Ticket one again,To Do,High
//	⇒ succeeded imported=2, ONE issue in the workspace, and the warnings:
//	    no "Description" column in this export — 1 issue(s) already in Track were re-imported
//	    and had their description overwritten with an empty value; …
//	    no "Labels" column in this export — 1 issue(s) already in Track were re-imported …
//
// Nothing was in Track and nothing was re-imported. The sentence sends an operator looking for an
// earlier import that never happened, and for a stored description that never existed.
//
// ⚠ AND ON A GENUINE RE-IMPORT THE COUNT IS OF ROWS, NOT OF ISSUES, which is the same conflation
// #139 measured (`imported` counts ROWS WRITTEN and a workspace holds ISSUES: 185 rows → 118
// issues on a real Linear export). Two rows naming ONE issue that IS already in Track render
// "2 issue(s) already in Track were re-imported" in a workspace holding one such issue.
//
// ⚠ THE POPULATION IS THE ONE #139 CENSUSED, INTERSECTED WITH #121's: exports naming an issue twice
// are 3 of 305 real Jira exports (71 of 17,923 rows) and 3 of 45 real Linear ones (96 of 3,099),
// and exports carrying no `Labels` column are 203 of 305 (66.6%), no `Description` column 16 of 305.
//
// ⚠ WHAT THE FIX DOES AND DOES NOT DO. It does NOT change a write, a count, or a status: imports
// are byte-for-byte identical. It withholds `NotesIfUpdated` from a row whose write landed on an
// identifier this same job had already written — the exact condition run() ALREADY computes for
// `viaDuplicateInSameImport`. That collision keeps being reported, by the warning built for it, so
// nothing goes unsaid: the assertion below that demands the duplicate line is what makes that claim
// falsifiable rather than a promise in a comment.
//
// ⚠ BOTH TESTS WERE RED BEFORE THE FIX AND THAT IS STILL NOT ENOUGH — a suppression can go too far
// (silence the real report) or not far enough (key on the wrong state) with both of them green.
// scripts/w34-csv-clobbered-columns-same-import-controls.py runs the four mutations that tell those
// apart, each NAMING the tests it expects to red; 4 of 4 caught by exactly those tests, and TWO OF
// THE FOUR PREDICTIONS WERE CORRECTED BY THE RUN rather than by the code (see its C2 and C3).

import (
	"context"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/testutil"
)

// A narrow export (no Description, no Labels) naming ONE issue on TWO rows. The titles differ so
// the second row provably reached the write path — the conflict arm CLOBBERS title, so the stored
// title is the tell.
const jiraCSVNarrowSameKeyTwice = "Issue key,Summary,Status,Priority\n" +
	"JRASERVER-64802,Ticket one,To Do,High\n" +
	"JRASERVER-64802,Ticket one again,To Do,High\n"

// The same shape carrying the columns, so a wide FIRST import can put a real body in Track for the
// count test below to be about.
const jiraCSVWideSingleKey = "Issue key,Summary,Description,Status,Priority,Labels\n" +
	"JRASERVER-64802,Ticket one,the body Jira holds,To Do,High,alpha\n"

// issueTitle reads back the one column the conflict arm clobbers that differs between the two rows
// of the fixture above — the only way to prove the SECOND row took the UPDATE branch.
func issueTitle(t *testing.T, d *testutil.DB, wsID, identifier string) string {
	t.Helper()
	var title string
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT title FROM issues WHERE workspace_id=$1 AND identifier=$2`,
		wsID, identifier).Scan(&title); err != nil {
		t.Fatalf("read back %q: %v", identifier, err)
	}
	return title
}

// TestJobRow_JiraCSV_AFirstImportNamingOneIssueTwiceIsNotReportedAsARe_import — the finding.
//
// TestJobRow_JiraCSV_AFirstNarrowImportIsNotReported already states the property this file
// completes ("a warning that fires when nothing was overwritten is a warning an operator learns to
// ignore"). It holds for a first import of DISTINCT keys and fails for exactly the duplicate-key
// population, which is why it needs its own fixture rather than another assertion on that one.
func TestJobRow_JiraCSV_AFirstImportNamingOneIssueTwiceIsNotReportedAsARe_import(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	job := importJiraCSVInto(t, d, ws.ID, team.ID, jiraCSVNarrowSameKeyTwice)

	// PREMISE, asserted rather than assumed: both rows landed and they collapsed onto ONE issue.
	// Without this the absence of a warning below is satisfied by an import that refused a row.
	if job.Status != importer.JobSucceeded || job.Imported != 2 {
		t.Fatalf("premise: job = %s imported=%d skipped=%d failed=%d %q, want succeeded/2",
			job.Status, job.Imported, job.Skipped, job.Failed, job.ErrorSummary)
	}
	if n := issuesInWorkspace(t, d, ws.ID); n != 1 {
		t.Fatalf("premise: %d issues in the workspace, want 1 — the two rows did not collide, so "+
			"the UPDATE branch this test is about was never taken", n)
	}
	// PREMISE: the second row really did take the UPDATE branch. The conflict arm clobbers title,
	// so the later row's title is the only thing that can prove it.
	if title := issueTitle(t, d, ws.ID, "JRASERVER-64802"); title != "Ticket one again" {
		t.Fatalf("premise: stored title = %q, want the SECOND row's %q — the update never ran and "+
			"every assertion below is vacuous", title, "Ticket one again")
	}

	// THE ASSERTION. Nothing was in this workspace before this job started.
	for _, needle := range []string{`no "Description" column`, `no "Labels" column`} {
		if w := warningMentioning(job, needle); w != "" {
			t.Errorf("a FIRST import into an EMPTY workspace was told an issue was ALREADY IN TRACK "+
				"and RE-IMPORTED: %q\nThe row it overwrote was written by this same job, seconds "+
				"earlier, by another row of the same file.", w)
		}
	}

	// THE CONTROL ON THE FIX, and it is the half that keeps the suppression honest: the collision
	// is still reported, by the warning built for it. A fix that silenced the event as well as the
	// false sentence would pass every assertion above.
	if w := warningMentioning(job, `named the issue already written as "JRASERVER-64802"`); w == "" {
		t.Errorf("the export named one issue twice and the job says nothing about it at all.\n"+
			"warnings: %q", job.Warnings)
	}
}

// TestJobRow_JiraCSV_ARe_importNamingOneIssueTwiceCountsTheIssueOnce — the count half. The issue IS
// already in Track here, so the sentence is TRUE; the number in it is not.
func TestJobRow_JiraCSV_ARe_importNamingOneIssueTwiceCountsTheIssueOnce(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	first := importJiraCSVInto(t, d, ws.ID, team.ID, jiraCSVWideSingleKey)
	if first.Status != importer.JobSucceeded || first.Imported != 1 {
		t.Fatalf("premise: first job = %s imported=%d %q, want succeeded/1",
			first.Status, first.Imported, first.ErrorSummary)
	}
	if desc, labels := readIssueBody(t, d, ws.ID, "JRASERVER-64802"); desc == "" || len(labels) != 1 {
		t.Fatalf("premise: the wide import did not land a body — description=%q labels=%v", desc, labels)
	}

	job := importJiraCSVInto(t, d, ws.ID, team.ID, jiraCSVNarrowSameKeyTwice)
	if job.Status != importer.JobSucceeded || job.Imported != 2 {
		t.Fatalf("premise: re-import job = %s imported=%d %q, want succeeded/2",
			job.Status, job.Imported, job.ErrorSummary)
	}
	if n := issuesInWorkspace(t, d, ws.ID); n != 1 {
		t.Fatalf("premise: %d issues in the workspace, want 1", n)
	}

	// The warning is CORRECT here and must still fire — the description really was emptied and the
	// issue really was already in Track. Only the number is at issue.
	for _, needle := range []string{`no "Description" column`, `no "Labels" column`} {
		if n := countWarningsMentioning(job, needle); n != 1 {
			t.Fatalf("%s: %d warning lines, want exactly 1.\nwarnings: %q", needle, n, job.Warnings)
		}
	}
	for _, needle := range []string{`no "Description" column`, `no "Labels" column`} {
		w := warningMentioning(job, needle)
		if !strings.Contains(w, "1 issue(s)") {
			t.Errorf("the workspace holds ONE issue already in Track and the report says otherwise: %q\n"+
				"The count is of ROWS THAT UPDATED, and two rows of one export naming one issue are "+
				"two updates of one issue.", w)
		}
	}
}
