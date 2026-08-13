package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/model"
)

// status_fidelity_test.go — W3.4: an unrecognised provider state is REPORTED, never silently rewritten.
//
// MEASURED ON MAIN (014b6e2), which is why this file exists. Of a realistic provider vocabulary,
// 11 of 22 Jira statuses and 7 of 13 Linear states fell through the mapper's `default` branch and
// became `backlog` — including "Deployed", "Released", "Shipped" and Linear's own default-workflow
// "Duplicate". Driving the async runner over a jira_csv holding two SHIPPED issues and one real
// backlog item on real Postgres produced three rows all reading status=backlog and a job row of
// {status:"succeeded", imported:3, skipped:0, failed:0, error_summary:""} — a finished issue
// imports as un-started work and the import reports itself clean.
//
// The fix does NOT invent a meaning for "Deployed" (that needs the provider's canonical state
// category, which for Linear cannot be requested without a schema change no CI test here can
// validate — see the queue). It makes the degradation VISIBLE: the mapper says whether it
// recognised the value, and the pipeline counts and reports what it did not.
//
// TWO RULES, deliberately not one:
//
//	SOURCE-DERIVED — every `case` literal in the shipped switches must be reported recognised, and
//	  a value that is in no switch must be reported unrecognised. Parsed from the AST, so a new
//	  case cannot drift away from the guard.
//	PINNED — the vocabulary the doc comments promise callers is listed here by hand. The
//	  source-derived rule alone would stay green if someone DELETED a case (the parse and the
//	  mapper move together); this rule goes red.

// ── rule 1: source-derived ───────────────────────────────────────────────────────────────────────

// caseLiteralsOf returns every string literal appearing in a `case` clause inside the named
// function in the named file. Uses the AST, not a regex: a `case "won't do":` carries an
// apostrophe and a `case "in progress", "in_progress":` carries two literals on one clause, and
// a line-shaped matcher gets both wrong.
func caseLiteralsOf(t *testing.T, file, fn string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err) // a broken root is a FAILURE, never an empty pass
	}
	var out []string
	var found bool
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Name.Name != fn {
			continue
		}
		found = true
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			cc, ok := n.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, e := range cc.List {
				bl, ok := e.(*ast.BasicLit)
				if !ok || bl.Kind != token.STRING {
					continue
				}
				s, err := strconv.Unquote(bl.Value)
				if err != nil {
					t.Fatalf("unquote %s in %s: %v", bl.Value, fn, err)
				}
				out = append(out, s)
			}
			return true
		})
	}
	if !found {
		t.Fatalf("%s: function %q not found — this guard reads the shipped source and its root moved", file, fn)
	}
	if len(out) == 0 {
		t.Fatalf("%s: %q has no case literals — the guard cannot pass by finding nothing", file, fn)
	}
	return out
}

func TestSourceDerived_EveryShippedCaseIsRecognised(t *testing.T) {
	statusFns := map[string]func(string) (model.IssueStatus, bool){
		"mapJiraStatus":   mapJiraStatus,
		"mapLinearStatus": mapLinearStatus,
	}
	for name, fn := range statusFns {
		lits := caseLiteralsOf(t, "csv.go", name)
		t.Logf("%s: %d case literals parsed from the source", name, len(lits))
		for _, lit := range lits {
			if _, ok := fn(lit); !ok {
				t.Errorf("%s(%q): the shipped switch has this case, but the mapper reports it UNRECOGNISED", name, lit)
			}
		}
	}
	prioFns := map[string]func(string) (model.IssuePriority, bool){
		"mapJiraPriority":   mapJiraPriority,
		"mapLinearPriority": mapLinearPriority,
	}
	for name, fn := range prioFns {
		lits := caseLiteralsOf(t, "csv.go", name)
		t.Logf("%s: %d case literals parsed from the source", name, len(lits))
		for _, lit := range lits {
			if _, ok := fn(lit); !ok {
				t.Errorf("%s(%q): the shipped switch has this case, but the mapper reports it UNRECOGNISED", name, lit)
			}
		}
	}
}

