package member_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/gatewayauth"
	"github.com/talyvor/track/internal/member"
	"github.com/talyvor/track/internal/testutil"
)

const testGWSecret = "test-gateway-transit-secret-0123456789"

// mgmtChain mounts the member-management handler behind the SAME chain main.go uses for
// /v1 (gwAuth then wsAuthz), against real Postgres. So a request must carry a valid
// transit proof + an X-User-Email that the PG resolver maps to a membership — this is
// what makes "an added member can authenticate" a real end-to-end assertion.
func mgmtChain(d *testutil.DB) http.Handler {
	exempt := func(string) bool { return false }
	h := member.NewMgmtHandler(member.NewStore(d.Pool))
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(gatewayauth.Middleware(testGWSecret, exempt))
		r.Use(authz.Middleware(authz.NewPGResolver(d.Pool), exempt))
		h.Mount(r)
	})
	return r
}

func mreq(method, path, body, email string) *http.Request {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.Header.Set(gatewayauth.HeaderGatewayAuth, testGWSecret)
	if email != "" {
		r.Header.Set(gatewayauth.HeaderUserEmail, email)
	}
	return r
}

func do(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	return rr
}

func listMembers(t *testing.T, h http.Handler, wsID, asEmail string) []map[string]any {
	t.Helper()
	rr := do(h, mreq(http.MethodGet, "/v1/workspaces/"+wsID+"/members", "", asEmail))
	if rr.Code != http.StatusOK {
		t.Fatalf("list as %s = %d, want 200; body=%s", asEmail, rr.Code, rr.Body.String())
	}
	var out []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode list: %v; body=%s", err, rr.Body.String())
	}
	return out
}

func memberID(t *testing.T, list []map[string]any, email string) string {
	t.Helper()
	for _, m := range list {
		if m["email"] == email {
			return m["id"].(string)
		}
	}
	t.Fatalf("email %q not in member list %v", email, list)
	return ""
}

func codeOf(body string) string {
	var e struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal([]byte(body), &e)
	return e.Code
}

// Add → appears in list → the added member can AUTHENTICATE. The authenticate proof is
// the transition: before add, bob is a non-member (403 WORKSPACE_FORBIDDEN from wsAuthz);
// after add he is a member who only fails the owner gate (403 OWNER_REQUIRED); after
// promotion he passes it (200).
func TestMemberMgmt_Lifecycle_AddListPromote(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	seedMember(t, d, ws.ID, "alice@x.com", authz.RoleOwner)
	h := mgmtChain(d)
	base := "/v1/workspaces/" + ws.ID + "/members"

	// Before add: bob is not a member at all.
	pre := do(h, mreq(http.MethodGet, base, "", "bob@x.com"))
	if pre.Code != http.StatusForbidden || codeOf(pre.Body.String()) != "WORKSPACE_FORBIDDEN" {
		t.Fatalf("pre-add bob = %d/%s, want 403/WORKSPACE_FORBIDDEN", pre.Code, codeOf(pre.Body.String()))
	}

	// Owner adds bob as a member.
	add := do(h, mreq(http.MethodPost, base, `{"email":"bob@x.com","role":"member"}`, "alice@x.com"))
	if add.Code != http.StatusCreated {
		t.Fatalf("add bob = %d, want 201; body=%s", add.Code, add.Body.String())
	}

	// Owner's list now shows both.
	list := listMembers(t, h, ws.ID, "alice@x.com")
	if len(list) != 2 || memberID(t, list, "alice@x.com") == "" || memberID(t, list, "bob@x.com") == "" {
		t.Fatalf("list after add = %v, want alice+bob", list)
	}

	// Authenticate proof: bob is now a member and can READ the roster (list is member-readable).
	// A non-member gets WORKSPACE_FORBIDDEN (asserted pre-add), so a 200 here proves he authenticated.
	asBob := do(h, mreq(http.MethodGet, base, "", "bob@x.com"))
	if asBob.Code != http.StatusOK {
		t.Fatalf("post-add bob list = %d, want 200 (member can read the roster)", asBob.Code)
	}

	// Promote bob to owner; now bob authenticates AND passes the owner gate.
	bobID := memberID(t, list, "bob@x.com")
	pr := do(h, mreq(http.MethodPatch, base+"/"+bobID, `{"role":"owner"}`, "alice@x.com"))
	if pr.Code != http.StatusOK {
		t.Fatalf("promote bob = %d, want 200; body=%s", pr.Code, pr.Body.String())
	}
	asOwnerBob := do(h, mreq(http.MethodGet, base, "", "bob@x.com"))
	if asOwnerBob.Code != http.StatusOK {
		t.Fatalf("promoted bob list = %d, want 200; body=%s", asOwnerBob.Code, asOwnerBob.Body.String())
	}
}

