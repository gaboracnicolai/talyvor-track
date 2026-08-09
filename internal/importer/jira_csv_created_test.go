package importer

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
	"time"
)

// jira_csv_created_test.go — the guards behind jira_csv_created.go.
//
// The package's two-rule shape, for the reason #72 established and #82's C10 re-proved: rule 1
// reads the SHIPPED SOURCE, so a claim about the code cannot rot into decoration; rule 2 pins the
// MEASURED bytes BY HAND, because a source-derived rule CANNOT SEE A DELETION — delete the thing it
// parses and the parse and the code move together, leaving it green.
//
// Provenance for every literal below: scripts/w34-jira-csv-created-probe.py, run 2026-08-09 against
// jira.atlassian.com's anonymous issue-navigator "csv-all-fields" export, negative-controlled first
// (fabricated host ⇒ no resolution · fabricated view ⇒ 400 text/html · fabricated project ⇒ 400
// text/html). 200 resolved issues, 325 columns, exactly one "Created".

// ─── rule 1: the source claim ──────────────────────────────────────────────
//
// jira_csv_created.go's header says it reuses parseJiraCSVTime "rather than pinning a second list".
// That is the whole reason the layout list can stay as small as the measurement: two copies drift,
// and a second copy would lend this column the provenance the FIRST measurement gathered for two
// other columns (#75's overclaim, which this package has now committed once and refused twice).
//
// ⚠ THIS RULE IS AN ABSENCE ASSERTION AND AN ABSENCE IS GREEN ON A DELETED BODY — #82's C11, the
// exact trap that cost that merge a control. So it is paired with a floor that asserts a ONE: the
// file must actually CALL parseJiraCSVTime. A rule that asserts a zero needs a companion that
// asserts a one.
func TestJiraCSVCreated_Rule1_PinsNoLayoutOfItsOwn(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "jira_csv_created.go", nil, 0)
	if err != nil {
		t.Fatalf("parse the shipped source: %v", err)
	}
	var (
		parseCalls   int
		timeParse    int
		layoutLikeIn []string
	)
	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.CallExpr:
			switch fn := v.Fun.(type) {
			case *ast.Ident:
				if fn.Name == "parseJiraCSVTime" {
					parseCalls++
				}
			case *ast.SelectorExpr:
				if pkg, ok := fn.X.(*ast.Ident); ok && pkg.Name == "time" && fn.Sel.Name == "Parse" {
					timeParse++
				}
			}
		case *ast.BasicLit:
			if v.Kind == token.STRING && looksLikeAGoTimeLayout(v.Value) {
				layoutLikeIn = append(layoutLikeIn, v.Value)
			}
		}
		return true
	})
	if parseCalls != 1 {
		t.Errorf("jira_csv_created.go calls parseJiraCSVTime %d times, want exactly 1 — "+
			"the FLOOR under the two absence assertions below, so a deleted body cannot pass them", parseCalls)
	}
	if timeParse != 0 {
		t.Errorf("jira_csv_created.go calls time.Parse %d times; it must go through parseJiraCSVTime "+
			"so the layout list has ONE definition and ONE measurement behind it", timeParse)
	}
	if len(layoutLikeIn) != 0 {
		t.Errorf("jira_csv_created.go carries its own time layout(s) %v — a second copy of the list "+
			"drifts from the first and borrows a measurement it did not make", layoutLikeIn)
	}
}

// looksLikeAGoTimeLayout spots a Go reference-time layout by the reference date's own components.
// Deliberately crude and deliberately not a regex over "date-ish" text: it must fire on
// "2/Jan/2006 3:04 PM" and not on the column spelling "Created".
func looksLikeAGoTimeLayout(lit string) bool {
	return strings.Contains(lit, "2006") || strings.Contains(lit, "Jan") ||
		strings.Contains(lit, "15:04") || strings.Contains(lit, "3:04")
}

// The mapper must actually be WIRED. jiraCSVCreated can be perfect and unreachable — this package
// has shipped an exported sentinel with zero callers once already (#81), and its doc comment
// described the intention while handing enforcement to nobody.
func TestJiraCSVCreated_Rule1_TheMapperIsWiredIntoJiraRowMapper(t *testing.T) {
	src, err := os.ReadFile("csv.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "jiraCSVCreated(ci, row)") {
		t.Fatal("jiraRowMapper does not call jiraCSVCreated — the column is read by nobody")
	}
}

// ─── rule 2: the measured vocabulary, pinned by hand ───────────────────────

// TestJiraCSVCreated_Rule2_TheMeasuredBytes hardcodes what a real export sent. A source-derived rule
// cannot see a DELETION: remove the column const and rule 1's parse moves with it.
func TestJiraCSVCreated_Rule2_TheMeasuredBytes(t *testing.T) {
	if jiraCSVCreatedColumn != "Created" {
		t.Errorf("column spelling = %q, want %q — measured on the real export's 325 headers",
			jiraCSVCreatedColumn, "Created")
	}
	// Every one of these was READ OFF a real export. The unpadded hour and the unpadded day are the
	// point: a layout with %02d in either position refuses them.
	for _, raw := range []string{
		"23/Jul/2026 7:36 PM",  // probe run 2026-08-09, row 1
		"16/Jul/2026 11:24 PM", // padded hour
		"07/Aug/2026 12:54 PM", // recorded in jira_csv_dates.go's header since #78 and never read
		"09/Aug/2026 8:15 AM",  // unpadded hour
	} {
		if _, ok := parseJiraCSVTime(raw); !ok {
			t.Errorf("parseJiraCSVTime refuses %q, which a real Jira export emitted in the Created column", raw)
		}
	}
	// And the shapes it must NOT silently accept, so "we recognise this" stays falsifiable.
	for _, raw := range []string{
		"2026-07-23T19:36:00.000+0000", // the API serialisation — a DIFFERENT transport's bytes
		"2026-07-23",                   // the API's bare due-date shape
		"23/13/2026 7:36 PM",           // month 13
		"not a date",
	} {
		if _, ok := parseJiraCSVTime(raw); ok {
			t.Errorf("parseJiraCSVTime ACCEPTS %q — the CSV layout list must stay as small as the measurement", raw)
		}
	}
}

