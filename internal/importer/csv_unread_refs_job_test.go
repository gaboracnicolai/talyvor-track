package importer_test

// csv_unread_refs_job_test.go — the report read back OUT OF THE JOB ROW on real Postgres, through
// the shipped async runner.
//
// ⚠ IT IS NOT THE UNIT TEST TWICE. csv_unread_refs_test.go proves the mapper emits a note; this
// proves the note survives run()'s tally, renderWarnings' bound, JobStore.Finish's TEXT[] write and
// the read back — the four layers between a note and an operator. This package's own history is
// assumptions about the layer below turning out to be structural zeros.
//
// ⚠⚠ THREE SUBTESTS SHARE ONE DATABASE, AND THAT IS A MEASUREMENT RATHER THAN A STYLE CHOICE. The
// first version of this file called testutil.New three times and CI went RED — `panic: test timed
// out after 2m0s`, the package at 120.018s. internal/importer took 58.011s on main under
// `-race -timeout 120s`, so each fresh database (create + 26 migrations on a 2-core runner) is
// worth roughly twenty seconds of a sixty-second headroom, and three of them spent all of it. One
// harness with three workspaces costs one. The headroom itself is reported in the queue: it is a
// ceiling this package will hit again, and the timeout is not this merge's to raise.

import (
	"context"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/importer"
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
}
