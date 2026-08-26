package importer_test

// payload_lifetime_test.go — WHAT HAPPENS TO THE UPLOADED FILE AFTER THE IMPORT IS OVER.
//
// migration 0020 says of import_job_payloads: "ON DELETE CASCADE ties the blob's lifetime to the
// job." That sentence is TRUE as a statement about the constraint and it is NOT a statement about
// retention, because NOTHING IN THIS PRODUCT EVER DELETES AN import_jobs ROW. The blob's lifetime is
// tied to a row nothing ends, so the uploaded export — every issue title, description, reporter name
// and comment the customer exported out of Jira or Linear — is kept in Postgres forever.
//
// ⚠ WHY THE EXISTING TEST COULD NOT SEE IT. jobs_integration_test.go's
// TestJob_PayloadAtomicityAndCascade asserts "ON DELETE CASCADE removes the payload with the job" by
// executing `DELETE FROM import_jobs` ITSELF, from the test body. That proves the CONSTRAINT works.
// It is silent on whether any code path ever issues that statement, and the census below measures
// that none does. A mechanism demonstrated only by the test that demonstrates it is inert in the
// product.
//
// ⚠ AND THE ONE ROUTE THAT COULD HAVE CASCADED THE BLOB AWAY CANNOT RUN. import_jobs.workspace_id
// REFERENCES workspaces(id) with NO ON DELETE clause, so deleting the workspace would not cascade
// either — it would be REFUSED. Measured through the real handler below: an OWNER's
// DELETE /v1/workspaces/{wsID} on a workspace made by the production create path answers HTTP 500
// and the workspace survives. There is no reachable path in this product that removes an uploaded
// import payload.
//
// ⚠ 0020's SENTENCE CANNOT BE CORRECTED IN PLACE, WHICH IS WHY THE CORRECTION LIVES HERE. Migrations
// are checksummed: internal/migrate records a sha256 per file in schema_migrations and REFUSES to run
// on a mismatch ("checksum mismatch / missing file / gap — refuse before touching anything"). Editing
// even a comment in an applied migration would stop every deployed database from migrating. So the
// record of what that sentence does and does not claim belongs in this file and in
// docs/import-payload-retention-measured.md, not in 0020.
//
// ⚠ NOTHING HERE CHANGES BEHAVIOUR. These four tests PIN THE MEASURED TRUTH so the finding cannot be
// lost, and so that whoever decides the retention/deletion policy has to make these assertions say
// something new rather than quietly land beside them. The decision itself — cascade the tenant's
// data away, or refuse a non-empty workspace honestly instead of with a 500 — is a product call and
// is filed in the queue, not made here.
//
// ⚠ ONE THING THE CONTROLS TAUGHT ME ABOUT THESE TESTS, RECORDED RATHER THAN TUNED AWAY. I predicted
// that making members and teams CASCADE would let the workspace delete succeed and turn arm A of (3)
// green. IT DID NOT: arm A's workspace also holds an import job, and import_jobs refuses on its own,
// so the delete still answered 500 for a different constraint. That is the correct outcome for this
// file — the payload is still unreachable — but it means arm A is over-determined, and control C5b
// in scripts/w34-payload-lifetime-controls-r8kw.py exists to prove arm A can move at all: with
// members, teams AND import_jobs all cascading, it goes red as it should.

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/testutil"
	"github.com/talyvor/track/internal/workspace"
)

// A small Jira export standing in for a real one: the payload is the customer's own text, which is
// the whole reason its lifetime is worth an assertion.
const retentionJiraCSV = "Summary,Issue key,Issue id,Issue Type,Status,Project key,Project name,Project type,Priority,Reporter,Created,Updated\n" +
	"Customer reported a billing discrepancy,ACME-1,10000,Bug,To Do,ACME,Acme,software,High,Dana Whitfield,6/21/2025 16:14,6/21/2025 17:13\n" +
	"Rotate the production signing key,ACME-2,10001,Task,To Do,ACME,Acme,software,Medium,Dana Whitfield,6/21/2025 16:15,6/21/2025 16:21\n"

