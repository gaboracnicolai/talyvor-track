package importer

// linear_query_schema_test.go — THE SHIPPED LINEAR DOCUMENT, CHECKED AGAINST LINEAR'S OWN SCHEMA.
//
// ⚠ THE HOLE THIS CLOSES IS ONE W3.4 STATES IN ITS OWN WORDS: "adding `type` to the Linear GraphQL
// query would 400 the WHOLE query if wrong, and no CI test can catch that (the tests use a fake
// server that accepts any query). DO NOT SHIP THAT BLIND." That is exactly right about this package
// as it stood. MEASURED rather than assumed: every Linear test here drives `cannedPages`, which
// answers a fixed body and never reads the request — so `linearIssuesQuery` could name a field that
// does not exist, or misspell one that does, and the whole suite stays green while every real
// import 400s forever. `api_updated_job_test.go` is the closest thing to a check and it is a
// `strings.Contains(linearIssuesQuery, "updatedAt")` — a substring, not a validation.
//
// WHAT MAKES A CHECK POSSIBLE WITH NO TENANT AND NO CREDENTIAL. Linear VALIDATES a GraphQL document
// BEFORE it authenticates, and serves introspection unauthenticated. Both measured from this machine
// on 2026-08-13 (the run is in scripts/w34-linear-schema-snapshot.py, which REFUSES to write a
// snapshot if the discriminator below ever stops holding):
//
//	POST {"query":"query { thisFieldDoesNotExistXyz }"}  -> HTTP 400  GRAPHQL_VALIDATION_FAILED
//	POST {"query":"query { __typename }"}                -> HTTP 401  AUTHENTICATION_ERROR
//	POST the SHIPPED linearIssuesQuery, verbatim         -> HTTP 401  AUTHENTICATION_ERROR
//
// The third line is the measurement: the document Track ships today is VALID against the live
// schema. This file makes that a re-runnable guard instead of a paragraph.
//
// ⚠ IT DOES NOT USE THE NETWORK, AND THAT IS DELIBERATE. A test that POSTs to api.linear.app makes
// CI depend on a third party being up and on this repo being allowed to reach it. The snapshot in
// testdata/ is the measurement, taken once by the script and committed with its provenance; this
// test validates the shipped document against THAT. The cost is honest and stated: the snapshot can
// go stale, and `python3 scripts/w34-linear-schema-snapshot.py --check` is the one command that
// tells you it has.
//
// ⚠ THE SNAPSHOT IS NOT DERIVED FROM THE QUERY. It carries EVERY field of the eight types the
// document walks (Query 166, Team 98, Issue 86, IssueLabel 18, WorkflowState 12, PageInfo 4,
// IssueConnection 3, IssueLabelConnection 3), because a snapshot generated from the document could
// never contradict the document — it would be a mirror, and a mirror is always green.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ─── the pinned schema ────────────────────────────────────────────────────────────────────────

type linearSchemaField struct {
	Type       string            `json:"type"`
	Deprecated bool              `json:"deprecated"`
	Args       map[string]string `json:"args"`
}

type linearSchemaType struct {
	Kind   string                       `json:"kind"`
	Fields map[string]linearSchemaField `json:"fields"`
}

type linearSchema struct {
	Provenance struct {
		Endpoint      string         `json:"endpoint"`
		FetchedUTC    string         `json:"fetched_utc"`
		Authenticated bool           `json:"authenticated"`
		Controls      map[string]int `json:"controls"`
	} `json:"_provenance"`
	Types map[string]linearSchemaType `json:"types"`
}

