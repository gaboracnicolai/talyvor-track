package issue_test

import (
	"context"
	"testing"

	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/testutil"
)

// ── T8 Build C.2 — issue.Store.UpsertByIdentifier proofs (real PG). The two omission classes are the gate:
//    (b) money-path never touched, (c) local workflow preserved — plus (d) descriptive fields clobbered. ──

func baseImport(ws, team string) model.Issue {
	return model.Issue{
		WorkspaceID: ws, TeamID: team, CreatorID: "importer",
		Identifier: "ENG-123", Title: "Imported", Description: "d",
		Status: model.StatusTodo, Priority: 2, Labels: []string{"bug"},
	}
}

func mustUpsert(t *testing.T, s *issue.Store, iss model.Issue) (*model.Issue, bool) {
	t.Helper()
	out, inserted, err := s.UpsertByIdentifier(context.Background(), iss)
	if err != nil {
		t.Fatalf("UpsertByIdentifier: %v", err)
	}
	return out, inserted
}

func exec(t *testing.T, d *testutil.DB, sql string, args ...any) {
	t.Helper()
	if _, err := d.Pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec: %v", err)
	}
}

// (a) INSERT PATH: a new (workspace, identifier) creates the issue with identifier=provider-key, an allocated
// number, content set, inserted=true.
func TestUpsertByIdentifier_InsertPath(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	s := issue.NewStore(d.Pool)

	got, inserted := mustUpsert(t, s, baseImport(ws.ID, team.ID))
	if !inserted {
		t.Fatal("first upsert must report inserted=true")
	}
	if got.Identifier != "ENG-123" {
		t.Fatalf("identifier=%q, want ENG-123 (caller-supplied provider-key, NOT auto TEAM-N)", got.Identifier)
	}
	if got.Number == 0 {
		t.Fatal("number must be allocated on insert")
	}
	if got.Title != "Imported" || got.Status != model.StatusTodo || got.Priority != 2 {
		t.Fatalf("content not set on insert: %+v", got)
	}
}

// (b) THE MONEY-PATH PROOF (red-first by design): a naive upsert that set ai_cost_usd=EXCLUDED.ai_cost_usd
// would reset the accumulated cost to the re-import's default 0. Because ai_cost_usd/ai_tokens/lens_feature
// are OMITTED from the UPDATE SET, re-import leaves them UNTOUCHED.
func TestUpsertByIdentifier_NeverTouchesAICost(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	s := issue.NewStore(d.Pool)

	first, _ := mustUpsert(t, s, baseImport(ws.ID, team.ID))
	// Accumulate cost + attribution (PR #30's money-path writes these; set directly here).
	exec(t, d, `UPDATE issues SET ai_cost_usd=5.00, ai_tokens=1234, lens_feature='agent.run' WHERE id=$1`, first.ID)

	// Re-import the SAME identifier with changed content; the incoming issue carries ai_cost_usd=0 (default) —
	// so a clobber WOULD zero it. Omission keeps it.
	reimport := baseImport(ws.ID, team.ID)
	reimport.Title = "Changed"
	_, inserted := mustUpsert(t, s, reimport)
	if inserted {
		t.Fatal("re-import must be an UPDATE (inserted=false)")
	}

	var cost float64
	var tokens int
	var lensFeature string
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT ai_cost_usd, ai_tokens, lens_feature FROM issues WHERE id=$1`, first.ID).
		Scan(&cost, &tokens, &lensFeature); err != nil {
		t.Fatal(err)
	}
	if cost != 5.00 {
		t.Fatalf("MONEY-PATH BREACH: ai_cost_usd=%v after re-import, want 5.00 (must be untouched)", cost)
	}
	if tokens != 1234 {
		t.Fatalf("ai_tokens=%d after re-import, want 1234 (untouched)", tokens)
	}
	if lensFeature != "agent.run" {
		t.Fatalf("lens_feature=%q after re-import, want agent.run (untouched)", lensFeature)
	}
}

// (c) THE WORKFLOW-PRESERVE PROOF: a Track user's local status/priority edits survive re-import — the
// provider's stale values do NOT clobber them (they are OMITTED from the UPDATE SET).
func TestUpsertByIdentifier_PreservesLocalWorkflow(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	s := issue.NewStore(d.Pool)

	first, _ := mustUpsert(t, s, baseImport(ws.ID, team.ID)) // status=todo, priority=2
	// A Track user moves it to Done and re-prioritises — a local workflow action.
	exec(t, d, `UPDATE issues SET status='done', priority=4 WHERE id=$1`, first.ID)

	// The provider re-imports its STALE values.
	reimport := baseImport(ws.ID, team.ID)
	reimport.Status = model.StatusInProgress
	reimport.Priority = 1
	mustUpsert(t, s, reimport)

	var status string
	var priority int
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT status, priority FROM issues WHERE id=$1`, first.ID).Scan(&status, &priority); err != nil {
		t.Fatal(err)
	}
	if status != "done" {
		t.Fatalf("status=%q after re-import, want done (local workflow edit must survive)", status)
	}
	if priority != 4 {
		t.Fatalf("priority=%d after re-import, want 4 (local edit preserved)", priority)
	}
}

