package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// spa_field_contract_test.go — every FIELD the shipped SPA declares on a response type, against
// every json key this server can actually put on the wire.
//
// spa_api_surface_census_test.go asks the same question one level up and pins the answer: every
// SPA request reaches a registered route with a matching method, in both directions. It says
// nothing about what is INSIDE the response, and a path that resolves perfectly can still return
// an object missing half the keys its TypeScript type promises. That gap is what this file closes.
//
// THE RESULT, MEASURED AT fc4a6b0 (W3.67): 333 declared properties over the 40 response types
// reachable from the 31 generic roots of BOTH client wrappers, against the 350 distinct json keys
// the 302 producible types and the map literals in internal/+cmd/ can emit. **Every one maps to a
// key some non-test Go file can send OUTBOUND, and no non-optional property maps to a key whose
// every outbound site carries omitempty.** One exemption, Issue.template_id, is inbound-only and
// documented as such at its declaration. NO DEFECT FOUND — this file is what keeps that true.
//
// ⚠ WHY A GUARD ON A CLEAN RESULT. TypeScript checks the SPA against its OWN declaration, never
// against the server. Add a field to an interface and forget the handler, or rename a json tag and
// forget the interface, and the compiler is silent, the tests pass, and the field arrives at a
// person as `undefined` — rendered as a blank cell or a NaN, not as an error. The estate has
// shipped the neighbouring class four times: talyvor-lens `token_events.cached` twice (a structural
// 0 reported as a measured cache hit rate; `estimated_savings_usd = $0.00` for a year) and
// talyvor-track `members.avatar_url` and `guests.last_seen_at` (W3.66, `fc4a6b0`). Those are values
// that are WRONG. A key the server never sends is a value that is ABSENT, and the SPA's own type
// says it cannot be.
//
// ⚠⚠ HOW THE FIRST VERSION OF THIS MEASUREMENT LIED, BECAUSE IT IS THE REUSABLE PART AND BOTH
// FAILURES WERE IN THE FLATTERING DIRECTION.
//
//  1. A NAME-KEYED CENSUS — "does this TS property name appear as a json tag anywhere in Go?" —
//     RETURNED 0 CANDIDATES OUT OF 331 AND WAS WORTHLESS. It cannot even see the one instance this
//     repository has already proved: `GuestRecord.last_seen_at`, pinned by
//     internal/guest/last_seen_at_writerless_realpg_test.go as a key the API has NEVER emitted.
//     The tag exists in Go (`Guest.LastSeenAt`, store.go); it is `omitempty` on a `*time.Time`
//     nothing writes. Comparing a spelling to a spelling scores every field as honoured. The
//     omitempty half below is what makes the census able to fire at all.
//
//  2. A `json:"x"` ON A REQUEST BODY WAS SCORED AS PROOF THE SERVER SENDS x. Issue.template_id is
//     emitted by nothing; the tag exists only on `createBody`, whose sole use is
//     `httpx.DecodeJSON(w, r, &body)`. The census called it covered, which meant the exemption map
//     below excused a field nothing was accusing — decoration. Found by control M8 (empty the map
//     and template_id must resurface: it did not), never by reading. Fixed by classifying each
//     emit site inbound/outbound; see goEmitSite.Inbound and outboundTypes.
//
//  3. THE FIRST INBOUND CLASSIFIER WAS BINARY AND A TYPE CAN BE BOTH — it flagged four fields that
//     are plainly sent (CustomField.options, CustomField.position, FeatureBoard.public,
//     FeatureBoard.allow_anonymous) because those structs are the RESPONSE shape and the create
//     body at once. Inbound-only means decoded into AND produced by nothing. 50 types qualify;
//     302 are producible. Control M10 is what keeps that correction load-bearing.
//
//  4. THE POPULATION WAS A FLOOR, NOT A CENSUS, AND NOTHING SAID SO. Roots taken from
//     `apiRequest<T>` alone miss `publicRequest<T>` — a SECOND fetch wrapper in api/featureboard.ts
//     that deliberately omits the Authorization header — and with it `PublicBoardResponse` and the
//     whole public feature-board surface. TestTheSPAHasExactlyTheKnownHTTPClients exists because of
//     that miss: a third wrapper means a third unmeasured surface, and the only cheap moment to
//     notice is when it is added.
//
// ⚠ WHAT THIS FILE DOES NOT CLAIM. It is a REACHABILITY check over the union of emit sites, not an
// endpoint-by-endpoint proof: it establishes that some handler CAN produce each key, not that the
// specific handler behind a specific path does. Pairing a TS type to one Go struct by key overlap
// was tried and is not sound — `RoadmapProject`'s best overlap is `model.Project`, whose six
// "missing" fields all live on `project/roadmap.go`'s own struct. The union is the claim that can
// be made honestly, and it is strictly stronger than what existed before, which was nothing.

