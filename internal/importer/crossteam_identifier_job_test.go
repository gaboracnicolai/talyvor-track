package importer_test

// crossteam_identifier_job_test.go — A SECOND IMPORT INTO A DIFFERENT TEAM LANDED NOTHING IN THAT
// TEAM, OVERWROTE THE FIRST TEAM'S ISSUES, AND REPORTED `succeeded`. Measured END TO END on real
// Postgres through the shipped async runner and read back OUT OF THE issues TABLE.
//
// ⚠⚠ THE FINDING IS THE OTHER HALF OF THE PREDICATE #71 WROTE. `issues.identifier` is
// UN-NAMESPACED and UNIQUE per (workspace_id, identifier); UpsertByIdentifier's conflict arm asks
// ONE question before overwriting a row — `WHERE issues.creator_id = 'importer'` — and that
// question separates an IMPORT from a HUMAN. It does not separate one import from another. The
// store's own comment reasons all the way to "a team called ENG in Track and a team called ENG in
// Linear collide in this one un-namespaced column" and stops one step short of asking what happens
// when the colliding row is ALSO an import.
//
// MEASURED, before any of this file existed, through the runner on real Postgres (two teams in ONE
// workspace, the ordinary shape of a workspace that runs more than one team):
//
//	job 1  team A  jira_csv    ENG-1,ENG-2                       succeeded imported=2   → 2 rows in TEAM A
//	job 2  team B  jira_csv    the SAME export, the SAME keys    succeeded imported=2   → 0 rows in TEAM B
//	job 3  team B  linear_csv  ENG-1,ENG-2 from the OTHER
//	                           provider, different titles/
//	                           bodies/labels                     succeeded imported=2   → 0 rows in TEAM B
//
//	after job 3, the rows in TEAM A read:
//	  ENG-1  title="Linear ticket one"  description="linear body one"  labels=[zulu]
//	  ENG-2  title="Linear ticket two"  description="linear body two"  labels=[yankee]
//
// Two separate losses out of one predicate, and the operator was told `succeeded` three times:
//
//   - THE TEAM THE OPERATOR ASKED FOR IS EMPTY. `team_id` is not in the UPDATE SET (correctly — a
//     re-imported issue keeps its identity) and it is not in the WHERE either, so the write lands
//     on the OTHER team's row and `run()` counts it as Imported. Importing into the wrong team and
//     then into the right one is the ordinary way to reach this, and it needs no key collision at
//     all: the second job says imported=N and that team has nothing in it.
//
//   - THE FIRST TEAM'S ISSUES ARE OVERWRITTEN. title, description and labels are the three columns
//     the conflict arm CLOBBERS ("provider is source of truth"), and here the source of truth is a
//     DIFFERENT PROVIDER describing a DIFFERENT ISSUE that merely shares a short uppercase key.
//     #106 measured what a narrower export does to those three columns; this is what another
//     project does to them.
//
// ⚠ THE COLLISION IS THE NAMESPACE, NOT A COINCIDENCE. Whole-population over the 305 real Jira
// exports cached at /tmp/w34-jira-corpus, keys taken from the `Issue key` column the product routes
// on: 17,625 key cells · 172 distinct project keys · MEDIAN LENGTH 4 (min 2, max 11) · 70 of them
// (40.7%) carried by two or more exports. That last unit is the FILE, and a file is not a Jira
// site, so the number that answers the question is the owner-attributed one: NINE keys are carried
// by exports from two or more DISTINCT REPOSITORY OWNERS, headed by `SCRUM` (9 owners), `KAN` (3)
// and `PROJ` (2) — the keys Jira Software's own Scrum and Kanban project templates hand out by
// default. Two unrelated Jira sites imported into one Track workspace collide on the key the
// provider chose for both of them. Re-runnable:
// `python3 scripts/w34-crossteam-identifier-probe.py --owners`, and the half that decides anything
// with the shipped mapper is TestJiraCSVCorpus_ProjectKeyNamespace next door.
//
// ⚠ WHAT THIS FIXES AND WHAT IT DOES NOT. It makes the write REFUSE rather than land on another
// team's row, and counts the refusal where #71's refusal already goes (Refused → the job's
// `skipped`), so an import that lands nothing can no longer report itself clean. It does NOT move
// the issue into the team the operator asked for: `team_id` is not in `updatableFields`, so no
// Track path moves an issue between teams, and `number` is UNIQUE per (team_id, number) — carrying
// a row across would mean reallocating an issue's number under a user. That is a product decision
// and it is written up in the queue with these numbers rather than guessed at here.

