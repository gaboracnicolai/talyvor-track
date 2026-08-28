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

// env_example_census_test.go — a variable the process reads and `.env.example` does not list is
// UNDISCOVERABLE: the operator's only inventory of what this service can be configured with does
// not mention it. For a tuning knob that is untidy. For a CREDENTIAL it decides whether a security
// control is armed, and the operator is never asked.
//
// ⚠ THIS IS A DIFFERENT AXIS FROM compose_env_reach_test.go, WHICH IS WHY IT IS A SEPARATE FILE.
// That guard asks "does the compose file FORWARD this credential to the container" — a variable
// can be perfectly forwarded and still be one nobody knows to set. This one asks "is it written
// down where an operator looks". Both can hold while the other fails.
//
// ⚠⚠ MEASURED, W3.44 (tab-j4q7), BEFORE THIS GUARD EXISTED: the process read 18 variables and
// `.env.example` listed 13, and SIX of the gaps were operator-facing — three of them credentials:
// TRACK_GUEST_SECRET, TRACK_MEMBER_SYNC_SECRET and TRACK_INTEGRATION_ENCRYPTION_KEY, plus
// TRACK_INVITE_BASE_URL, TRACK_LENS_WEBHOOK_FRESHNESS and TRACK_LISTEN_ADDR.
//
// ⚠⚠⚠ THE WORST OF THEM IS TRACK_GUEST_SECRET AND ITS COST IS NOT UNTIDINESS. Unset,
// guest.newStore SILENTLY SYNTHESISES a random 32-byte secret per process. Measured by driving
// the real constructor: a token signed by one store is rejected by a second with
// "guest: signature mismatch", while two stores given the same explicit secret agree. So on an
// undocumented deployment every outstanding guest link dies at each restart, and across two
// instances guest access fails intermittently — with no error at boot and no log line. The code
// comment that knows this says "operator must set GUEST_SECRET" — a variable name that does not
// exist anywhere in this tree; the real one is TRACK_GUEST_SECRET, and it was in no template.
// Whether that fallback should fail closed instead is an OPERATIONS DECISION and is filed, not
// taken here. Making the variable discoverable is not a decision.

// envExampleExemptions are variables the process reads that must NOT appear in `.env.example`,
// with the reason. An exemption is a decision; it is written here so the next person can disagree
// with it rather than guess. The guard FAILS if an exemption stops being needed, so a stale one
// cannot quietly widen the hole.
var envExampleExemptions = map[string]string{
	"TRACK_TEST_DATABASE_URL": "test-harness only: internal/testutil.RequireDatabaseURL reads it so " +
		"real-Postgres tests FAIL rather than skip. It is a CI input, never a deployment input, and " +
		"listing it in an operator template would invite someone to point production at it.",
}

// envReadCall matches the shapes this tree uses to read the environment. Kept in one place so the
// AST walk below and this list cannot drift apart.
func isEnvReadFunc(name string) bool {
	return name == "Getenv" || name == "LookupEnv" || strings.HasPrefix(name, "getEnv")
}

type envRead struct {
	name string // resolved variable name
	site string // file:line
}