// ── the two HTTP clients, and why the list is pinned ────────────────────────────────────────────
//
// Every request the SPA makes goes through one of these. Each is a `fetch(` call site; the census
// below derives its roots from the type argument of each wrapper's generic. A new wrapper adds a
// response surface this file would silently not measure, so the set is pinned rather than derived.
var spaHTTPClients = map[string]string{
	"api/client.ts":       "apiRequest<T> — the authenticated client; attaches Authorization + X-Member-Id from localStorage",
	"api/featureboard.ts": "publicRequest<T> — the PUBLIC feature board; deliberately sends NO Authorization header so an admin's key never leaks onto the public surface",
}

// spaResponseRootRe matches the type argument of either client's generic at a call site.
var spaResponseRootRe = regexp.MustCompile(`(?:apiRequest|publicRequest)<([^>(]*)>`)

// tsInterfaceRe matches an exported interface declaration and its optional `extends` clause.
var tsInterfaceRe = regexp.MustCompile(`^export\s+interface\s+(\w+)(?:\s+extends\s+([\w,\s]+?))?\s*\{\s*$`)

// tsPropRe matches a top-level property of an interface: two spaces of indent, a name, an
// optional `?`, and a type up to the semicolon. Nested object literals sit deeper and are skipped
// by the brace-depth counter in parseTSInterfaces.
var tsPropRe = regexp.MustCompile(`^\s{2}(\w+)(\??)\s*:\s*([^;]+);\s*$`)

// tsCapRe pulls capitalised identifiers out of a TS type expression so the population can be
// closed over property types (`milestones: RoadmapMilestone[]` pulls RoadmapMilestone in).
var tsCapRe = regexp.MustCompile(`\b([A-Z]\w+)\b`)

type tsProp struct {
	Name     string
	Optional bool
	Type     string
	Owner    string // the interface that DECLARES it — BlockingIssue extends Issue, and
	File     string // Issue.template_id must not need a second exemption row per subtype
	Line     int
}

type tsIface struct {
	Name    string
	Extends []string
	Props   []tsProp
}

// ── inbound-only declarations: a field on a response type that the server READS and never SENDS ──
//
// ⚠ EACH ENTRY NEEDS A REASON SOMEBODY HAD TO TYPE, and the reason must be checkable at the
// declaration. An entry with no reason excuses nothing; TestEveryInboundOnlyExemptionIsExplained
// enforces that the map is not used as a place to put inconvenient fields.
var inboundOnlyTSFields = map[string]string{
	"Issue.template_id": "accepted only on Create — internal/issue/handler.go's createBody carries the only " +
		"`template_id` tag in the tree and it is a REQUEST struct. types.ts says so at the declaration " +
		"(\"Never returned on reads\"), and the field is optional there, so the SPA cannot rely on it coming back.",
}

