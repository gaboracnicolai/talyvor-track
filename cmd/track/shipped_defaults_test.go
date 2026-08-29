package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// shipped_defaults_test.go — THE VALUE A CALLER WHO SUPPLIES NOTHING ACTUALLY GETS.
//
// ⚠ MEASURED BEFORE IT WAS WRITTEN, BY MUTATION (W3.49, tab-k4m7). Two harnesses
// (~/talyvor-queue/w349-default-census-k4m7.py and w349-inline-census-k4m7.py) changed one
// shipped default at a time and ran the whole suite against real Postgres. RED = something
// depends on the value; GREEN = it can be set to anything and CI never notices.
//
//	61 defaults measured · 13 PINNED · 48 UNPINNED
//
// This repo is the fifth and last in an estate-wide sweep (talyvor-docs 12/14 undefended,
// talyvor-code's MCP bind default, talyvor-lens 12/26, talyvor-suite/apps/bff 4/10). It is
// the dirtiest of the five, and the split is the interesting part rather than the number.
//
// ⚠ THE SHAPE THE OTHER FOUR REPOS ALSO HAD, AND THIS ONE SHOWS IT IN ONE FILE: the repo
// defends the values it thought about, and the value on the next line went unwatched.
// internal/workspace/store.go writes `ws.Plan = "free"` TWICE — once on create, once on
// update. Change the create copy to "enterprise" and TestCreate_InsertsWorkspace goes red.
// Change the UPDATE copy to "enterprise" and the entire suite stays green. Same literal,
// same file, same billing field; one defended, one not.
//
// ⚠ WHAT ELSE CAME BACK GREEN, so the list is arguable rather than implied: the SEC-7 replay
// window and the bind address (both in internal/config, and both pinned by
// internal/config/shipped_defaults_test.go instead of here — see the note on reach below);
// httpx.DefaultMaxBody, the body cap EVERY JSON route inherits; ImportMaxBody at 96 MiB;
// every dbresil breaker constant; both Lens sync intervals; the invite-link base URL, which
// falls back to http://localhost:5173 and is stitched into a link emitted to a human.
//
// ⚠ THIS FILE CHANGES NO VALUE. Every literal recorded below is the literal already
// shipping. Whether any of them is the RIGHT value — whether 0.0.0.0 is the right bind,
// whether 96 MiB is the right import cap — is a product or operations decision and is
// deliberately NOT taken here. What changes is that altering one becomes an edit to a named
// table with a reason next to it, instead of a silent one-token diff nothing can see.
//
// ⚠ WHAT THIS DOES NOT PROVE, stated so it is not over-read. (1) REACH: pinning
// jobMaxUploadBytes does not prove any handler applies it. (2) It pins the DECLARED literal,
// read out of the source, not a value observed at runtime — for a compile-time constant
// those are the same thing, but it is not the same instrument. (3) Its population is the two
// SHAPES below and nothing else: a default written any other way is invisible to it, which
// is exactly why internal/config's three defaults are pinned by OBSERVATION in their own
// package — `getEnv("TRACK_LISTEN_ADDR", "0.0.0.0:3000")` is a call argument and neither
// shape here can see it. A census that cannot see a whole shape must say so out loud.

// ⚠ THE COST OF THE `#n` ORDINAL, NAMED RATHER THAN DISCOVERED LATER. Two fallbacks for the
// same left-hand side in one file are numbered in source order, so INSERTING one above an
// existing one renumbers every row below it and reds several at once. That is loud and it is
// correct — the alternative, keying on line number, breaks on any edit above; keying on value
// alone cannot tell ws.Plan's two copies apart, which is the distinction that produced this
// file's sharpest finding. The renumbering is a re-record, not a bug.

// defaultNameRE selects declarations whose NAME says they are a default. It is deliberately
// loose (any case, anywhere in the name) because a narrow pattern is how a default escapes.
var defaultNameRE = regexp.MustCompile(`(?i)default`)