// The other direction. A guard that answered `true` to everything would pass the rule above.
func TestSourceDerived_AValueInNoSwitchIsUnrecognised(t *testing.T) {
	inNoSwitch := func(fn string, v string) bool {
		for _, lit := range caseLiteralsOf(t, "csv.go", fn) {
			if lit == strings.ToLower(strings.TrimSpace(v)) {
				return false
			}
		}
		return true
	}
	// Real names, measured on main as falling to the default branch.
	for _, v := range []string{"Deployed", "Released", "Ready for QA", "Waiting for customer", "Blocked"} {
		if !inNoSwitch("mapJiraStatus", v) {
			t.Fatalf("fixture %q is in mapJiraStatus's switch — pick a value the mapper genuinely does not know", v)
		}
		if got, ok := mapJiraStatus(v); ok {
			t.Errorf("mapJiraStatus(%q) = (%s, recognised=true); it is in no case clause and must be reported unrecognised", v, got)
		}
	}
	for _, v := range []string{"Duplicate", "Triage", "Shipped", "Needs Review"} {
		if !inNoSwitch("mapLinearStatus", v) {
			t.Fatalf("fixture %q is in mapLinearStatus's switch — pick a value the mapper genuinely does not know", v)
		}
		if got, ok := mapLinearStatus(v); ok {
			t.Errorf("mapLinearStatus(%q) = (%s, recognised=true); it is in no case clause and must be reported unrecognised", v, got)
		}
	}
	// Jira's other extremely common priority scheme.
	for _, v := range []string{"P1", "P2", "Urgent"} {
		if got, ok := mapJiraPriority(v); ok {
			t.Errorf("mapJiraPriority(%q) = (%d, recognised=true); it is in no case clause", v, got)
		}
	}
}

// ── rule 2: pinned vocabulary ────────────────────────────────────────────────────────────────────
// Deleting a `case` moves the parse and the mapper together, so rule 1 stays green. This does not.
// These are the names the ImportLinearCSV / ImportJiraCSV doc comments promise callers.

func TestPinned_DocumentedVocabularyStaysMapped(t *testing.T) {
	for in, want := range map[string]model.IssueStatus{
		"Backlog": model.StatusBacklog, "To Do": model.StatusTodo, "todo": model.StatusTodo,
		"Open": model.StatusTodo, "Reopened": model.StatusTodo,
		"In Progress": model.StatusInProgress, "In Review": model.StatusInReview,
		"Code Review": model.StatusInReview, "Done": model.StatusDone,
		"Closed": model.StatusDone, "Resolved": model.StatusDone,
		"Cancelled": model.StatusCancelled, "Won't Do": model.StatusCancelled,
	} {
		got, ok := mapJiraStatus(in)
		if !ok || got != want {
			t.Errorf("mapJiraStatus(%q) = (%s, %v), want (%s, true)", in, got, ok, want)
		}
	}
	for in, want := range map[string]model.IssueStatus{
		"Backlog": model.StatusBacklog, "Todo": model.StatusTodo, "To Do": model.StatusTodo,
		"In Progress": model.StatusInProgress, "In Review": model.StatusInReview,
		"Done": model.StatusDone, "Completed": model.StatusDone,
		"Cancelled": model.StatusCancelled, "Canceled": model.StatusCancelled,
	} {
		got, ok := mapLinearStatus(in)
		if !ok || got != want {
			t.Errorf("mapLinearStatus(%q) = (%s, %v), want (%s, true)", in, got, ok, want)
		}
	}
	for in, want := range map[string]model.IssuePriority{
		"Highest": model.PriorityUrgent, "Blocker": model.PriorityUrgent, "Critical": model.PriorityUrgent,
		"High": model.PriorityHigh, "Major": model.PriorityHigh, "Medium": model.PriorityMedium,
		"Low": model.PriorityLow, "Lowest": model.PriorityLow, "Trivial": model.PriorityLow, "Minor": model.PriorityLow,
	} {
		got, ok := mapJiraPriority(in)
		if !ok || got != want {
			t.Errorf("mapJiraPriority(%q) = (%d, %v), want (%d, true)", in, got, ok, want)
		}
	}
	// Linear's API priority is a number, not a name. 0 is "No priority" — a REAL value the user
	// chose, not a failure to map, so it is recognised. Anything outside 0..4 is not.
	//
	// ⚠ THE INTEGRAL SPELLINGS ARE ASSERTED HERE AND THE OTHER LEGAL SPELLINGS OF THE SAME DOUBLE
	// ARE NOT — `2.0` and `2e0` are exercised through the WIRE in linear_api_priority_test.go,
	// because the decoder is what refused them and a mapper called directly never meets it.
	for p := 0; p <= 4; p++ {
		if _, ok := linearPriorityFromNumber(json.Number(strconv.Itoa(p))); !ok {
			t.Errorf("linearPriorityFromNumber(%d): Linear's documented scale must be recognised", p)
		}
	}
	for _, p := range []int{-1, 5, 7, 99} {
		if got, ok := linearPriorityFromNumber(json.Number(strconv.Itoa(p))); ok {
			t.Errorf("linearPriorityFromNumber(%d) = (%d, recognised=true); outside Linear's 0..4 scale", p, got)
		}
	}
	// An ABSENT priority is a genuine "no priority", not a mismatch — so it must NOT warn.
	// An ABSENT status is not: both providers always have one, so an empty one means we failed
	// to find it (a misnamed CSV column silently importing every row as backlog is the trap).
	if _, ok := mapJiraPriority(""); !ok {
		t.Error(`mapJiraPriority(""): an absent priority is "none", not an unrecognised value`)
	}
	if _, ok := mapLinearPriority(""); !ok {
		t.Error(`mapLinearPriority(""): an absent priority is "none", not an unrecognised value`)
	}
	if _, ok := mapJiraStatus(""); ok {
		t.Error(`mapJiraStatus(""): an absent status must be reported, not silently defaulted to backlog`)
	}
	if _, ok := mapLinearStatus(""); ok {
		t.Error(`mapLinearStatus(""): an absent status must be reported, not silently defaulted to backlog`)
	}
}