// parseTSInterfaces reads every exported interface in frontend/src/api and returns them by name.
func parseTSInterfaces(t *testing.T) map[string]tsIface {
	t.Helper()
	dir := filepath.Join(repoRoot(t), "frontend", "src", "api")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("frontend/src/api not readable at %s: %v", dir, err)
	}
	out := map[string]tsIface{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ts") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		lines := strings.Split(string(raw), "\n")
		for i := 0; i < len(lines); i++ {
			m := tsInterfaceRe.FindStringSubmatch(lines[i])
			if m == nil {
				continue
			}
			iface := tsIface{Name: m[1]}
			for _, p := range strings.Split(m[2], ",") {
				if p = strings.TrimSpace(p); p != "" {
					iface.Extends = append(iface.Extends, p)
				}
			}
			depth, j := 1, i+1
			for ; j < len(lines) && depth > 0; j++ {
				depth += strings.Count(lines[j], "{") - strings.Count(lines[j], "}")
				if depth < 1 {
					continue
				}
				// ⚠ Only depth-1 properties are scored. A property whose type is an inline object
				// literal has its members at depth 2, and scoring those as top-level keys would
				// invent fields the response never claims to have.
				if pm := tsPropRe.FindStringSubmatch(lines[j]); pm != nil && depth == 1 {
					iface.Props = append(iface.Props, tsProp{
						Name: pm[1], Optional: pm[2] == "?", Type: strings.TrimSpace(pm[3]),
						Owner: iface.Name, File: e.Name(), Line: j + 1,
					})
				}
			}
			out[iface.Name] = iface
			i = j - 1
		}
	}
	return out
}

// spaResponseRoots returns the named types every client generic is instantiated with.
func spaResponseRoots(t *testing.T) map[string]bool {
	t.Helper()
	dir := filepath.Join(repoRoot(t), "frontend", "src", "api")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("frontend/src/api not readable: %v", err)
	}
	roots := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ts") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		for _, m := range spaResponseRootRe.FindAllStringSubmatch(stripTSComments(string(raw)), -1) {
			arg := strings.TrimSpace(m[1])
			if arg == "T" { // the wrapper's own declaration, not a call site
				continue
			}
			for _, c := range tsCapRe.FindAllStringSubmatch(arg, -1) {
				roots[c[1]] = true
			}
		}
	}
	return roots
}

// closeOverTSTypes walks `extends` and property types until the population stops growing.
func closeOverTSTypes(types map[string]tsIface, roots map[string]bool) map[string]bool {
	seen := map[string]bool{}
	var stack []string
	for r := range roots {
		if _, ok := types[r]; ok {
			stack = append(stack, r)
		}
	}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[n] {
			continue
		}
		seen[n] = true
		for _, p := range types[n].Extends {
			if _, ok := types[p]; ok && !seen[p] {
				stack = append(stack, p)
			}
		}
		for _, pr := range types[n].Props {
			for _, c := range tsCapRe.FindAllStringSubmatch(pr.Type, -1) {
				if _, ok := types[c[1]]; ok && !seen[c[1]] {
					stack = append(stack, c[1])
				}
			}
		}
	}
	return seen
}

// tsPropsOf returns a type's own properties plus everything it inherits through `extends`.
func tsPropsOf(types map[string]tsIface, name string, guard map[string]bool) []tsProp {
	if guard[name] {
		return nil
	}
	iface, ok := types[name]
	if !ok {
		return nil
	}
	guard[name] = true
	var acc []tsProp
	for _, p := range iface.Extends {
		acc = append(acc, tsPropsOf(types, p, guard)...)
	}
	return append(acc, iface.Props...)
}

// goEmitSite is one place a json key can be produced.
//
// ⚠ Inbound is the field this instrument's first version did not have, and its absence made the
// exemption map below decoration. `Issue.template_id` is declared by the SPA and emitted by
// nothing — but the string `json:"template_id"` DOES exist in the tree, on `createBody`, a struct
// whose only use is `httpx.DecodeJSON(w, r, &body)`. Scoring a REQUEST tag as proof that a
// RESPONSE field is sent is the same spelling-for-a-write substitution W3.66 caught at the
// database and W6.47 caught at the BFF. It was found here by a positive control (M8: empty the
// exemption map and template_id must resurface — it did not), not by reading the code.
type goEmitSite struct {
	OmitEmpty bool
	Inbound   bool // the owning type is only ever a decode target, so this tag is never sent
	Where     string
}

