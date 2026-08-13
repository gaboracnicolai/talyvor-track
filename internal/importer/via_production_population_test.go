package importer

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// via_production_population_test.go — A VIA NOTHING CONSTRUCTS IS A SENTENCE NO IMPORT CAN EVER EMIT.
//
// This is the OTHER half of via_render_population_test.go, and that file names the gap out loud:
//
//	"⚠ WHAT THIS DELIBERATELY DOES NOT GUARD: whether every via is PRODUCED by production code. That
//	 census was run by hand at ded75dd and came back clean, and its first pass was WRONG — it
//	 reported ten vias as 'handled but never produced' because they are produced through helper
//	 constructors (csv_done_without_completion.go's pair builder, statusFallback{via: …}) rather than
//	 a literal `Via:` field, and a name-matching pass cannot see that. A guard that cannot be made
//	 accurate is worse than no guard, so that half is left out and said out loud rather than shipped
//	 weak."
//
// The half was right to leave out in THAT form. What it needed was not more pattern-matching but a
// question a text pass can answer soundly, and there is exactly one:
//
//	A via referenced NOWHERE in production code except (a) its own declaration and (b) the render
//	switch, AND whose string VALUE appears in no other production literal, CANNOT BE CONSTRUCTED BY
//	ANY PRODUCTION PATH.
//
// That direction has no false positives from helper constructors — `statusFallback{via: viaCategory}`
// IS a reference to viaCategory outside declaration and render, so it is producible and this guard
// says so. All ten of the vias that broke the hand census are classified producible here, and
// TestViaProduction_TheHelperConstructorShapesAreSeen asserts that rather than trusting it.
//
// ⚠ IT CLAIMS ONE DIRECTION AND NOT THE OTHER, and the asymmetry is the point. A via this file calls
// producible may still be unreachable for a reason only data-flow analysis could see (a producer
// behind a condition no input satisfies). That question is NOT answered here and is not pretended
// to be. What IS answered: a via with no constructor anywhere is caught mechanically, and the
// package's own precedent for why that matters is in csv_clobbered_columns.go — `title` is
// deliberately absent from the clobbered set because "listing it would produce a warning no import
// can ever emit".
//
// ⚠⚠ EXCLUDING THE RENDER SWITCH IS WHAT MAKES THIS A GUARD RATHER THAN A TAUTOLOGY. Every via has
// a `case n.Via == viaX` arm — via_render_population_test.go is the guard that requires one — so a
// census that counted those arms as references would report every via producible for ever and could
// not fail. Control D in the header of TestViaProduction_EveryDeclaredViaHasAProducer records that
// run: with the exclusion removed, a deliberately dead via is reported PRODUCIBLE.
//
// ⚠ THE POPULATION IS READ OUT OF THE SOURCE TREE by declaredVias, the reader the sibling guard
// already owns, for the reason that file gives: a curated list has the same shape as the bug. It is
// reused rather than re-implemented — and it is worth saying why in a file about census population:
// the throwaway probe that found this gap wrote its OWN reader, matched `^\s*via[A-Z]` and missed
// every lone `const viaX = "…"` declaration, reporting 25 of the 34. A second reader is a second
// population boundary to get wrong.

// viaProducer is one production reference that could construct a via: the file and line, and how it
// was seen — an identifier or a bare string literal carrying the via's value.
type viaProducer struct {
	File string
	Line int
	Kind string // "ident" | "literal"
}

// productionViaProducers returns, for every declared via, the production sites that could construct
// it: every reference to the constant, and every string literal equal to its value, EXCLUDING the
// declaration itself and the body of FieldNote.render.
//
// ⚠ FAILING TO FIND render IS A FATAL, NOT AN EMPTY EXCLUSION. If the method were renamed and this
// walk quietly excluded nothing, every via would be "produced" by its own case arm and this whole
// file would pass while reading nothing — the exact vacuity it exists to prevent.
func productionViaProducers(t *testing.T, vias []declaredVia) map[string][]viaProducer {
	t.Helper()

	byName := make(map[string]declaredVia, len(vias))
	byValue := make(map[string]string, len(vias))
	for _, v := range vias {
		byName[v.Name] = v
		byValue[v.Value] = v.Name
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)

	out := map[string][]viaProducer{}
	renderBodies := 0
	for _, name := range names {
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, name, nil, 0)
		if perr != nil {
			t.Fatalf("parsing %s: %v", name, perr)
		}

		// The spans this walk must NOT count: FieldNote.render's body, and each via's own
		// declaration spec.
		type span struct{ from, to token.Pos }
		var excluded []span
		ast.Inspect(f, func(n ast.Node) bool {
			switch d := n.(type) {
			case *ast.FuncDecl:
				if d.Name.Name == "render" && d.Recv != nil && len(d.Recv.List) == 1 && d.Body != nil {
					if id, ok := d.Recv.List[0].Type.(*ast.Ident); ok && id.Name == "FieldNote" {
						excluded = append(excluded, span{d.Body.Pos(), d.Body.End()})
						renderBodies++
					}
				}
			case *ast.ValueSpec:
				for _, ident := range d.Names {
					if _, isVia := byName[ident.Name]; isVia {
						excluded = append(excluded, span{d.Pos(), d.End()})
					}
				}
			}
			return true
		})
		inExcluded := func(p token.Pos) bool {
			for _, s := range excluded {
				if p >= s.from && p < s.to {
					return true
				}
			}
			return false
		}

		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.Ident:
				if _, isVia := byName[x.Name]; isVia && !inExcluded(x.Pos()) {
					out[x.Name] = append(out[x.Name], viaProducer{name, fset.Position(x.Pos()).Line, "ident"})
				}
			case *ast.BasicLit:
				if x.Kind != token.STRING || inExcluded(x.Pos()) {
					return true
				}
				val, uerr := strconv.Unquote(x.Value)
				if uerr != nil {
					return true
				}
				if via, isVia := byValue[val]; isVia {
					out[via] = append(out[via], viaProducer{name, fset.Position(x.Pos()).Line, "literal"})
				}
			}
			return true
		})
	}
	if renderBodies != 1 {
		t.Fatalf("found %d FieldNote.render bodies to exclude, want exactly 1 — with none excluded "+
			"every via is 'produced' by its own case arm and every assertion in this file passes "+
			"without reading anything; with more than one, this walk does not know which is the "+
			"renderer", renderBodies)
	}
	return out
}

