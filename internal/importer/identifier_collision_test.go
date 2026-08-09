package importer

import (
	"context"
	"testing"

	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/team"
	"github.com/talyvor/track/internal/testutil"
)

// identifier_collision_test.go — W3.4: the imported provider key and the Track-derived key share ONE column.
//
// Track derives a native issue's key itself: issue.Create sets identifier = "<team identifier>-<number>"
// (store.go, Create). An API import does NOT derive it — the provider key arrives verbatim and lands in the
// same column through UpsertByIdentifier, under UNIQUE (workspace_id, identifier) (migration 0022).
//
// Both providers emit exactly the Track shape: Linear "ENG-123" (team key + number), Jira "PROJ-123". So the
// moment a Track team's identifier equals the provider's prefix — the natural choice when migrating a team
// called ENG into a team called ENG — the two namespaces are ONE namespace, and Track's own number allocator
// does not know the provider's keys are in it.
//
// These tests MEASURE what that costs. They drive the real write pipeline (run → UpsertByIdentifier) against
// real Postgres via the real issue.Store.

// teamWithIdentifier seeds a team whose identifier is EXACTLY id — testutil.Team generates its own, and the
// whole point here is that the Track prefix and the provider prefix are the same string.
func teamWithIdentifier(t *testing.T, d *testutil.DB, workspaceID, id string) *model.Team {
	t.Helper()
	tm, err := team.NewStore(d.Pool).Create(context.Background(), model.Team{
		WorkspaceID: workspaceID,
		Name:        "Team " + id,
		Identifier:  id,
	})
	if err != nil {
		t.Fatalf("seed team %q: %v", id, err)
	}
	return tm
}

