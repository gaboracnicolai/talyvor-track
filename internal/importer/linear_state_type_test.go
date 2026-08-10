package importer

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/model"
)

// linear_state_type_test.go — LINEAR'S CANONICAL STATE CATEGORY, the half #72 scoped, #73 shipped for
// Jira, and three merges in a row deliberately refused to ship here.
//
// ⚠ THE REASON IT WAS REFUSED IS NO LONGER TRUE, AND THAT IS THIS MERGE'S FINDING. W3.4 has said, in
// four separate entries, that `state { type }` "needs a GraphQL query change that 400s the WHOLE query
// if wrong, and the test fake accepts any query, so no CI test in this repo can catch it — it needs one
// real call against a real tenant". The first half is right. The conclusion is wrong, because it
// assumes validating a query document requires a tenant.
//
// MEASURED 2026-08-09 against api.linear.app/graphql, negative-controlled FIRST (a fabricated host
// resolved to nothing, curl exit 6; a fabricated path on the REAL host answered 404 — so the answers
// below are not blanket):
//
//	POST {"query":"query { thisFieldDoesNotExist }"}   ⇒ HTTP 400  GRAPHQL_VALIDATION_FAILED
//	                                                     `Cannot query field "thisFieldDoesNotExist"`
//	POST the SHIPPED linearIssuesQuery, verbatim        ⇒ HTTP 401  AUTHENTICATION_ERROR
//	POST that query with `state { name type }`          ⇒ HTTP 401  AUTHENTICATION_ERROR
//
// Linear VALIDATES THE DOCUMENT BEFORE IT AUTHENTICATES. So "this query would 400" and "this query is
// well-formed" are distinguishable with NO CREDENTIALS AT ALL, and the exact risk that blocked this
// field for four merges is measurable from an empty machine. The probe is committed as
// scripts/w34-linear-schema-probe.py so the claim is re-runnable rather than remembered.
//
// ⚠ IT IS NOT WIRED INTO CI AND MUST NOT BE. A gate that depends on a third party's uptime is a gate
// people re-run rather than read (`c71ca9c`'s lesson, one repo over). What CI holds is the wire
// contract below — the query document, pinned locally, so a change to it is deliberate.
//
// ⚠ AND THE VOCABULARY IS NOT AN ENUM, WHICH IS THE ASYMMETRY WITH #73. Jira's
// GET /rest/api/2/statuscategory enumerates exactly four, so #73 could say "there is no fifth to
// miss". Linear models WorkflowState.type as `String!` — measured: 1,132 types, 115 enums, and NOT
// ONE of them carries the issue-state vocabulary (`ProjectStatusType` is the near miss and is for
// projects). The vocabulary comes from the schema's own field description instead, fetched by
// introspection, which is unauthenticated and answered 200:
//
//	WorkflowState.type: `One of "triage", "backlog", "unstarted", "started", "completed",
//	                     "canceled", "duplicate".`
//
// ⚠ THAT IS SEVEN, AND LINEAR'S PUBLIC DOCS LIST SIX. `duplicate` is the one an implementation
// written from memory drops — which is exactly the "no fifth to miss" hazard #73 named, arriving as a
// seventh. It is pinned by hand below, because a description string is prose and could be reworded;
// re-run the probe rather than trusting this comment.

// The vocabulary, PINNED BY HAND from the measurement above. This is rule 2: rule 1 parses the
// mapper's own switch, and deleting a `case` moves the parse and the mapper TOGETHER (#72 measured
// this and #73 re-confirmed it by running it), so a source-derived rule alone cannot see a deletion.
var measuredLinearStateTypes = []string{
	"triage", "backlog", "unstarted", "started", "completed", "canceled", "duplicate",
}