func payloadBytes(t *testing.T, d *testutil.DB, jobID string) ([]byte, bool) {
	t.Helper()
	var p []byte
	err := d.Pool.QueryRow(context.Background(),
		`SELECT payload FROM import_job_payloads WHERE job_id=$1`, jobID).Scan(&p)
	if err != nil {
		return nil, false
	}
	return p, true
}

// ---------------------------------------------------------------------------------------------
// (1) The retention itself, driven END TO END through the shipped async runner.
// ---------------------------------------------------------------------------------------------

// TestMeasured_TheUploadedPayloadOutlivesTheFinishedImport runs a real jira_csv job to a terminal
// state and reads the payload table back. The bytes are still there, byte-identical to the upload.
func TestMeasured_TheUploadedPayloadOutlivesTheFinishedImport(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	js := importer.NewJobStore(d.Pool)
	runner := importer.NewRunner(js, importer.New(issue.NewStore(d.Pool)))

	upload := []byte(retentionJiraCSV)
	jobID, err := js.Create(ctx, ws.ID, team.ID, "jira_csv", upload)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	did, err := runner.RunOnce(ctx)
	if err != nil || !did {
		t.Fatalf("RunOnce did=%v err=%v", did, err)
	}
	job, err := js.Get(ctx, jobID)
	if err != nil || job == nil {
		t.Fatalf("get job: job=%v err=%v", job, err)
	}

	// MUST-STAY-GREEN PREMISE, asserted first: the import really finished and really landed rows.
	// Without it, a fixture broken for an unrelated reason would leave a payload behind for the
	// wrong reason and read as a confirmed finding.
	if job.Status != importer.JobSucceeded || job.Imported != 2 {
		t.Fatalf("PREMISE FAILED: job = %s imported=%d skipped=%d failed=%d %q, want succeeded/2. "+
			"Nothing below is readable.",
			job.Status, job.Imported, job.Skipped, job.Failed, job.ErrorSummary)
	}
	if job.FinishedAt == nil {
		t.Fatal("PREMISE FAILED: a succeeded job must carry finished_at")
	}

	got, ok := payloadBytes(t, d, jobID)
	if !ok {
		t.Fatalf("MEASURED BEHAVIOUR CHANGED: the payload row is GONE after the job finished.\n"+
			"Something now removes an uploaded import payload. That is the outcome this repository "+
			"wanted and it must be DESCRIBED rather than discovered: say where the deletion happens, "+
			"whether it is bounded by time or by job state, and update migration 0020's lifetime "+
			"sentence and the census in %s.", "TestMeasured_NothingInProductionEverDeletesAnImportJobOrItsPayload")
	}
	if !bytes.Equal(got, upload) {
		t.Errorf("payload bytes changed: got %d bytes, uploaded %d", len(got), len(upload))
	}
	// The point is not that a row exists. It is that the customer's text is readable out of it.
	if !strings.Contains(string(got), "Customer reported a billing discrepancy") {
		t.Errorf("the retained payload no longer contains the exported issue text")
	}
}

// ---------------------------------------------------------------------------------------------
// (2) The census: no production path ends a job's life.
// ---------------------------------------------------------------------------------------------