// TestImport_DoesNotClobberANativeIssueSharingTheProviderKey — a human's issue is not the provider's to
// overwrite. ENG-1 written by a user in Track and ENG-1 imported from Linear are DIFFERENT issues that
// happen to collide in a namespace Track never namespaced.
func TestImport_DoesNotClobberANativeIssueSharingTheProviderKey(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	tm := teamWithIdentifier(t, d, ws.ID, "ENG")
	store := issue.NewStore(d.Pool)

	native, err := store.Create(ctx, model.Issue{
		WorkspaceID: ws.ID,
		TeamID:      tm.ID,
		Title:       "a human wrote this",
		Description: "and this",
		CreatorID:   "user-1",
	})
	if err != nil {
		t.Fatalf("seed native issue: %v", err)
	}
	if native.Identifier != "ENG-1" {
		t.Fatalf("precondition: native identifier = %q, want ENG-1 (Create derives <team>-<number>)", native.Identifier)
	}

	// An import whose provider key happens to be ENG-1 — the SAME string Track derived for a different issue.
	imp := New(store)
	out, err := imp.run(ctx, ws.ID, tm.ID, &sliceSource{rows: []SourceRow{{
		Issue:  model.Issue{Identifier: "ENG-1", Title: "from the provider", Description: "provider body"},
		RowNum: 1,
	}}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	got, err := store.GetByIdentifier(ctx, "ENG-1", ws.ID)
	if err != nil {
		t.Fatalf("read back ENG-1: %v", err)
	}
	if got.Title != "a human wrote this" || got.Description != "and this" {
		t.Fatalf("THE IMPORT OVERWROTE A NATIVE ISSUE.\n"+
			"  native ENG-1 was {title:%q description:%q}\n"+
			"  after the import it is {title:%q description:%q}\n"+
			"  and the caller was told Imported=%d Skipped=%d Errors=%v",
			"a human wrote this", "and this", got.Title, got.Description, out.Imported, out.Skipped, out.Errors)
	}
	// Not clobbering is only half of it: a row the import could not land must be REPORTED, never counted
	// as imported. A silent no-op is the same lie in the other direction.
	//
	// ⚠ THE COUNTER MOVED AND THE CLAIM GOT STRONGER, NOT WEAKER. This assertion used to read
	// `out.Skipped != 1`, back when a refusal and a failure shared one counter — which is how a
	// protective refusal reached the job's `failed` column and reported {status:"failed"} for an
	// import that worked. It now asserts BOTH halves: the refusal is reported (Refused=1, one error)
	// AND it is not miscounted as a failure (Skipped=0). See refused_rows_job_test.go.
	if out.Refused != 1 || out.Skipped != 0 || len(out.Errors) != 1 {
		t.Fatalf("a collision must be reported AS A REFUSAL: Imported=%d Refused=%d Skipped=%d Errors=%v, "+
			"want Refused=1 Skipped=0 with one error",
			out.Imported, out.Refused, out.Skipped, out.Errors)
	}
}

// TestImport_ReImportStillUpdatesItsOwnRow — the guard above must not break the feature it protects. A row
// the IMPORTER created is still the provider's to update (the C.2 re-import policy: clobber title /
// description / labels, preserve status / priority).
func TestImport_ReImportStillUpdatesItsOwnRow(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	tm := teamWithIdentifier(t, d, ws.ID, "ENG")
	store := issue.NewStore(d.Pool)
	imp := New(store)

	first := &sliceSource{rows: []SourceRow{{
		Issue:  model.Issue{Identifier: "ENG-900", Title: "v1", Description: "d1", Status: model.StatusTodo},
		RowNum: 1,
	}}}
	if _, err := imp.run(ctx, ws.ID, tm.ID, first); err != nil {
		t.Fatalf("first import: %v", err)
	}

	second := &sliceSource{rows: []SourceRow{{
		Issue:  model.Issue{Identifier: "ENG-900", Title: "v2", Description: "d2", Status: model.StatusTodo},
		RowNum: 1,
	}}}
	out, err := imp.run(ctx, ws.ID, tm.ID, second)
	if err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if out.Imported != 1 || out.Skipped != 0 {
		t.Fatalf("re-import of the importer's OWN row must succeed: Imported=%d Skipped=%d Errors=%v",
			out.Imported, out.Skipped, out.Errors)
	}
	got, err := store.GetByIdentifier(ctx, "ENG-900", ws.ID)
	if err != nil {
		t.Fatalf("read back ENG-900: %v", err)
	}
	if got.Title != "v2" || got.Description != "d2" {
		t.Fatalf("re-import must still clobber title/description: %+v", got)
	}
}

// TestNativeIssueCreation_SurvivesAnImportedKeyInItsNumberRange — the wedge.
//
// An imported row takes a Track-allocated number (MAX(number)+1) but keeps the PROVIDER's identifier, so the
// two disagree: import ENG-3 into an empty team and you get {number:1, identifier:"ENG-3"}. Track's allocator
// counts numbers and never looks at identifiers, so it will eventually derive "ENG-3" for a native issue —
// and hit the UNIQUE constraint. Because MAX(number) does not advance when the INSERT fails, the SAME number
// is retried forever: the team cannot create another issue, permanently.
func TestNativeIssueCreation_SurvivesAnImportedKeyInItsNumberRange(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	tm := teamWithIdentifier(t, d, ws.ID, "ENG")
	store := issue.NewStore(d.Pool)

	// One imported issue whose provider key is ENG-3. It lands as number 1.
	imp := New(store)
	if _, err := imp.run(ctx, ws.ID, tm.ID, &sliceSource{rows: []SourceRow{{
		Issue:  model.Issue{Identifier: "ENG-3", Title: "imported", Description: "d"},
		RowNum: 1,
	}}}); err != nil {
		t.Fatalf("import: %v", err)
	}

	// Now a user works in Track. Every one of these must succeed — the import is not allowed to take a
	// number the allocator is going to reach.
	for i := 1; i <= 4; i++ {
		created, err := store.Create(ctx, model.Issue{
			WorkspaceID: ws.ID,
			TeamID:      tm.ID,
			Title:       "native issue",
			CreatorID:   "user-1",
		})
		if err != nil {
			t.Fatalf("native Create #%d FAILED after an import: %v\n"+
				"  the imported row holds identifier ENG-3 on number 1, so the allocator derives a key that already exists;\n"+
				"  MAX(number) does not advance on a failed INSERT, so this team can never create another issue",
				i, err)
		}
		if created.Identifier == "ENG-3" {
			t.Fatalf("native Create #%d produced ENG-3 — the same key as the imported row", i)
		}
	}
}
