package importer

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/model"
)

// status_category_test.go — W3.4: Jira's CANONICAL state category, the one the mapper never asked for.
//
// MEASURED AGAINST A REAL JIRA (jira.atlassian.com, anonymous REST, 2026-08-09), not read from a
// doc and not assumed from a fixture. Two calls, the second controlled by a fabricated host that
// answered 404 so the 200 was not a blanket answer:
//
//	GET /rest/api/2/search?fields=status&maxResults=1  ⇒  "status":{"name":"Gathering Interest",
//	    "id":"11772","statusCategory":{"id":2,"key":"new","colorName":"default","name":"To Do"}}
//	GET /rest/api/2/statuscategory                     ⇒  EXACTLY FOUR categories:
//	    id=1 key="undefined" (No Category) · id=2 key="new" · id=4 key="indeterminate" · id=3 key="done"
//
// Two things follow, and they are why this change is safe to make and was worth making:
//
//	IT IS FREE. statusCategory is NESTED INSIDE the `status` object jiraFields already requests, so
//	  no query changes, nothing can 400, and a provider that omits it degrades to today's behaviour.
//	  (This is the whole reason the Jira half shipped and the Linear half did not: Linear's
//	  `state { type }` needs a GraphQL query change that 400s the WHOLE query if wrong, and the test
//	  fake here accepts any query, so no CI test in this repo could catch it. Still unshipped.)
//	IT IS NOT A ROUNDING ERROR. That same instance defines 46 statuses. mapJiraStatus knows 9. The
//	  other 37 ALL import as `backlog` today — 13 of them sit in `indeterminate` (in flight) and 4
//	  in `done` (finished: "Completed", "Implemented", "Published", "Closed - Commenting Disabled").
//	  The category resolves 37 of 37. Against the 9 names the mapper DOES know it disagrees ZERO
//	  times — which is why the name mapping still goes first below: this changes nothing that
//	  already worked, it only reaches rows that were being silently mis-imported.
//
// ⚠ AND THE TRAP THE QUEUE NAMED BEFORE I STARTED: a field this environment cannot prove ever
// arrives on a CUSTOMER's tenant is exactly the structural-zero class — ship a silent category read
// and a Jira that never sends one is byte-indistinguishable from a Jira whose categories resolved
// everything. So the warning NAMES THE PATH THAT FIRED. Three distinguishable lines, asserted below:
// resolved-via-category · category-present-but-unusable · no-category-present. The first real import
// against a real tenant therefore REPORTS whether this code ran, instead of hiding it.

// jiraIssueWithCategoryJSON is jiraIssueJSON plus the nested statusCategory, shaped exactly like the
// measured response above. categoryKey == "" omits the object entirely — a provider that sends none.
func jiraIssueWithCategoryJSON(key, summary, status, categoryKey, categoryName string) string {
	statusObj := fmt.Sprintf(`{"name":%q}`, status)
	if categoryKey != "" {
		statusObj = fmt.Sprintf(`{"name":%q,"id":"11772","statusCategory":{"id":2,"key":%q,"colorName":"default","name":%q}}`,
			status, categoryKey, categoryName)
	}
	// ⚠ `"resolution": null`, NOT AN ABSENT KEY, and that is the shape rather than a detail. These
	// fixtures carry no resolutiondate, and the provider pairs the two: measured on the shipped
	// endpoint, an unresolved issue comes back with the key PRESENT and null, while a key that is
	// missing entirely means the `fields` list never asked — which api_resolution.go reports as a
	// structural zero. A fixture that omitted it would be exercising that fault, not this one.
	return fmt.Sprintf(`{"key":%q,"fields":{"summary":%q,"description":null,"status":%s,"priority":{"name":"Medium"},"labels":[],"created":%q,"updated":%q,"resolution":null}}`,
		key, summary, statusObj, fixtureJiraCreated, fixtureJiraUpdated)
}

