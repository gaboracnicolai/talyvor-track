package importer_test

// csv_unread_refs_job_test.go — the report read back OUT OF THE JOB ROW on real Postgres, through
// the shipped async runner.
//
// ⚠ IT IS NOT THE UNIT TEST TWICE. csv_unread_refs_test.go proves the mapper emits a note; this
// proves the note survives run()'s tally, renderWarnings' bound, JobStore.Finish's TEXT[] write and
// the read back — the four layers between a note and an operator. This package's own history is
// assumptions about the layer below turning out to be structural zeros.
//
// ⚠⚠ SIX SUBTESTS SHARE ONE DATABASE, AND THAT IS A MEASUREMENT RATHER THAN A STYLE CHOICE. The
// first version of this file called testutil.New three times and CI went RED — `panic: test timed
// out after 2m0s`, the package at 120.018s. internal/importer took 58.011s on main under
// `-race -timeout 120s`, so each fresh database (create + 26 migrations on a 2-core runner) is
// worth roughly twenty seconds of a sixty-second headroom, and three of them spent all of it. One
// harness with three workspaces costs one. The headroom itself is reported in the queue: it is a
// ceiling this package will hit again, and the timeout is not this merge's to raise.
//
// ⚠⚠ THE FIFTH REFERENCE'S THREE SUBTESTS SIT HERE BESIDE THE FOUR THEY EXTEND — AND THE REASON IS
// NOT THE ONE THE PARAGRAPH ABOVE WOULD SUGGEST, BECAUSE THAT REASON WAS MEASURED AND DID NOT HOLD.
// They began as their own file with its own testutil.New, and the first two runs read 54.977s
// (without them) against 103.237s (with them) under `-race -timeout 120s`, which looks exactly like
// "one database costs fifty seconds". It is not: repeated, the shipped shape runs 42.5 / 37.7 /
// 43.8s and the SAME package with one EXTRA empty testutil.New runs 37.4 / 36.9 / 35.9s. One more
// database is INSIDE the noise on this machine, and the 103.2s reading was a single COLD run
// immediately after the container was created — a page cache, not a database count. Two runs of one
// configuration each cannot tell a cost from a cold start.
//
// ⚠ WHAT SURVIVES THE CORRECTION IS THE CEILING, NOT THE PER-DATABASE PRICE. 103.2s against a 120s
// timeout was really observed here, so the headroom warning above stands on its own evidence; the
// twenty-seconds-per-database figure is a 2-core CI number and does not reproduce on this machine.
// Re-measure — three runs of each shape — before either adding a database or citing a number here.

import (
	"context"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/testutil"
)

// THREE rows, not one, and that is not padding: the report must be ONE line per column however many
// issues it covers. An unbounded per-row report is the failure #80's bound exists to prevent.
const linearCSVWithReferences = "ID,Title,Status,Priority,Assignee,Project,Cycle Name,Parent issue\n" +
	"ENG-1,Ticket one,Todo,High,ada@example.com,Platform,Cycle 12,ENG-9\n" +
	"ENG-2,Ticket two,Todo,High,grace@example.com,Platform,Cycle 12,ENG-9\n" +
	"ENG-3,Ticket three,Todo,High,alan@example.com,Compiler,Cycle 13,ENG-8\n"

const jiraCSVWithReferences = "Issue key,Summary,Status,Priority,Assignee,Sprint,Parent\n" +
	"JRA-1,Ticket one,To Do,High,ada@example.com,Sprint 4,JRA-9\n" +
	"JRA-2,Ticket two,To Do,High,grace@example.com,Sprint 4,JRA-9\n" +
	"JRA-3,Ticket three,To Do,High,alan@example.com,Sprint 5,JRA-8\n"