// ── the pipeline reports it ──────────────────────────────────────────────────────────────────────

func TestRun_ReportsUnrecognisedStatus(t *testing.T) {
	imp := New(&fakeIssueStore{})
	csv := "Summary,Description,Status,Priority,Labels\n" +
		"Shipped last week,d,Deployed,High,bug\n" +
		"Also shipped,d,Deployed,High,bug\n" +
		"Waiting,d,Waiting for customer,P1,bug\n" +
		"A real backlog item,d,Backlog,Low,bug\n"
	out, err := imp.ImportJiraCSV(t.Context(), "ws1", "team1", strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}
	if out.Imported != 4 || out.Skipped != 0 {
		t.Fatalf("counts = imported %d skipped %d, want 4/0 (the rows DID import — that is the point)", out.Imported, out.Skipped)
	}
	joined := strings.Join(out.Warnings, "\n")
	for _, want := range []string{`"Deployed"`, `2 issue`, `"Waiting for customer"`, `"P1"`, `"backlog"`} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings must mention %s; got:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, `"Backlog"`) || strings.Contains(joined, `"Low"`) {
		t.Errorf("a RECOGNISED value must not be warned about; got:\n%s", joined)
	}
}

// A 10,000-row import of one unknown status must produce ONE warning, not 10,000 — otherwise the
// report is unreadable and the fix trades a silent failure for a useless one.
func TestRun_WarningsAreCountedNotAccumulated(t *testing.T) {
	var b strings.Builder
	b.WriteString("Summary,Description,Status,Priority,Labels,Created,Updated\n")
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&b, "row %d,d,Deployed,High,bug,23/Jul/2026 7:36 PM,23/Jul/2026 7:36 PM\n", i)
	}
	out, err := New(&fakeIssueStore{}).ImportJiraCSV(t.Context(), "ws1", "team1", strings.NewReader(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Warnings) != 1 {
		t.Fatalf("500 rows of one unknown status = %d warnings, want exactly 1:\n%s", len(out.Warnings), strings.Join(out.Warnings, "\n"))
	}
	if !strings.Contains(out.Warnings[0], "500 issue") {
		t.Fatalf("the single warning must carry the count; got %q", out.Warnings[0])
	}
}

// CANNOT PASS BY ABSENCE, and cannot pass by warning about everything: a wholly recognised import
// must produce ZERO warnings. Paired with the test above, a mapper stuck on either answer fails.
func TestRun_CleanImportProducesNoWarnings(t *testing.T) {
	// ⚠ `Resolved` IS A COLUMN HERE FOR THE REASON `Created` ALREADY WAS: row "Three" imports as
	// DONE, and a done issue whose export supplies no completion instant is now reported
	// (csv_done_without_completion.go). A fully recognised import is one that gives the mapper
	// everything it reads; suppressing the line instead would hide the loss it exists to make audible.
	csv := "Summary,Description,Status,Priority,Labels,Created,Updated,Resolved\n" +
		"One,d,To Do,Highest,bug,23/Jul/2026 7:36 PM,23/Jul/2026 7:36 PM,\n" +
		"Two,d,In Progress,Medium,bug,23/Jul/2026 7:36 PM,23/Jul/2026 7:36 PM,\n" +
		"Three,d,Resolved,Lowest,bug,23/Jul/2026 7:36 PM,23/Jul/2026 7:36 PM,23/Jul/2026 7:36 PM\n"
	out, err := New(&fakeIssueStore{}).ImportJiraCSV(t.Context(), "ws1", "team1", strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Warnings) != 0 {
		t.Fatalf("a fully recognised import must warn about nothing; got:\n%s", strings.Join(out.Warnings, "\n"))
	}
	if out.Warnings == nil {
		t.Error("Warnings must be an empty slice, not nil, so the JSON is [] and not null")
	}
}

