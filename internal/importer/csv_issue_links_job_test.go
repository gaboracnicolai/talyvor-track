package importer_test

// csv_issue_links_job_test.go — the issue-link report read back OUT OF THE JOB ROW on real
// Postgres, through the shipped async runner, plus the two facts about the DATABASE the rendered
// sentence claims.
//
// ⚠ IT IS NOT THE UNIT TEST TWICE. csv_issue_links_test.go proves the mapper emits a note; this
// proves the note survives run()'s tally, renderWarnings' bound, JobStore.Finish's TEXT[] write and
// the read back — and, unlike the unit half, it can check the CONSEQUENCE the line asserts, because
// `issue_relations` and Issue.IsBlocked exist only against a database.
//
// ⚠ ONE testutil.New FOR THE WHOLE FILE. internal/importer runs 55–64s against `-timeout 120s` on
// CI's runners and the package has hit 120.02s before; the harness note in
// csv_unread_refs_job_test.go has the measurements. Three workspaces, one database.

import (
	"context"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/dependency"
	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/testutil"
)

// THREE ROWS, TWO LINK TYPES, and one row deliberately carrying NO link: the report must be one
// line per COLUMN however many issues it covers, and the count in that line must be the number of
// issues that actually lost something rather than the number imported.
const jiraCSVWithIssueLinks = "Issue key,Summary,Status,Priority,Outward issue link (Blocks),Inward issue link (Relates)\n" +
	"JRA-1,Ticket one,To Do,High,JRA-9,JRA-8\n" +
	"JRA-2,Ticket two,To Do,High,JRA-7,\n" +
	"JRA-3,Ticket three,To Do,High,,\n"

// Linear's three fixed link columns, all populated on two of three rows.
const linearCSVWithIssueLinks = "ID,Title,Status,Priority,Related to,Blocked by,Duplicate of\n" +
	"ENG-1,Ticket one,Todo,High,ENG-7,ENG-8,ENG-9\n" +
	"ENG-2,Ticket two,Todo,High,ENG-6,ENG-5,ENG-4\n" +
	"ENG-3,Ticket three,Todo,High,,,\n"

func countIssueRelations(t *testing.T, d *testutil.DB, wsID string) int {
	t.Helper()
	var n int
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM issue_relations WHERE workspace_id=$1`, wsID).Scan(&n); err != nil {
		t.Fatalf("count issue_relations: %v", err)
	}
	return n
}

func issueIDOf(t *testing.T, d *testutil.DB, wsID, identifier string) string {
	t.Helper()
	var id string
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT id FROM issues WHERE workspace_id=$1 AND identifier=$2`, wsID, identifier).Scan(&id); err != nil {
		t.Fatalf("read back id of %s: %v", identifier, err)
	}
	return id
}