// THE FIFTH REFERENCE'S FIXTURES. Three rows so "one line per column, whatever the row count" is a
// claim they can falsify, and three DIFFERENT people in the Creator cells: the note keys on the
// COLUMN, never the cell, so three distinct humans must still produce one line — and if the value
// ever reached the note this import would put three email addresses into a job row every member of
// the workspace can read.
const linearCSVWithCreators = "ID,Title,Status,Priority,Creator,Assignee\n" +
	"ENG-1,Ticket one,Todo,High,ada@example.com,\n" +
	"ENG-2,Ticket two,Todo,High,grace@example.com,\n" +
	"ENG-3,Ticket three,Todo,High,alan@example.com,\n"

// Jira's two person-who-filed columns, DIFFERENT on every row — the 8.6%-of-the-corpus case made
// the whole fixture, so one shared line cannot pass for both.
const jiraCSVWithCreatorAndReporter = "Issue key,Summary,Status,Priority,Creator,Reporter\n" +
	"JRA-1,Ticket one,To Do,High,ada@example.com,grace@example.com\n" +
	"JRA-2,Ticket two,To Do,High,ada@example.com,alan@example.com\n" +
	"JRA-3,Ticket three,To Do,High,ada@example.com,edsger@example.com\n"

func creatorIDOf(t *testing.T, d *testutil.DB, wsID, identifier string) string {
	t.Helper()
	var got string
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT creator_id FROM issues WHERE workspace_id=$1 AND identifier=$2`, wsID, identifier).
		Scan(&got); err != nil {
		t.Fatalf("read back creator_id of %s: %v", identifier, err)
	}
	return got
}

// countWarningsMentioning is the assertion the "one line per column" claim actually needs — a
// presence check would pass on three identical lines, which is the shape being ruled out.
func countWarningsMentioning(job *importer.Job, needle string) int {
	n := 0
	for _, w := range job.Warnings {
		if strings.Contains(w, needle) {
			n++
		}
	}
	return n
}

func TestJobRow_CSV_TheUnreadObjectReferencesReachTheJobRow(t *testing.T) {
	d := testutil.New(t)

	t.Run("linear reports all four, once each", func(t *testing.T) {
		ws := d.Workspace(t)
		team := d.Team(t, ws.ID)
		job := importLinearCSVInto(t, d, ws.ID, team.ID, linearCSVWithReferences)
		if job.Status != importer.JobSucceeded || job.Imported != 3 {
			t.Fatalf("premise: job = %s imported=%d %q, want succeeded/3", job.Status, job.Imported, job.ErrorSummary)
		}

		// THE PREMISE THE WHOLE REPORT RESTS ON, asserted rather than assumed: the four references
		// are NULL on every row that just imported. If this ever fails a transport has started
		// filling one of them, and the warning it replaces describes something that no longer
		// happens.
		for _, ident := range []string{"ENG-1", "ENG-2", "ENG-3"} {
			var assignee, project, cycle, parent *string
			if err := d.Pool.QueryRow(context.Background(),
				`SELECT assignee_id, project_id, cycle_id, parent_id FROM issues WHERE workspace_id=$1 AND identifier=$2`,
				ws.ID, ident).Scan(&assignee, &project, &cycle, &parent); err != nil {
				t.Fatalf("read back %s: %v", ident, err)
			}
			if assignee != nil || project != nil || cycle != nil || parent != nil {
				t.Fatalf("A TRANSPORT NOW FILLS A TRACK OBJECT REFERENCE on %s: assignee=%v project=%v cycle=%v parent=%v. "+
					"That is the fix the queue asks for — remove that column's entry from csv_unread_refs.go "+
					"WITH the change rather than leaving a warning about a loss that no longer occurs.",
					ident, assignee, project, cycle, parent)
			}
		}

		for _, col := range []string{`"Assignee"`, `"Project"`, `"Cycle Name"`, `"Parent issue"`} {
			if got := countWarningsMentioning(job, col); got != 1 {
				t.Errorf("three issues lost their %s and the job row carries %d line(s) about it, want exactly 1.\nwarnings: %q",
					col, got, job.Warnings)
			}
		}
		// The count belongs in the line: an operator who cannot see how many issues it covers
		// cannot tell a stray cell from a whole column.
		if got := countWarningsMentioning(job, "3 issue(s)"); got == 0 {
			t.Errorf("no warning names how many issues were affected.\nwarnings: %q", job.Warnings)
		}
	})

	t.Run("jira reports its three, once each", func(t *testing.T) {
		ws := d.Workspace(t)
		team := d.Team(t, ws.ID)
		job := importJiraCSVInto(t, d, ws.ID, team.ID, jiraCSVWithReferences)
		if job.Status != importer.JobSucceeded || job.Imported != 3 {
			t.Fatalf("premise: job = %s imported=%d %q, want succeeded/3", job.Status, job.Imported, job.ErrorSummary)
		}
		for _, col := range []string{`"Assignee"`, `"Sprint"`, `"Parent"`} {
			if got := countWarningsMentioning(job, col); got != 1 {
				t.Errorf("three issues lost their %s and the job row carries %d line(s) about it, want exactly 1.\nwarnings: %q",
					col, got, job.Warnings)
			}
		}
	})

	// An import that lost nothing must say nothing. Without this the four lines would appear on
	// every import of a full export, and a report that always fires is a report nobody reads.
	t.Run("an import that lost no reference is silent about them", func(t *testing.T) {
		ws := d.Workspace(t)
		team := d.Team(t, ws.ID)
		// The same header — so the columns EXIST — with every reference cell empty.
		job := importLinearCSVInto(t, d, ws.ID, team.ID,
			"ID,Title,Status,Priority,Assignee,Project,Cycle Name,Parent issue\n"+
				"ENG-1,Ticket one,Todo,High,,,,\n")
		if job.Status != importer.JobSucceeded || job.Imported != 1 {
			t.Fatalf("premise: job = %s imported=%d %q, want succeeded/1", job.Status, job.Imported, job.ErrorSummary)
		}
		if got := countWarningsMentioning(job, "does not read"); got != 0 {
			t.Errorf("an import that lost no reference produced %d unread-column warning(s).\nwarnings: %q",
				got, job.Warnings)
		}
	})

	// ─── the fifth reference: the one that is STAMPED rather than left empty ───
	//
	// ⚠ THESE ARE NOT THE UNIT TESTS AGAIN. csv_creator_ref_test.go proves the mapper emits the
	// note and that the rendered line does not claim creator_id is empty; these read creator_id
	// back OUT OF `issues` in the same test, so the sentence the operator is shown is checked
	// against the database it describes. A rendered sentence checked only against itself is a
	// claim about a format string.

	t.Run("linear: one line for the Creator column, and the row says importer", func(t *testing.T) {
		ws := d.Workspace(t)
		team := d.Team(t, ws.ID)
		job := importLinearCSVInto(t, d, ws.ID, team.ID, linearCSVWithCreators)
		if job.Status != importer.JobSucceeded || job.Imported != 3 {
			t.Fatalf("premise: job = %s imported=%d %q, want succeeded/3", job.Status, job.Imported, job.ErrorSummary)
		}
		if got := countWarningsMentioning(job, `"Creator"`); got != 1 {
			t.Errorf("three issues lost their Creator and the job row carries %d line(s) about it, "+
				"want exactly 1.\nwarnings: %q", got, job.Warnings)
		}
		// THE SENTENCE, CHECKED AGAINST THE DATABASE IT DESCRIBES.
		for _, ident := range []string{"ENG-1", "ENG-2", "ENG-3"} {
			if got := creatorIDOf(t, d, ws.ID, ident); got != model.ImporterCreatorID {
				t.Fatalf("%s.creator_id = %q, want %q — the warning tells the operator Track recorded "+
					"the importer; if that is no longer what happens the line is a false sentence",
					ident, got, model.ImporterCreatorID)
			}
		}
		var line string
		for _, w := range job.Warnings {
			if strings.Contains(w, `"Creator"`) {
				line = w
			}
		}
		for _, want := range []string{"3 issue(s)", model.ImporterCreatorID} {
			if !strings.Contains(line, want) {
				t.Errorf("the creator line %q does not carry %q", line, want)
			}
		}
		if strings.Contains(line, "left empty") {
			t.Errorf("the creator line claims creator_id is left empty: %q — it holds %q on all three rows",
				line, model.ImporterCreatorID)
		}
		// The cell values must NOT reach the job row: three humans' addresses, readable by anyone
		// who can read the job, and one distinct note per row instead of one per column.
		for _, w := range job.Warnings {
			for _, email := range []string{"ada@example.com", "grace@example.com", "alan@example.com"} {
				if strings.Contains(w, email) {
					t.Errorf("a job-row warning carries the cell value %q: %q — the note keys on the "+
						"COLUMN precisely so identities stay out of a row every member can read", email, w)
				}
			}
		}
	})

	t.Run("jira: both person-who-filed columns reported apart", func(t *testing.T) {
		ws := d.Workspace(t)
		team := d.Team(t, ws.ID)
		job := importJiraCSVInto(t, d, ws.ID, team.ID, jiraCSVWithCreatorAndReporter)
		if job.Status != importer.JobSucceeded || job.Imported != 3 {
			t.Fatalf("premise: job = %s imported=%d %q, want succeeded/3", job.Status, job.Imported, job.ErrorSummary)
		}
		for _, col := range []string{`"Creator"`, `"Reporter"`} {
			if got := countWarningsMentioning(job, col); got != 1 {
				t.Errorf("the job row carries %d line(s) about %s, want exactly 1 — Jira's two "+
					"person-who-filed columns disagree on 8.6%% of real rows, so one line cannot "+
					"stand for the other.\nwarnings: %q", got, col, job.Warnings)
			}
		}
	})

	// THE RE-IMPORT BRANCH, and it is not the same statement. The upsert's conflict arm does NOT
	// name creator_id in its SET — it only matches rows that already carry the importer — so a
	// re-imported issue keeps the stamped value by a different mechanism than the INSERT does. The
	// warning says the same thing on both paths and both must be true.
	t.Run("a re-import still reports it, and the row still says importer", func(t *testing.T) {
		ws := d.Workspace(t)
		team := d.Team(t, ws.ID)
		first := importLinearCSVInto(t, d, ws.ID, team.ID, linearCSVWithCreators)
		if first.Status != importer.JobSucceeded || first.Imported != 3 {
			t.Fatalf("premise: first job = %s imported=%d %q", first.Status, first.Imported, first.ErrorSummary)
		}
		second := importLinearCSVInto(t, d, ws.ID, team.ID, linearCSVWithCreators)
		if second.Status != importer.JobSucceeded || second.Imported != 3 {
			t.Fatalf("premise: second job = %s imported=%d %q — the re-import must land on the same "+
				"rows, not refuse them", second.Status, second.Imported, second.ErrorSummary)
		}
		var n int
		if err := d.Pool.QueryRow(context.Background(),
			`SELECT COUNT(*) FROM issues WHERE workspace_id=$1`, ws.ID).Scan(&n); err != nil {
			t.Fatalf("count issues: %v", err)
		}
		if n != 3 {
			t.Fatalf("premise: %d issues after two imports of the same bytes, want 3 (the UPDATE branch)", n)
		}
		if got := countWarningsMentioning(second, `"Creator"`); got != 1 {
			t.Errorf("the re-import's job row carries %d creator line(s), want exactly 1.\nwarnings: %q",
				got, second.Warnings)
		}
		if got := creatorIDOf(t, d, ws.ID, "ENG-1"); got != model.ImporterCreatorID {
			t.Fatalf("after a re-import ENG-1.creator_id = %q, want %q", got, model.ImporterCreatorID)
		}
	})
}