import (
	"context"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/testutil"
)

// The same two provider keys from two providers. ENG is the shape both emit: Jira's project key and
// Linear's team key are the same short uppercase abbreviation, and a workspace running both has one
// of each for the same team.
const crossTeamJiraExport = "Issue key,Summary,Description,Status,Priority,Labels\n" +
	"ENG-1,Jira ticket one,jira body one,To Do,High,alpha\n" +
	"ENG-2,Jira ticket two,jira body two,To Do,High,beta\n"

const crossTeamLinearExport = "ID,Title,Description,Status,Priority,Labels\n" +
	"ENG-1,Linear ticket one,linear body one,Todo,High,zulu\n" +
	"ENG-2,Linear ticket two,linear body two,Todo,High,yankee\n"

func issuesInTeam(t *testing.T, d *testutil.DB, teamID string) int {
	t.Helper()
	var n int
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM issues WHERE team_id=$1`, teamID).Scan(&n); err != nil {
		t.Fatalf("count issues in team: %v", err)
	}
	return n
}

func readIssueTitle(t *testing.T, d *testutil.DB, wsID, identifier string) string {
	t.Helper()
	var title string
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT title FROM issues WHERE workspace_id=$1 AND identifier=$2`, wsID, identifier).Scan(&title); err != nil {
		t.Fatalf("read back %q: %v", identifier, err)
	}
	return title
}

// ─── the empty team ─────────────────────────────────────────────────────────

// TestJobRow_AnImportIntoAnotherTeamDoesNotReportRowsThatTeamNeverGot is the report half: whatever
// the policy for a colliding key turns out to be, a job that put ZERO issues in the team it was
// asked to fill must not say `succeeded imported=2`.
func TestJobRow_AnImportIntoAnotherTeamDoesNotReportRowsThatTeamNeverGot(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	teamA := d.Team(t, ws.ID)
	teamB := d.Team(t, ws.ID)

	first := importJiraCSVInto(t, d, ws.ID, teamA.ID, crossTeamJiraExport)
	if first.Status != importer.JobSucceeded || first.Imported != 2 {
		t.Fatalf("premise: first job = %s imported=%d %q, want succeeded/2", first.Status, first.Imported, first.ErrorSummary)
	}
	if got := issuesInTeam(t, d, teamA.ID); got != 2 {
		t.Fatalf("premise: team A holds %d issues after its own import, want 2", got)
	}

	second := importJiraCSVInto(t, d, ws.ID, teamB.ID, crossTeamJiraExport)
	landed := issuesInTeam(t, d, teamB.ID)
	if second.Imported > landed {
		t.Errorf("the job reported imported=%d into a team that holds %d issue(s): %s imported=%d skipped=%d failed=%d %q",
			second.Imported, landed, second.Status, second.Imported, second.Skipped, second.Failed, second.ErrorSummary)
	}
	if landed == 0 && second.Status == importer.JobSucceeded {
		t.Errorf("an import that landed NOTHING in the team it was given reported %q", second.Status)
	}
}

// TestJobRow_AnImportIntoAnotherTeamCountsTheRowsAsRefused pins WHERE the count goes. A row the
// importer declined to write is #71's Refused (the job's `skipped`), never `failed` — that split is
// the whole of dcfbaa3 and this refusal must not re-create the state it ended.
func TestJobRow_AnImportIntoAnotherTeamCountsTheRowsAsRefused(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	teamA := d.Team(t, ws.ID)
	teamB := d.Team(t, ws.ID)

	importJiraCSVInto(t, d, ws.ID, teamA.ID, crossTeamJiraExport)
	second := importJiraCSVInto(t, d, ws.ID, teamB.ID, crossTeamJiraExport)

	if second.Skipped != 2 {
		t.Errorf("refused count = %d, want 2 (the job's `skipped` column is where a refusal goes)", second.Skipped)
	}
	if second.Failed != 0 {
		t.Errorf("failed count = %d, want 0 — a refusal is the policy working, not a row that failed", second.Failed)
	}
	if second.Status != importer.JobFailed {
		t.Errorf("status = %q, want %q — nothing landed", second.Status, importer.JobFailed)
	}
}