// ─── behaviour: the four outcomes, and the two absences kept apart ─────────

func TestJiraCSVCreated_TheFourOutcomes(t *testing.T) {
	const header = "Summary,Created"
	for _, tc := range []struct {
		name     string
		header   string
		row      []string
		wantZero bool
		wantVia  string
		why      string
	}{
		{
			name:   "a value the layouts accept lands and says nothing",
			header: header, row: []string{"t", "23/Jul/2026 7:36 PM"},
			wantZero: false, wantVia: "",
			why: "the normal case must be silent or the report is unreadable — #82's rule",
		},
		{
			name:   "no Created column at all is its own report",
			header: "Summary,Status", row: []string{"t", "Closed"},
			wantZero: true, wantVia: viaNoCreatedColumn,
			why: "created_at is never null, so this is the ONLY signal that the read had nothing to read",
		},
		{
			name:   "an empty cell under a present header is a different report",
			header: header, row: []string{"t", ""},
			wantZero: true, wantVia: viaNoCreatedValue,
			why: "a re-export fixes a missing column; nothing fixes a blank cell, and they are not one finding",
		},
		{
			name:   "a shape no pinned layout accepts is REPORTED, never defaulted",
			header: header, row: []string{"t", "2026-07-23T19:36:00.000+0000"},
			wantZero: true, wantVia: viaUnparseableDate,
			why: "#74's rule — and it matters most here, where the silent fallback is a plausible timestamp",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ci := buildIndex(strings.Split(tc.header, ","))
			got, notes := jiraCSVCreated(ci, tc.row)
			if got.IsZero() != tc.wantZero {
				t.Errorf("instant zero = %v, want %v — %s", got.IsZero(), tc.wantZero, tc.why)
			}
			if tc.wantVia == "" {
				if len(notes) != 0 {
					t.Errorf("notes = %v, want none — %s", notes, tc.why)
				}
				return
			}
			if len(notes) != 1 || notes[0].Via != tc.wantVia {
				t.Fatalf("notes = %+v, want exactly one with Via=%q — %s", notes, tc.wantVia, tc.why)
			}
			if notes[0].Field != fieldCreated {
				t.Errorf("note field = %q, want %q", notes[0].Field, fieldCreated)
			}
		})
	}
}

// The two absent-cases must RENDER as two different sentences. Keeping them apart in the struct and
// then collapsing them in the text would be the same defect one layer down.
func TestJiraCSVCreated_TheTwoAbsencesRenderDifferently(t *testing.T) {
	missing := FieldNote{Field: fieldCreated, Via: viaNoCreatedColumn}.render(7)
	blank := FieldNote{Field: fieldCreated, Via: viaNoCreatedValue}.render(7)
	if missing == blank {
		t.Fatal("the missing-column and empty-cell warnings render identically")
	}
	if !strings.Contains(missing, `"Created"`) || !strings.Contains(missing, "7 issue(s)") {
		t.Errorf("missing-column line does not name the column and the count: %q", missing)
	}
	if !strings.Contains(missing, "time-to-resolution") {
		t.Errorf("missing-column line does not say what breaks, which is the only reason it exists: %q", missing)
	}
	if !strings.Contains(blank, "7 issue(s)") {
		t.Errorf("empty-cell line does not carry its count: %q", blank)
	}
}

// jiraRowMapper must put the instant on the ISSUE. The mapper and the struct are two statements
// (#82's C8 seam): a perfect jiraCSVCreated whose value is dropped on the floor reds nothing else.
func TestJiraRowMapper_CarriesTheProvidersOpeningTime(t *testing.T) {
	ci := buildIndex([]string{"Summary", "Status", "Created"})
	got, err := jiraRowMapper(ci, []string{"t", "Closed", "23/Jul/2026 7:36 PM"})
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 7, 23, 19, 36, 0, 0, time.UTC)
	if !got.issue.CreatedAt.Equal(want) {
		t.Errorf("mapped CreatedAt = %v, want %v", got.issue.CreatedAt, want)
	}
}

// ⚠ THE LINEAR CSV HALF IS UNTOUCHED AND THIS PINS IT AS A KNOWN GAP RATHER THAN AN OVERSIGHT.
// Linear's export is produced in-app behind authentication and nothing in this environment can
// fetch one, so whether it even carries a creation-time column — and how it would serialise it — is
// UNMEASURED. Closing that gap by guessing is exactly #75's move. This test fails if anyone does.
func TestLinearRowMapper_CreationTimeStaysUnmeasuredRatherThanGuessed(t *testing.T) {
	ci := buildIndex([]string{"Title", "Status", "Created", "Created At", "createdAt"})
	got, err := linearRowMapper(ci, []string{"t", "Done", "23/Jul/2026 7:36 PM", "2026-07-23", "2026-07-23T19:36:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.issue.CreatedAt.IsZero() {
		t.Fatal("linearRowMapper now reads a creation time. That may well be right — but no Linear " +
			"export has been measured from this environment, so the column spelling and the " +
			"serialisation would be guesses. Measure one first, then delete this test with the bytes " +
			"in the commit message.")
	}
}