// outboundTypes returns every named type identity this package can PRODUCE: anything that appears
// in a function's result list, plus anything handed directly to a response writer.
//
// ⚠ THIS FUNCTION EXISTS BECAUSE THE FIRST CLASSIFIER WAS BINARY AND A TYPE CAN BE BOTH. Marking
// every decode target inbound flagged FOUR fields that are plainly sent — CustomField.options,
// CustomField.position, FeatureBoard.public, FeatureBoard.allow_anonymous — because
// `customfield.CustomField` is BOTH the response struct AND the create/update request body
// (`var in CustomField` at handler.go:77). Inbound-only means decoded into AND never produced;
// `createBody` is returned by no function and passed to no writer, which is what makes it the
// only real instance in the tree.
func outboundTypes(f *ast.File, fset *token.FileSet, pkg, rel string) map[string]bool {
	out := map[string]bool{}
	var named func(e ast.Expr) string
	named = func(e ast.Expr) string {
		switch t := e.(type) {
		case *ast.Ident:
			return pkg + "." + t.Name
		case *ast.StarExpr:
			return named(t.X)
		case *ast.ArrayType:
			return named(t.Elt)
		case *ast.SelectorExpr:
			if x, ok := t.X.(*ast.Ident); ok {
				return x.Name + "." + t.Sel.Name
			}
		}
		return ""
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if ft, ok := n.(*ast.FuncType); ok && ft.Results != nil {
			for _, r := range ft.Results.List {
				if id := named(r.Type); id != "" {
					out[id] = true
				}
			}
		}
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			return true
		}
		local := map[string]string{}
		ast.Inspect(fn.Body, func(m ast.Node) bool {
			switch v := m.(type) {
			case *ast.ValueSpec:
				for _, nm := range v.Names {
					if v.Type != nil {
						if id := named(v.Type); id != "" {
							local[nm.Name] = id
						} else if st, ok := v.Type.(*ast.StructType); ok {
							local[nm.Name] = rel + ":" + itoa(fset.Position(st.Pos()).Line)
						}
					}
				}
			case *ast.AssignStmt:
				if len(v.Lhs) == 1 && len(v.Rhs) == 1 {
					if id, ok := v.Lhs[0].(*ast.Ident); ok {
						if cl, ok := v.Rhs[0].(*ast.CompositeLit); ok {
							if nm := named(cl.Type); nm != "" {
								local[id.Name] = nm
							} else if st, ok := cl.Type.(*ast.StructType); ok {
								local[id.Name] = rel + ":" + itoa(fset.Position(st.Pos()).Line)
							}
						}
					}
				}
			}
			return true
		})
		ast.Inspect(fn.Body, func(m ast.Node) bool {
			call, ok := m.(*ast.CallExpr)
			if !ok {
				return true
			}
			var arg ast.Expr
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				if fun.Name == "writeJSON" && len(call.Args) == 3 {
					arg = call.Args[2]
				}
			case *ast.SelectorExpr:
				if (fun.Sel.Name == "Encode" || fun.Sel.Name == "Marshal" ||
					fun.Sel.Name == "MarshalIndent") && len(call.Args) >= 1 {
					arg = call.Args[0]
				}
			}
			if arg == nil {
				return true
			}
			switch a := arg.(type) {
			case *ast.Ident:
				if ty, ok := local[a.Name]; ok {
					out[ty] = true
				}
			case *ast.CompositeLit:
				if nm := named(a.Type); nm != "" {
					out[nm] = true
				}
			case *ast.UnaryExpr:
				if id, ok := a.X.(*ast.Ident); ok {
					if ty, ok := local[id.Name]; ok {
						out[ty] = true
					}
				}
			}
			return true
		})
		return true
	})
	return out
}

