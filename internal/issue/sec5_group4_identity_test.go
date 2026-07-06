package issue_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/gatewayauth"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/notification"
	"github.com/talyvor/track/internal/testutil"
	"github.com/talyvor/track/internal/timetracking"
)

// SEC-5 Group 4: IDENTITY. The actor is trusted from the request body/query (member_id / author_id)
// instead of the verified session (authz.MemberID). Alice, a member of A, forges Carol's id (also a
// member of A — isolates the identity flaw from tenancy) and acts AS Carol. GREEN: the supplied id is
// ignored; the actor is always authz.MemberID(ctx). Mirrors SEC-4's forged-header retirement.
func sec5IdentityChain(d *testutil.DB) http.Handler {
	noExempt := func(string) bool { return false }
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(gatewayauth.Middleware(sec5Secret, noExempt))
		r.Use(authz.Middleware(authz.NewPGResolver(d.Pool), noExempt))
		notification.NewHandler(notification.NewStore(d.Pool)).Mount(r)
		timetracking.NewHandler(timetracking.NewStore(d.Pool)).Mount(r)
		issue.NewHandler(issue.NewStore(d.Pool)).Mount(r)
	})
	return r
}

func postJSONAs(wsID, subpath, email, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/workspaces/"+wsID+subpath, strings.NewReader(body))
	req.Header.Set(gatewayauth.HeaderGatewayAuth, sec5Secret)
	req.Header.Set(gatewayauth.HeaderUserEmail, email)
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestSEC5_Group4_ForgedActor(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	wsA := d.Workspace(t)
	sec5Member(t, d, wsA.ID, "alice@corp.com")
	sec5Member(t, d, wsA.ID, "carol@corp.com")
	aliceID := memberID(t, d, wsA.ID, "alice@corp.com")
	carolID := memberID(t, d, wsA.ID, "carol@corp.com")

	teamA := d.Team(t, wsA.ID)
	issueA := d.Issue(t, wsA.ID, teamA.ID)

	// A notification belonging to Carol.
	var carolNotif string
	if err := d.Pool.QueryRow(ctx,
		`INSERT INTO notifications (workspace_id, member_id, type, title, body)
         VALUES ($1, $2, 'mention', 'Carol secret', 'b') RETURNING id`, wsA.ID, carolID).Scan(&carolNotif); err != nil {
		t.Fatalf("seed notification: %v", err)
	}

	chain := sec5IdentityChain(d)
	send := func(r *http.Request) (int, string) {
		rr := httptest.NewRecorder()
		chain.ServeHTTP(rr, r)
		return rr.Code, rr.Body.String()
	}
	notifRead := func(id string) bool {
		var read bool
		if err := d.Pool.QueryRow(ctx, `SELECT read FROM notifications WHERE id=$1`, id).Scan(&read); err != nil {
			t.Fatalf("read notif: %v", err)
		}
		return read
	}
	fieldOf := func(body, key string) string {
		var m map[string]any
		_ = json.Unmarshal([]byte(body), &m)
		if v, ok := m[key].(string); ok {
			return v
		}
		return ""
	}

	// ── MarkAllRead: Alice forges Carol's member_id → must NOT mark Carol's notifications ──
	send(postJSONAs(wsA.ID, "/notifications/read-all", "alice@corp.com", `{"member_id":"`+carolID+`"}`))
	if notifRead(carolNotif) {
		t.Errorf("MarkAllRead honored a FORGED member_id — Carol's notification was marked read by Alice")
	}

	// ── List: Alice queries ?member_id=Carol → must NOT return Carol's notifications ──
	if _, body := send(getAs(wsA.ID, "/notifications?member_id="+carolID, "alice@corp.com")); strings.Contains(body, "Carol secret") {
		t.Errorf("List honored a FORGED member_id — Alice read Carol's notifications: %s", body)
	}

	// ── StartTimer: Alice forges member_id=Carol → the entry's actor must be Alice ──
	if _, body := send(postJSONAs(wsA.ID, "/timer/start", "alice@corp.com",
		`{"issue_id":"`+issueA.ID+`","member_id":"`+carolID+`","description":"x"}`)); fieldOf(body, "member_id") == carolID {
		t.Errorf("StartTimer honored a FORGED member_id — timer attributed to Carol, not the session actor Alice")
	}

	// ── CreateComment: Alice forges author_id=Carol → the comment's author must be Alice ──
	if _, body := send(postJSONAs(wsA.ID, "/issues/"+issueA.ID+"/comments", "alice@corp.com",
		`{"author_id":"`+carolID+`","body":"forged"}`)); fieldOf(body, "author_id") == carolID {
		t.Errorf("CreateComment honored a FORGED author_id — comment attributed to Carol, not the session actor Alice")
	}

	_ = aliceID // documents the intended actor for every action above
}