// collectEnvReads enumerates every environment variable the NON-TEST tree reads, by PARSING.
// It is shared: compose_env_reach_test.go's credential-forwarding guard reads the same
// population, so "how this tree reads the environment" is defined exactly once.
//
// ⚠ _test.go IS SKIPPED, and the reason is carried here because the sentence that used to
// state it lived in the regex this replaced: a test reading an env var says nothing about
// what the DEPLOYMENT needs, and counting them would produce exemptions for names no
// container ever wants and no operator should be handed.
//
// ⚠ IT RESOLVES CONSTANTS, AND THAT IS THE POINT RATHER THAN A FLOURISH. The sibling
// compose_env_reach_test.go matches env reads with a regex that requires a STRING LITERAL
// argument, so `os.Getenv(config.LogLevelEnv)` in main.go is invisible to it. Nothing that
// indirection currently hides is credential-shaped, so that guard is correct today — but it is
// correct BY COINCIDENCE, not by construction, and one `const MintKeyEnv = "..."` would blind it
// with no test going red. This walk takes the argument, and if it cannot resolve it to a concrete
// name it FAILS LOUDLY rather than skipping: an unresolvable read is precisely the blind spot.
func collectEnvReads(t *testing.T) ([]envRead, []string) {
	t.Helper()
	fset := token.NewFileSet()
	var files []struct {
		path string
		f    *ast.File
	}
	for _, root := range []string{"..", "../../internal"} {
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") ||
				strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				t.Fatalf("parse %s: %v", path, perr)
			}
			files = append(files, struct {
				path string
				f    *ast.File
			}{path, f})
			return nil
		})
	}

	// Pass 1 — every top-level string constant in the tree, by bare name. `config.LogLevelEnv`
	// and a local `EnvDatabaseURL` both resolve through this.
	consts := map[string]string{}
	for _, e := range files {
		for _, d := range e.f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, nm := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if v, err := strconv.Unquote(lit.Value); err == nil {
							consts[nm.Name] = v
						}
					}
				}
			}
		}
	}

	// Pass 2 — the call sites.
	var reads []envRead
	var unresolved []string
	for _, e := range files {
		ast.Inspect(e.f, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok {
				return true
			}
			// Parameters of the enclosing function are FORWARDING, not hiding:
			// internal/config's getEnv(key, fallback) does os.Getenv(key). The variable is
			// named at that helper's own call sites, which this walk visits separately.
			params := map[string]bool{}
			if fn.Type.Params != nil {
				for _, p := range fn.Type.Params.List {
					for _, nm := range p.Names {
						params[nm.Name] = true
					}
				}
			}
			ast.Inspect(fn, func(m ast.Node) bool {
				ce, ok := m.(*ast.CallExpr)
				if !ok || len(ce.Args) == 0 {
					return true
				}
				var fname string
				switch f := ce.Fun.(type) {
				case *ast.Ident:
					fname = f.Name
				case *ast.SelectorExpr:
					fname = f.Sel.Name
				default:
					return true
				}
				if !isEnvReadFunc(fname) {
					return true
				}
				site := fset.Position(ce.Pos()).String()
				switch a := ce.Args[0].(type) {
				case *ast.BasicLit:
					if v, err := strconv.Unquote(a.Value); err == nil {
						reads = append(reads, envRead{v, site})
						return true
					}
				case *ast.Ident:
					if params[a.Name] {
						return true // forwarding helper
					}
					if v, ok := consts[a.Name]; ok {
						reads = append(reads, envRead{v, site})
						return true
					}
				case *ast.SelectorExpr:
					if v, ok := consts[a.Sel.Name]; ok {
						reads = append(reads, envRead{v, site})
						return true
					}
				}
				unresolved = append(unresolved, site+" ("+fname+")")
				return true
			})
			return false
		})
	}
	return reads, unresolved
}

var envExampleEntry = regexp.MustCompile(`(?m)^([A-Z][A-Z0-9_]*)=`)

func TestEveryEnvVarTheProcessReadsIsInEnvExample(t *testing.T) {
	reads, unresolved := collectEnvReads(t)

	// An env read this walk cannot resolve is the blind spot itself, not a nuisance.
	if len(unresolved) > 0 {
		t.Fatalf("%d environment read(s) could not be resolved to a variable name: %v\n"+
			"Each is a variable no census can see. Give it a top-level string const, or add a "+
			"resolution rule here — do not leave it unnamed.", len(unresolved), unresolved)
	}

	// Non-vacuity: an enumeration that finds nothing passes everything, which is exactly the
	// failure this file exists to prevent one level down.
	uniq := map[string]string{}
	for _, r := range reads {
		if _, seen := uniq[r.name]; !seen {
			uniq[r.name] = r.site
		}
	}
	if len(uniq) < 15 {
		t.Fatalf("only %d environment variables found in the tree — the enumeration is broken, "+
			"and a guard that enumerates nothing passes everything. Found: %v", len(uniq), sortedNames(uniq))
	}

	raw, err := os.ReadFile("../../.env.example")
	if err != nil {
		t.Fatalf("read .env.example: %v", err)
	}
	documented := map[string]bool{}
	for _, m := range envExampleEntry.FindAllStringSubmatch(string(raw), -1) {
		documented[m[1]] = true
	}
	if len(documented) < 10 {
		t.Fatalf("only %d entries parsed out of .env.example — the parse is broken, not the file",
			len(documented))
	}

	var missing []string
	for name, site := range uniq {
		if _, exempt := envExampleExemptions[name]; exempt {
			continue
		}
		if !documented[name] {
			missing = append(missing, name+"  (read at "+site+")")
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d environment variable(s) the process reads are absent from .env.example, so an "+
			"operator has no way to learn they exist:\n  %s\nAdd an entry (a blank value and a "+
			"comment is enough) or an exemption with a reason.",
			len(missing), strings.Join(missing, "\n  "))
	}

	// A stale exemption is a hole that widens silently: if the variable is no longer read, or has
	// since been documented, the exemption must go rather than sit there excusing a name.
	for name, reason := range envExampleExemptions {
		if _, stillRead := uniq[name]; !stillRead {
			t.Errorf("exemption for %s is stale — nothing reads it any more. Reason on file: %s", name, reason)
		}
		if documented[name] {
			t.Errorf("%s is exempt from .env.example but IS listed there — the exemption says it must "+
				"not be. Reason on file: %s", name, reason)
		}
	}
}

func sortedNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
