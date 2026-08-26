package authz_test

// resolved_workspace_test.go — THE STRUCTURAL HALF OF THE FLAT-ROUTE TENANCY GATE.
//
// WHY THIS IS STRUCTURAL AND NOT BEHAVIOURAL, MEASURED RATHER THAN ASSERTED.
// AuthorizeWorkspace matches with EXACT STRING EQUALITY (authz.go#membershipFor:
// `if m.WorkspaceID == wsID`). So on every path where the gate says ok, the
// caller-supplied argument and the returned Membership.WorkspaceID are THE SAME
// STRING BY CONSTRUCTION. Swapping one for the other downstream changes no
// observable behaviour on any input a test can construct — there is no fixture in
// which the two differ while the gate passes. That is not a gap in the test suite;
// it is a proof that no behavioural test can hold this property, which is why the
// rule is enforced here on the SYNTAX instead.
//
// WHAT IT COSTS TO GET IT WRONG. The equality is a property of TODAY'S matcher, not
// of the surface. Anything that makes resolution non-identity — case-insensitive
// match, whitespace trim, a slug or alias resolving to an id, a workspace merge
// mapping an old id to a new one — instantly splits the two values, and every site
// still holding the caller's string starts scoping on the string the CALLER chose
// while the 403 that is supposed to be the tenancy boundary keeps passing. The
// caller's string is data; Membership.WorkspaceID is the server's answer.
//
// THE RULE: for every authz.AuthorizeWorkspace(ctx, X) where X is a plain
// identifier, X must not be referenced anywhere after that call in the enclosing
// function. Take the workspace from the returned Membership.
//
// It deliberately does NOT require the Membership to be bound: a site that writes
// `if _, ok := authz.AuthorizeWorkspace(ctx, wsID); !ok` and then keeps using wsID
// has exactly the same defect, and keying the rule on the binding would make
// discarding the Membership the way to evade the rule.
//
// HONEST LIMITS, so nobody reads this as more than it is:
//   - "After" is TEXTUAL POSITION, not control flow. A gate inside a loop whose
//     argument is used textually earlier in that loop body would not be seen.
//   - An argument that is not a plain identifier (job.WorkspaceID at
//     importer/job_handler.go#status) carries no name to track, so the rule says
//     nothing about it. That site authorizes a workspace READ FROM THE DATABASE,
//     not one supplied by the caller, which is why it is a different shape.
//   - An ALIAS TAKEN BEFORE THE GATE evades it: `wsID := workspaceID` above the call,
//     then `newClient(..., wsID, ...)` below it, carries the caller's string under a
//     name the rule never learned. MEASURED, not assumed — control C10 in
//     scripts/w34-resolvedws-controls-8j5q.py scores it NOT CAUGHT. Closing it needs
//     dataflow rather than a name, which is a bigger instrument than this one; it is
//     written down here so the next reader inherits the limit instead of rediscovering
//     it from an incident.
//   - This says nothing about whether the refusal is enforced — .semgrep/
//     workspace-authz.yml rule C's message records that limit, and the refusal is
//     held by the behavioural cross-tenant tests. That hand-off is no longer taken on
//     trust either: control C9b deletes the integrations status refusal and asks the
//     BEHAVIOURAL test, which catches it. C9 (structural stays green) and C9b
//     (behavioural reds) are the two halves of one claim.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Population floor. Measured at dda4029: EIGHT production call sites (importer x3,
// integrations x2, importer/job status, mcp, realtime). A floor rather than an
// equality so a new authorized route does not red — but a rename of
// AuthorizeWorkspace, a package move, or a walk that stops reading files empties
// the scan, and a rule with nothing to say reports a clean product. That is the
// failure mode this number exists for, and it is positive-controlled.
const minGateSites = 8

// The scan must actually read the tree. Measured at dda4029: 114 non-test .go files
// under internal/ + cmd/.
const minParsedFiles = 100

type gateSite struct {
	pos     token.Position
	fn      string
	argName string           // "" when the argument is not a plain identifier
	uses    []token.Position // references to argName AFTER the gate call
}

func TestAuthorizeWorkspace_DownstreamUsesTheResolvedWorkspace(t *testing.T) {
	root := repoRoot(t)
	sites, parsed := scanGateSites(t, root, "AuthorizeWorkspace")

	if parsed < minParsedFiles {
		t.Fatalf("the scan read %d non-test .go files, floor is %d — the walk is not reading the tree, "+
			"so a clean result here would mean nothing", parsed, minParsedFiles)
	}
	if len(sites) < minGateSites {
		t.Fatalf("found %d authz.AuthorizeWorkspace call sites, floor is %d — the scan has been emptied "+
			"(renamed callee, moved package, or a broken walk); a rule that matches nothing cannot fail",
			len(sites), minGateSites)
	}

	for _, s := range sites {
		if len(s.uses) == 0 {
			continue
		}
		for _, u := range s.uses {
			t.Errorf("%s: %s uses the CALLER-SUPPLIED %q after authz.AuthorizeWorkspace (gate at %s).\n"+
				"\tThe authorized workspace is the returned Membership's WorkspaceID — use m.WorkspaceID.\n"+
				"\tThe two are equal today only because membershipFor matches on exact string equality; "+
				"that is a property of the matcher, not of this route.",
				u, s.fn, s.argName, s.pos)
		}
	}
}

// scanGateSites parses every non-test .go file under internal/ and cmd/ and returns
// one gateSite per call to authz.<callee>, plus the number of files parsed. callee is
// a parameter ONLY so the positive control can blind the scan by name.
func scanGateSites(t *testing.T, root, callee string) ([]gateSite, int) {
	t.Helper()
	fset := token.NewFileSet()
	var sites []gateSite
	parsed := 0

	for _, sub := range []string{"internal", "cmd"} {
		err := filepath.Walk(filepath.Join(root, sub), func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			f, perr := parser.ParseFile(fset, p, nil, 0)
			if perr != nil {
				return perr
			}
			parsed++
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				sites = append(sites, gateSitesIn(fset, fn, callee)...)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", sub, err)
		}
	}
	return sites, parsed
}

func gateSitesIn(fset *token.FileSet, fn *ast.FuncDecl, callee string) []gateSite {
	var out []gateSite
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !isAuthzCall(call, callee) || len(call.Args) == 0 {
			return true
		}
		s := gateSite{pos: fset.Position(call.Pos()), fn: fn.Name.Name}
		arg, ok := call.Args[len(call.Args)-1].(*ast.Ident)
		if !ok {
			out = append(out, s) // no name to track — counted for the floor, no rule applied
			return true
		}
		s.argName = arg.Name
		end := call.End()
		ast.Inspect(fn.Body, func(n2 ast.Node) bool {
			id, ok := n2.(*ast.Ident)
			if !ok || id.Name != arg.Name || id.Pos() <= end {
				return true
			}
			s.uses = append(s.uses, fset.Position(id.Pos()))
			return true
		})
		out = append(out, s)
		return true
	})
	return out
}

func isAuthzCall(call *ast.CallExpr, callee string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != callee {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "authz"
}

// repoRoot walks up from the test's working directory to the directory holding
// go.mod. Fatals rather than falling back to a relative guess: a scan rooted at the
// wrong directory finds nothing and reads as a clean product.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s — cannot locate the repo root to scan", dir)
		}
		dir = parent
	}
}