// The three WRITES are owner-gated; the READ (list) is member-readable.
func TestMemberMgmt_OwnerGated(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	seedMember(t, d, ws.ID, "alice@x.com", authz.RoleOwner)
	seedMember(t, d, ws.ID, "bob@x.com", authz.RoleMember)
	h := mgmtChain(d)
	base := "/v1/workspaces/" + ws.ID + "/members"

	aliceID := memberID(t, listMembers(t, h, ws.ID, "alice@x.com"), "alice@x.com")

	// bob (member) is refused on the three WRITES, always OWNER_REQUIRED (he passed membership).
	for _, tc := range []struct {
		name string
		r    *http.Request
	}{
		{"add", mreq(http.MethodPost, base, `{"email":"carol@x.com","role":"member"}`, "bob@x.com")},
		{"change", mreq(http.MethodPatch, base+"/"+aliceID, `{"role":"member"}`, "bob@x.com")},
		{"remove", mreq(http.MethodDelete, base+"/"+aliceID, "", "bob@x.com")},
	} {
		rr := do(h, tc.r)
		if rr.Code != http.StatusForbidden || codeOf(rr.Body.String()) != "OWNER_REQUIRED" {
			t.Errorf("%s as member = %d/%s, want 403/OWNER_REQUIRED", tc.name, rr.Code, codeOf(rr.Body.String()))
		}
	}

	// The READ is allowed for any member (assignee/@mention/reviewer picker source).
	if lr := do(h, mreq(http.MethodGet, base, "", "bob@x.com")); lr.Code != http.StatusOK {
		t.Errorf("member list = %d, want 200 (roster readable by any member)", lr.Code)
	}

	// Owner is allowed (add carol succeeds).
	ok := do(h, mreq(http.MethodPost, base, `{"email":"carol@x.com","role":"member"}`, "alice@x.com"))
	if ok.Code != http.StatusCreated {
		t.Fatalf("owner add = %d, want 201; body=%s", ok.Code, ok.Body.String())
	}
}