// jiraRowsFrom drains a one-page canned Jira response through the real source.
func jiraRowsFrom(t *testing.T, issues ...string) []SourceRow {
	t.Helper()
	page := fmt.Sprintf(`{"issues":[%s],"isLast":true}`, strings.Join(issues, ","))
	srv := httptest.NewServer(cannedPages([]string{page}, `{"issues":[],"isLast":true}`))
	t.Cleanup(srv.Close)
	src := newJiraSource("e:t", "PROJ", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep

	var out []SourceRow
	for {
		row, ok := src.Next()
		if !ok {
			return out
		}
		if row.Err != nil {
			t.Fatalf("unexpected source error: %v", row.Err)
		}
		out = append(out, row)
	}
}

// ── the finding ──────────────────────────────────────────────────────────────────────────────────

// The exact status the real instance returned. Track has never heard of "Gathering Interest"; Jira
// says plainly that it is a To Do. Today that issue imports as backlog.
func TestJiraSource_UnrecognisedNameIsResolvedByItsCategory(t *testing.T) {
	for _, tc := range []struct {
		name, categoryKey, categoryName string
		want                            model.IssueStatus
	}{
		{"Gathering Interest", "new", "To Do", model.StatusTodo},
		{"Ready for QA", "indeterminate", "In Progress", model.StatusInProgress},
		{"Implemented", "done", "Done", model.StatusDone},
		{"Published", "done", "Done", model.StatusDone},
	} {
		rows := jiraRowsFrom(t, jiraIssueWithCategoryJSON("PROJ-1", "S", tc.name, tc.categoryKey, tc.categoryName))
		if len(rows) != 1 {
			t.Fatalf("%q: got %d rows, want 1", tc.name, len(rows))
		}
		if got := rows[0].Issue.Status; got != tc.want {
			t.Errorf("status %q (statusCategory %q) imported as %q, want %q — the provider said which it was",
				tc.name, tc.categoryKey, got, tc.want)
		}
	}
}

// A resolution is still a DEGRADATION worth reporting — "Ready for QA" loses the in_review
// distinction — and it is the only signal that this code path executed at all.
func TestJiraSource_ResolvedRowSaysItCameFromTheCategory(t *testing.T) {
	rows := jiraRowsFrom(t, jiraIssueWithCategoryJSON("PROJ-1", "S", "Gathering Interest", "new", "To Do"))
	notes := rows[0].Notes
	if len(notes) != 1 {
		t.Fatalf("notes = %+v, want exactly one (status); the priority Medium is recognised", notes)
	}
	line := renderWarnings(map[FieldNote]int{notes[0]: 1})[0]
	for _, want := range []string{`"Gathering Interest"`, "statusCategory", `"new"`, `"todo"`} {
		if !strings.Contains(line, want) {
			t.Errorf("the warning must name the path that fired and mention %s; got:\n  %s", want, line)
		}
	}
}

// THE STRUCTURAL-ZERO GUARD. A tenant whose Jira sends no category must produce a DIFFERENT line
// from one whose categories resolved everything — otherwise nobody can ever tell whether the
// category read works in production, which is the entire failure mode this file was written against.
func TestJiraSource_NoCategoryPresentSaysSo(t *testing.T) {
	rows := jiraRowsFrom(t, jiraIssueWithCategoryJSON("PROJ-1", "S", "Deployed", "", ""))
	if got := rows[0].Issue.Status; got != model.StatusBacklog {
		t.Errorf("with no category the fallback is UNCHANGED: status = %q, want backlog", got)
	}
	line := renderWarnings(map[FieldNote]int{rows[0].Notes[0]: 1})[0]
	if !strings.Contains(line, "no statusCategory present") {
		t.Errorf("a Jira import that never saw a statusCategory must say so; got:\n  %s", line)
	}
}

// Jira's own "No Category" (key `undefined`, id 1 — a real row in the measured enumeration) is Jira
// saying it does not know either. Answering it with a Track status would invent exactly the meaning
// this change exists to stop inventing. It is reported as arrived-but-unusable, which is a third
// distinct line: the field DID arrive, so the code ran, and it still could not place the row.
func TestJiraSource_UndefinedCategoryIsNotAResolution(t *testing.T) {
	rows := jiraRowsFrom(t, jiraIssueWithCategoryJSON("PROJ-1", "S", "Deployed", "undefined", "No Category"))
	if got := rows[0].Issue.Status; got != model.StatusBacklog {
		t.Errorf(`statusCategory "undefined" must NOT resolve: status = %q, want backlog`, got)
	}
	line := renderWarnings(map[FieldNote]int{rows[0].Notes[0]: 1})[0]
	if !strings.Contains(line, `"undefined"`) || strings.Contains(line, "resolved via") {
		t.Errorf("an unusable category must be reported as arrived-and-unusable, not as a resolution; got:\n  %s", line)
	}
	if strings.Contains(line, "no statusCategory present") {
		t.Errorf("the category DID arrive — saying it did not would hide that the read works; got:\n  %s", line)
	}
}

// PRECEDENCE, pinned deliberately against a CONTRADICTORY fixture. Measured on the real instance:
// the name mapping and the category disagree ZERO times across the 9 names Track knows. So the name
// still wins, and a recognised import is byte-for-byte what it was — no warning, no re-mapping.
func TestJiraSource_ARecognisedNameStillWins(t *testing.T) {
	rows := jiraRowsFrom(t, jiraIssueWithCategoryJSON("PROJ-1", "S", "Done", "new", "To Do"))
	if got := rows[0].Issue.Status; got != model.StatusDone {
		t.Errorf("a name the mapper knows must not be re-decided by the category: status = %q, want done", got)
	}
	if len(rows[0].Notes) != 0 {
		t.Errorf("a fully recognised row must warn about nothing; got %+v", rows[0].Notes)
	}
}

// ── the pipeline reports it ──────────────────────────────────────────────────────────────────────

// Counted, not accumulated — and counted PER PATH, so a tenant with a mix reports both truths.
func TestRun_WarningsNameTheirPathAndStayCounted(t *testing.T) {
	rows := jiraRowsFrom(t,
		jiraIssueWithCategoryJSON("PROJ-1", "a", "Gathering Interest", "new", "To Do"),
		jiraIssueWithCategoryJSON("PROJ-2", "b", "Gathering Interest", "new", "To Do"),
		jiraIssueWithCategoryJSON("PROJ-3", "c", "Deployed", "", ""),
	)
	degraded := map[FieldNote]int{}
	for _, r := range rows {
		for _, n := range r.Notes {
			degraded[n]++
		}
	}
	got := renderWarnings(degraded)
	if len(got) != 2 {
		t.Fatalf("two distinct (value, path) pairs must produce two lines, got %d:\n%s", len(got), strings.Join(got, "\n"))
	}
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "2 issue(s)") {
		t.Errorf("the resolved pair must carry its count; got:\n%s", joined)
	}
	if !strings.Contains(joined, "resolved via statusCategory") || !strings.Contains(joined, "no statusCategory present") {
		t.Errorf("both paths must be nameable in one report; got:\n%s", joined)
	}
}

