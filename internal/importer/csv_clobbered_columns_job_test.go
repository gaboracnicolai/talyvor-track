package importer_test

// csv_clobbered_columns_job_test.go — THE COLUMNS A RE-IMPORT DELETES, measured END TO END on real
// Postgres through the shipped async runner and read back OUT OF THE issues TABLE.
//
// ⚠⚠ THE FINDING, AND IT IS A WRITE THIS PACKAGE MAKES RATHER THAN A VALUE IT DROPS. Since #98/#99
// both CSV transports carry a provider key, so a re-import takes issue.Store.UpsertByIdentifier and
// lands on the row it already wrote. That statement's conflict arm CLOBBERS three columns on
// purpose — `title`, `description`, `labels` — under the argument "provider is source of truth".
//
// The argument holds for a value the provider SENT. It does not hold for a column the export does
// not carry: `columnIndex.get` answers "" for an absent column exactly as it does for an empty cell,
// so a narrower second export tells the write path "this issue has no description" when the export
// said nothing about descriptions at all.
//
// MEASURED, before any of this file existed, through the runner on real Postgres:
//
//	import 1   Issue key,Summary,Description,Status,Priority,Labels   description="the body Jira holds" labels=[alpha]
//	import 2   Issue key,Summary,Status,Priority                      description=""                    labels=[]
//	                                                                  job: succeeded imported=1 skipped=0 failed=0
//
// A "current fields" export re-imported over an "all fields" one empties the description and the
// labels of every issue it names, and the operator is told the import succeeded.
//
// ⚠ IT IS THE ORDINARY EXPORT, NOT AN EXOTIC ONE. Whole-population over the 305 real Jira exports
// cached at /tmp/w34-jira-corpus (#103's corpus, the file-selection predicate spelled the way
// buildIndex spells it — lowercased): 203 of 305 (66.6%) carry NO `Labels` column and 16 of 305
// (5.2%) carry no `Description` column. Re-runnable: scripts/w34-jira-csv-clobbered-columns-probe.py.
//
// ⚠ WHAT THIS MERGE DOES AND DOES NOT DO. It does NOT change the write. Whether an absent column
// should PRESERVE the stored value is a decision with three defensible answers (preserve always /
// clobber always / preserve only when the column is absent), it needs a way to say "no statement"
// that model.Issue does not have — `Description` is a `string`, and the store's existing idiom for
// "no statement" is a nullable parameter — and it is written up in the queue with these numbers
// rather than guessed at here. What it DOES is report it, in the vocabulary this package already
// uses for exactly this condition one column over: `viaNoCreatedColumn` and `viaNoUpdatedColumn`
// already say "no %q column in this export" for the two date columns whose absence DEFAULTS a
// value. The two columns whose absence DESTROYS one said nothing.
//
// ⚠ THE REPORT IS GATED ON THE WRITE, NOT ON THE HEADER, AND THAT IS WHAT KEEPS IT TRUE. A first
// import of a narrow export overwrites nothing — there is no stored row to overwrite — so warning
// on the header alone would fire on every first import and be ignored within a week. The gate is
// the `inserted` boolean UpsertByIdentifier has always returned and run() has always discarded.

import (
	"context"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/testutil"
)

// The same issue, exported twice with different column selections. Jira's export screen offers
// "all fields" and "current fields"; these are the two shapes that produces.
// THREE rows, not one, and that is not padding: the report must be ONE line per column however many
// issues it covers. #80 built the bound this package needed because an unbounded per-row report is
// the failure mode a 10,000-row import turns into 10,000 warnings.
const jiraCSVWideExport = "Issue key,Summary,Description,Status,Priority,Labels\n" +
	"JRASERVER-64802,Ticket one,the body Jira holds,To Do,High,alpha\n" +
	"JRASERVER-64803,Ticket two,another body,To Do,High,beta\n" +
	"JRASERVER-64804,Ticket three,a third body,To Do,High,gamma\n"

