package analytics_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/analytics"
	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/testutil"
)

func analyticsReq(path, wsID string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	return r.WithContext(authz.WithAuthorizedRole(r.Context(), wsID, "m1", authz.RoleMember))
}

// seedCycle inserts a cycle (name is the leak canary) into teamID/wsID and returns its id.
func seedCycle(t *testing.T, d *testutil.DB, wsID, teamID, name string, number int) string {
	t.Helper()
	var id string
	if err := d.Pool.QueryRow(context.Background(),
		`INSERT INTO cycles (team_id, workspace_id, name, number, start_date, end_date)
         VALUES ($1,$2,$3,$4, NOW(), NOW() + interval '14 days') RETURNING id`,
		teamID, wsID, name, number).Scan(&id); err != nil {
		t.Fatalf("seed cycle: %v", err)
	}
	return id
}

// GET .../analytics/velocity?team_id= must be workspace-scoped: a wsA member naming a wsB team
// must not receive wsB's cycles (team_id is caller-supplied).
func TestAnalytics_Velocity_WorkspaceScoped(t *testing.T) {
	d := testutil.New(t)
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	teamB := d.Team(t, wsB.ID)
	seedCycle(t, d, wsB.ID, teamB.ID, "B-Cycle-Velocity", 1)
	h := analytics.NewHandler(analytics.New(d.Pool))

	rr := httptest.NewRecorder()
	h.Velocity(rr, analyticsReq("/v1/workspaces/"+wsA.ID+"/analytics/velocity?team_id="+teamB.ID, wsA.ID))
	if strings.Contains(rr.Body.String(), "B-Cycle-Velocity") {
		t.Fatalf("CROSS-WS LEAK: wsA caller saw wsB team's velocity cycle: %s", rr.Body.String())
	}

	// Positive: the caller's own team's cycle appears.
	teamA := d.Team(t, wsA.ID)
	seedCycle(t, d, wsA.ID, teamA.ID, "A-Cycle-Velocity", 1)
	rrA := httptest.NewRecorder()
	h.Velocity(rrA, analyticsReq("/v1/workspaces/"+wsA.ID+"/analytics/velocity?team_id="+teamA.ID, wsA.ID))
	if !strings.Contains(rrA.Body.String(), "A-Cycle-Velocity") {
		t.Errorf("own-team velocity should appear; got %s", rrA.Body.String())
	}
}

// GET .../analytics/burndown?cycle_id= must be workspace-scoped: a wsA member naming a wsB
// cycle must not receive that cycle's burndown (cycle_id is caller-supplied).
func TestAnalytics_Burndown_WorkspaceScoped(t *testing.T) {
	d := testutil.New(t)
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	teamB := d.Team(t, wsB.ID)
	cycleB := seedCycle(t, d, wsB.ID, teamB.ID, "B-Cycle-Burndown", 1)
	h := analytics.NewHandler(analytics.New(d.Pool))

	rr := httptest.NewRecorder()
	h.Burndown(rr, analyticsReq("/v1/workspaces/"+wsA.ID+"/analytics/burndown?cycle_id="+cycleB, wsA.ID))
	if strings.Contains(rr.Body.String(), "B-Cycle-Burndown") {
		t.Fatalf("CROSS-WS LEAK: wsA caller saw wsB cycle's burndown: %s", rr.Body.String())
	}

	// Positive: the caller's own cycle's burndown is returned (200 with its name).
	teamA := d.Team(t, wsA.ID)
	cycleA := seedCycle(t, d, wsA.ID, teamA.ID, "A-Cycle-Burndown", 1)
	rrA := httptest.NewRecorder()
	h.Burndown(rrA, analyticsReq("/v1/workspaces/"+wsA.ID+"/analytics/burndown?cycle_id="+cycleA, wsA.ID))
	if rrA.Code != http.StatusOK || !strings.Contains(rrA.Body.String(), "A-Cycle-Burndown") {
		t.Errorf("own-cycle burndown should return 200 with its name; got %d %s", rrA.Code, rrA.Body.String())
	}
}