// TestMeasured_NothingInProductionEverDeletesAnImportJobOrItsPayload scans non-test Go source for
// DELETE statements.
//
// ⚠ THIS IS A CENSUS THAT ASSERTS AN ABSENCE, the shape most likely to be reporting its own
// blindness: "no deletion found" and "no deletion exists" look identical from outside. So it carries
// its own positive control — the SAME scanner must find the deletions that DO exist, by name. If the
// scanner walks no files, or finds none of the known deletions, it REFUSES rather than passing.
func TestMeasured_NothingInProductionEverDeletesAnImportJobOrItsPayload(t *testing.T) {
	root := repoRootFromTest(t)

	type hit struct{ file, line string }
	var deletes []hit
	filesWalked := 0

	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.Walk(filepath.Join(root, dir), func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			filesWalked++
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			for _, ln := range strings.Split(string(b), "\n") {
				if strings.Contains(strings.ToUpper(ln), "DELETE FROM ") {
					deletes = append(deletes, hit{rel, strings.TrimSpace(ln)})
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}

	// --- the scanner's own positive control, BEFORE its finding is read ---
	if filesWalked < 50 {
		t.Fatalf("REFUSING: the scan walked %d non-test .go files under internal/ and cmd/. "+
			"That is not this repository; the census below would be an absence produced by walking "+
			"nothing.", filesWalked)
	}
	if len(deletes) < 10 {
		t.Fatalf("REFUSING: the scanner found %d production DELETE statements in %d files. This "+
			"repository has more than that, so the scanner is broken and its silence about "+
			"import_jobs would mean nothing.", len(deletes), filesWalked)
	}
	// Name one it MUST see. If this stops matching, the scanner has stopped reading SQL and the
	// absence finding below is worthless.
	sawWorkspaces := false
	for _, h := range deletes {
		if strings.Contains(h.line, "DELETE FROM workspaces") {
			sawWorkspaces = true
		}
	}
	if !sawWorkspaces {
		t.Fatalf("REFUSING: the scanner did not find `DELETE FROM workspaces`, which is in "+
			"internal/workspace/store.go. It cannot see deletions, so it cannot report their absence. "+
			"(found %d deletions across %d files)", len(deletes), filesWalked)
	}

	// --- the finding ---
	var forImports []string
	for _, h := range deletes {
		up := strings.ToUpper(h.line)
		if strings.Contains(up, "IMPORT_JOBS") || strings.Contains(up, "IMPORT_JOB_PAYLOADS") {
			forImports = append(forImports, h.file+": "+h.line)
		}
	}
	if len(forImports) != 0 {
		sort.Strings(forImports)
		t.Fatalf("MEASURED BEHAVIOUR CHANGED: production code now deletes import job rows:\n  %s\n"+
			"This is the outcome the queue asked for. Update migration 0020's lifetime sentence, "+
			"which currently says the CASCADE ties the blob's lifetime to the job while nothing ends "+
			"the job, and re-point this census at whatever bound the new path uses.",
			strings.Join(forImports, "\n  "))
	}
}

// repoRootFromTest walks up from the working directory to the module root (the directory holding
// go.mod). Taking it from the filesystem rather than hardcoding a relative depth keeps the census
// working if this file moves.
func repoRootFromTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("REFUSING: no go.mod found above %s — the census cannot locate the repository", dir)
	return ""
}

// ---------------------------------------------------------------------------------------------
// (3) The only cascade that could have reached the payload, driven through the real handler.
// ---------------------------------------------------------------------------------------------

// wsDeleteAsOwner drives the REAL workspace handler's Delete with the context the authz middleware
// leaves behind for an OWNER — the one role the route admits.
func wsDeleteAsOwner(h *workspace.Handler, wsID string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodDelete, "/v1/workspaces/"+wsID, nil)
	r = r.WithContext(authz.WithAuthorizedRole(r.Context(), wsID, "m1", authz.RoleOwner))
	rr := httptest.NewRecorder()
	h.Delete(rr, r)
	return rr
}