// What each measured type must mean to Track. A type that carries NO honest Track counterpart is
// deliberately NOT a resolution — #73's `undefined` rule, and the reason is the same: answering it
// would invent precisely the meaning this change exists to stop inventing.
//
//	triage    — Linear's pre-workflow inbox. Track has no such state. NOT resolved.
//	duplicate — a closed-as-duplicate state. Cancelled is CLOSE and is not the same claim. NOT resolved.
var measuredLinearStateTypeMeaning = map[string]model.IssueStatus{
	"backlog":   model.StatusBacklog,
	"unstarted": model.StatusTodo,
	"started":   model.StatusInProgress,
	"completed": model.StatusDone,
	"canceled":  model.StatusCancelled,
}

// ── driving the REAL client against a fake, so the mapping is proven through the shipped path ──

func linNodeTyped(id, stateName, stateType string, prio int) string {
	st := fmt.Sprintf(`{"name":%q}`, stateName)
	if stateType != "" {
		st = fmt.Sprintf(`{"name":%q,"type":%q}`, stateName, stateType)
	}
	return fmt.Sprintf(`{"identifier":%q,"title":"T-%s","description":"d","state":%s,"priority":%d,"labels":{"nodes":[{"name":"bug"}]},"createdAt":%q,"updatedAt":%q}`,
		id, id, st, prio, fixtureLinearCreated, fixtureLinearUpdated)
}

// linearRowsFrom drains one canned page through newLinearSource — the real client, the real decoder,
// the real mapper. A fixture-level unit test would not prove the JSON field is even read.
func linearRowsFrom(t *testing.T, nodes ...string) []SourceRow {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeRaw(w, linPage(false, "", nodes...))
	}))
	defer srv.Close()

	src := newLinearSource("k", "TEAM", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep
	var out []SourceRow
	for {
		row, ok := src.Next()
		if !ok {
			break
		}
		if row.Err != nil {
			t.Fatalf("unexpected source error: %v", row.Err)
		}
		out = append(out, row)
	}
	return out
}

// THE FINDING AS A TEST. Every one of these state names falls through mapLinearStatus's default and
// imports as `backlog` today — a SHIPPED issue filed as un-started work, #72's "data loss reported as
// success" one field over. The type says which it was, on a response we can now prove is well-formed.
func TestLinearSource_StateTypeResolvesAnUnrecognisedName(t *testing.T) {
	cases := []struct {
		name, typ string
		want      model.IssueStatus
	}{
		{"Shipped", "completed", model.StatusDone},
		{"Won't Fix", "canceled", model.StatusCancelled},
		{"Needs Review", "started", model.StatusInProgress},
		{"Ready", "unstarted", model.StatusTodo},
		{"Icebox", "backlog", model.StatusBacklog},
	}
	for _, tc := range cases {
		rows := linearRowsFrom(t, linNodeTyped("ENG-1", tc.name, tc.typ, 1))
		if got := rows[0].Issue.Status; got != tc.want {
			t.Errorf("state %q (type %q) imported as %q, want %q — the provider said which it was",
				tc.name, tc.typ, got, tc.want)
		}
	}
}

// A resolution is still a DEGRADATION worth reporting — "Needs Review" loses the in_review
// distinction — and it is the only signal this code path executed at all.
func TestLinearSource_ResolvedRowSaysItCameFromTheStateType(t *testing.T) {
	rows := linearRowsFrom(t, linNodeTyped("ENG-1", "Shipped", "completed", 1))
	notes := rows[0].Notes
	if len(notes) != 1 {
		t.Fatalf("notes = %+v, want exactly one (status); priority 1 (urgent) is recognised", notes)
	}
	line := renderWarnings(map[FieldNote]int{notes[0]: 1})[0]
	for _, want := range []string{`"Shipped"`, "state.type", `"completed"`, `"done"`} {
		if !strings.Contains(line, want) {
			t.Errorf("the warning must name the path that fired and mention %s; got:\n  %s", want, line)
		}
	}
}