// ── the read cannot silently stop arriving ───────────────────────────────────────────────────────

// The category rides on the `status` field. Narrow jiraFields and it vanishes, every unrecognised
// status quietly returns to backlog, and nothing in the suite above would notice — every fixture
// here supplies its own JSON. So assert the field list on the WIRE, from the request the client
// actually sent, not from the package variable.
func TestJiraRequest_AsksForTheStatusFieldThatCarriesTheCategory(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		writeRaw(w, `{"issues":[],"isLast":true}`)
	}))
	defer srv.Close()

	src := newJiraSource("e:t", "PROJ", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep
	src.Next()

	var sent struct {
		Fields []string `json:"fields"`
	}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("could not read the request the client sent (%q): %v", string(body), err)
	}
	if len(sent.Fields) == 0 {
		t.Fatal("the request asked for NO fields — this guard cannot pass by finding nothing")
	}
	var hasStatus bool
	for _, f := range sent.Fields {
		if f == "status" {
			hasStatus = true
		}
	}
	if !hasStatus {
		t.Fatalf("the outgoing request must ask for %q — statusCategory is nested inside it and arrives only with it; asked for %v",
			"status", sent.Fields)
	}
}

// ── the same two rules the status mappers already carry ──────────────────────────────────────────
// Rule 1 reads the shipped switch out of the AST so a new case cannot drift away from the guard.
// Rule 2 pins the vocabulary by hand, because DELETING a case moves the parse and the mapper
// together and rule 1 stays green — measured on #72, and it is still true here.