// Warning order must be stable — a report that reshuffles between identical runs cannot be diffed.
func TestRun_WarningOrderIsDeterministic(t *testing.T) {
	csv := "Summary,Description,Status,Priority,Labels\n" +
		"a,d,Deployed,P1,x\nb,d,Released,P2,x\nc,d,Blocked,P3,x\nd,d,QA,P4,x\n"
	var first []string
	for i := 0; i < 8; i++ {
		out, err := New(&fakeIssueStore{}).ImportJiraCSV(t.Context(), "ws1", "team1", strings.NewReader(csv))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = out.Warnings
			if !sort.StringsAreSorted(first) {
				t.Fatalf("warnings must be sorted; got:\n%s", strings.Join(first, "\n"))
			}
			continue
		}
		if strings.Join(out.Warnings, "|") != strings.Join(first, "|") {
			t.Fatalf("run %d reordered warnings:\n%s\nvs\n%s", i, strings.Join(out.Warnings, "|"), strings.Join(first, "|"))
		}
	}
}

// The MISNAMED-COLUMN trap: a CSV whose status column is called "State" imports every row as
// backlog. Today that is silent. It must be reported as an absent status, not an unknown one.
func TestRun_AbsentStatusColumnIsReported(t *testing.T) {
	csv := "Summary,Description,State,Priority,Labels\n" +
		"One,d,Done,High,bug\nTwo,d,Done,High,bug\n"
	out, err := New(&fakeIssueStore{}).ImportJiraCSV(t.Context(), "ws1", "team1", strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(out.Warnings, "\n")
	if !strings.Contains(joined, "no status value") || !strings.Contains(joined, "2 issue") {
		t.Fatalf("a missing Status column must be reported; got:\n%s", joined)
	}
}

// ── the API sources carry it too ─────────────────────────────────────────────────────────────────

func TestJiraSource_CarriesTheNote(t *testing.T) {
	page := `{"issues":[{"key":"PROJ-7","fields":{"summary":"Ship it","description":null,` +
		`"status":{"name":"Deployed"},"priority":{"name":"P1"},"labels":[],"created":"` + fixtureJiraCreated + `","updated":"` + fixtureJiraUpdated + `"}}],"isLast":true}`
	srv := httptest.NewServer(cannedPages([]string{page}, `{"issues":[],"isLast":true}`))
	defer srv.Close()
	src := newJiraSource(context.Background(), "e:t", "PROJ", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep

	row, ok := src.Next()
	if !ok || row.Err != nil {
		t.Fatalf("ok=%v err=%v", ok, row.Err)
	}
	if len(row.Notes) != 2 {
		t.Fatalf("notes = %+v, want one for status and one for priority", row.Notes)
	}
	if row.Issue.Status != model.StatusBacklog {
		t.Errorf("the fallback itself is unchanged: status = %q, want backlog", row.Issue.Status)
	}
}

func TestLinearSource_CarriesTheNote(t *testing.T) {
	page := `{"data":{"team":{"issues":{"pageInfo":{"hasNextPage":false,"endCursor":""},` +
		`"nodes":[{"identifier":"ENG-7","title":"Dup","description":"","state":{"name":"Duplicate"},` +
		`"priority":9,"labels":{"nodes":[]},"createdAt":"` + fixtureLinearCreated + `","updatedAt":"` + fixtureLinearUpdated + `"}]}}}}`
	srv := httptest.NewServer(cannedPages([]string{page}, `{"data":{"team":{"issues":{"pageInfo":{"hasNextPage":false},"nodes":[]}}}}`))
	defer srv.Close()
	src := newLinearSource(context.Background(), "tok", "TEAM", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep

	row, ok := src.Next()
	if !ok || row.Err != nil {
		t.Fatalf("ok=%v err=%v", ok, row.Err)
	}
	if len(row.Notes) != 2 {
		t.Fatalf("notes = %+v, want one for state \"Duplicate\" and one for priority 9", row.Notes)
	}
}

// A row that FAILS to import must not also be counted as a degraded one — it never landed.
func TestRun_SkippedRowContributesNoWarning(t *testing.T) {
	store := &fakeIssueStore{failNext: true}
	csv := "Summary,Description,Status,Priority,Labels\n" +
		"Boom,d,Deployed,High,bug\n"
	out, err := New(store).ImportJiraCSV(t.Context(), "ws1", "team1", strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}
	if out.Imported != 0 || out.Skipped != 1 {
		t.Fatalf("counts = %d/%d, want 0 imported / 1 skipped", out.Imported, out.Skipped)
	}
	if len(out.Warnings) != 0 {
		t.Fatalf("a row that never landed must not be reported as degraded; got:\n%s", strings.Join(out.Warnings, "\n"))
	}
}