func TestJobRow_CSV_TheDroppedIssueLinksReachTheJobRow(t *testing.T) {
	d := testutil.New(t)

	t.Run("jira reports one line per link column, with the issue count", func(t *testing.T) {
		ws := d.Workspace(t)
		team := d.Team(t, ws.ID)
		job := importJiraCSVInto(t, d, ws.ID, team.ID, jiraCSVWithIssueLinks)
		if job.Status != importer.JobSucceeded || job.Imported != 3 {
			t.Fatalf("premise: job = %s imported=%d %q, want succeeded/3", job.Status, job.Imported, job.ErrorSummary)
		}

		// ⚠ THE PREMISE THE WHOLE REPORT RESTS ON, ASSERTED RATHER THAN ASSUMED: not one row of
		// issue_relations exists after an import of three linked issues. If this ever fails, a
		// transport has started creating relations and the warning describes a loss that no longer
		// happens — delete the entry WITH that change.
		if n := countIssueRelations(t, d, ws.ID); n != 0 {
			t.Fatalf("A TRANSPORT NOW CREATES ISSUE RELATIONS: %d row(s) in issue_relations. That is "+
				"the fix the queue asks for — remove the report in csv_issue_links.go with it, "+
				"rather than leaving a warning about a loss that no longer occurs", n)
		}

		for _, col := range []string{`"outward issue link (blocks)"`, `"inward issue link (relates)"`} {
			if got := countWarningsMentioning(job, col); got != 1 {
				t.Errorf("the job row carries %d line(s) about %s, want exactly 1.\nwarnings: %q",
					got, col, job.Warnings)
			}
		}
		// The counts are per COLUMN and they differ: two rows carry a Blocks link, one carries a
		// Relates link, and the third row carries neither. A report that counted imported rows, or
		// rows-with-any-link, would print the same number twice.
		if got := countWarningsMentioning(job, "2 issue(s) carried a \"outward issue link (blocks)\""); got != 1 {
			t.Errorf("the Blocks line does not say 2 issue(s).\nwarnings: %q", job.Warnings)
		}
		if got := countWarningsMentioning(job, "1 issue(s) carried a \"inward issue link (relates)\""); got != 1 {
			t.Errorf("the Relates line does not say 1 issue(s).\nwarnings: %q", job.Warnings)
		}
		// ⚠ AND THE SENTENCE ITSELF, CHECKED AGAINST THE DATABASE IT DESCRIBES RATHER THAN AGAINST
		// A FORMAT STRING. It says a blocked issue imports as unblocked; JRA-2 arrived carrying a
		// Blocks link and issue.Store's blockedChecker — the one Track's Kanban BlockerAlert and
		// IssueDetail read — must therefore answer false for it.
		blocked, err := dependency.NewStore(d.Pool).IsBlocked(context.Background(), issueIDOf(t, d, ws.ID, "JRA-2"))
		if err != nil {
			t.Fatalf("IsBlocked: %v", err)
		}
		if blocked {
			t.Fatalf("JRA-2 reads as BLOCKED after an import that created no relation — the rendered " +
				"line's consequence clause is no longer true and must be re-measured, not reworded")
		}
		if got := countWarningsMentioning(job, "imports as unblocked"); got == 0 {
			t.Errorf("no warning tells the operator the blocked state did not survive the import.\nwarnings: %q",
				job.Warnings)
		}
	})

	t.Run("linear reports its three, once each", func(t *testing.T) {
		ws := d.Workspace(t)
		team := d.Team(t, ws.ID)
		job := importLinearCSVInto(t, d, ws.ID, team.ID, linearCSVWithIssueLinks)
		if job.Status != importer.JobSucceeded || job.Imported != 3 {
			t.Fatalf("premise: job = %s imported=%d %q, want succeeded/3", job.Status, job.Imported, job.ErrorSummary)
		}
		if n := countIssueRelations(t, d, ws.ID); n != 0 {
			t.Fatalf("A TRANSPORT NOW CREATES ISSUE RELATIONS: %d row(s)", n)
		}
		for _, col := range []string{`"related to"`, `"blocked by"`, `"duplicate of"`} {
			if got := countWarningsMentioning(job, col); got != 1 {
				t.Errorf("the job row carries %d line(s) about %s, want exactly 1.\nwarnings: %q",
					got, col, job.Warnings)
			}
		}
		if got := countWarningsMentioning(job, "2 issue(s) carried a \"blocked by\""); got != 1 {
			t.Errorf("the Blocked by line does not say 2 issue(s).\nwarnings: %q", job.Warnings)
		}
	})

	// An import that lost no link must say nothing about links. Without this the lines would appear
	// on every import of a 34-column Linear export — 14 of the 45 real ones carry no link column at
	// all, and most rows of the other 31 carry no link — and a report that always fires is one
	// nobody reads.
	t.Run("an import that lost no link is silent about links", func(t *testing.T) {
		ws := d.Workspace(t)
		team := d.Team(t, ws.ID)
		// The same header — so the columns EXIST — with every link cell empty.
		job := importLinearCSVInto(t, d, ws.ID, team.ID,
			"ID,Title,Status,Priority,Related to,Blocked by,Duplicate of\n"+
				"ENG-1,Ticket one,Todo,High,,,\n")
		if job.Status != importer.JobSucceeded || job.Imported != 1 {
			t.Fatalf("premise: job = %s imported=%d %q, want succeeded/1", job.Status, job.Imported, job.ErrorSummary)
		}
		for _, w := range job.Warnings {
			if strings.Contains(w, "issue relation") {
				t.Errorf("an import that lost no link produced an issue-link warning: %q", w)
			}
		}
	})
}
