package timetracking_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/testutil"
	"github.com/talyvor/track/internal/timetracking"
)

func issueEntriesReq(wsID, issueID string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/v1/workspaces/"+wsID+"/issues/"+issueID+"/time-entries", nil)
	r = r.WithContext(authz.WithAuthorizedRole(r.Context(), wsID, "m1", authz.RoleMember))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", issueID)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func seedMemberID(t *testing.T, d *testutil.DB, wsID, email string) string {
	t.Helper()
	var id string
	if err := d.Pool.QueryRow(context.Background(),
		`INSERT INTO members (workspace_id, name, email, role) VALUES ($1,$2,$3,'member') RETURNING id`,
		wsID, email, email).Scan(&id); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	return id
}

func seedTimeEntry(t *testing.T, d *testutil.DB, issueID, wsID, memberID, desc string) {
	t.Helper()
	if _, err := d.Pool.Exec(context.Background(),
		`INSERT INTO time_entries (issue_id, workspace_id, member_id, description, started_at, duration_sec, billable)
         VALUES ($1,$2,$3,$4, NOW(), 3600, true)`, issueID, wsID, memberID, desc); err != nil {
		t.Fatalf("seed time entry: %v", err)
	}
}

// GET .../issues/{id}/time-entries must be workspace-scoped: a wsA member naming a wsB issue
// must not receive that issue's time entries or summary (the id is caller-supplied).
func TestTimeTracking_ListIssueEntries_WorkspaceScoped(t *testing.T) {
	d := testutil.New(t)
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	tB := d.Team(t, wsB.ID)
	issB := d.Issue(t, wsB.ID, tB.ID)
	mB := seedMemberID(t, d, wsB.ID, "bob@x.com")
	seedTimeEntry(t, d, issB.ID, wsB.ID, mB, "B-secret-work")
	h := timetracking.NewHandler(timetracking.NewStore(d.Pool))

	rr := httptest.NewRecorder()
	h.ListIssueEntries(rr, issueEntriesReq(wsA.ID, issB.ID))
	if strings.Contains(rr.Body.String(), "B-secret-work") {
		t.Fatalf("CROSS-WS LEAK: wsA caller saw wsB issue's time entry: %s", rr.Body.String())
	}

	// Positive: own-workspace issue's entries appear.
	tA := d.Team(t, wsA.ID)
	issA := d.Issue(t, wsA.ID, tA.ID)
	mA := seedMemberID(t, d, wsA.ID, "alice@x.com")
	seedTimeEntry(t, d, issA.ID, wsA.ID, mA, "A-own-work")
	rrA := httptest.NewRecorder()
	h.ListIssueEntries(rrA, issueEntriesReq(wsA.ID, issA.ID))
	if !strings.Contains(rrA.Body.String(), "A-own-work") {
		t.Errorf("own-workspace time entry should appear; got %s", rrA.Body.String())
	}
}