// THE STRUCTURAL-ZERO GUARD, #73's rule applied to the other provider. A tenant whose response carries
// no `type` must produce a DIFFERENT line from one whose types resolved everything — otherwise nobody
// can ever tell from a production import whether the read runs at all.
func TestLinearSource_NoStateTypePresentSaysSo(t *testing.T) {
	rows := linearRowsFrom(t, linNodeTyped("ENG-1", "Shipped", "", 1))
	if got := rows[0].Issue.Status; got != model.StatusBacklog {
		t.Errorf("with no type the fallback is UNCHANGED: status = %q, want backlog", got)
	}
	line := renderWarnings(map[FieldNote]int{rows[0].Notes[0]: 1})[0]
	if !strings.Contains(line, "no state.type present") {
		t.Errorf("a Linear import that never saw a state.type must say so; got:\n  %s", line)
	}
	if strings.Contains(line, "resolved via") {
		t.Errorf("nothing resolved it; got:\n  %s", line)
	}
}

// `triage` and `duplicate` ARRIVED and still could not place the row. That is a THIRD distinct line
// from both of the above: the field is there, so the code ran, and Track has no honest counterpart.
func TestLinearSource_TriageAndDuplicateAreNotResolutions(t *testing.T) {
	for _, typ := range []string{"triage", "duplicate"} {
		rows := linearRowsFrom(t, linNodeTyped("ENG-1", "Inbox", typ, 1))
		if got := rows[0].Issue.Status; got != model.StatusBacklog {
			t.Errorf("state.type %q must NOT resolve: status = %q, want backlog", typ, got)
		}
		line := renderWarnings(map[FieldNote]int{rows[0].Notes[0]: 1})[0]
		if !strings.Contains(line, strconv.Quote(typ)) || strings.Contains(line, "resolved via") {
			t.Errorf("%q must be reported as arrived-and-unusable, not as a resolution; got:\n  %s", typ, line)
		}
		if strings.Contains(line, "no state.type present") {
			t.Errorf("the type DID arrive — saying it did not would hide that the read works; got:\n  %s", line)
		}
	}
}

// PRECEDENCE, pinned against a CONTRADICTORY fixture. The name still wins, so a recognised import is
// byte-for-byte what it was: no warning, no re-mapping.
//
// ⚠ AND THE CONTRADICTION IS REAL HERE, WHICH IT WAS NOT FOR JIRA. #73 measured the name mapping and
// the category disagreeing ZERO times across the 9 Jira names Track knows. Linear's `in review` and
// `in progress` BOTH carry type `started`, so the type is strictly coarser than the name for the one
// state Track models separately. Name-first is what makes this merge purely additive — it is not a
// preference, it is why no existing import can change.
func TestLinearSource_ARecognisedNameStillWins(t *testing.T) {
	rows := linearRowsFrom(t, linNodeTyped("ENG-1", "In Review", "started", 1))
	if got := rows[0].Issue.Status; got != model.StatusInReview {
		t.Errorf(`"In Review" is a name the mapper knows: status = %q, want in_review (NOT the coarser type)`, got)
	}
	if len(rows[0].Notes) != 0 {
		t.Errorf("a recognised name must produce no note at all; got %+v", rows[0].Notes)
	}
}

// ── THE WIRE. The query document is the thing that 400s, so it is pinned here, locally. ──