// alsoNamed are declarations that ARE the value you get when you ask for nothing but do not
// carry the word "default" — the caps and bounds. They are listed rather than pattern-matched
// because a pattern for `^(max|min)[A-Z]` sweeps in loop bounds and buffer sizes that are not
// defaults at all, and a census padded with rows nobody means is worse than a shorter one.
var alsoNamed = map[string]string{
	"maxLimit":                    "hard cap on the roster page size",
	"jobMaxUploadBytes":           "upload cap on the async import job",
	"maxIntegrationBody":          "body cap on the provider-token route",
	"GitHubWebhookMaxBody":        "body cap on the GitHub webhook",
	"ImportMaxBody":               "body cap on the multipart CSV import",
	"MinMemberSyncSecretLen":      "minimum strength of the all-tenant roster token",
	"IntegrationEncryptionKeyLen": "AES-256 key length",
	"MinSecretLen":                "shortest gateway secret the auth boundary will defend",
}

type foundDefault struct {
	key   string // "<relpath>::<name>" or "<relpath>::<lhs>#<n>"
	value string // normalised source of the value expression
}

// discoverDefaults parses every non-test .go file under internal/ and cmd/track/ and returns
// the two shapes this census covers:
//
//	NAMED    — a const/var whose name matches defaultNameRE or is in alsoNamed
//	FALLBACK — `if X == "" | X == 0 | X <= 0 { X = <literal> }`, the inline shape
//
// It PARSES rather than greps, for the reason W3.46 already established in this repo: a
// regex over source answers a question about text, and the question here is about code.
func discoverDefaults(t *testing.T, root string) []foundDefault {
	t.Helper()
	fset := token.NewFileSet()
	constNames := declaredConstNames(t, root, fset)
	var out []foundDefault

	render := func(e ast.Expr) string {
		var b bytes.Buffer
		if err := printer.Fprint(&b, fset, e); err != nil {
			t.Fatalf("render expr: %v", err)
		}
		return strings.Join(strings.Fields(b.String()), " ")
	}

	for _, dir := range []string{"internal", filepath.Join("cmd", "track")} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				return rerr
			}
			file, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				return perr
			}
			seq := map[string]int{}
			ast.Inspect(file, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.ValueSpec:
					for i, name := range node.Names {
						if i >= len(node.Values) {
							continue
						}
						if !defaultNameRE.MatchString(name.Name) {
							if _, ok := alsoNamed[name.Name]; !ok {
								continue
							}
						}
						// Composite literals (slices/maps of seed data) are not scalars and
						// pinning their whole source here would be a copy of the data, not a
						// guard on a value. Out of population, by construction.
						if _, isComposite := node.Values[i].(*ast.CompositeLit); isComposite {
							continue
						}
						if _, isFunc := node.Values[i].(*ast.FuncLit); isFunc {
							continue
						}
						out = append(out, foundDefault{rel + "::" + name.Name, render(node.Values[i])})
					}
				case *ast.IfStmt:
					bin, ok := node.Cond.(*ast.BinaryExpr)
					if !ok {
						return true
					}
					// The three ways this tree spells "the caller gave me nothing".
					isAbsent := (bin.Op == token.EQL && render(bin.Y) == `""`) ||
						(bin.Op == token.EQL && render(bin.Y) == "0") ||
						(bin.Op == token.LEQ && render(bin.Y) == "0")
					if !isAbsent || node.Body == nil {
						return true
					}
					lhs := render(bin.X)
					for _, stmt := range node.Body.List {
						as, ok := stmt.(*ast.AssignStmt)
						if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 || render(as.Lhs[0]) != lhs {
							continue
						}
						// A fallback is a DEFAULT only when its right-hand side is a CONSTANT
						// — a literal, an arithmetic expression over literals, or a name that
						// resolves to a declared const. `pendingSeen = m.Version` and
						// `into.Description = t.Body` have exactly the same syntax as
						// `role = authz.RoleMember`, and only the last one is a default: the
						// others copy a value out of live data. Distinguishing them by SHAPE is
						// impossible, so it is done by asking whether the name is declared as a
						// const anywhere in this module (constNames, gathered in a first pass).
						//
						// This admission is what brings the AUTHZ defaults into the census —
						// guest.GuestRoleViewer and authz.RoleMember are the role a caller who
						// names none is given, and they are named constants, not literals. A
						// literals-only rule would have been simpler and would have dropped the
						// two highest-stakes rows in the table.
						if !isConstValued(as.Rhs[0], render, constNames) {
							continue
						}
						seq[lhs]++
						out = append(out, foundDefault{
							rel + "::" + lhs + "#" + strconv.Itoa(seq[lhs]), render(as.Rhs[0])})
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}

// floorDiscovered stands between "every shipped default is recorded" and "my walker matched
// nothing and reported a clean product". Set below the count at the commit that introduced
// this file, far enough above zero that a walker reading no files reds instead of passing.
const floorDiscovered = 45

func TestEveryShippedDefaultIsRecorded(t *testing.T) {
	root := repoRoot(t)
	found := discoverDefaults(t, root)

	if len(found) < floorDiscovered {
		t.Fatalf("the walker found %d defaults, floor is %d. A census that suddenly sees "+
			"fewer defaults has usually stopped seeing FILES — check the walk before "+
			"lowering this number", len(found), floorDiscovered)
	}

	seen := map[string]bool{}
	var unrecorded []string
	for _, f := range found {
		seen[f.key] = true
		rec, ok := recordedDefaults[f.key]
		if !ok {
			unrecorded = append(unrecorded, "\t\""+f.key+"\": {\""+f.value+"\", \"WHAT IS THIS AND WHAT DOES CHANGING IT COST?\"},")
			continue
		}
		if rec.value != f.value {
			t.Errorf("%s:\n  ships as   %s\n  recorded as %s  (%s)\n"+
				"If the change is deliberate, edit the recorded value in this file in the SAME "+
				"commit and say why — that is the entire point of this table.",
				f.key, f.value, rec.value, rec.note)
		}
	}
	if len(unrecorded) > 0 {
		t.Errorf("%d shipped default(s) are in the tree and NOT in this table. A default nobody "+
			"recorded is one nobody can notice changing — which is the state 48 of this repo's "+
			"61 defaults were in when this file was written. Paste and fill in:\n%s",
			len(unrecorded), strings.Join(unrecorded, "\n"))
	}

	// The other direction, and it is not symmetric decoration. A recorded entry that no
	// longer resolves means the declaration was renamed, moved or deleted — and a census
	// that quietly drops the row keeps passing while covering less. Loud, or it narrows.
	var vanished []string
	for key := range recordedDefaults {
		if !seen[key] {
			vanished = append(vanished, key)
		}
	}
	if len(vanished) > 0 {
		sort.Strings(vanished)
		t.Errorf("%d recorded default(s) no longer exist in the tree: %s\n"+
			"Delete the row in the same commit that removed the default, and say so. A row "+
			"that resolves to nothing is a guard covering less than it appears to.",
			len(vanished), strings.Join(vanished, ", "))
	}
}

// declaredConstNames is the FIRST pass, and it exists so the second pass can tell a default
// from a copy. It returns every name declared in a `const` block anywhere under internal/ or
// cmd/track. Package qualification is deliberately ignored (`authz.RoleMember` matches on
// `RoleMember`): the alternative is resolving imports, and the cost of the loose match is
// admitting a row that names a constant from elsewhere — a row too many, which this file's
// completeness check surfaces loudly, rather than a row too few, which nothing would.
func declaredConstNames(t *testing.T, root string, fset *token.FileSet) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	for _, dir := range []string{"internal", filepath.Join("cmd", "track")} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				return perr
			}
			for _, decl := range file.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok || gd.Tok != token.CONST {
					continue
				}
				for _, spec := range gd.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok {
						for _, n := range vs.Names {
							names[n.Name] = true
						}
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("const pass %s: %v", dir, err)
		}
	}
	if len(names) < 50 {
		t.Fatalf("the const pass found %d names; a repo this size has hundreds. A first pass "+
			"that reads nothing would make EVERY constant-valued fallback invisible to the "+
			"second — silently, and in the direction of looking clean", len(names))
	}
	return names
}

// isConstValued answers "is this right-hand side a constant?" for the three shapes a Go
// fallback uses: a literal, arithmetic over literals, and a (possibly qualified) name that
// declaredConstNames saw in a const block.
func isConstValued(e ast.Expr, render func(ast.Expr) string, constNames map[string]bool) bool {
	switch v := e.(type) {
	case *ast.BasicLit:
		return true
	case *ast.BinaryExpr:
		return isConstValued(v.X, render, constNames) && isConstValued(v.Y, render, constNames)
	case *ast.Ident:
		return constNames[v.Name]
	case *ast.SelectorExpr:
		return constNames[v.Sel.Name]
	}
	return false
}
