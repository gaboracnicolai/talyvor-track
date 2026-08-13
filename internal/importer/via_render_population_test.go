package importer

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// via_render_population_test.go — A VIA WITH NO CASE OF ITS OWN IS INVISIBLE, AND THE ONLY LIST THAT
// CAN SEE THE NEXT ONE IS THE ONE NOBODY TYPES.
//
// This is the CLASS guard behind TestFieldNoteRender_EveryCreatedViaHasItsUpdatedTwin, widened from
// a hand-written pair list to the whole population. The failure it guards has already shipped twice:
//
//   - #141: viaNoUpdatedField and viaNullUpdatedAt were declared, produced, documented as needing
//     "a different sentence ... rather than a shared one" — and had no branch in FieldNote.render.
//     Both fell through to `default:` and rendered the SAME line, naming a value nothing recorded.
//   - The switch still compiled, the note still rendered, and every test stayed green. A missing
//     branch is not a wrong answer; it is a plausible one, which is why no behavioural test found it.
//
// The sibling guard holds the FIVE Created/Updated pairs — ten of the thirty-four vias in this
// package. The other twenty-four (viaShortRow, viaWideRow, viaDuplicateInSameImport, viaCategory,
// viaStateType, viaADFNodeDropped, the four not-read/not-created vias, and the rest) were covered by
// nothing at all: a hand-written list can only ever hold the vias its author remembered, which is
// the same shape as the defect.
//
// ⚠ THE POPULATION IS READ OUT OF THE SOURCE TREE, NOT DECLARED HERE. Adding
// `const viaWhatever = "…"` anywhere in this package is what makes this guard fire; nobody has to
// remember to append to a slice. This is the shape cmd/track/compose_env_reach_test.go uses for
// environment variables, and it is used here for the same reason: the curated list IS the bug.
//
// ⚠ THE ENUMERATION WAS THE STATED REASON THIS GUARD DID NOT EXIST, AND THE REASON DOES NOT HOLD.
// W3.4 records it as "the constants are declared across nine files with no marker, so the
// enumeration needs a convention decided first". The marker is the `via` prefix — every one of the
// thirty-four has it — and the two declaration shapes in this package (a grouped `const (…)` block
// in csv.go, a lone `const viaX = "…"` in adf_attrs.go and its neighbours) are both ordinary
// *ast.GenDecl/*ast.ValueSpec, so go/parser reads them without a convention being invented. The two
// anchors below exist to prove that BOTH shapes are actually reached rather than assumed.
//
// ⚠ WHAT THIS DELIBERATELY DOES NOT GUARD: whether every via is PRODUCED by production code. That
// census was run by hand at ded75dd and came back clean, and its first pass was WRONG — it reported
// ten vias as "handled but never produced" because they are produced through helper constructors
// (csv_done_without_completion.go's pair builder, statusFallback{via: …}) rather than a literal
// `Via:` field, and a name-matching pass cannot see that. A guard that cannot be made accurate is
// worse than no guard, so that half is left out and said out loud rather than shipped weak.

// viaPopulationFloor is a FLOOR, not a pin. Thirty-four vias are declared today; a new one should be
// swept by this guard, not rejected by it, so the count is deliberately not asserted exactly. But an
// enumeration that came back with a handful would make every assertion below pass for the worst
// possible reason — a reader that went blind to most of the package looks exactly like a package
// with almost no vias. Thirty is "the reader still sees essentially all of them".
const viaPopulationFloor = 30

// declaredVia is one `via*` constant as the SOURCE declares it: the identifier, its string value,
// and the file it lives in so a failure names somewhere to go.
type declaredVia struct {
	Name  string
	Value string
	File  string
}