// decodeTargets returns the set of type identities — "pkg.TypeName" for named types, or
// "file:line" for an inline `var in struct{…}` — that appear as the destination of a JSON
// decode. A tag on one of these is a field the server READS, never one it WRITES.
//
// The three idioms in this repo at fc4a6b0: httpx.DecodeJSON (42), json.Unmarshal (101) and
// json.NewDecoder(r.Body).Decode (7). All three are matched; a fourth would simply not be
// classified, which fails in the SAFE direction (a request tag counted as emittable, i.e. the
// behaviour before this function existed) and is why the count is asserted below.
func decodeTargets(f *ast.File, fset *token.FileSet, pkg string, rel string) map[string]bool {
	out := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			return true
		}
		// ident -> type identity, for the locals declared in this function
		local := map[string]string{}
		ast.Inspect(fn.Body, func(m ast.Node) bool {
			switch v := m.(type) {
			case *ast.ValueSpec:
				for _, nm := range v.Names {
					switch tv := v.Type.(type) {
					case *ast.Ident:
						local[nm.Name] = pkg + "." + tv.Name
					case *ast.SelectorExpr:
						if x, ok := tv.X.(*ast.Ident); ok {
							local[nm.Name] = x.Name + "." + tv.Sel.Name
						}
					case *ast.StructType:
						local[nm.Name] = rel + ":" + itoa(fset.Position(tv.Pos()).Line)
					}
				}
			case *ast.AssignStmt:
				if len(v.Lhs) != 1 || len(v.Rhs) != 1 {
					return true
				}
				id, ok := v.Lhs[0].(*ast.Ident)
				if !ok {
					return true
				}
				if cl, ok := v.Rhs[0].(*ast.CompositeLit); ok {
					switch tv := cl.Type.(type) {
					case *ast.Ident:
						local[id.Name] = pkg + "." + tv.Name
					case *ast.SelectorExpr:
						if x, ok := tv.X.(*ast.Ident); ok {
							local[id.Name] = x.Name + "." + tv.Sel.Name
						}
					case *ast.StructType:
						local[id.Name] = rel + ":" + itoa(fset.Position(tv.Pos()).Line)
					}
				}
			}
			return true
		})
		ast.Inspect(fn.Body, func(m ast.Node) bool {
			call, ok := m.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			var arg ast.Expr
			switch {
			case ok && sel.Sel.Name == "DecodeJSON" && len(call.Args) >= 1:
				arg = call.Args[len(call.Args)-1]
			case ok && sel.Sel.Name == "Unmarshal" && len(call.Args) == 2:
				arg = call.Args[1]
			case ok && sel.Sel.Name == "Decode" && len(call.Args) == 1:
				arg = call.Args[0]
			default:
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "DecodeJSON" && len(call.Args) >= 1 {
					arg = call.Args[len(call.Args)-1]
				}
			}
			if arg == nil {
				return true
			}
			un, ok := arg.(*ast.UnaryExpr)
			if !ok || un.Op != token.AND {
				return true
			}
			id, ok := un.X.(*ast.Ident)
			if !ok {
				return true
			}
			if ty, ok := local[id.Name]; ok {
				out[ty] = true
			}
			return true
		})
		return true
	})
	return out
}