// TestJobRow_AnImportIntoAnotherTeamSaysWHICHTeamHasTheIssue — the sentence an operator reads has
// to name the thing they can act on. "already exists" sends someone to look for a duplicate; the
// truth is that the issue is in another team of the same workspace and this import did not move it.
func TestJobRow_AnImportIntoAnotherTeamSaysWHICHTeamHasTheIssue(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	teamA := d.Team(t, ws.ID)
	teamB := d.Team(t, ws.ID)

	importJiraCSVInto(t, d, ws.ID, teamA.ID, crossTeamJiraExport)
	second := importJiraCSVInto(t, d, ws.ID, teamB.ID, crossTeamJiraExport)

	if !strings.Contains(second.ErrorSummary, "another team") {
		t.Errorf("the summary does not say the issue is in another team: %q", second.ErrorSummary)
	}
	if !strings.Contains(second.ErrorSummary, "ENG-1") {
		t.Errorf("the summary names no identifier, so nobody can act on it: %q", second.ErrorSummary)
	}
	// The refusal this import made is NOT #71's refusal, and telling an operator their issue "was
	// not created by an import" when it was created by their OWN earlier import is a false sentence
	// that sends them to the wrong place.
	if strings.Contains(second.ErrorSummary, "not created by an import") {
		t.Errorf("a cross-team refusal is reported with the NATIVE-collision sentence: %q", second.ErrorSummary)
	}
}

// ─── the overwrite ──────────────────────────────────────────────────────────

// TestJobRow_ACollidingKeyFromAnotherProviderDoesNotOverwriteTheOtherTeamsIssues is the data half.
// The three columns the conflict arm clobbers are the three an unrelated project overwrites.
func TestJobRow_ACollidingKeyFromAnotherProviderDoesNotOverwriteTheOtherTeamsIssues(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	teamA := d.Team(t, ws.ID)
	teamB := d.Team(t, ws.ID)

	importJiraCSVInto(t, d, ws.ID, teamA.ID, crossTeamJiraExport)
	if got := readIssueTitle(t, d, ws.ID, "ENG-1"); got != "Jira ticket one" {
		t.Fatalf("premise: team A's ENG-1 reads %q before the second import", got)
	}

	importLinearCSVInto(t, d, ws.ID, teamB.ID, crossTeamLinearExport)

	title := readIssueTitle(t, d, ws.ID, "ENG-1")
	if title != "Jira ticket one" {
		t.Errorf("a Linear import into ANOTHER TEAM rewrote team A's Jira issue: title=%q, want %q",
			title, "Jira ticket one")
	}
	desc, labels := readIssueBody(t, d, ws.ID, "ENG-1")
	if desc != "jira body one" {
		t.Errorf("description overwritten by the other team's import: %q", desc)
	}
	if len(labels) != 1 || labels[0] != "alpha" {
		t.Errorf("labels overwritten by the other team's import: %v", labels)
	}
}

// ─── the two must-stay-greens ───────────────────────────────────────────────
//
// Both of these are the behaviour the refusal must NOT reach. A predicate that refuses too much
// turns every legitimate re-import into a failed job, which is a worse defect than the one above
// and it would look identical from the job row alone.

// A re-import into THE SAME TEAM still updates the row it already wrote. This is #98/#99's whole
// merge and the reason both CSV transports read their key column.
func TestJobRow_AReimportIntoTheSameTeamStillUpdates(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	importJiraCSVInto(t, d, ws.ID, team.ID, crossTeamJiraExport)
	second := importJiraCSVInto(t, d, ws.ID, team.ID,
		"Issue key,Summary,Description,Status,Priority,Labels\n"+
			"ENG-1,Jira ticket one EDITED,jira body one edited,To Do,High,alpha\n"+
			"ENG-2,Jira ticket two,jira body two,To Do,High,beta\n")

	if second.Status != importer.JobSucceeded || second.Imported != 2 || second.Skipped != 0 {
		t.Fatalf("a same-team re-import reported %s imported=%d skipped=%d %q, want succeeded/2/0",
			second.Status, second.Imported, second.Skipped, second.ErrorSummary)
	}
	if got := readIssueTitle(t, d, ws.ID, "ENG-1"); got != "Jira ticket one EDITED" {
		t.Errorf("the same-team re-import did not update the title: %q", got)
	}
	if got := issuesInTeam(t, d, team.ID); got != 2 {
		t.Errorf("the same-team re-import wrote a second copy: %d rows, want 2", got)
	}
}