func loadLinearSchema(t *testing.T) linearSchema {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "linear_schema_snapshot.json"))
	if err != nil {
		t.Fatalf("read the pinned Linear schema: %v\n"+
			"Regenerate it with `python3 scripts/w34-linear-schema-snapshot.py`. This file is the only "+
			"thing standing between a misspelled field in linearIssuesQuery and a 400 on every real import.", err)
	}
	var s linearSchema
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("parse the pinned Linear schema: %v", err)
	}
	// A snapshot that carries no types would pass every check below by having nothing to disagree
	// with. Assert the population BEFORE using it — the same reason every corpus census in this
	// package carries a floor.
	if len(s.Types) < 8 {
		t.Fatalf("the pinned schema carries %d types, want the 8 the document walks — a snapshot that "+
			"lost its types makes every check below vacuous", len(s.Types))
	}
	for _, name := range []string{"Query", "Team", "IssueConnection", "PageInfo", "Issue", "WorkflowState", "IssueLabelConnection", "IssueLabel"} {
		if len(s.Types[name].Fields) == 0 {
			t.Fatalf("the pinned schema carries no fields for %q — refresh it with "+
				"`python3 scripts/w34-linear-schema-snapshot.py`", name)
		}
	}
	return s
}

// ─── the smallest GraphQL reader that can answer this question ────────────────────────────────
//
// A dependency was considered and refused: adding a GraphQL parser to go.mod for one test file is a
// supply-chain decision this session may not take on its own. What is parsed here is one document
// of one shape — named operation, variable definitions, nested selection sets with literal and
// variable arguments — and the FLOOR on visited paths below is what keeps a parser that quietly
// reads nothing from passing as a guard.

type gqlField struct {
	name     string
	args     map[string]string // arg name -> the literal source text of its value ("$after", "100")
	sel      []gqlField
	path     string
	hasBrace bool
}

type gqlTokenizer struct {
	toks []string
	pos  int
}

