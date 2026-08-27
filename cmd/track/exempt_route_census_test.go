package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// exempt_route_census_test.go — the routes that skip gwAuth + wsAuthz, and what authenticates each
// of them instead.
//
// ⚠ WHY THIS IS THE CENSUS AND "EVERY ROUTE REACHES AN AUTHZ DECISION" IS NOT. main.go wraps the
// whole /v1 tree — `r.Use(gwAuth)` then `r.Use(wsAuthz)` — so authz here is enforced ONE LAYER
// ABOVE the registration site, and 122 of 136 routes correctly have no authz call anywhere near
// them. A lens-shaped census that looked for a decision at the registration site would report 122
// findings, every one false. The population that matters is the EXEMPT one.
//
// `gwExempt(p)` is the predicate that turns both middlewares off. A route matching it is
// unauthenticated as far as the shared chain is concerned, and must bring its own. These 14 are the
// product's entire unauthenticated surface and nothing in this repository enumerated them.
//
// ⚠ ALL FOURTEEN CARRY THEIR OWN AUTH TODAY — MEASURED, ONE AT A TIME, NOT ASSUMED. That is the
// finding: there is nothing to fix, and no signal at all if the fifteenth does not. Adding
// `/v1/public/anything` today is silently unauthenticated; this test is what makes it a decision.

// ── the resolver ────────────────────────────────────────────────────────────────────────────────
//
// ⚠ IT IS AST-BASED BECAUSE A TEXT SCAN GETS THIS WRONG, AND MINE DID. Routes are registered
// relative to enclosing `r.Route(prefix, func(r chi.Router){…})` blocks and inside `Mount(r)`
// methods whose `r` is main.go's `/v1` router. A grep for path literals reported that the
// `/v1/public/` exemption matched NO route — because featureboard registers its five public routes
// inside a nested Route("/public/boards/{wsSlug}/{boardSlug}"). Five anonymous endpoints, three of
// them WRITES, invisible to the scan that was about to declare the prefix dead.

var httpVerbs = map[string]bool{
	"Get": true, "Post": true, "Put": true, "Delete": true, "Patch": true, "Head": true, "Options": true,
}

func routeLiteral(e ast.Expr) (string, bool) {
	b, ok := e.(*ast.BasicLit)
	if !ok || b.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(b.Value)
	return s, err == nil
}

// collectRoutes walks a function body, carrying the prefix accumulated from enclosing Route calls.
func collectRoutes(n ast.Node, prefix string, out *[]string) {
	ast.Inspect(n, func(nd ast.Node) bool {
		call, ok := nd.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		lit, isLit := routeLiteral(call.Args[0])
		if sel.Sel.Name == "Route" && isLit && len(call.Args) == 2 {
			if fl, ok := call.Args[1].(*ast.FuncLit); ok {
				collectRoutes(fl.Body, prefix+lit, out)
				return false // recursed with the deeper prefix; do not walk it flat as well
			}
		}
		if httpVerbs[sel.Sel.Name] && isLit && strings.HasPrefix(lit, "/") {
			*out = append(*out, sel.Sel.Name+" "+prefix+lit)
		}
		return true
	})
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("root %s has no go.mod", root)
	}
	return root
}