// goEmittableKeys collects every json key any non-test Go file under internal/ and cmd/ can put on
// the wire, from THREE idioms.
//
// ⚠ ALL THREE ARE LOAD-BEARING AND THE MEASUREMENT SAYS SO: at fc4a6b0 the named structs carry 570
// tagged fields, the inline anonymous struct literals another 84, and `map[string]T{...}` literals
// 465 keys. A census that walks only *ast.TypeSpec sees barely half the surface and reports a clean
// population it never looked at — talyvor-suite W6.47 recorded exactly that (41 of 67 fields) and
// the ratio here is worse, because a handler in this repo answers from a map more often than not.
func goEmittableKeys(t *testing.T) map[string][]goEmitSite {
	t.Helper()
	root := repoRoot(t)
	keys := map[string][]goEmitSite{}
	dynamic := 0
	inbound := map[string]bool{}
	produced := map[string]bool{}

	// PASS 1 — which type identities are decode destinations. Must complete before any tag is
	// classified, because a struct declared in one file is decoded into in another.
	var files []struct {
		path, rel, pkg string
		fset           *token.FileSet
		f              *ast.File
	}
	for _, sub := range []string{"internal", "cmd"} {
		err := filepath.Walk(filepath.Join(root, sub), func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				if info.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			f, perr := parser.ParseFile(fset, p, nil, 0)
			if perr != nil {
				t.Fatalf("parse %s: %v", p, perr)
			}
			rel, _ := filepath.Rel(root, p)
			files = append(files, struct {
				path, rel, pkg string
				fset           *token.FileSet
				f              *ast.File
			}{p, rel, f.Name.Name, fset, f})
			for ty := range decodeTargets(f, fset, f.Name.Name, rel) {
				inbound[ty] = true
			}
			for ty := range outboundTypes(f, fset, f.Name.Name, rel) {
				produced[ty] = true
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", sub, err)
		}
	}
	// A type that is decoded into AND produced somewhere is DUAL-USE, and its tags are sent.
	for ty := range produced {
		delete(inbound, ty)
	}
	// ⚠ AN ARMING FLOOR ON THE CLASSIFIER. If it resolves nothing, every request struct is scored
	// as an emit site again and the exemption map goes back to being decoration — silently.
	if len(inbound) < 25 {
		t.Fatalf("resolved %d inbound-only types; 50 at fc4a6b0. With none, a `json:\"x\"` on a "+
			"REQUEST body counts as proof the server SENDS x — which is the defect M8 caught.",
			len(inbound))
	}

	collect := func(st *ast.StructType, fset *token.FileSet, where string, owner string) {
		for _, fld := range st.Fields.List {
			if fld.Tag == nil {
				continue
			}
			raw := strings.Trim(fld.Tag.Value, "`")
			i := strings.Index(raw, `json:"`)
			if i < 0 {
				continue
			}
			rest := raw[i+6:]
			j := strings.Index(rest, `"`)
			if j < 0 {
				continue
			}
			parts := strings.Split(rest[:j], ",")
			name := parts[0]
			if name == "" || name == "-" {
				continue
			}
			omit := false
			for _, p := range parts[1:] {
				if p == "omitempty" {
					omit = true
				}
			}
			keys[name] = append(keys[name], goEmitSite{
				OmitEmpty: omit, Inbound: inbound[owner], Where: where,
			})
		}
	}

	// PASS 2 — the tags themselves, each classified against the decode-target set.
	for _, fl := range files {
		rel, fset := fl.rel, fl.fset
		ast.Inspect(fl.f, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.TypeSpec:
				if st, ok := v.Type.(*ast.StructType); ok {
					collect(st, fset, rel+":"+itoa(fset.Position(st.Pos()).Line), fl.pkg+"."+v.Name.Name)
				}
			case *ast.CompositeLit:
				// map[string]T{"key": ...} — a write no struct-tag census can see
				if mt, ok := v.Type.(*ast.MapType); ok {
					if kid, ok := mt.Key.(*ast.Ident); ok && kid.Name == "string" {
						for _, el := range v.Elts {
							kv, ok := el.(*ast.KeyValueExpr)
							if !ok {
								dynamic++
								continue
							}
							bl, ok := kv.Key.(*ast.BasicLit)
							if !ok || bl.Kind != token.STRING {
								dynamic++
								continue
							}
							key := strings.Trim(bl.Value, `"`)
							// A map literal has no omitempty: the key is always written. A map is
							// also never a decode target in this tree (the 149 decodes all name a
							// struct), so these sites are outbound by construction.
							keys[key] = append(keys[key], goEmitSite{
								Where: rel + ":" + itoa(fset.Position(kv.Pos()).Line) + " (map literal)",
							})
						}
					}
				}
				// struct{...}{...} — the inline anonymous response shape
				if st, ok := v.Type.(*ast.StructType); ok {
					where := rel + ":" + itoa(fset.Position(st.Pos()).Line)
					collect(st, fset, where+" (inline struct)", where)
				}
			case *ast.ValueSpec:
				if st, ok := v.Type.(*ast.StructType); ok {
					where := rel + ":" + itoa(fset.Position(st.Pos()).Line)
					collect(st, fset, where+" (var struct)", where)
				}
			}
			return true
		})
	}
	return keys
}