// TestMeasured_TheOnlyCascadeThatCouldReachThePayloadCannotRun measures DELETE /v1/workspaces/{wsID}
// against a workspace made the way production makes them.
//
// ⚠ THE TWO ARMS ARE THE POINT, AND THE SECOND ONE IS WHY NOBODY HAD SEEN THIS. Arm B seeds with
// testutil.DB.Workspace, which calls workspace.Store.Create — no owner member, no default team. That
// workspace deletes cleanly. It is also a shape THIS PRODUCT CANNOT PRODUCE: both HTTP creators
// (POST /v1/workspaces and POST /v1/bootstrap) call CreateWithOwner, which seeds an owner member and
// a default team in the same transaction, and migration 0025 backfilled a team into every workspace
// that predates it. The existing owner-gate test asserts a 200 from arm B's shape and is green.
//
// ⚠ THE REPOSITORY HAS ALREADY BEEN BITTEN BY EXACTLY THIS DIVERGENCE ONCE. first_issue_test.go
// records the create path being "unreachable for every new user, on every deployment" because a
// bootstrapped workspace held zero teams — found only when a test finally seeded through
// CreateWithOwner. This is the same seam, on the delete path.
func TestMeasured_TheOnlyCascadeThatCouldReachThePayloadCannotRun(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	store := workspace.NewStore(d.Pool)
	h := workspace.NewHandler(store)

	// --- ARM A: the production shape, carrying an import job and its payload ---
	ws, err := store.CreateWithOwner(ctx, model.Workspace{Name: "Acme", Slug: "acme-retention"}, "owner@example.com")
	if err != nil {
		t.Fatalf("CreateWithOwner: %v", err)
	}
	var members, teams int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM members WHERE workspace_id=$1`, ws.ID).Scan(&members); err != nil {
		t.Fatalf("count members: %v", err)
	}
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM teams WHERE workspace_id=$1`, ws.ID).Scan(&teams); err != nil {
		t.Fatalf("count teams: %v", err)
	}
	if members != 1 || teams != 1 {
		t.Fatalf("PREMISE FAILED: the production create path left members=%d teams=%d, want 1/1. "+
			"This test measures what happens to a workspace production can actually make; if that "+
			"shape has changed, the arms below are measuring something else.", members, teams)
	}

	var teamID string
	if err := d.Pool.QueryRow(ctx, `SELECT id FROM teams WHERE workspace_id=$1`, ws.ID).Scan(&teamID); err != nil {
		t.Fatalf("read seeded team: %v", err)
	}
	jobID, err := importer.NewJobStore(d.Pool).Create(ctx, ws.ID, teamID, "jira_csv", []byte(retentionJiraCSV))
	if err != nil {
		t.Fatalf("create import job: %v", err)
	}

	rr := wsDeleteAsOwner(h, ws.ID)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("MEASURED BEHAVIOUR CHANGED: an owner's DELETE of a production-shaped workspace "+
			"answered %d, not 500.\nbody=%s\n"+
			"Today this route cannot succeed on any workspace this product creates. THREE separate "+
			"constraints refuse it independently on this arm — members, teams and import_jobs all "+
			"reference workspaces(id) with no ON DELETE clause — so a partial fix does not move this "+
			"assertion; see control C5/C5b. If it has been decided — cascade, or an honest refusal "+
			"instead of a 500 — say which here, and say what happened to the import payloads a "+
			"cascade now reaches.", rr.Code, rr.Body.String())
	}
	var stillThere int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM workspaces WHERE id=$1`, ws.ID).Scan(&stillThere); err != nil {
		t.Fatalf("count workspaces: %v", err)
	}
	if stillThere != 1 {
		t.Errorf("the workspace row is gone after a 500 — the refusal must leave the tenant intact")
	}
	if _, ok := payloadBytes(t, d, jobID); !ok {
		t.Errorf("the import payload is gone after a refused workspace delete")
	}

	// --- ARM B: the fixture shape, which is why the existing tests are green ---
	// MUST STAY GREEN. It proves the difference between the arms is the SEEDING PATH and not the
	// handler: without it, "arm A returns 500" is satisfied by a delete route that is simply broken
	// for everyone, and the finding would be a different, smaller one.
	bare := d.Workspace(t)
	rrBare := wsDeleteAsOwner(h, bare.ID)
	if rrBare.Code != http.StatusOK {
		t.Fatalf("PREMISE FAILED: the Create-only fixture shape answered %d, not 200; body=%s.\n"+
			"Arm A's 500 can then no longer be attributed to the production seeding path.",
			rrBare.Code, rrBare.Body.String())
	}
	var bareLeft int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM workspaces WHERE id=$1`, bare.ID).Scan(&bareLeft); err != nil {
		t.Fatalf("count workspaces: %v", err)
	}
	if bareLeft != 0 {
		t.Errorf("the Create-only workspace survived a 200 delete")
	}
}

