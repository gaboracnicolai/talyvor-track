package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// route_execution_census_test.go — the routes that have NEVER BEEN EXECUTED BY ANYTHING.
//
// spa_api_surface_census_test.go answers "does a CLIENT call this route" and tags 35 of the 70
// SPA-less routes "NO CALLER ANYWHERE IN THE ESTATE". That is one half of "has this code ever
// run". The other half is "does a TEST execute it", and the two together are the only way to say
// a handler has never executed in production OR in CI.
//
// THE RESULT, MEASURED AT b52d928 (W3.28, tab-c2m8): of those 35, ELEVEN are reached by no
// client and executed by no test. They are `routesNeverExecuted` below. Their handlers are
// registered, authz-gated, and have never run — the strongest form of unverified there is.
//
// ⚠ THE MEASUREMENT TOOK TWO INSTRUMENTS AND THE CHEAPER ONE IS WRONG ON ITS OWN. This is the
// part worth reading before re-running anything.
//
//	(1) VERB SWAP at the registration site (change r.Get to r.Post, see what reds) answers
//	    "does anything depend on this route REGISTRATION".
//	(2) panic() injected as the first statement of the HANDLER BODY, registration left
//	    byte-identical, answers "does anything EXECUTE this handler".
//
//	They disagree, and (1) over-reports untested. Of 19 routes that instrument (1) called
//	untested, instrument (2) proved EIGHT are executed by tests — 42%. The reason is structural
//	and general to this repository: its tests mount a production handler on their OWN
//	`chi.NewRouter()` at a hand-written path literal, e.g.
//	internal/mcp/sprint_no_active_cycle_test.go registers
//	`/v1/workspaces/{workspaceID}/teams/{teamID}/cycles/active` — note `{workspaceID}` where
//	production registers `{wsID}` — and calls `cycle.NewHandler(...).GetActive` directly. Such a
//	test executes the handler thoroughly while depending on the production registration NOT AT
//	ALL. A registration-level mutation cannot see it, in either direction.
//
// ⚠ WHAT CI RE-VERIFIES HERE AND WHAT IT CANNOT, stated rather than implied — the same posture
// spa_api_surface_census_test.go takes about its own estate half:
//
//	RE-VERIFIED EVERY RUN: that each of the eleven is still a REGISTERED route (from the AST, so
//	renaming or deleting one reds), that each is still tagged NO CALLER ANYWHERE IN THE ESTATE in
//	routesWithNoSPACaller (so wiring a UI to one reds), and that the named handler method still
//	EXISTS (so renaming the function reds rather than leaving a row pointing at nothing).
//
//	NOT RE-VERIFIED: the execution half itself. Proving it costs one full `go test ./...` per
//	handler with a panic injected — ~90s x 19 here — which does not belong on the critical path.
//	So this table can go stale in ONE direction only: someone writes a test that executes one of
//	these handlers and the row stays. That direction OVER-reports "never executed", which is the
//	safe way for it to be wrong. Re-measure with
//	~/talyvor-queue/w328-execution-probe-c2m8.py, which carries its own arming controls.
var routesNeverExecuted = map[string]string{
	"GET /v1/workspaces/{}/automation/rules":                   "automation.ListRules",
	"POST /v1/workspaces/{}/automation/rules":                  "automation.CreateRule",
	"GET /v1/workspaces/{}/labels":                             "label.List",
	"POST /v1/workspaces/{}/labels":                            "label.Create",
	"POST /v1/workspaces/{}/notifications/{}/read":             "notification.MarkRead",
	"GET /v1/workspaces/{}/projects/{}/milestones":             "milestone.List",
	"POST /v1/workspaces/{}/projects/{}/milestones":            "milestone.Create",
	"GET /v1/workspaces/{}/teams/{}/cycles":                    "cycle.List",
	"POST /v1/workspaces/{}/teams/{}/cycles":                   "cycle.Create",
	"PATCH /v1/workspaces/{}/teams/{}/statuses/{}":             "workflow.Update",
	"POST /v1/workspaces/{}/teams/{}/cycles/{}/suggest-issues": "ai.SuggestSprint",
}