// outboundSites drops the sites whose owning type is only ever decoded into.
func outboundSites(sites []goEmitSite) []goEmitSite {
	out := make([]goEmitSite, 0, len(sites))
	for _, s := range sites {
		if !s.Inbound {
			out = append(out, s)
		}
	}
	return out
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// ── (1) every declared response field maps to a key this server can emit ────────────────────────

func TestEverySPAResponseFieldHasAServerEmitSite(t *testing.T) {
	types := parseTSInterfaces(t)
	roots := spaResponseRoots(t)
	pop := closeOverTSTypes(types, roots)
	keys := goEmittableKeys(t)

	// ⚠ VACUITY FLOORS FIRST. Without them a parser that silently returns nothing makes every
	// loop below iterate zero times and this file passes green over a tree it never read —
	// "no missing fields" and "no fields" are the same output otherwise.
	if len(types) < 40 {
		t.Fatalf("parsed %d TS interfaces; 44 exist at fc4a6b0. The parser stopped seeing "+
			"declarations — fix it before reading any result below.", len(types))
	}
	if len(roots) < 25 {
		t.Fatalf("derived %d client generic roots; 31 exist at fc4a6b0 across both wrappers.", len(roots))
	}
	if len(pop) < 30 {
		t.Fatalf("closed population is %d types; 40 at fc4a6b0.", len(pop))
	}
	if len(keys) < 300 {
		t.Fatalf("collected %d distinct emittable json keys from internal/+cmd/; 350 at fc4a6b0. "+
			"A collapsed key set makes every field below look unsent.", len(keys))
	}

	var missing []string
	scored := 0
	names := make([]string, 0, len(pop))
	for n := range pop {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		for _, p := range tsPropsOf(types, n, map[string]bool{}) {
			scored++
			if len(outboundSites(keys[p.Name])) > 0 {
				continue
			}
			if _, exempt := inboundOnlyTSFields[p.Owner+"."+p.Name]; exempt {
				continue
			}
			missing = append(missing, n+"."+p.Name+" ("+p.File+":"+itoa(p.Line)+", type "+p.Type+")")
		}
	}
	if scored < 250 {
		t.Fatalf("scored only %d properties; 333 at fc4a6b0. The walk is not reaching the "+
			"interfaces it claims to.", scored)
	}
	if len(missing) > 0 {
		t.Errorf("%d field(s) the SPA declares on a decoded response type map to NO json key any "+
			"non-test Go file emits:\n  %s\n\n"+
			"Each is `undefined` at runtime while TypeScript says it is present. Either wire the "+
			"handler, delete the declaration, or — if the field is inbound-only like "+
			"Issue.template_id — add it to inboundOnlyTSFields WITH the reason.",
			len(missing), strings.Join(missing, "\n  "))
	}
}

// ── (2) a required TS field whose only Go sites are omitempty ───────────────────────────────────
//
// This is the arm that can actually fire, and the reason the name-keyed version of this census was
// worthless. `GuestRecord.last_seen_at` is the worked example: the tag EXISTS, on a `*time.Time`
// with omitempty that nothing writes, so the key never reaches the wire. It is declared optional in
// types.ts and is therefore honest. Flip that `?` off and this test reds — which is exactly the
// mutation used to prove the arm is not vacuous.

func TestNoRequiredSPAFieldIsOmitEmptyOnEveryServerSite(t *testing.T) {
	types := parseTSInterfaces(t)
	pop := closeOverTSTypes(types, spaResponseRoots(t))
	keys := goEmittableKeys(t)

	if len(pop) < 30 || len(keys) < 300 {
		t.Fatalf("population %d / key set %d is below the floor; see the sibling test.", len(pop), len(keys))
	}

	// ⚠ AN ARMING FLOOR ON THE ARM ITSELF. If nothing in the tree carries omitempty, this test
	// can never fire no matter what the SPA declares, and its green means "no omitempty" rather
	// than "no defect". 57 tags carry it at fc4a6b0.
	omitTags := 0
	for _, sites := range keys {
		for _, s := range outboundSites(sites) {
			if s.OmitEmpty {
				omitTags++
				break
			}
		}
	}
	if omitTags < 20 {
		t.Fatalf("only %d distinct keys have an omitempty site; 41 at fc4a6b0. With none, this "+
			"test cannot fail and its pass means nothing.", omitTags)
	}

	var bad []string
	names := make([]string, 0, len(pop))
	for n := range pop {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		for _, p := range tsPropsOf(types, n, map[string]bool{}) {
			if p.Optional {
				continue
			}
			sites := outboundSites(keys[p.Name])
			if len(sites) == 0 {
				continue // no outbound site at all — covered by the sibling test
			}
			allOmit := true
			for _, s := range sites {
				if !s.OmitEmpty {
					allOmit = false
					break
				}
			}
			if allOmit {
				where := make([]string, 0, len(sites))
				for _, s := range sites {
					where = append(where, s.Where)
				}
				bad = append(bad, n+"."+p.Name+" ("+p.File+":"+itoa(p.Line)+") — every Go site is "+
					"omitempty: "+strings.Join(where, ", "))
			}
		}
	}
	if len(bad) > 0 {
		t.Errorf("%d field(s) are declared NON-OPTIONAL by the SPA while every json tag that could "+
			"produce them carries omitempty, so the server may legally omit the key:\n  %s\n\n"+
			"TypeScript guarantees a value the wire does not. Mark the property optional in "+
			"types.ts, or drop omitempty on the Go side if the field really is always present.",
			len(bad), strings.Join(bad, "\n  "))
	}
}

// ── (3) the client census: a new fetch wrapper is a new unmeasured surface ──────────────────────

func TestTheSPAHasExactlyTheKnownHTTPClients(t *testing.T) {
	root := filepath.Join(repoRoot(t), "frontend", "src")
	found := map[string]bool{}
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".ts") && !strings.HasSuffix(p, ".tsx") {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if strings.Contains(stripTSComments(string(raw)), "fetch(") {
			rel, _ := filepath.Rel(root, p)
			found[filepath.ToSlash(rel)] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk frontend/src: %v", err)
	}
	if len(found) == 0 {
		t.Fatal("no file in frontend/src calls fetch( — the walk read nothing; this test would " +
			"pass green over an empty tree.")
	}

	var appeared, gone []string
	for f := range found {
		if _, ok := spaHTTPClients[f]; !ok {
			appeared = append(appeared, f)
		}
	}
	for f := range spaHTTPClients {
		if !found[f] {
			gone = append(gone, f)
		}
	}
	sort.Strings(appeared)
	sort.Strings(gone)

	if len(appeared) > 0 {
		t.Errorf("%d file(s) call fetch( and are not in spaHTTPClients:\n  %s\n\n"+
			"A new HTTP client is a new response surface, and the field census above derives its "+
			"population from the KNOWN wrappers' generics — it would silently not measure this "+
			"one. That is how publicRequest<T> and the whole public feature-board surface were "+
			"missed on the first pass. Add the file with what its wrapper does, and make sure "+
			"spaResponseRootRe names the wrapper.",
			len(appeared), strings.Join(appeared, "\n  "))
	}
	if len(gone) > 0 {
		t.Errorf("%d pinned client(s) no longer call fetch(:\n  %s\n\nIf a wrapper was renamed or "+
			"removed, update spaHTTPClients AND spaResponseRootRe together — leaving the regex "+
			"naming a wrapper that no longer exists silently shrinks the measured population.",
			len(gone), strings.Join(gone, "\n  "))
	}
}

// ── (4) the exemption map may not be used as a dumping ground ───────────────────────────────────

func TestEveryInboundOnlyExemptionIsExplained(t *testing.T) {
	types := parseTSInterfaces(t)
	for key, reason := range inboundOnlyTSFields {
		if len(strings.Fields(reason)) < 8 {
			t.Errorf("inboundOnlyTSFields[%q] has a %d-word reason. An exemption with no argument "+
				"excuses nothing — say which REQUEST struct carries the tag and where types.ts "+
				"documents it.", key, len(strings.Fields(reason)))
		}
		parts := strings.SplitN(key, ".", 2)
		if len(parts) != 2 {
			t.Errorf("inboundOnlyTSFields key %q is not Type.field — an unqualified field name is "+
				"not a claim, because `id` and `created_at` exist on nearly every interface.", key)
			continue
		}
		iface, ok := types[parts[0]]
		if !ok {
			t.Errorf("inboundOnlyTSFields names type %q, which no longer exists. A stale exemption "+
				"hides a real never-sent field.", parts[0])
			continue
		}
		// The exempted field must still be DECLARED, and must still be optional — an inbound-only
		// field the SPA marks required is a defect the exemption must not cover.
		var found *tsProp
		for i := range iface.Props {
			if iface.Props[i].Name == parts[1] {
				found = &iface.Props[i]
			}
		}
		if found == nil {
			t.Errorf("inboundOnlyTSFields exempts %s, which %s no longer declares. Delete the row.",
				key, parts[0])
			continue
		}
		if !found.Optional {
			t.Errorf("%s is exempted as inbound-only but is declared NON-OPTIONAL. The server never "+
				"sends it, so the SPA's type is a guarantee the wire cannot keep — mark it `?`.", key)
		}
	}
}