const jiraCSVNarrowExport = "Issue key,Summary,Status,Priority\n" +
	"JRASERVER-64802,Ticket one,To Do,High\n" +
	"JRASERVER-64803,Ticket two,To Do,High\n" +
	"JRASERVER-64804,Ticket three,To Do,High\n"

const linearCSVWideExport = "ID,Title,Description,Status,Priority,Labels\n" +
	"ENG-123,Ticket one,the body Linear holds,Todo,High,alpha\n"

const linearCSVNarrowExport = "ID,Title,Status,Priority\n" +
	"ENG-123,Ticket one,Todo,High\n"

// readIssueBody returns the two columns the conflict arm clobbers.
func readIssueBody(t *testing.T, d *testutil.DB, wsID, identifier string) (string, []string) {
	t.Helper()
	var desc string
	var labels []string
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT description, labels FROM issues WHERE workspace_id=$1 AND identifier=$2`,
		wsID, identifier).Scan(&desc, &labels); err != nil {
		t.Fatalf("read back %q: %v", identifier, err)
	}
	return desc, labels
}

func warningMentioning(job *importer.Job, needle string) string {
	for _, w := range job.Warnings {
		if strings.Contains(w, needle) {
			return w
		}
	}
	return ""
}

// ─── the measurement, pinned ────────────────────────────────────────────────
//
// TestJobRow_JiraCSV_ANarrowerReimportEmptiesTheClobberedColumns pins THE DEFECT, not the fix, and
// it is GREEN BEFORE AND AFTER this merge on purpose. It is the premise every assertion below rests
// on: without it, a warning about an overwrite could be reported for a write that never happened.
//
// ⚠ ITS FAILURE MESSAGE CARRIES ITS OWN EXPIRY. If this ever goes red the write path has changed —
// which is the outcome the queue entry asks for — and the warnings below are then describing
// something that no longer occurs. Delete them together, not separately.
func TestJobRow_JiraCSV_ANarrowerReimportEmptiesTheClobberedColumns(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	first := importJiraCSVInto(t, d, ws.ID, team.ID, jiraCSVWideExport)
	if first.Status != importer.JobSucceeded || first.Imported != 3 {
		t.Fatalf("premise: first job = %s imported=%d %q, want succeeded/3", first.Status, first.Imported, first.ErrorSummary)
	}
	desc, labels := readIssueBody(t, d, ws.ID, "JRASERVER-64802")
	if desc != "the body Jira holds" || len(labels) != 1 {
		t.Fatalf("premise: the wide export did not land its body — description=%q labels=%v", desc, labels)
	}

	second := importJiraCSVInto(t, d, ws.ID, team.ID, jiraCSVNarrowExport)
	if second.Status != importer.JobSucceeded || second.Imported != 3 {
		t.Fatalf("premise: second job = %s imported=%d %q, want succeeded/3", second.Status, second.Imported, second.ErrorSummary)
	}
	desc, labels = readIssueBody(t, d, ws.ID, "JRASERVER-64802")
	if desc != "" || len(labels) != 0 {
		t.Fatalf("THE WRITE PATH HAS CHANGED: a re-import from an export with no Description/Labels "+
			"column left description=%q labels=%v, where it used to empty both. That is the fix the "+
			"queue asked for — the two warnings in this file now describe something that no longer "+
			"happens, and they must be removed WITH the change rather than left to rot.", desc, labels)
	}
}

// ─── the report ─────────────────────────────────────────────────────────────

func TestJobRow_JiraCSV_ANarrowerReimportIsReported(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	importJiraCSVInto(t, d, ws.ID, team.ID, jiraCSVWideExport)
	job := importJiraCSVInto(t, d, ws.ID, team.ID, jiraCSVNarrowExport)

	if got := warningMentioning(job, `no "Description" column`); got == "" {
		t.Errorf("the re-import emptied every description and said nothing about it.\nwarnings: %q", job.Warnings)
	}
	if got := warningMentioning(job, `no "Labels" column`); got == "" {
		t.Errorf("the re-import emptied every label list and said nothing about it.\nwarnings: %q", job.Warnings)
	}
	// The sentence must name the WRITE, not just the header — an operator who reads "no Description
	// column" alone has been told about their export, not about their data.
	if w := warningMentioning(job, `no "Description" column`); w != "" && !strings.Contains(w, "overwritten") {
		t.Errorf("the Description warning does not say what happened to the stored value: %q", w)
	}

	// ONE LINE PER COLUMN, WHATEVER THE ROW COUNT, and the count is IN the line. This is the
	// property #80 built the warning bound for, and it is not free here: a note that carried the
	// issue key in its Value would be a distinct FieldNote per row, so a 10,000-row re-import would
	// render 10,000 warnings and a real finding would be pushed out of the report by its own noise.
	// The 3-row fixture is what makes this assertion able to fail; C11 is the mutation it catches.
	for _, needle := range []string{`no "Description" column`, `no "Labels" column`} {
		n := 0
		for _, w := range job.Warnings {
			if strings.Contains(w, needle) {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s: %d warning lines for a 3-row re-import, want exactly 1.\nwarnings: %q", needle, n, job.Warnings)
		}
	}
	if w := warningMentioning(job, `no "Description" column`); w != "" && !strings.Contains(w, "3 issue(s)") {
		t.Errorf("the one line does not carry the count it stands for: %q", w)
	}
}

func TestJobRow_LinearCSV_ANarrowerReimportIsReported(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	importLinearCSVInto(t, d, ws.ID, team.ID, linearCSVWideExport)
	job := importLinearCSVInto(t, d, ws.ID, team.ID, linearCSVNarrowExport)

	if got := warningMentioning(job, `no "Description" column`); got == "" {
		t.Errorf("the Linear re-import emptied every description and said nothing about it.\nwarnings: %q", job.Warnings)
	}
	if got := warningMentioning(job, `no "Labels" column`); got == "" {
		t.Errorf("the Linear re-import emptied every label list and said nothing about it.\nwarnings: %q", job.Warnings)
	}
}

// ─── the two refusals ───────────────────────────────────────────────────────
//
// A warning that fires when nothing was overwritten is a warning an operator learns to ignore, and
// both of these are the shape that would do it. They pass VACUOUSLY today (nothing warns at all),
// which is why each has a positive control in scripts/w34-csv-clobbered-columns-controls.py.

// A FIRST import of the narrow export overwrites nothing: there is no stored row.
func TestJobRow_JiraCSV_AFirstNarrowImportIsNotReported(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	job := importJiraCSVInto(t, d, ws.ID, team.ID, jiraCSVNarrowExport)
	if job.Status != importer.JobSucceeded || job.Imported != 3 {
		t.Fatalf("premise: job = %s imported=%d %q, want succeeded/3", job.Status, job.Imported, job.ErrorSummary)
	}
	for _, needle := range []string{`no "Description" column`, `no "Labels" column`} {
		if w := warningMentioning(job, needle); w != "" {
			t.Errorf("a FIRST import overwrote nothing and was warned about anyway: %q", w)
		}
	}
}

// A re-import of an export that DOES carry both columns overwrites them with what the provider
// actually said, which is the behaviour the conflict arm is for.
func TestJobRow_JiraCSV_AWideReimportIsNotReported(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	importJiraCSVInto(t, d, ws.ID, team.ID, jiraCSVWideExport)
	job := importJiraCSVInto(t, d, ws.ID, team.ID, jiraCSVWideExport)
	for _, needle := range []string{`no "Description" column`, `no "Labels" column`} {
		if w := warningMentioning(job, needle); w != "" {
			t.Errorf("an export carrying both columns was reported as carrying neither: %q", w)
		}
	}
	// PREMISE: the body really is still there, so the absence of a warning above is the right
	// absence rather than an import that did nothing.
	for _, key := range []string{"JRASERVER-64802", "JRASERVER-64803", "JRASERVER-64804"} {
		if desc, labels := readIssueBody(t, d, ws.ID, key); desc == "" || len(labels) != 1 {
			t.Fatalf("premise: the wide re-import did not keep %s's body — description=%q labels=%v", key, desc, labels)
		}
	}
}