// LOCKOUT HAZARD (b): removing the last owner is refused; with a second owner it succeeds.
func TestMemberMgmt_RemoveLastOwner_Refused(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	seedMember(t, d, ws.ID, "alice@x.com", authz.RoleOwner)
	h := mgmtChain(d)
	base := "/v1/workspaces/" + ws.ID + "/members"

	aliceID := memberID(t, listMembers(t, h, ws.ID, "alice@x.com"), "alice@x.com")

	// Only owner: refuse self-removal.
	rr := do(h, mreq(http.MethodDelete, base+"/"+aliceID, "", "alice@x.com"))
	if rr.Code != http.StatusConflict {
		t.Fatalf("remove last owner = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}

	// Add a second owner, then removing the first succeeds (an owner remains).
	if a := do(h, mreq(http.MethodPost, base, `{"email":"bob@x.com","role":"owner"}`, "alice@x.com")); a.Code != http.StatusCreated {
		t.Fatalf("add bob owner = %d; body=%s", a.Code, a.Body.String())
	}
	bobID := memberID(t, listMembers(t, h, ws.ID, "alice@x.com"), "bob@x.com")
	if rr := do(h, mreq(http.MethodDelete, base+"/"+bobID, "", "alice@x.com")); rr.Code != http.StatusOK {
		t.Fatalf("remove bob (alice remains owner) = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	// Alice is the last owner again → refused.
	if rr := do(h, mreq(http.MethodDelete, base+"/"+aliceID, "", "alice@x.com")); rr.Code != http.StatusConflict {
		t.Fatalf("remove last owner (again) = %d, want 409", rr.Code)
	}
}

// LOCKOUT HAZARD (c): demoting the last owner is refused; with a second owner it succeeds.
func TestMemberMgmt_DemoteLastOwner_Refused(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	seedMember(t, d, ws.ID, "alice@x.com", authz.RoleOwner)
	h := mgmtChain(d)
	base := "/v1/workspaces/" + ws.ID + "/members"
	aliceID := memberID(t, listMembers(t, h, ws.ID, "alice@x.com"), "alice@x.com")

	// Only owner: refuse demotion.
	if rr := do(h, mreq(http.MethodPatch, base+"/"+aliceID, `{"role":"member"}`, "alice@x.com")); rr.Code != http.StatusConflict {
		t.Fatalf("demote last owner = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}

	// Add a second owner; demoting the first now succeeds.
	if a := do(h, mreq(http.MethodPost, base, `{"email":"bob@x.com","role":"owner"}`, "alice@x.com")); a.Code != http.StatusCreated {
		t.Fatalf("add bob owner = %d", a.Code)
	}
	if rr := do(h, mreq(http.MethodPatch, base+"/"+aliceID, `{"role":"member"}`, "alice@x.com")); rr.Code != http.StatusOK {
		t.Fatalf("demote alice (bob remains owner) = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
}

// Add validation: explicit-role default (hazard a), invalid role, duplicate, missing email.
func TestMemberMgmt_AddValidation(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	seedMember(t, d, ws.ID, "alice@x.com", authz.RoleOwner)
	h := mgmtChain(d)
	base := "/v1/workspaces/" + ws.ID + "/members"

	// Duplicate email → 409.
	if rr := do(h, mreq(http.MethodPost, base, `{"email":"alice@x.com","role":"member"}`, "alice@x.com")); rr.Code != http.StatusConflict {
		t.Fatalf("duplicate add = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
	// Invalid role → 400.
	if rr := do(h, mreq(http.MethodPost, base, `{"email":"z@x.com","role":"admin"}`, "alice@x.com")); rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid role = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	// Missing email → 400.
	if rr := do(h, mreq(http.MethodPost, base, `{"role":"member"}`, "alice@x.com")); rr.Code != http.StatusBadRequest {
		t.Fatalf("missing email = %d, want 400", rr.Code)
	}
	// Omitted role → defaults to member EXPLICITLY (hazard a: no reliance on the DB default).
	if rr := do(h, mreq(http.MethodPost, base, `{"email":"carol@x.com"}`, "alice@x.com")); rr.Code != http.StatusCreated {
		t.Fatalf("add w/o role = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	list := listMembers(t, h, ws.ID, "alice@x.com")
	for _, m := range list {
		if m["email"] == "carol@x.com" && m["role"] != authz.RoleMember {
			t.Fatalf("carol role = %v, want member (explicit default)", m["role"])
		}
	}
}

// GET members is readable by ANY workspace member (assignee/@mention/reviewer picker source)
// and projects to EXACTLY the picker fields — id, name, email, role, avatar_url — leaking no
// more. A non-member of the workspace still gets the workspace-scope refusal, not the roster.
func TestMemberMgmt_List_MemberReadable_AndProjection(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	seedMember(t, d, ws.ID, "owner@x.com", authz.RoleOwner)
	seedMember(t, d, ws.ID, "member@x.com", authz.RoleMember)
	h := mgmtChain(d)
	base := "/v1/workspaces/" + ws.ID + "/members"

	// A non-member of this workspace: workspace-scope refusal, never the member list.
	other := do(h, mreq(http.MethodGet, base, "", "stranger@x.com"))
	if other.Code != http.StatusForbidden || codeOf(other.Body.String()) != "WORKSPACE_FORBIDDEN" {
		t.Fatalf("non-member list = %d/%s, want 403/WORKSPACE_FORBIDDEN", other.Code, codeOf(other.Body.String()))
	}

	// A plain member reads the roster (200).
	rr := do(h, mreq(http.MethodGet, base, "", "member@x.com"))
	if rr.Code != http.StatusOK {
		t.Fatalf("member list = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var got []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode list: %v; body=%s", err, rr.Body.String())
	}
	if len(got) != 2 {
		t.Fatalf("want 2 members, got %d: %v", len(got), got)
	}
	// Projection: EXACTLY id/name/email/role/avatar_url (5) — never workspace_id/created_at.
	allowed := map[string]bool{"id": true, "name": true, "email": true, "role": true, "avatar_url": true}
	for _, m := range got {
		if len(m) != 5 {
			t.Errorf("member row has %d fields, want exactly 5 (id/name/email/role/avatar_url): %v", len(m), m)
		}
		for k := range m {
			if !allowed[k] {
				t.Errorf("member list leaks field %q — want only id/name/email/role/avatar_url", k)
			}
		}
		if _, ok := m["avatar_url"]; !ok {
			t.Errorf("member row missing avatar_url (a picker shows avatars): %v", m)
		}
		if m["email"] == "" || m["role"] == "" || m["id"] == "" {
			t.Errorf("member row missing a picker field: %v", m)
		}
	}
}