// (d) THE CLOBBER PROOF: descriptive fields (title, description, labels) DO follow the provider on re-import —
// proving preserve isn't over-broad.
func TestUpsertByIdentifier_ClobbersDescriptive(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	s := issue.NewStore(d.Pool)

	base := baseImport(ws.ID, team.ID)
	base.Title, base.Description, base.Labels = "old", "olddesc", []string{"a"}
	first, _ := mustUpsert(t, s, base)

	re := baseImport(ws.ID, team.ID)
	re.Title, re.Description, re.Labels = "new", "newdesc", []string{"b", "c"}
	mustUpsert(t, s, re)

	var title, description string
	var labels []string
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT title, description, labels FROM issues WHERE id=$1`, first.ID).
		Scan(&title, &description, &labels); err != nil {
		t.Fatal(err)
	}
	if title != "new" || description != "newdesc" {
		t.Fatalf("descriptive fields not clobbered: title=%q description=%q", title, description)
	}
	if len(labels) != 2 || labels[0] != "b" {
		t.Fatalf("labels not clobbered: %v", labels)
	}
}

// (e) inserted-vs-updated: first import INSERTs (inserted=true), re-import UPDATEs (inserted=false).
func TestUpsertByIdentifier_InsertedFlag(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	s := issue.NewStore(d.Pool)

	_, ins1 := mustUpsert(t, s, baseImport(ws.ID, team.ID))
	_, ins2 := mustUpsert(t, s, baseImport(ws.ID, team.ID))
	if !ins1 {
		t.Fatal("first upsert must report inserted=true")
	}
	if ins2 {
		t.Fatal("re-import must report inserted=false (UPDATE)")
	}
}

// (f) NUMBER STABILITY: re-import does NOT change the issue's number — even after another Create bumps
// MAX(number) for the team. A re-imported issue keeps its identity.
func TestUpsertByIdentifier_NumberStable(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	s := issue.NewStore(d.Pool)

	first, _ := mustUpsert(t, s, baseImport(ws.ID, team.ID))
	// Another issue in the same team bumps MAX(number).
	if _, err := s.Create(context.Background(),
		model.Issue{WorkspaceID: ws.ID, TeamID: team.ID, Title: "other", CreatorID: "u"}); err != nil {
		t.Fatal(err)
	}
	re := baseImport(ws.ID, team.ID)
	re.Title = "changed"
	second, _ := mustUpsert(t, s, re)

	if second.Number != first.Number {
		t.Fatalf("number changed on re-import: %d → %d (identity must be stable)", first.Number, second.Number)
	}
}

// TENANCY: a team from ANOTHER workspace is rejected (team-in-workspace lookup) — nothing written.
func TestUpsertByIdentifier_CrossWorkspaceTeam_Rejected(t *testing.T) {
	d := testutil.New(t)
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	teamB := d.Team(t, wsB.ID)
	s := issue.NewStore(d.Pool)

	iss := baseImport(wsA.ID, teamB.ID) // authorized for A, but team is B's
	if _, _, err := s.UpsertByIdentifier(context.Background(), iss); err == nil {
		t.Fatal("cross-workspace team must be rejected")
	}
	var n int
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM issues WHERE workspace_id=$1`, wsA.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("rejected upsert wrote %d issues into A", n)
	}
}