// ⚠ THE ASSERTION HARDCODES THE SELECTION ON PURPOSE — #75's C6 lesson. Writing
// `strings.Contains(body, linearIssuesQuery)` compares the constant to itself and passes for every
// possible value including "". Before this file, `linearIssuesQuery` and `defaultLinearURL` appeared
// in ZERO test files and every fake in this package answers ANY path and ANY method with the same
// body — the same hole #75 closed for Jira, still open on this side. Positive-controlled: the same
// grep DOES find jira.go's endpoint and auth assertions, so the instrument reads.
func TestLinearRequest_WireContract(t *testing.T) {
	var gotPath, gotMethod, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		gotPath, gotMethod, gotAuth, gotBody = r.URL.Path, r.Method, r.Header.Get("Authorization"), string(b)
		writeRaw(w, linPage(false, ""))
	}))
	defer srv.Close()

	src := newLinearSource("lin_api_SECRET", "TEAM", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep
	src.Next()

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST — GraphQL over GET is a different contract", gotMethod)
	}
	if gotPath != "/" {
		t.Errorf("path = %q, want the server root — the endpoint is the whole URL, /graphql", gotPath)
	}
	// linear.go's own header calls this out: "Auth is the unusual `Authorization: <API_KEY>`
	// (NO 'Bearer ')". Nothing asserted it. A Bearer prefix on a personal API key is a 401 nobody
	// would see until a real import.
	if gotAuth != "lin_api_SECRET" {
		t.Errorf("Authorization = %q, want the key VERBATIM — Linear personal keys take no Bearer prefix", gotAuth)
	}
	if strings.Contains(gotAuth, "Bearer") {
		t.Errorf("Authorization carries a Bearer prefix (%q); linear.go's contract is the raw key", gotAuth)
	}
	// THE FIELD THIS MERGE ADDS, pinned at the wire. Narrowing the selection back would silently take
	// the category away and every fixture in this package supplies its own JSON, so nothing else
	// would notice — #74's argument for the Jira `fields` list, same shape.
	if !strings.Contains(gotBody, "state { name type }") {
		t.Errorf("the outgoing query must ask for `state { name type }`; body was:\n  %s", gotBody)
	}
	// The variables the document declares must actually be sent, or the server rejects the document
	// it just validated.
	for _, want := range []string{`"teamId"`, `$teamId: String!`, `issues(first: 100, after: $after)`} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("outgoing request must contain %s; body was:\n  %s", want, gotBody)
		}
	}
}

// The endpoint literal, second-copied so a change is a deliberate two-place edit rather than a silent
// one. Same reasoning as #75's measuredJiraCloudSearchPath.
func TestLinearDefaultEndpoint_IsPinned(t *testing.T) {
	const measuredLinearGraphQLURL = "https://api.linear.app/graphql"
	if defaultLinearURL != measuredLinearGraphQLURL {
		t.Errorf("defaultLinearURL = %q, want %q (measured 2026-08-09: this host validates a query "+
			"document before authenticating, which is what makes scripts/w34-linear-schema-probe.py work)",
			defaultLinearURL, measuredLinearGraphQLURL)
	}
}

// ── RULE 1: the mapper's own switch, parsed, both directions against the pinned vocabulary. ──

// linearStateTypeCases parses `mapLinearStateType`'s switch out of csv.go's AST and returns every case
// literal with whether that clause resolves (i.e. does not return the not-recognised form). A regex
// would not survive a clause carrying two literals, which #72 hit for real.
func linearStateTypeCases(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "csv.go", nil, 0)
	if err != nil {
		t.Fatalf("parse csv.go: %v", err) // a broken path must FAIL, never silently find nothing
	}
	var fn *ast.FuncDecl
	ast.Inspect(f, func(n ast.Node) bool {
		if d, ok := n.(*ast.FuncDecl); ok && d.Name.Name == "mapLinearStateType" {
			fn = d
		}
		return true
	})
	if fn == nil {
		t.Fatal("mapLinearStateType not found in csv.go — the canonical Linear state category is unread")
	}
	out := map[string]bool{}
	ast.Inspect(fn, func(n ast.Node) bool {
		cc, ok := n.(*ast.CaseClause)
		if !ok || cc.List == nil { // the default clause carries no literals
			return true
		}
		// A clause resolves iff its return says so — the second result is the recognised flag.
		resolves := false
		ast.Inspect(cc, func(m ast.Node) bool {
			if r, ok := m.(*ast.ReturnStmt); ok && len(r.Results) == 2 {
				if id, ok := r.Results[1].(*ast.Ident); ok && id.Name == "true" {
					resolves = true
				}
			}
			return true
		})
		for _, e := range cc.List {
			if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				v, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("unquote %s: %v", lit.Value, err)
				}
				out[v] = resolves
			}
		}
		return true
	})
	if len(out) == 0 {
		t.Fatal("parsed mapLinearStateType and found ZERO case literals — the instrument is blind")
	}
	return out
}