// ⚠ FLOORS, and they exist because every check in this file reports an ABSENCE — which is also
// what a gutted table and a blind scanner report. minRegisteredRoutes floors the DERIVED set:
// an extractor that returns nothing agrees with any table at all, so it must fail loudly instead.
const (
	minRegisteredRoutes = 120 // 136 registered at b52d928
	neverExecutedCount  = 11  // measured at b52d928; see the header for how
)

func TestRoutesNeverExecutedAreStillRegisteredAndUncalled(t *testing.T) {
	byPair, _ := serverSurface(t)

	// Vacuity floor first: without it every loop below passes on an empty census.
	if len(byPair) < minRegisteredRoutes {
		t.Fatalf("the route extractor found %d registered routes, want >=%d (136 at b52d928). "+
			"It has gone blind — every check in this file would now pass vacuously. Do not lower "+
			"this to make a red go green.", len(byPair), minRegisteredRoutes)
	}

	var unregistered, nowCalled []string
	for pair, handler := range routesNeverExecuted {
		if _, ok := byPair[pair]; !ok {
			unregistered = append(unregistered, pair+"  ("+handler+")")
		}
		tag, ok := routesWithNoSPACaller[pair]
		if !ok || !strings.HasPrefix(tag, "NO CALLER ANYWHERE") {
			nowCalled = append(nowCalled, pair+"  ("+handler+")")
		}
	}
	sort.Strings(unregistered)
	sort.Strings(nowCalled)

	if len(unregistered) > 0 {
		t.Errorf("%d row(s) name a route that is NO LONGER REGISTERED:\n  %s\n\n"+
			"Either the route was renamed — in which case fix the row — or it was deleted, which "+
			"is the good outcome for a handler nothing has ever executed. Deleting the row is "+
			"correct, and neverExecutedCount must come down with it.",
			len(unregistered), strings.Join(unregistered, "\n  "))
	}
	if len(nowCalled) > 0 {
		t.Errorf("%d row(s) are no longer tagged NO CALLER ANYWHERE IN THE ESTATE:\n  %s\n\n"+
			"Something now calls them, which is the direction this table exists to notice. Drop "+
			"those rows — a route with a caller has been executed and is not this table's "+
			"business.", len(nowCalled), strings.Join(nowCalled, "\n  "))
	}

	// The handler each row names must still exist, or the row points at nothing and the table
	// degrades into prose that agrees with any tree.
	for pair, handler := range routesNeverExecuted {
		pkg, method, ok := strings.Cut(handler, ".")
		if !ok {
			t.Errorf("row %q has handler %q, want pkg.Method", pair, handler)
			continue
		}
		if !handlerMethodExists(t, pkg, method) {
			t.Errorf("row %q names handler %s.%s, and no such method exists under internal/%s. "+
				"A row pointing at a function that is gone cannot fail for the right reason again.",
				pair, pkg, method, pkg)
		}
	}

	if len(routesNeverExecuted) < neverExecutedCount {
		t.Fatalf("the table has %d rows; %d were MEASURED at b52d928. Shrinking it means a "+
			"handler that had never run now runs — genuinely good news, and it has to be earned "+
			"rather than assumed: re-run ~/talyvor-queue/w328-execution-probe-c2m8.py, confirm "+
			"the row's handler reds under an injected panic, and lower this constant in the same "+
			"commit that drops the row.", len(routesNeverExecuted), neverExecutedCount)
	}
}

// handlerMethodExists reports whether internal/<pkg> declares a method <method> whose first
// parameter is an http.ResponseWriter. Parsed from the AST rather than grepped, so a mention in
// a comment or a string literal is not a declaration — the distinction W4.35's C8 control exists
// for. Test files are excluded: a handler that only a test declares is not a shipped handler.
func handlerMethodExists(t *testing.T, pkg, method string) bool {
	t.Helper()
	dir := filepath.Join(repoRoot(t), "internal", pkg)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, 0)
		if perr != nil {
			continue
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || fd.Name.Name != method || fd.Type.Params == nil {
				continue
			}
			if len(fd.Type.Params.List) == 0 {
				continue
			}
			if sel, ok := fd.Type.Params.List[0].Type.(*ast.SelectorExpr); ok && sel.Sel.Name == "ResponseWriter" {
				return true
			}
		}
	}
	return false
}
