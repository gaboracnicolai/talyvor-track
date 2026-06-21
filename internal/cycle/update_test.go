package cycle_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/cycle"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/testutil"
)

func seedCycle(t *testing.T, st *cycle.Store, wsID, teamID, name string) *model.Cycle {
	t.Helper()
	start := time.Now().UTC()
	c, err := st.Create(context.Background(), model.Cycle{
		WorkspaceID: wsID, TeamID: teamID, Name: name,
		StartDate: start, EndDate: start.AddDate(0, 0, 14),
	})
	if err != nil {
		t.Fatalf("seed cycle: %v", err)
	}
	return c
}

func TestUpdate_PatchesNameStatusDates(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	st := cycle.NewStore(d.Pool)
	c := seedCycle(t, st, ws.ID, team.ID, "Sprint 1")

	name, status := "Sprint 1 (renamed)", "active"
	newEnd := c.EndDate.AddDate(0, 0, 7)
	out, err := st.Update(context.Background(), c.ID, ws.ID, cycle.CycleUpdate{
		Name: &name, Status: &status, EndDate: &newEnd,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if out.Name != "Sprint 1 (renamed)" || out.Status != "active" {
		t.Errorf("got name=%q status=%q", out.Name, out.Status)
	}
	if !out.EndDate.Equal(newEnd) {
		t.Errorf("end_date = %v, want %v", out.EndDate, newEnd)
	}
	if !out.StartDate.Equal(c.StartDate) { // untouched (partial update)
		t.Errorf("start_date changed: %v != %v", out.StartDate, c.StartDate)
	}
}

func TestUpdate_WorkspaceScoped_NotFound(t *testing.T) {
	d := testutil.New(t)
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	teamA := d.Team(t, wsA.ID)
	st := cycle.NewStore(d.Pool)
	c := seedCycle(t, st, wsA.ID, teamA.ID, "A's sprint")

	name := "hijacked"
	if _, err := st.Update(context.Background(), c.ID, wsB.ID, cycle.CycleUpdate{Name: &name}); !errors.Is(err, cycle.ErrNotFound) {
		t.Fatalf("cross-workspace update: got %v, want ErrNotFound", err)
	}
	if got, _ := st.GetByID(context.Background(), c.ID); got.Name != "A's sprint" {
		t.Errorf("cycle mutated across workspace: %q", got.Name)
	}
}

func TestUpdate_InvalidDates_Rejected(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	st := cycle.NewStore(d.Pool)
	c := seedCycle(t, st, ws.ID, team.ID, "Sprint")

	bad := c.StartDate.AddDate(0, 0, -1) // end before start
	if _, err := st.Update(context.Background(), c.ID, ws.ID, cycle.CycleUpdate{EndDate: &bad}); err == nil {
		t.Fatal("expected error for end_date before start_date, got nil")
	}
}

func TestUpdate_InvalidStatus_Rejected(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	st := cycle.NewStore(d.Pool)
	c := seedCycle(t, st, ws.ID, team.ID, "Sprint")

	bogus := "in_progress_typo"
	if _, err := st.Update(context.Background(), c.ID, ws.ID, cycle.CycleUpdate{Status: &bogus}); err == nil {
		t.Fatal("expected error for invalid status, got nil")
	}
}

// TestUpdate_Handler_PatchesReturns200 — the documented route works now (was a 501 stub).
func TestUpdate_Handler_PatchesReturns200(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	st := cycle.NewStore(d.Pool)
	c := seedCycle(t, st, ws.ID, team.ID, "Sprint")

	h := cycle.NewHandler(st)
	r := chi.NewRouter()
	r.Patch("/workspaces/{wsID}/teams/{teamID}/cycles/{id}", h.Update)

	req := httptest.NewRequest(http.MethodPatch,
		"/workspaces/"+ws.ID+"/teams/"+team.ID+"/cycles/"+c.ID,
		bytes.NewBufferString(`{"name":"Renamed via API"}`))
	req.Header.Set("Content-Type", "application/json")
	// T10: the handler now reads the server-AUTHORIZED workspace from context
	// (set by the authz middleware), not the URL param. Inject the authorized
	// workspace under test so the handler scopes to it instead of 403-ing.
	req = req.WithContext(authz.WithAuthorized(req.Context(), ws.ID, "test-member"))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH cycle = %d, want 200 (was a 501 stub); body=%s", rr.Code, rr.Body.String())
	}
}