// TestViaProduction_EveryDeclaredViaHasAProducer is the guard.
//
// ⚠ CONTROLS RUN AND OBSERVED, not described:
//
//	A (the defect it exists to catch) — declare `viaProbeDead = "probe-dead"` in a production file
//	  and give it its own render arm, so the SIBLING guard stays green. This test goes RED naming
//	  viaProbeDead and its file. Observed.
//	B (blindness) — rename FieldNote.render. This test FATALs on the exclusion count rather than
//	  passing. Observed.
//	C (the false positive that killed the hand census) — the ten helper-constructor vias must NOT be
//	  reported. Asserted permanently in TestViaProduction_TheHelperConstructorShapesAreSeen.
//	D (tautology) — with the render-body exclusion removed, control A's dead via is reported
//	  PRODUCIBLE and this test passes. That is the run that proves the exclusion is load-bearing.
func TestViaProduction_EveryDeclaredViaHasAProducer(t *testing.T) {
	vias := declaredVias(t)
	if len(vias) < viaPopulationFloor {
		t.Fatalf("enumerated only %d via* constants (floor %d) — a short read makes this guard assert "+
			"almost nothing. Found: %v", len(vias), viaPopulationFloor, viaNames(vias))
	}
	producers := productionViaProducers(t, vias)

	for _, v := range vias {
		if len(producers[v.Name]) == 0 {
			t.Errorf("%s (declared in %s, value %s) is DECLARED and RENDERED and NOTHING IN PRODUCTION CONSTRUCTS IT — "+
				"no reference outside its own declaration and FieldNote.render, and no string literal "+
				"carrying %q. It is a sentence no import can emit: either a producer was lost, or the "+
				"via is dead and belongs deleted along with its render arm.",
				v.Name, v.File, strconv.Quote(v.Value), v.Value)
		}
	}
}

// TestViaProduction_TheHelperConstructorShapesAreSeen is control C, kept.
//
// The hand census at ded75dd reported ten vias as "handled but never produced" because it looked for
// a literal `Via:` field and those ten are built elsewhere. This asserts that the shapes which broke
// it are seen here — a reader that regressed to `Via:`-matching would fail THIS test rather than
// silently start reporting live vias as dead.
func TestViaProduction_TheHelperConstructorShapesAreSeen(t *testing.T) {
	vias := declaredVias(t)
	producers := productionViaProducers(t, vias)

	// One via per construction shape the package actually uses. Each is built WITHOUT a literal
	// `Via:` field, which is exactly what a name-matching pass cannot see.
	for _, c := range []struct{ via, shape string }{
		{"viaCategory", "statusFallback{via: …} — jira.go"},
		{"viaNoStateType", "statusFallback{via: …} — linear.go"},
		{"viaNoResolvedColumn", "the done-without-completion pair builder — csv_done_without_completion.go"},
		{"viaNoLinearCompletedValue", "the done-without-completion pair builder — csv_done_without_completion.go"},
		{"viaColumnNotRead", "an unreadRef table entry — csv_unread_refs.go"},
		{"viaColumnNotReadStamped", "an unreadRef table entry — csv_unread_refs.go"},
	} {
		if len(producers[c.via]) == 0 {
			t.Errorf("%s is constructed in production via %s and this reader did not see it — that is "+
				"the ded75dd false positive returning, and it turns the sibling assertion into a "+
				"generator of false reports about live code", c.via, c.shape)
		}
	}

	// And the anchor in the other direction: a via built the ORDINARY way must also be seen, so a
	// reader that somehow saw only the exotic shapes is caught too.
	if len(producers["viaShortRow"]) == 0 {
		t.Error("viaShortRow is constructed by a literal Via: field in source.go and this reader did " +
			"not see it — the walk is not reading ordinary composite literals")
	}
}
