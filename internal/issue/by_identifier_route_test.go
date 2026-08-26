package issue_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/testutil"
)

// W3.6 — TRACK HAD NO REST ROUTE THAT RESOLVES AN ISSUE IDENTIFIER, AND ITS OWN CLI CLIENT WAS
// BUILT ASSUMING ONE.
//
// MEASURED FROM THE OTHER SIDE (talyvor-code #57, W4.20), on the wire against a real server:
// the agent's track client builds /v1/workspaces/{ws}/issues/{identifier} because its package
// doc says it exists to "resolve an issue identifier (ENG-42)". This repo's route is `/{id}`
// and reads `WHERE id = $1` — a uuid. With the row present, ENG-42 answered 404 and the same
// request with issues.id answered 200 carrying identifier ENG-42.
//
// ⚠ AND THERE WAS NOTHING TO POINT THE CLIENT AT, CHECKED THREE WAYS: Mount registered no
// identifier lookup; /issues/search?q=ENG-42 returned [] (it searches title and description);
// and /issues?identifier=ENG-42 LOOKED like a filter and was INERT — with two issues seeded it
// returned BOTH, byte-identically to ?zzz=nope.
//
// The store already had the scoped lookup (GetByIdentifier: `WHERE identifier = $1 AND
// workspace_id = $2`, refusing an empty workspace). This exposes it, and the tests below are
// about the two things that could go wrong doing so: the no-oracle posture, and route shape.

// ⚠ THE POSTURE THAT MUST NOT BE LOST. h.Get answers 404 for a foreign id deliberately, so it
// is not a cross-tenant existence oracle (SEC-5). An identifier route that distinguished "wrong
// workspace" from "no such identifier" would hand back exactly the oracle that was removed —
// and identifiers are GUESSABLE in a way uuids are not (ENG-1, ENG-2, …), so the same leak is
// strictly worse here.
func TestByIdentifier_ForeignWorkspaceIs404_AndIndistinguishableFromAbsent(t *testing.T) {
	d := testutil.New(t)
	h := sec5ReadsChain(d)
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	sec5Member(t, d, wsA.ID, "alice@corp.com")
	sec5Member(t, d, wsB.ID, "bob@corp.com")

	issB := d.Issue(t, wsB.ID, "")
	if issB.Identifier == "" {
		t.Fatal("seed produced no identifier — this test would assert nothing")
	}

	// alice asks HER OWN workspace for bob's identifier: it is not hers, so 404.
	foreign := httptest.NewRecorder()
	h.ServeHTTP(foreign, getAs(wsA.ID, "/issues/by-identifier/"+issB.Identifier, "alice@corp.com"))

	// …and an identifier nobody has: also 404, with the SAME body. That equality is the
	// property — a difference of any kind is the oracle.
	absent := httptest.NewRecorder()
	h.ServeHTTP(absent, getAs(wsA.ID, "/issues/by-identifier/ZZZ-99999", "alice@corp.com"))

	if foreign.Code != http.StatusNotFound {
		t.Fatalf("foreign identifier = %d, want 404 (it must not be readable, and it must not "+
			"be distinguishable from absent)", foreign.Code)
	}
	if absent.Code != http.StatusNotFound {
		t.Fatalf("absent identifier = %d, want 404", absent.Code)
	}
	if foreign.Body.String() != absent.Body.String() {
		t.Fatalf("a foreign identifier and an absent one answer DIFFERENTLY:\n  foreign: %s\n  absent:  %s\n"+
			"That difference is a cross-tenant existence oracle, and identifiers are guessable.",
			foreign.Body.String(), absent.Body.String())
	}
}

// The lookup must actually work inside its own workspace, or the test above is satisfied by a
// route that returns 404 to everyone.
func TestByIdentifier_OwnWorkspaceResolves(t *testing.T) {
	d := testutil.New(t)
	h := sec5ReadsChain(d)
	ws := d.Workspace(t)
	sec5Member(t, d, ws.ID, "alice@corp.com")
	iss := d.Issue(t, ws.ID, "")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, getAs(ws.ID, "/issues/by-identifier/"+iss.Identifier, "alice@corp.com"))
	if rec.Code != http.StatusOK {
		t.Fatalf("own identifier = %d, want 200 — a route that 404s for everyone would satisfy "+
			"the no-oracle test and be useless", rec.Code)
	}
	var got model.Issue
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v — body %s", err, rec.Body.String())
	}
	if got.ID != iss.ID || got.Identifier != iss.Identifier {
		t.Fatalf("resolved the WRONG issue: got id=%s identifier=%s, want id=%s identifier=%s",
			got.ID, got.Identifier, iss.ID, iss.Identifier)
	}
}