// allRoutes returns every registered route as "VERB /full/path".
//
// ⚠ THE `Mount` BASE IS "/v1" AND THAT IS CHECKED, NOT ASSUMED — see
// TestEveryMountIsInsideTheV1RouteBlock. If a handler were ever mounted somewhere else, every path
// this function reports for it would be wrong, silently.
func allRoutes(t *testing.T) []string {
	t.Helper()
	var routes []string
	fset := token.NewFileSet()
	err := filepath.Walk(repoRoot(t), func(p string, i os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		if i.IsDir() {
			switch i.Name() {
			case ".git", "vendor", "node_modules", "frontend", "migrations", "scripts", "docs":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, p, nil, 0)
		if perr != nil {
			return nil // a file this parser cannot read is not a route source
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			base := ""
			if fd.Recv != nil && fd.Name.Name == "Mount" {
				base = "/v1"
			}
			collectRoutes(fd.Body, base, &routes)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	sort.Strings(routes)
	return routes
}

// gwExemptPrefixes parses the prefixes out of main.go's own gwExempt predicate rather than copying
// them. ⚠ A HAND-TYPED COPY OF A SECURITY PREDICATE IS A SECOND SOURCE OF TRUTH: add a seventh
// prefix in main.go and a copied list here would keep passing while the surface grew.
var exemptPrefixRe = regexp.MustCompile(`strings\.HasPrefix\(p, "([^"]+)"\)`)

func gwExemptPrefixes(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(repoRoot(t), "cmd", "track", "main.go"))
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	body := string(src)
	start := strings.Index(body, "gwExempt := func(p string) bool {")
	if start < 0 {
		t.Fatal("gwExempt is gone from main.go — the exemption predicate this census is about no " +
			"longer exists under that name, and every result below is meaningless until it is found")
	}
	end := strings.Index(body[start:], "\n\t}")
	if end < 0 {
		t.Fatal("could not find the end of gwExempt")
	}
	var out []string
	for _, m := range exemptPrefixRe.FindAllStringSubmatch(body[start:start+end], -1) {
		out = append(out, m[1])
	}
	sort.Strings(out)
	if len(out) < 3 {
		t.Fatalf("parsed %d exempt prefixes (%v) — the parse is broken, and a broken parse reports "+
			"an empty unauthenticated surface", len(out), out)
	}
	return out
}

// ── the record ──────────────────────────────────────────────────────────────────────────────────

// exemptRoutes is every route that skips gwAuth + wsAuthz, and what authenticates it instead.
// ⚠ EACH ANSWER WAS READ, NOT INFERRED FROM THE PREFIX.
var exemptRoutes = map[string]string{
	"Post /v1/lens/webhook":    "HMAC-SHA256 of the body against the shared secret, X-Lens-Signature; crypto/hmac + crypto/subtle in internal/lensintegration/webhook.go.",
	"Post /v1/webhooks/github": "HMAC-SHA256, X-Hub-Signature-256, in internal/automation/github.go — AND it fails CLOSED: ServeHTTP 401s when the secret is empty, BEFORE verifyGitHubSignature is reached. That ordering is load-bearing: hmac.New with an empty key produces a signature anyone can compute, so an empty secret without the early return would accept forged deliveries. Already guarded by TestGitHub_EmptySecretRefuses, which signs with the empty key and asserts 401 — W6.32 wrote a second version of that test before finding it, and deleted it rather than ship the duplicate.",

	"Get /v1/service/members":    "constant-time bearer against TRACK_MEMBER_SYNC_SECRET; secret==\"\" 401s ALL requests (member-sync disabled rather than open).",
	"Get /v1/service/workspaces": "the same constant-time service bearer (internal/member/workspaces.go).",

	"Get /v1/invite/{token}":         "the invite token IS the credential — an opaque token in the path, required to accept without an account.",
	"Post /v1/invite/{token}/accept": "same invite token.",

	"Get /v1/guest/workspaces/{wsID}/projects/{projectID}/issues": "guest Bearer token: the route group applies store.Middleware AND store.RequireGuest (a hard gate — the middleware alone is deliberately permissive), then the handler checks claims.WorkspaceID against the path {wsID}.",
	"Get /v1/guest/workspaces/{wsID}/issues/{id}":                 "same guest gate; handler 403s on WS_MISMATCH and narrows further for project-scoped guests.",
	"Post /v1/guest/workspaces/{wsID}/issues/{id}/comments":       "same guest gate; role + object tenancy enforced in the handler (guest WRITE).",

	"Get /v1/public/boards/{wsSlug}/{boardSlug}/":                       "ANONYMOUS BY DESIGN. Store.GetPublicBoard resolves by slug with `AND b.public = true`, so a private workspace can host one public board without exposing the others.",
	"Get /v1/public/boards/{wsSlug}/{boardSlug}/posts":                  "same public-board filter.",
	"Post /v1/public/boards/{wsSlug}/{boardSlug}/posts":                 "ANONYMOUS WRITE. Same b.public filter, plus board.AllowAnonymous: a board that has not opted in requires an author email.",
	"Post /v1/public/boards/{wsSlug}/{boardSlug}/posts/{postID}/vote":   "ANONYMOUS WRITE. Same b.public filter.",
	"Delete /v1/public/boards/{wsSlug}/{boardSlug}/posts/{postID}/vote": "ANONYMOUS WRITE. Same b.public filter.",
}

// totalRoutes is recorded so the census cannot silently start covering a fraction of the tree.
const totalRoutes = 136

func exemptSubset(t *testing.T) []string {
	t.Helper()
	prefixes := gwExemptPrefixes(t)
	var out []string
	for _, r := range allRoutes(t) {
		p := strings.SplitN(r, " ", 2)[1]
		for _, pre := range prefixes {
			if strings.HasPrefix(p, pre) {
				out = append(out, r)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// ⚠ THE GUARD. A route under an exempt prefix is unauthenticated by the shared chain. Adding one is
// currently silent; this makes it a decision.
func TestEveryExemptRouteIsRecordedWithItsOwnAuth(t *testing.T) {
	got := exemptSubset(t)
	if len(got) == 0 {
		t.Fatal("no exempt routes found — either the resolver or the prefix parse is broken, and " +
			"both failures report an empty unauthenticated surface")
	}
	for _, r := range got {
		why, ok := exemptRoutes[r]
		if !ok {
			t.Errorf("%s skips gwAuth AND wsAuthz and nothing here says what authenticates it.\n"+
				"    It matched one of main.go's gwExempt prefixes, so neither the gateway transit "+
				"proof nor workspace membership is checked for it. Say what does — an HMAC, a "+
				"constant-time bearer, a guest token, or \"anonymous by design, and here is the "+
				"filter that bounds it\".", r)
			continue
		}
		if strings.TrimSpace(why) == "" {
			t.Errorf("%s is recorded with an empty reason", r)
		}
	}
	for r := range exemptRoutes {
		if !contains(got, r) {
			t.Errorf("this census records %s as exempt and it is no longer registered under an "+
				"exempt prefix. If it moved behind the auth chain, delete the entry and say so — a "+
				"stale entry makes the unauthenticated surface look bigger than it is", r)
		}
	}
	t.Logf("MEASURED: %d routes total, %d exempt from gwAuth+wsAuthz, all %d with their own auth "+
		"recorded.", len(allRoutes(t)), len(got), len(exemptRoutes))
}

// ⚠ THE PREFIX LIST IS READ FROM main.go, so widening the exemption fails HERE until the new
// routes are classified.
func TestExemptPrefixesAreTheOnesMainGoUses(t *testing.T) {
	got := gwExemptPrefixes(t)
	want := []string{"/v1/guest/", "/v1/invite/", "/v1/lens/webhook", "/v1/public/", "/v1/service/", "/v1/webhooks/"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("gwExempt's prefixes changed.\n  now:   %v\n  W6.32: %v\n\n"+
			"    ADDING one turns off the gateway transit proof AND workspace membership for every "+
			"route beneath it. REMOVING one puts routes behind the chain — check they still work "+
			"for their callers, which are webhooks and anonymous browsers.", got, want)
	}
}

// ⚠ THE RESOLVER'S OWN CONTROL. A flat scan for path literals misses everything registered inside a
// nested Route — which is where five of the fourteen live, three of them anonymous writes. If this
// ever stops finding them, the census silently shrinks to the easy cases.
func TestResolverFindsRoutesNestedInsideARouteBlock(t *testing.T) {
	got := allRoutes(t)
	for _, must := range []string{
		"Post /v1/public/boards/{wsSlug}/{boardSlug}/posts",
		"Delete /v1/public/boards/{wsSlug}/{boardSlug}/posts/{postID}/vote",
	} {
		if !contains(got, must) {
			t.Errorf("the resolver did not find %q, which featureboard registers inside "+
				"r.Route(\"/public/boards/{wsSlug}/{boardSlug}\", …). A scan that misses nested "+
				"Route blocks reports this product's anonymous write surface as empty.", must)
		}
	}
	if len(got) != totalRoutes {
		t.Errorf("resolved %d routes; W6.32 measured %d. A DROP means the resolver stopped seeing a "+
			"registration shape — check nested Route and Mount before believing the number.",
			len(got), totalRoutes)
	}
}

// ⚠ THE PREMISE `Mount` RESTS ON. Every path this census reports for a Mount method assumes that
// method's router is main.go's /v1 tree. Mount one somewhere else and every one of those paths is
// wrong — silently, and in the direction that hides an exempt route.
func TestEveryMountIsInsideTheV1RouteBlock(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(repoRoot(t), "cmd", "track", "main.go"))
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	lines := strings.Split(string(src), "\n")
	start, end := -1, -1
	for i, l := range lines {
		if start < 0 && strings.Contains(l, `r.Route("/v1", func(r chi.Router) {`) {
			start = i
			continue
		}
		if start >= 0 && l == "\t})" {
			end = i
			break
		}
	}
	if start < 0 || end < 0 {
		t.Fatal(`could not locate the r.Route("/v1", …) block — the /v1 base every Mount() path in ` +
			"this census assumes is no longer identifiable")
	}
	var outside []int
	for i, l := range lines {
		if !strings.Contains(l, ".Mount(r)") {
			continue
		}
		if i < start || i > end {
			outside = append(outside, i+1)
		}
	}
	if len(outside) > 0 {
		t.Errorf("%d .Mount(r) call(s) sit outside the /v1 Route block (lines %v). Every route that "+
			"handler registers is reported by this census under /v1 and is served somewhere else.",
			len(outside), outside)
	}
}

func contains(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}