// BOTH DIRECTIONS. A type the mapper handles that the measurement never saw is a guess; a measured
// type the mapper does not name is a silent drop. Either fails.
func TestLinearStateTypeMapping_AgreesWithTheMeasuredVocabulary(t *testing.T) {
	cases := linearStateTypeCases(t)
	measured := map[string]bool{}
	for _, v := range measuredLinearStateTypes {
		measured[v] = true
	}
	for _, v := range measuredLinearStateTypes {
		if _, ok := cases[v]; !ok {
			t.Errorf("measured state.type %q is not a case in mapLinearStateType — it would fall to the "+
				"default and import as backlog with no way to tell it from a type that never arrived", v)
		}
	}
	for got := range cases {
		if !measured[got] {
			t.Errorf("mapLinearStateType handles %q, which is NOT in the measured vocabulary — either the "+
				"schema changed (re-run scripts/w34-linear-schema-probe.py and update the pin) or this is a guess", got)
		}
	}
}

// RULE 2: the MEANING, pinned by hand. Rule 1 can only ask whether a case exists; deleting a case
// moves the parse and the mapper together (#72 measured this, #73 re-confirmed by running it), and
// nothing source-derived can see that a resolution quietly became a refusal.
// It is driven THROUGH THE SOURCE rather than by calling the mapper, for two reasons: this file then
// compiles against today's code, so every red below is BEHAVIOURAL rather than a build failure
// (#73's rule, and #74's C1 measured why — a control that cannot tell "the guard caught it" from
// "nothing compiled" is not a control); and a mapper that is right while nothing calls it is exactly
// the structural zero this item keeps finding.
func TestLinearStateTypeMeaning_IsWhatWasMeasured(t *testing.T) {
	// A state NAME the mapper cannot place, so the type is the only thing that can decide the row.
	const unknownName = "Talyvor Custom State"
	for typ, want := range measuredLinearStateTypeMeaning {
		rows := linearRowsFrom(t, linNodeTyped("ENG-1", unknownName, typ, 1))
		if got := rows[0].Issue.Status; got != want {
			t.Errorf("state.type %q imported as %q, want %q", typ, got, want)
		}
	}
	// The two that must NOT resolve, named individually so widening either is a deliberate edit.
	for _, typ := range []string{"triage", "duplicate"} {
		rows := linearRowsFrom(t, linNodeTyped("ENG-1", unknownName, typ, 1))
		if got := rows[0].Issue.Status; got != model.StatusBacklog {
			t.Errorf("state.type %q imported as %q — Track has no counterpart, and answering it invents "+
				"the meaning this change exists to stop inventing (#73's `undefined` rule)", typ, got)
		}
		line := renderWarnings(map[FieldNote]int{rows[0].Notes[0]: 1})[0]
		if strings.Contains(line, "resolved via") {
			t.Errorf("%q must not be reported as a resolution; got:\n  %s", typ, line)
		}
	}
	// A value outside the vocabulary is not a resolution either.
	rows := linearRowsFrom(t, linNodeTyped("ENG-1", unknownName, "talyvorTotallyFakeType", 1))
	if got := rows[0].Issue.Status; got != model.StatusBacklog {
		t.Errorf("an unmeasured type imported as %q; the mapper must refuse what it has never seen", got)
	}
}

// THE FLOOR. Every guard above reads either the source or a fixture, and both go quiet if the
// vocabulary shrinks to nothing. This asserts the pin itself is non-empty and still carries the
// seventh value that a from-memory implementation drops.
func TestMeasuredLinearVocabulary_IsNotEmptyAndCarriesDuplicate(t *testing.T) {
	if len(measuredLinearStateTypes) != 7 {
		t.Fatalf("the measured vocabulary has %d entries, want 7 — re-run the probe before changing this",
			len(measuredLinearStateTypes))
	}
	var hasDuplicate bool
	for _, v := range measuredLinearStateTypes {
		if v == "duplicate" {
			hasDuplicate = true
		}
	}
	if !hasDuplicate {
		t.Error(`"duplicate" is the value Linear's public docs omit and the schema description carries; ` +
			"dropping it is how this vocabulary silently becomes the six everyone remembers")
	}
}