// ⚠ THE POPULATION-OF-ONE TRAP, WHICH IS WHY THIS TEST EXISTS SEPARATELY. The inert
// `?identifier=` filter measured in W4.20 returned the right issue with ONE issue seeded and
// BOTH with two. Any lookup asserted against a single row proves nothing about selection.
func TestByIdentifier_SelectsAmongSiblings(t *testing.T) {
	d := testutil.New(t)
	h := sec5ReadsChain(d)
	ws := d.Workspace(t)
	sec5Member(t, d, ws.ID, "alice@corp.com")
	team := d.Team(t, ws.ID)
	first := d.Issue(t, ws.ID, team.ID)
	second := d.Issue(t, ws.ID, team.ID)
	if first.Identifier == second.Identifier {
		t.Fatalf("both seeds share identifier %q — this test cannot distinguish anything", first.Identifier)
	}

	for _, want := range []*model.Issue{first, second} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, getAs(ws.ID, "/issues/by-identifier/"+want.Identifier, "alice@corp.com"))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d, want 200", want.Identifier, rec.Code)
		}
		var got model.Issue
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.ID != want.ID {
			t.Fatalf("asked for %s and got %s — the route is returning a row rather than SELECTING one",
				want.Identifier, got.Identifier)
		}
	}
}

// ⚠ ROUTE SHAPE. This repo already carries a warning that `bulk-update` "sits above the {id}
// pattern so chi resolves the literal path before the wildcard. Reorder with care." A new
// sibling of `/{id}` is exactly that hazard, so the neighbours are asserted rather than assumed.
func TestByIdentifier_DoesNotShadowTheExistingRoutes(t *testing.T) {
	d := testutil.New(t)
	h := sec5ReadsChain(d)
	ws := d.Workspace(t)
	sec5Member(t, d, ws.ID, "alice@corp.com")
	iss := d.Issue(t, ws.ID, "")

	for _, c := range []struct {
		name, path string
		want       int
	}{
		{"GET /issues/{id} still resolves by id", "/issues/" + iss.ID, http.StatusOK},
		{"GET /issues/{id}/comments still resolves", "/issues/" + iss.ID + "/comments", http.StatusOK},
		{"GET /issues/search still resolves", "/issues/search?q=Issue", http.StatusOK},
		{"GET /issues still lists", "/issues", http.StatusOK},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, getAs(ws.ID, c.path, "alice@corp.com"))
		if rec.Code != c.want {
			t.Errorf("%s: %d, want %d — the new route changed a neighbour", c.name, rec.Code, c.want)
		}
	}
}

// ⚠ MEASURED, NOT ASSUMED: the two routes read through DIFFERENT store functions. /{id} uses
// getInWorkspace, a bare SELECT; by-identifier uses GetByIdentifier, which additionally attaches
// field values, blocked state, tracked time and scores. Two routes serving the same resource in
// two shapes is a thing a client will trip over, so it is asserted here rather than discovered
// later — whichever way it comes out, this test is the record of which.
func TestByIdentifier_PayloadMatchesTheByIDRoute(t *testing.T) {
	d := testutil.New(t)
	h := sec5ReadsChain(d)
	ws := d.Workspace(t)
	sec5Member(t, d, ws.ID, "alice@corp.com")
	iss := d.Issue(t, ws.ID, "")

	byID := httptest.NewRecorder()
	h.ServeHTTP(byID, getAs(ws.ID, "/issues/"+iss.ID, "alice@corp.com"))
	byIdent := httptest.NewRecorder()
	h.ServeHTTP(byIdent, getAs(ws.ID, "/issues/by-identifier/"+iss.Identifier, "alice@corp.com"))

	if byID.Code != http.StatusOK || byIdent.Code != http.StatusOK {
		t.Fatalf("statuses: by-id %d, by-identifier %d", byID.Code, byIdent.Code)
	}
	var a, b map[string]any
	if err := json.Unmarshal(byID.Body.Bytes(), &a); err != nil {
		t.Fatalf("by-id decode: %v", err)
	}
	if err := json.Unmarshal(byIdent.Body.Bytes(), &b); err != nil {
		t.Fatalf("by-identifier decode: %v", err)
	}
	for k, av := range a {
		bv, present := b[k]
		if !present {
			t.Errorf("by-identifier DROPS the field %q that /{id} returns", k)
			continue
		}
		if toJSON(t, av) != toJSON(t, bv) {
			t.Errorf("field %q differs: by-id %s, by-identifier %s", k, toJSON(t, av), toJSON(t, bv))
		}
	}
	for k := range b {
		if _, present := a[k]; !present {
			t.Errorf("by-identifier ADDS the field %q that /{id} does not return — two shapes for "+
				"one resource is a client trap", k)
		}
	}
	// ⚠ HONEST LIMIT, stated rather than implied: the issue this compares is a BARE seed — no
	// custom field values, no score, no tracked time, not blocked. It therefore proves the two
	// routes emit the same KEY SET, and that their values agree for an empty issue. It does NOT
	// prove they agree for a POPULATED one, and they might not: GetByIdentifier runs four attach
	// helpers that getInWorkspace does not. Seeding those is the next step if anyone needs the
	// stronger claim; asserting it here without seeding them would be the stronger claim made
	// vacuously.
}

func toJSON(t *testing.T, v any) string {
	t.Helper()
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(out)
}