// ---------------------------------------------------------------------------------------------
// (4) The catalog census behind all of it.
// ---------------------------------------------------------------------------------------------

// TestMeasured_TheWorkspaceChildTablesThatRefuseADelete reads the DELETE action of every foreign key
// pointing at workspaces OUT OF THE CATALOG, not out of the migration text — the migrations are 26
// files and a later ALTER would not show up in the CREATE TABLE that declared the column.
//
// Pinning the two sets by name is deliberate: a new workspace-scoped table inherits one of these
// policies silently, and this is the only place that makes the choice visible.
func TestMeasured_TheWorkspaceChildTablesThatRefuseADelete(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()

	rows, err := d.Pool.Query(ctx, `
		SELECT tc.relname, c.confdeltype
		FROM pg_constraint c
		JOIN pg_class tc ON tc.oid = c.conrelid
		JOIN pg_class rc ON rc.oid = c.confrelid
		WHERE c.contype = 'f' AND rc.relname = 'workspaces'`)
	if err != nil {
		t.Fatalf("read foreign keys: %v", err)
	}
	defer rows.Close()

	var restrict, cascade []string
	for rows.Next() {
		var table string
		var action byte
		if err := rows.Scan(&table, &action); err != nil {
			t.Fatalf("scan: %v", err)
		}
		switch action {
		case 'a', 'r': // NO ACTION / RESTRICT — both refuse
			restrict = append(restrict, table)
		case 'c':
			cascade = append(cascade, table)
		default:
			t.Errorf("%s: unexpected ON DELETE action %q — classify it here", table, string(action))
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	sort.Strings(restrict)
	sort.Strings(cascade)

	if len(restrict)+len(cascade) < 15 {
		t.Fatalf("REFUSING: only %d foreign keys to workspaces were found. The schema has more than "+
			"that, so this census is reading an unmigrated database and its lists mean nothing.",
			len(restrict)+len(cascade))
	}

	wantRestrict := []string{
		"ai_spend_events", "automation_rules", "cycles", "import_jobs", "issue_relations",
		"issues", "labels", "members", "milestones", "notifications", "projects", "teams",
		"time_entries", "workspace_integrations",
	}
	wantCascade := []string{
		"custom_fields", "feature_boards", "feature_posts", "guest_invites", "guests",
		"issue_scores", "issue_templates",
	}

	if strings.Join(restrict, ",") != strings.Join(wantRestrict, ",") {
		t.Errorf("the tables that REFUSE a workspace delete changed.\n got: %v\nwant: %v\n"+
			"Every table here makes DELETE /v1/workspaces fail with a 500 for any workspace holding "+
			"one of its rows. members and teams are seeded by CreateWithOwner, so today that is every "+
			"workspace. If a table moved to CASCADE, a workspace delete now destroys its rows: say so.",
			restrict, wantCascade)
	}
	if strings.Join(cascade, ",") != strings.Join(wantCascade, ",") {
		t.Errorf("the tables that CASCADE on a workspace delete changed.\n got: %v\nwant: %v\n"+
			"A table added here is tenant data that a single owner-authenticated DELETE now removes.",
			cascade, wantCascade)
	}

	// import_jobs is named explicitly because it is this file's subject: the payload's only route out
	// is a cascade from the job, and the job's only route out would be a cascade from the workspace.
	found := false
	for _, r := range restrict {
		if r == "import_jobs" {
			found = true
		}
	}
	if !found {
		t.Errorf("import_jobs is no longer in the refusing set. If it now cascades, a workspace " +
			"delete reaches the uploaded payloads — which is a retention decision and must be " +
			"written down, in migration 0020 and here.")
	}
}