func TestSourceDerived_EveryShippedCategoryCaseIsRecognised(t *testing.T) {
	lits := caseLiteralsOf(t, "csv.go", "mapJiraStatusCategory")
	t.Logf("mapJiraStatusCategory: %d case literals parsed from the source", len(lits))
	for _, lit := range lits {
		if _, ok := mapJiraStatusCategory(lit); !ok {
			t.Errorf("mapJiraStatusCategory(%q): the shipped switch has this case, but the mapper reports it UNRECOGNISED", lit)
		}
	}
}

// The other direction, so a mapper that answered `true` to everything cannot pass.
func TestSourceDerived_ACategoryInNoSwitchIsUnrecognised(t *testing.T) {
	for _, k := range []string{"undefined", "", "closed", "backlog", "in progress"} {
		for _, lit := range caseLiteralsOf(t, "csv.go", "mapJiraStatusCategory") {
			if lit == strings.ToLower(strings.TrimSpace(k)) {
				t.Fatalf("fixture %q is in the switch — pick a key the mapper genuinely does not know", k)
			}
		}
		if got, ok := mapJiraStatusCategory(k); ok {
			t.Errorf("mapJiraStatusCategory(%q) = (%s, recognised=true); it is in no case clause", k, got)
		}
	}
}

// PINNED, and pinned to a MEASURED enumeration rather than to a doc: GET /rest/api/2/statuscategory
// on a real Jira returned exactly these four and no others, so this is the complete vocabulary —
// there is no fifth key for a future Jira to surprise us with, and `undefined` is deliberately NOT
// mapped. If Atlassian ever adds one, the warning line for it already reads
// `statusCategory "…" carries no Track status`, which is the honest answer until someone decides.
func TestPinned_TheFourMeasuredCategories(t *testing.T) {
	for key, want := range map[string]model.IssueStatus{
		"new":           model.StatusTodo,
		"indeterminate": model.StatusInProgress,
		"done":          model.StatusDone,
	} {
		got, ok := mapJiraStatusCategory(key)
		if !ok || got != want {
			t.Errorf("mapJiraStatusCategory(%q) = (%s, %v), want (%s, true)", key, got, ok, want)
		}
	}
	if got, ok := mapJiraStatusCategory("undefined"); ok {
		t.Errorf(`mapJiraStatusCategory("undefined") = (%s, recognised=true); Jira's own "No Category" is not a Track status`, got)
	}
}

// The CSV transports have no category and their wording must be untouched by this merge — a Jira
// CSV export carries no such column, so claiming "no statusCategory present" there would be a
// sentence about a field that was never in play.
func TestCSVWarningsAreUnchangedByThisMerge(t *testing.T) {
	out, err := New(&fakeIssueStore{}).ImportJiraCSV(t.Context(), "ws1", "team1",
		strings.NewReader("Summary,Description,Status,Priority,Labels,Created,Updated\nOne,d,Deployed,High,bug,23/Jul/2026 7:36 PM,23/Jul/2026 7:36 PM\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := `unrecognised status "Deployed" on 1 issue(s) — imported as "backlog"`
	if len(out.Warnings) != 1 || out.Warnings[0] != want {
		t.Fatalf("the CSV wording must be byte-identical to #72's:\n got %q\nwant %q", strings.Join(out.Warnings, "|"), want)
	}
}
