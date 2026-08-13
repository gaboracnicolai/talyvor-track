package dependency_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/talyvor/track/internal/testutil"
)

// THE SOURCE HALF OF THE SEC-5 BULK GUARD HAD NO TEST, AND THE TEST THAT NAMES IT PASSES
// WITHOUT IT.
//
// BulkCreateRelations makes TWO workspace proofs before it writes (store.go): the source
// issue must be in rel.WorkspaceID (`SELECT EXISTS … WHERE id = $1 AND workspace_id = $2`),
// and every target must be too (`… NOT EXISTS … i.id = t AND i.workspace_id = $2`). The
// source proof is the one its own comment is about — "the source was never bound to the
// caller's authorized workspace" — because sourceID is `chi.URLParam(r, "id")`, chosen
// wholly by the caller, while wsID comes from authz.
//
// TestSEC_BulkCreateRelations_CrossWorkspace_Rejected puts BOTH the source AND the targets
// in the foreign workspace, so the TARGET proof alone refuses it. MEASURED by mutation at
// 11e88bf: neuter the source proof (`AND workspace_id = $2` → `AND $2 = $2`) and
// `go test ./...` passes across the WHOLE repository, and the `.semgrep/` tenancy lock
// reports 0 findings — the statement is exempted from child-insert-requires-parent-
// workspace-guard because its SQL mentions `workspace_id`, and cross-object-insert-requires-
// tenancy-guard's column alternation has no `source_id`/`target_id`. Nothing at all held it.
//
// The missing case is a FOREIGN SOURCE with IN-WORKSPACE TARGETS: the target proof passes,
// so only the source proof can refuse. RED with the source proof neutered (201 + a row
// referencing wsB's issue), GREEN as shipped (refused, 0 rows).
//
// INVALIDATED IF BulkCreateRelations stops taking the source from the URL, or the source
// proof moves into the INSERT itself (then the refusal is the statement's, not this check's).
func TestSEC_BulkCreateRelations_ForeignSourceInWorkspaceTargets_Rejected(t *testing.T) {
	d := testutil.New(t)
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	depSeedMember(t, d, wsA.ID, "alice@corp.com") // member of wsA only

	teamA := d.Team(t, wsA.ID)
	targetA := d.Issue(t, wsA.ID, teamA.ID) // target IS in the caller's workspace

	teamB := d.Team(t, wsB.ID)
	foreignSource := d.Issue(t, wsB.ID, teamB.ID) // source is NOT

	rr := httptest.NewRecorder()
	depChain(d).ServeHTTP(rr, depPost(wsA.ID, foreignSource.ID, "/bulk", "alice@corp.com",
		`{"target_ids":["`+targetA.ID+`"],"type":"blocks"}`))

	if rr.Code == http.StatusCreated || rr.Code == http.StatusOK {
		t.Errorf("CROSS-TENANT WRITE: a wsA member rooted bulk relations at wsB's issue (HTTP %d): %s",
			rr.Code, rr.Body.String())
	}
	if n := relCount(t, d, foreignSource.ID); n != 0 {
		t.Errorf("%d relation row(s) reference wsB's issue — the source-in-workspace proof did not hold", n)
	}
	// The refusal must not be an artefact of a target that could never have landed: the SAME
	// target, with an in-workspace source, must still be writable. Without this, deleting the
	// whole bulk path would keep the assertions above green.
	sourceA := d.Issue(t, wsA.ID, teamA.ID)
	ok := httptest.NewRecorder()
	depChain(d).ServeHTTP(ok, depPost(wsA.ID, sourceA.ID, "/bulk", "alice@corp.com",
		`{"target_ids":["`+targetA.ID+`"],"type":"blocks"}`))
	if ok.Code != http.StatusCreated && ok.Code != http.StatusOK {
		t.Fatalf("control: the same target with an in-workspace source must succeed; got %d: %s",
			ok.Code, ok.Body.String())
	}
	var landed int
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM issue_relations WHERE source_id=$1 AND target_id=$2`,
		sourceA.ID, targetA.ID).Scan(&landed); err != nil {
		t.Fatalf("control count: %v", err)
	}
	if landed != 1 {
		t.Fatalf("control: the legitimate bulk relation did not land (%d rows) — the refusal above proves nothing", landed)
	}
}