// declaredVias parses every non-test .go file of this package and returns every constant whose name
// matches the `via[A-Z]…` convention.
//
// ⚠ A CONSTANT IT CANNOT READ IS A FAILURE, NOT A SKIP. A `via*` declared with no value of its own
// (an iota run, an implicit repetition in a const block) would otherwise be dropped silently, and a
// via this function cannot see is exactly the via the guard exists to catch.
func declaredVias(t *testing.T) []declaredVia {
	t.Helper()

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
	if len(names) == 0 {
		t.Fatal("found ZERO non-test .go files in this package — the enumeration below would be empty " +
			"and every assertion in this file would pass without reading anything")
	}

	fset := token.NewFileSet()
	var out []declaredVia
	for _, name := range names {
		f, perr := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if perr != nil {
			t.Fatalf("parsing %s: %v", name, perr)
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, ident := range vs.Names {
					if !looksLikeVia(ident.Name) {
						continue
					}
					if i >= len(vs.Values) {
						t.Errorf("%s declares %s with no value of its own (an iota run or an implicit "+
							"repetition). This reader cannot resolve it, and a via it cannot see is "+
							"precisely the via this guard exists to catch — give it an explicit string "+
							"literal or teach this function to resolve the shape.", name, ident.Name)
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						t.Errorf("%s declares %s with a non-literal value (%T). Same reason as above: "+
							"an unresolvable via is an unguarded via.", name, ident.Name, vs.Values[i])
						continue
					}
					val, uerr := strconv.Unquote(lit.Value)
					if uerr != nil {
						t.Errorf("%s declares %s with an unquotable literal %s: %v", name, ident.Name, lit.Value, uerr)
						continue
					}
					out = append(out, declaredVia{Name: ident.Name, Value: val, File: name})
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// looksLikeVia is the mechanical class test: `via` followed by an upper-case letter. It needs no
// judgement to apply and no memory to maintain, which is the whole point of reading the tree.
func looksLikeVia(name string) bool {
	if !strings.HasPrefix(name, "via") || len(name) <= len("via") {
		return false
	}
	return unicode.IsUpper(rune(name[len("via")]))
}

// TestFieldNoteRender_EveryDeclaredViaHasItsOwnSentence drives FieldNote.render with every via the
// source declares and requires each to produce something other than the default arm.
//
// ⚠ IT DRIVES THE FUNCTION; IT DOES NOT GREP THE SWITCH. Searching csv.go for `case n.Via == X`
// would be satisfied by an arm that exists and is unreachable — an arm shadowed by an earlier case,
// or one testing a constant that has drifted from the value the producer sets. Only calling render
// with the string a producer actually puts on the note can tell those apart.
//
// ⚠ THE REFERENCE SENTENCE IS COMPUTED, NOT TYPED. Rendering a via this switch has never heard of
// yields the default arm's own text, so a rewording of `default:` does not turn this guard red and
// does not turn it blind either.
func TestFieldNoteRender_EveryDeclaredViaHasItsOwnSentence(t *testing.T) {
	vias := declaredVias(t)

	// ---- POPULATION FLOOR. Every assertion below is a loop over this slice. ----
	if len(vias) < viaPopulationFloor {
		t.Fatalf("enumerated only %d via* constants (floor %d) — this guard asserts one thing per via, "+
			"so a short read makes it quietly assert almost nothing. Found: %v",
			len(vias), viaPopulationFloor, viaNames(vias))
	}

	// ---- ANCHORS: BOTH DECLARATION SHAPES ARE REALLY REACHED. ----
	// A reader that only ever saw csv.go would still clear the floor (that file alone holds 25) and
	// would be blind to every via declared beside the code that produces it. These two names are one
	// of each shape, and the assertion is on the VALUE the parser read, not merely on the name being
	// present: a reader that returned identifiers instead of their strings would pass a name check
	// and then compare the wrong thing forever.
	byName := map[string]declaredVia{}
	for _, v := range vias {
		byName[v.Name] = v
	}
	for name, compiled := range map[string]string{
		"viaCategory":       viaCategory,       // grouped `const (…)` block, csv.go
		"viaADFNodeDropped": viaADFNodeDropped, // lone `const viaX = "…"`, adf_attrs.go
	} {
		got, ok := byName[name]
		if !ok {
			t.Fatalf("the source reader did not find %s. It is declared in this package, so the reader "+
				"is blind to the file or to the declaration shape it uses — and a blind reader makes "+
				"every assertion below vacuous. Found: %v", name, viaNames(vias))
		}
		if got.Value != compiled {
			t.Fatalf("the source reader read %s as %q but the compiler says %q — the reader is not "+
				"resolving values, so every render below is being driven with the wrong string",
				name, got.Value, compiled)
		}
	}

	// ---- NO TWO VIAS MAY SHARE A STRING. ----
	// Two constants with the same value are ONE via: the first arm matching it answers for both, and
	// the second constant's sentence is unreachable while every census that counts names says it is
	// handled. That is the #141 failure wearing a disguise the name-based census cannot see.
	seen := map[string]declaredVia{}
	for _, v := range vias {
		if prev, dup := seen[v.Value]; dup {
			t.Errorf("%s (%s) and %s (%s) are both %q — they are the same via, so one arm answers for "+
				"both and the other's sentence can never render", v.Name, v.File, prev.Name, prev.File, v.Value)
			continue
		}
		seen[v.Value] = v
	}

	// ---- THE ASSERTION. ----
	// The fabricated via must not collide with a real one, or the reference sentence would be a real
	// arm's output and every comparison below would be against the wrong thing.
	const noSuchVia = "zz-no-such-via-3e6a"
	if v, clash := seen[noSuchVia]; clash {
		t.Fatalf("the fabricated reference via %q is actually declared as %s — pick another; the "+
			"default sentence is not what this is measuring", noSuchVia, v.Name)
	}
	defaultShape := FieldNote{Field: fieldUpdated, Via: noSuchVia}.render(1)

	for _, v := range vias {
		got := FieldNote{Field: fieldUpdated, Via: v.Value}.render(1)
		if got == defaultShape {
			t.Errorf("%s (%s, %q) has NO case of its own in FieldNote.render — it falls through to "+
				"`default:`, which renders %q. A via exists to be told apart from the others; one that "+
				"renders the shared default sentence reports a value nothing recorded and names no "+
				"consequence, which is the defect #141 fixed for two of these and this guard exists "+
				"to stop returning for the rest.", v.Name, v.File, v.Value, got)
		}
	}
}

func viaNames(vias []declaredVia) []string {
	out := make([]string, 0, len(vias))
	for _, v := range vias {
		out = append(out, v.Name)
	}
	return out
}