func tokenizeGraphQL(src string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range src {
		switch {
		case strings.ContainsRune("{}():,$!\"[]", r):
			flush()
			out = append(out, string(r))
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

func (z *gqlTokenizer) peek() string {
	if z.pos >= len(z.toks) {
		return ""
	}
	return z.toks[z.pos]
}

func (z *gqlTokenizer) next() string {
	tok := z.peek()
	z.pos++
	return tok
}

// parseSelectionSet reads `{ field ... }` and returns the fields, with each field's dotted path.
func (z *gqlTokenizer) parseSelectionSet(t *testing.T, parentPath string) []gqlField {
	t.Helper()
	if tok := z.next(); tok != "{" {
		t.Fatalf("parsing linearIssuesQuery at %q: expected `{`, got %q", parentPath, tok)
	}
	var out []gqlField
	for {
		tok := z.peek()
		if tok == "" {
			t.Fatalf("parsing linearIssuesQuery at %q: unexpected end of document", parentPath)
		}
		if tok == "}" {
			z.next()
			return out
		}
		name := z.next()
		f := gqlField{name: name, args: map[string]string{}, path: parentPath + "." + name}
		if z.peek() == "(" {
			z.next()
			for z.peek() != ")" {
				argName := z.next()
				if z.next() != ":" {
					t.Fatalf("parsing linearIssuesQuery at %q: expected `:` after argument %q", f.path, argName)
				}
				var val strings.Builder
				for {
					p := z.peek()
					if p == "," || p == ")" || p == "" {
						break
					}
					val.WriteString(z.next())
				}
				f.args[argName] = val.String()
				if z.peek() == "," {
					z.next()
				}
			}
			z.next() // ")"
		}
		if z.peek() == "{" {
			f.hasBrace = true
			f.sel = z.parseSelectionSet(t, f.path)
		}
		out = append(out, f)
	}
}

// parseLinearDocument reads the shipped document: `query($v: T, ...) { ... }`.
func parseLinearDocument(t *testing.T, src string) (vars map[string]string, sel []gqlField) {
	t.Helper()
	z := &gqlTokenizer{toks: tokenizeGraphQL(src)}
	vars = map[string]string{}
	if z.peek() == "query" {
		z.next()
	}
	if z.peek() == "(" {
		z.next()
		for z.peek() != ")" {
			if z.next() != "$" {
				t.Fatalf("parsing linearIssuesQuery: variable definitions must start with `$`")
			}
			name := z.next()
			if z.next() != ":" {
				t.Fatalf("parsing linearIssuesQuery: expected `:` after $%s", name)
			}
			var typ strings.Builder
			for {
				p := z.peek()
				if p == "," || p == ")" || p == "" {
					break
				}
				typ.WriteString(z.next())
			}
			vars["$"+name] = typ.String()
			if z.peek() == "," {
				z.next()
			}
		}
		z.next() // ")"
	}
	sel = z.parseSelectionSet(t, "query")
	return vars, sel
}

// namedType strips the wrappers a snapshot type carries: `[Issue!]!` -> `Issue`.
func namedType(s string) string {
	return strings.NewReplacer("[", "", "]", "", "!", "").Replace(s)
}

// ─── the guard ────────────────────────────────────────────────────────────────────────────────

// linearQueryMinPaths is a FLOOR on how many field paths the walk must actually check, and it is
// the anti-vacuity control: a parser that reads nothing, a document that lost its body, or a walk
// that stops at the first level all produce a small number here and fail. The shipped document
// carries 20; the floor is set below that so removing a field is a decision rather than a build
// break, and losing the body is not.
const linearQueryMinPaths = 15

func TestLinearIssuesQuery_EveryFieldExistsInLinearsPinnedSchema(t *testing.T) {
	schema := loadLinearSchema(t)
	vars, sel := parseLinearDocument(t, linearIssuesQuery)

	checked := 0
	usedVars := map[string]bool{}

	var walk func(typeName string, fields []gqlField)
	walk = func(typeName string, fields []gqlField) {
		st, ok := schema.Types[typeName]
		if !ok {
			t.Fatalf("the pinned schema does not carry type %q, so the walk cannot descend into it. "+
				"Add it to TYPES in scripts/w34-linear-schema-snapshot.py and refresh — a type the "+
				"snapshot has never seen must be a FAILURE, not a silently skipped subtree.", typeName)
		}
		for _, f := range fields {
			checked++
			sf, ok := st.Fields[f.name]
			if !ok {
				t.Errorf("linearIssuesQuery names %s, and Linear's schema has no field %q on type %q.\n"+
					"Linear validates a document BEFORE authenticating, so this is not a subtle "+
					"degradation: the provider answers HTTP 400 GRAPHQL_VALIDATION_FAILED and EVERY "+
					"linear_api import fails at the first page, forever. No other test in this package "+
					"can see it — cannedPages never reads the query it is sent.", f.path, f.name, typeName)
				continue
			}
			if sf.Deprecated {
				t.Errorf("linearIssuesQuery names %s, which Linear marks DEPRECATED. A deprecated field "+
					"still answers today and stops answering on the provider's schedule, not ours.", f.path)
			}
			for argName, argVal := range f.args {
				argType, ok := sf.Args[argName]
				if !ok {
					t.Errorf("linearIssuesQuery passes %s(%s:), and Linear's schema declares no such "+
						"argument on %s.%s — an unknown argument is a VALIDATION failure, the same 400 "+
						"as an unknown field.", f.path, argName, typeName, f.name)
					continue
				}
				if strings.HasPrefix(argVal, "$") {
					usedVars[argVal] = true
					declared, ok := vars[argVal]
					if !ok {
						t.Errorf("linearIssuesQuery uses %s at %s(%s:) and never declares it — an "+
							"undefined variable is a validation failure.", argVal, f.path, argName)
						continue
					}
					if declared != argType {
						t.Errorf("linearIssuesQuery declares %s as %q and uses it where %s.%s expects "+
							"%q. GraphQL checks variable usage against the argument type at VALIDATION "+
							"time, so a mismatch is the same 400 — including the nullability marker.",
							argVal, declared, typeName, f.name, argType)
					}
				}
			}
			// Leaf-vs-composite is a validation rule in its own right: a field whose type is an
			// object MUST carry a selection set, and a scalar MUST NOT. The snapshot only knows the
			// eight types it carries, so this fires exactly where it can be right.
			childType := namedType(sf.Type)
			_, childIsComposite := schema.Types[childType]
			switch {
			case f.hasBrace && !childIsComposite:
				t.Fatalf("linearIssuesQuery asks for a selection set on %s, whose type is %q — the "+
					"pinned schema does not carry that type. If it is an object, add it to TYPES in "+
					"scripts/w34-linear-schema-snapshot.py; if it is a scalar, the braces are a 400.",
					f.path, sf.Type)
			case !f.hasBrace && childIsComposite:
				t.Errorf("linearIssuesQuery asks for %s with NO selection set, but its type %q is an "+
					"object. A bare object field is a validation failure — the same 400.", f.path, sf.Type)
			case f.hasBrace:
				walk(childType, f.sel)
			}
		}
	}
	walk("Query", sel)

	for name := range vars {
		if !usedVars[name] {
			t.Errorf("linearIssuesQuery declares variable %s and never uses it. \"All Variables Used\" "+
				"is a GraphQL validation rule, so an orphaned declaration is a 400 and not a tidy-up.", name)
		}
	}

	if checked < linearQueryMinPaths {
		t.Fatalf("the walk checked only %d field paths, floor %d. Either the document lost its body or "+
			"this file's reader stopped reading — a guard that visits nothing passes everything.",
			checked, linearQueryMinPaths)
	}
	t.Logf("validated %d field paths of linearIssuesQuery against Linear's schema as pinned on %s "+
		"(%s, unauthenticated introspection; validation-before-auth controls: invalid doc -> %d, "+
		"valid doc -> %d)", checked, schema.Provenance.FetchedUTC, schema.Provenance.Endpoint,
		schema.Provenance.Controls["invalid_document_status"], schema.Provenance.Controls["valid_document_status"])
}

// TestLinearSchema_QueryTeamIsNonNull pins the ONE schema fact another test file in this package
// asserts a behaviour on top of, because that file states it BACKWARDS.
//
// ⚠ MEASURED, AND IT CONTRADICTS A COMMENT THIS PACKAGE ALREADY SHIPPED. linear_null_team_test.go's
// header opens with "`team` is a NULLABLE field: when the argument names nothing the credential can
// resolve, GraphQL's answer is `{"data":{"team":null}}` with NO `errors[]`". Linear's schema says
// `Query.team: Team!` — NON-NULL. Under the GraphQL spec a non-null field that resolves to null
// propagates the null to the nearest nullable parent and MUST be accompanied by an entry in
// `errors[]`, so the exact document that comment describes is one the spec does not allow this field
// to produce.
//
// ⚠ WHAT THAT DOES AND DOES NOT CHANGE, STATED PRECISELY, BECAUSE THE DIFFERENCE MATTERS: the tests
// in that file stay worth having. They lock the transport's behaviour against a body shape, and a
// transport that only survives spec-conformant providers is a transport that trusts a third party's
// error path. What is corrected is the PREMISE — the claim about Linear that names them. The
// unresolvable-team case a real tenant would produce is `{"data":null,"errors":[...]}`, which lands
// on the errors[] arm that file's case (4) already pins the ordering of.
//
// ⚠ AND THE PART THAT IS STILL NOT MEASURABLE FROM HERE: what Linear ACTUALLY sends for a team id
// that does not resolve. Authentication fails before any resolver runs, so a credential-free probe
// cannot see it. The schema's nullability is a fact; the wire body for that case remains open, and
// this test claims only the fact.
func TestLinearSchema_QueryTeamIsNonNull(t *testing.T) {
	schema := loadLinearSchema(t)
	team, ok := schema.Types["Query"].Fields["team"]
	if !ok {
		t.Fatalf("Linear's schema no longer carries Query.team — linearIssuesQuery is built on it")
	}
	if team.Type != "Team!" {
		t.Errorf("Query.team is %q in the pinned schema, want %q.\n"+
			"If Linear made this field NULLABLE, then linear_null_team_test.go's header is right after "+
			"all and this comment is the stale one — read both before changing either.", team.Type, "Team!")
	}
	if got := team.Args["id"]; got != "String!" {
		t.Errorf("Query.team(id:) is %q, want %q — linearIssuesQuery declares $teamId as String! and a "+
			"variable-usage mismatch is a validation 400", got, "String!")
	}
}
