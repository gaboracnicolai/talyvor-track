package workspace_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/gatewayauth"
	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/testutil"
	"github.com/talyvor/track/internal/workspace"
)

// throughAuthz sends a request for ownerEmail through the production authz middleware and reports the
// status it answers; a request authz lets through reaches a stub that answers 200.
func throughAuthz(t *testing.T, d *testutil.DB, method, path, ownerEmail string) int {
	t.Helper()
	mw := authz.Middleware(authz.NewPGResolver(d.Pool), nil)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	r := httptest.NewRequest(method, path, nil)
	r = r.WithContext(gatewayauth.WithIdentity(r.Context(), gatewayauth.Identity{Email: ownerEmail}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	return rr.Code
}

// An owner deletes a workspace (only once they confirm it by its slug), it stops answering, it appears
// in their list of deleted workspaces, and restoring it brings it back with its issues intact.
func TestWorkspace_DeleteConfirmedThenRestoredIntact(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	h := workspace.NewHandler(workspace.NewStore(d.Pool))

	const owner = "owner@example.com"
	ws, err := workspace.NewStore(d.Pool).CreateWithOwner(ctx, model.Workspace{Name: "Acme", Slug: "acme-b1830"}, owner)
	if err != nil {
		t.Fatalf("CreateWithOwner: %v", err)
	}
	team := d.Team(t, ws.ID)
	iss := d.Issue(t, ws.ID, team.ID)

	rr := httptest.NewRecorder()
	h.Delete(rr, wsReq(http.MethodDelete, ws.ID, authz.RoleOwner, ""))
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "CONFIRMATION_REQUIRED") {
		t.Fatalf("unconfirmed delete = %d %s, want 400 CONFIRMATION_REQUIRED", rr.Code, rr.Body.String())
	}
	if wsDeleted(t, d, ws.ID) {
		t.Fatal("an unconfirmed delete deleted the workspace")
	}

	rr = httptest.NewRecorder()
	h.Delete(rr, wsReq(http.MethodDelete, ws.ID, authz.RoleOwner, `{"confirm":"acme-b1830"}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("confirmed delete = %d %s, want 200", rr.Code, rr.Body.String())
	}
	var deleted model.Workspace
	if err := json.Unmarshal(rr.Body.Bytes(), &deleted); err != nil || deleted.DeletedAt == nil || deleted.RestorableUntil == nil {
		t.Fatalf("delete response lacks deleted_at/restorable_until: %s (err %v)", rr.Body.String(), err)
	}
	if got := deleted.RestorableUntil.Sub(*deleted.DeletedAt); got != 14*24*time.Hour {
		t.Errorf("restorable for %v, want 14 days", got)
	}

	// Deleted: every route answers 410 except restore.
	if code := throughAuthz(t, d, http.MethodGet, "/v1/workspaces/"+ws.ID+"/issues", owner); code != http.StatusGone {
		t.Errorf("a deleted workspace's issues answered %d, want 410", code)
	}
	if code := throughAuthz(t, d, http.MethodPost, "/v1/workspaces/"+ws.ID+"/restore", owner); code != http.StatusOK {
		t.Errorf("restore on a deleted workspace was refused by authz with %d", code)
	}

	// The owner can find it to restore it.
	lr := httptest.NewRequest(http.MethodGet, "/v1/workspaces?deleted=true", nil)
	lr = lr.WithContext(authz.WithMemberships(lr.Context(), []authz.Membership{{WorkspaceID: ws.ID, MemberID: "m1", Role: authz.RoleOwner, Deleted: true}}))
	rr = httptest.NewRecorder()
	h.List(rr, lr)
	if !strings.Contains(rr.Body.String(), ws.ID) {
		t.Errorf("the owner's deleted list does not include the deleted workspace: %s", rr.Body.String())
	}

	rr = httptest.NewRecorder()
	h.Restore(rr, wsReq(http.MethodPost, ws.ID, authz.RoleOwner, ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("restore = %d %s, want 200", rr.Code, rr.Body.String())
	}
	if wsDeleted(t, d, ws.ID) {
		t.Fatal("restore left the workspace deleted")
	}
	var n int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM issues WHERE id=$1 AND workspace_id=$2`, iss.ID, ws.ID).Scan(&n); err != nil || n != 1 {
		t.Errorf("the restored workspace's issue is missing: n=%d err=%v", n, err)
	}
	if code := throughAuthz(t, d, http.MethodGet, "/v1/workspaces/"+ws.ID+"/issues", owner); code != http.StatusOK {
		t.Errorf("a restored workspace's issues answered %d, want 200", code)
	}
}

// 14 days after it was deleted, a workspace can no longer be restored and the purge removes it and every
// row it owns; a workspace deleted 13 days ago and a live one are untouched.
func TestWorkspace_PurgedFourteenDaysAfterDelete(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	store := workspace.NewStore(d.Pool)
	h := workspace.NewHandler(store)

	old, err := store.CreateWithOwner(ctx, model.Workspace{Name: "Old", Slug: "old-b1830"}, "old@example.com")
	if err != nil {
		t.Fatalf("CreateWithOwner: %v", err)
	}
	var teamID string
	if err := d.Pool.QueryRow(ctx, `SELECT id FROM teams WHERE workspace_id=$1`, old.ID).Scan(&teamID); err != nil {
		t.Fatalf("read seeded team: %v", err)
	}
	iss := d.Issue(t, old.ID, teamID)
	d.Comment(t, iss.ID, "a comment")
	f := d.CustomField(t, old.ID, "Severity")
	d.SetFieldValue(t, iss.ID, f.ID, "high")
	jobID, err := importer.NewJobStore(d.Pool).Create(ctx, old.ID, teamID, "jira_csv", []byte("Summary\nx\n"))
	if err != nil {
		t.Fatalf("create import job: %v", err)
	}

	recent := d.Workspace(t)
	live := d.Workspace(t)
	if _, err := d.Pool.Exec(ctx, `UPDATE workspaces SET deleted_at = NOW() - interval '15 days' WHERE id=$1`, old.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool.Exec(ctx, `UPDATE workspaces SET deleted_at = NOW() - interval '13 days' WHERE id=$1`, recent.ID); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	h.Restore(rr, wsReq(http.MethodPost, old.ID, authz.RoleOwner, ""))
	if rr.Code != http.StatusConflict {
		t.Errorf("restore after 15 days = %d %s, want 409 NOT_RESTORABLE", rr.Code, rr.Body.String())
	}

	n, err := store.PurgeExpired(ctx, time.Now())
	if err != nil || n != 1 {
		t.Fatalf("PurgeExpired = %d, %v; want 1 workspace purged", n, err)
	}
	if wsExists(t, d, old.ID) {
		t.Fatal("the workspace deleted 15 days ago still has a row")
	}
	if !wsExists(t, d, recent.ID) || !wsExists(t, d, live.ID) {
		t.Fatal("the purge removed a workspace deleted 13 days ago, or a live one")
	}

	// No row anywhere still names the purged workspace.
	rows, err := d.Pool.Query(ctx, `SELECT table_name FROM information_schema.columns
		WHERE column_name = 'workspace_id' AND table_schema = current_schema()`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var tbl string
		if err := rows.Scan(&tbl); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, tbl)
	}
	rows.Close()
	for _, tbl := range tables {
		var left int
		if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM `+tbl+` WHERE workspace_id = $1`, old.ID).Scan(&left); err != nil {
			t.Fatalf("count %s: %v", tbl, err)
		}
		if left != 0 {
			t.Errorf("%s still holds %d rows of the purged workspace", tbl, left)
		}
	}
	var payloads int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM import_job_payloads WHERE job_id=$1`, jobID).Scan(&payloads); err != nil || payloads != 0 {
		t.Errorf("the purged workspace's import upload is still stored: n=%d err=%v", payloads, err)
	}
}