// #71's OWN refusal keeps its own sentence. A human-created issue whose key an import collides with
// is a different refusal from this one and the operator has to be able to tell them apart.
func TestJobRow_TheNativeCollisionRefusalKeepsItsOwnSentence(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	// A human's issue occupying the key the import is about to claim. Created by the store's own
	// Create (creator_id is a real member id, not "importer"), then re-keyed to the provider's key
	// — Create derives "<team>-<n>" and this test needs the collision, not the derivation.
	human := d.Issue(t, ws.ID, team.ID)
	if _, err := d.Pool.Exec(ctx,
		`UPDATE issues SET identifier='ENG-1' WHERE id=$1`, human.ID); err != nil {
		t.Fatalf("seed the human-owned key: %v", err)
	}

	job := importJiraCSVInto(t, d, ws.ID, team.ID, crossTeamJiraExport)
	if job.Skipped != 1 {
		t.Fatalf("premise: refused=%d, want 1 (#71's refusal)", job.Skipped)
	}
	if !strings.Contains(job.ErrorSummary, "not created by an import") {
		t.Errorf("#71's refusal lost its own sentence: %q", job.ErrorSummary)
	}
	if strings.Contains(job.ErrorSummary, "another team") {
		t.Errorf("a native collision is reported as a cross-team one: %q", job.ErrorSummary)
	}
	if got := readIssueTitle(t, d, ws.ID, "ENG-1"); got == "Jira ticket one" {
		t.Errorf("the import overwrote the human's issue")
	}
}

// ─── the two cases the controls found before a run did ──────────────────────
//
// Both of these PASS on their first run, which is the shape that has to be justified rather than
// trusted: each exists because a control showed the set above could not see the mutation, and each
// names the control that earns it.

// A HUMAN'S ISSUE IN ANOTHER TEAM IS STILL #71's REFUSAL. The two predicates can both decline the
// same row, and which sentence an operator gets is decided by the ORDER of diagnoseUpsertRefusal's
// branches — an order every test above is blind to, because in all of them the human's issue and
// the import share a team. C6 flips that order and this is the only thing that moves.
func TestJobRow_AHumanIssueInAnotherTeamIsStillTheNativeRefusal(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	teamA := d.Team(t, ws.ID)
	teamB := d.Team(t, ws.ID)

	human := d.Issue(t, ws.ID, teamA.ID) // creator_id is a member id, not "importer"
	if _, err := d.Pool.Exec(ctx, `UPDATE issues SET identifier='ENG-1' WHERE id=$1`, human.ID); err != nil {
		t.Fatalf("seed the human-owned key in team A: %v", err)
	}

	job := importJiraCSVInto(t, d, ws.ID, teamB.ID, crossTeamJiraExport)
	if job.Skipped != 1 {
		t.Fatalf("premise: refused=%d, want 1", job.Skipped)
	}
	if !strings.Contains(job.ErrorSummary, "not created by an import") {
		t.Errorf("a human's issue in another team is reported as a cross-team import collision, "+
			"which sends the operator to move a row a person owns: %q", job.ErrorSummary)
	}
}

// THE DIAGNOSTIC READ IS WORKSPACE-SCOPED. It runs after the write refused and its answer goes into
// a message a human reads, so an unscoped read would name ANOTHER TENANT'S team identifier in this
// workspace's job row. `identifier` is unique per workspace, not globally, so the same key really
// does exist in two workspaces at once — which is the ordinary state, not an exotic one. C7 drops
// the scope and this is the only thing that moves.
func TestJobRow_TheRefusalMessageNamesNoOtherWorkspacesTeam(t *testing.T) {
	d := testutil.New(t)
	other := d.Workspace(t)
	otherTeam := d.Team(t, other.ID)
	importJiraCSVInto(t, d, other.ID, otherTeam.ID, crossTeamJiraExport)

	ws := d.Workspace(t)
	teamA := d.Team(t, ws.ID)
	teamB := d.Team(t, ws.ID)
	importJiraCSVInto(t, d, ws.ID, teamA.ID, crossTeamJiraExport)
	job := importJiraCSVInto(t, d, ws.ID, teamB.ID, crossTeamJiraExport)

	if strings.Contains(job.ErrorSummary, otherTeam.Identifier) {
		t.Errorf("the refusal message names a team from ANOTHER WORKSPACE (%s): %q",
			otherTeam.Identifier, job.ErrorSummary)
	}
	if !strings.Contains(job.ErrorSummary, teamA.Identifier) {
		t.Errorf("the refusal message does not name this workspace's holding team (%s): %q",
			teamA.Identifier, job.ErrorSummary)
	}
}
