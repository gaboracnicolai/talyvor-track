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

// linear_csv_updated_test.go — the guards behind linear_csv_updated.go.
//
// The package's two-rule shape, for the reason #72 established and #82's C10 re-proved: rule 1
// reads the SHIPPED SOURCE, so a claim about the code cannot rot into decoration; rule 2 pins the
// MEASURED bytes BY HAND, because a source-derived rule CANNOT SEE A DELETION — delete the thing it
// parses and the parse and the code move together, leaving it green.
//
// Provenance for every literal below: scripts/w34-linear-csv-updated-probe.py, run 2026-08-10 over
// the public-repository corpus #99 opened, negative-controlled first (fabricated column set ⇒ 0
// files · fabricated repository ⇒ REFUSED · fabricated path in a real repository ⇒ REFUSED).
// 45 files · 3,026 data rows · 6 header shapes · `Updated` in 44 of 45 files · 2,947 non-empty
// cells. Second-hand bytes, and the code says so rather than borrowing the Jira probe's first-hand
// provenance — #75's overclaim.

// ─── rule 1: the source claims ─────────────────────────────────────────────
//
// linear_csv_updated.go's header says it parses with parseLinearCSVTime and deliberately NOT with
// parseJiraCSVTime, because the two lists are kept apart on measured grounds (one provider renders
// the same instant two completely different ways on its API and in its CSV — jira_csv_dates.go).
//
// ⚠ THE ABSENCE HALF IS GREEN ON A DELETED BODY — #82's C11 — so it is PAIRED with a floor that
// asserts a ONE: the file must actually CALL parseLinearCSVTime. A rule that asserts a zero needs a
// companion that asserts a one.
func TestLinearCSVUpdated_Rule1_ParsesWithTheLinearListAndPinsNoLayoutOfItsOwn(t *testing.T) {
	src, err := os.ReadFile("linear_csv_updated.go")
	if err != nil {
		t.Fatalf("read the shipped source: %v", err)
	}
	body := stripCommentsOf(t, "linear_csv_updated.go", string(src))

	// The floor: it must call the Linear parser. Without this the absence assertions below are
	// satisfied by a file that parses nothing at all.
	if !strings.Contains(body, "parseLinearCSVTime") {
		t.Fatalf("linear_csv_updated.go never calls parseLinearCSVTime — every assertion in this " +
			"file about WHICH parser it uses is vacuous")
	}
	// The absence: it must not reach for the Jira parser, which would lend a Linear export the
	// evidence a Jira measurement gathered.
	if strings.Contains(body, "parseJiraCSVTime") {
		t.Errorf("linear_csv_updated.go calls parseJiraCSVTime — that lends this column Jira's " +
			"observed-bytes provenance, which is #75's overclaim")
	}
}

// TestLinearCSVUpdated_Rule1_TheMapperIsWiredIntoLinearRowMapper — the wiring, scoped to the
// FUNCTION rather than to the file.
//
// ⚠ A FILE-WIDE `strings.Contains(csv.go, "UpdatedAt")` WOULD PASS ON THIS REPO FOREVER, because
// jiraRowMapper has assigned UpdatedAt since #85. The subject of this claim is linearRowMapper, so
// the guard reads linearRowMapper's own body and nothing else — a sibling's assignment must not
// answer for it.
func TestLinearCSVUpdated_Rule1_TheMapperIsWiredIntoLinearRowMapper(t *testing.T) {
	body := identsOfFunc(t, "csv.go", "linearRowMapper")
	if !strings.Contains(body, "linearCSVUpdated") {
		t.Fatalf("linearRowMapper never calls linearCSVUpdated — the column is measured, mapped, " +
			"unit-tested and never read")
	}
	if !strings.Contains(body, "UpdatedAt") {
		t.Errorf("linearRowMapper never assigns UpdatedAt — the mapper can be called and its " +
			"result dropped on the floor, which is exactly the shape this file exists to stop")
	}
	// The positive control on the guard itself: the scoping must be real. If identsOfFunc silently
	// returned the whole file, this assertion would be satisfied by jiraRowMapper's own call and
	// the guard above would be measuring nothing.
	if strings.Contains(body, "jiraCSVUpdated") {
		t.Errorf("linearRowMapper's identifier set contains jiraCSVUpdated — the function scoping " +
			"is not working, so the two assertions above are answered by the wrong mapper")
	}
}

// identsOfFunc returns the identifiers inside ONE named top-level function, so a claim about that
// function cannot be satisfied by its neighbours.
func identsOfFunc(t *testing.T, name, fn string) string {
	t.Helper()
	src, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, string(src), 0)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	var b strings.Builder
	found := false
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Name.Name != fn {
			continue
		}
		found = true
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				b.WriteString(id.Name)
				b.WriteString("\n")
			}
			return true
		})
	}
	if !found {
		t.Fatalf("%s has no top-level func %s — this guard is reading a function that no longer "+
			"exists, so it would pass by finding nothing", name, fn)
	}
	return b.String()
}

// ─── rule 2: the measured bytes, pinned by hand ────────────────────────────
//
// These are transcribed from the probe's own output, NOT produced by formatting a constant with
// itself (#75's C6). If the shipped layout list stops accepting the shape real exports carry, this
// fails — which a source-derived rule cannot do.
func TestLinearCSVUpdated_Rule2_TheMeasuredBytes(t *testing.T) {
	measured := []struct {
		raw  string
		want time.Time
	}{
		{"2024-09-05T04:59:25.361Z", time.Date(2024, 9, 5, 4, 59, 25, 361000000, time.UTC)},
		{"2024-09-09T06:01:58.687Z", time.Date(2024, 9, 9, 6, 1, 58, 687000000, time.UTC)},
		{"2026-04-17T00:00:00Z", time.Date(2026, 4, 17, 0, 0, 0, 0, time.UTC)},
	}
	for _, m := range measured {
		got, ok := parseLinearCSVTime(m.raw)
		if !ok {
			t.Errorf("a real Linear export's Updated value %q is not accepted by any pinned layout", m.raw)
			continue
		}
		if !got.Equal(m.want) {
			t.Errorf("parseLinearCSVTime(%q) = %s, want %s", m.raw, got, m.want)
		}
	}
}

// TestLinearCSVUpdated_Rule2_TheMeasuredLIMIT — the 25% of the corpus the pinned layouts REFUSE,
// pinned as a fact rather than left as prose.
//
// ⚠ THIS IS A LIMIT, NOT A BUG, AND THE DISTINCTION IS THE POINT. 746 of 2,947 real `Updated`
// cells — from SIX unrelated owners — are `Sun May 11 2025 07:43:48 GMT+0000 (GMT)`, JavaScript's
// Date.toString. Whether Linear emits that or those repositories re-serialised the export is NOT
// decidable from here, so the layout list is left exactly as #89 pinned it. What this test pins is
// that such a value is REFUSED, and therefore REPORTED by the caller, rather than silently becoming
// a guess. If someone later widens linearCSVTimeLayouts on better provenance, this test is where
// they will find the measurement that says what widening it would change.
func TestLinearCSVUpdated_Rule2_TheMeasuredShapeTheLayoutsRefuse(t *testing.T) {
	for _, raw := range []string{
		"Fri Apr 17 2026 04:00:00 GMT+0000 (GMT+00:00)",
		"Fri Feb 06 2026 10:01:29 GMT+0000 (GMT)",
	} {
		if _, ok := parseLinearCSVTime(raw); ok {
			t.Errorf("parseLinearCSVTime accepts %q — the pinned list has been widened, and the "+
				"25.3%% of the measured corpus that this test records as REFUSED is now a stale "+
				"number in linear_csv_updated.go's header", raw)
		}
	}
}

// TestLinearCSVUpdated_TheColumnSpellingIsPinned — the header string, by hand. Measured in 44 of 45
// real exports and in all six header shapes; a rename in the constant must fail here rather than
// silently send every import down the no-column branch.
func TestLinearCSVUpdated_TheColumnSpellingIsPinned(t *testing.T) {
	if linearCSVUpdatedColumn != "Updated" {
		t.Errorf("linearCSVUpdatedColumn = %q, want %q — the spelling measured on 44 of 45 real exports",
			linearCSVUpdatedColumn, "Updated")
	}
}

// ─── the four outcomes ─────────────────────────────────────────────────────
func TestLinearCSVUpdated_TheFourOutcomes(t *testing.T) {
	t.Run("a value the layouts accept lands and says nothing", func(t *testing.T) {
		ci, row := buildIndex([]string{"Title", "Updated"}), []string{"a", "2024-09-05T04:59:25.361Z"}
		got, notes := linearCSVUpdated(ci, row)
		if len(notes) != 0 {
			t.Errorf("notes = %+v, want none", notes)
		}
		if want := time.Date(2024, 9, 5, 4, 59, 25, 361000000, time.UTC); !got.Equal(want) {
			t.Errorf("got %s, want %s", got, want)
		}
	})

	t.Run("no Updated column at all is its own report", func(t *testing.T) {
		ci, row := buildIndex([]string{"Title", "Status"}), []string{"a", "Todo"}
		got, notes := linearCSVUpdated(ci, row)
		if !got.IsZero() {
			t.Errorf("got %s, want the zero time", got)
		}
		if len(notes) != 1 || notes[0].Via != viaNoLinearUpdatedColumn {
			t.Fatalf("notes = %+v, want exactly one %s", notes, viaNoLinearUpdatedColumn)
		}
	})

	t.Run("an empty cell under a present header is a different report", func(t *testing.T) {
		ci, row := buildIndex([]string{"Title", "Updated"}), []string{"a", ""}
		_, notes := linearCSVUpdated(ci, row)
		if len(notes) != 1 || notes[0].Via != viaNoUpdatedValue {
			t.Fatalf("notes = %+v, want exactly one %s", notes, viaNoUpdatedValue)
		}
	})

	t.Run("a shape no pinned layout accepts is REPORTED, never defaulted", func(t *testing.T) {
		const js = "Fri Feb 06 2026 10:01:29 GMT+0000 (GMT)"
		ci, row := buildIndex([]string{"Title", "Updated"}), []string{"a", js}
		got, notes := linearCSVUpdated(ci, row)
		if !got.IsZero() {
			t.Errorf("got %s, want the zero time — an unparseable value must not become a guess", got)
		}
		if len(notes) != 1 || notes[0].Via != viaUnparseableDate {
			t.Fatalf("notes = %+v, want exactly one %s", notes, viaUnparseableDate)
		}
		if notes[0].Value != js {
			t.Errorf("the note drops the provider's value; got %q", notes[0].Value)
		}
	})

	t.Run("the header lookup is case-insensitive, like every other column", func(t *testing.T) {
		ci, row := buildIndex([]string{"Title", "updated"}), []string{"a", "2024-09-09T06:01:58.687Z"}
		_, notes := linearCSVUpdated(ci, row)
		if len(notes) != 0 {
			t.Errorf("notes = %+v, want none — buildIndex lowercases both sides", notes)
		}
	})
}

// TestLinearCSVUpdated_TheTwoAbsencesRenderDifferently — the reason the two Via constants are kept
// apart at all. If they rendered the same sentence an operator could not tell "this export has no
// Updated column" from "these rows had empty cells", and #73's structural-zero defence would be
// decoration.
//
// ⚠ WHAT IS DELIBERATELY *NOT* ASSERTED, because it would be a false guard: that the LINEAR
// no-column sentence differs from the JIRA one. Both providers spell the column "Updated", so the
// two sentences are byte-identical TODAY by construction. The constants are separate so the two
// spellings can move apart without dragging each other — the same argument linear_csv_dates.go
// makes for Created — and what IS checkable is that they are distinct constants naming this
// provider's own column.
func TestLinearCSVUpdated_TheTwoAbsencesRenderDifferently(t *testing.T) {
	noColumn := FieldNote{Field: fieldUpdated, Via: viaNoLinearUpdatedColumn}.render(3)
	noValue := FieldNote{Field: fieldUpdated, Via: viaNoUpdatedValue}.render(3)
	if noColumn == noValue {
		t.Fatalf("both absences render the same sentence: %q", noColumn)
	}
	for _, s := range []string{noColumn, noValue} {
		if strings.TrimSpace(s) == "" {
			t.Fatalf("an absence rendered as the empty string — it would vanish from the report")
		}
	}
	if !strings.Contains(noColumn, linearCSVUpdatedColumn) {
		t.Errorf("the no-column sentence does not name %q: %q", linearCSVUpdatedColumn, noColumn)
	}
	if viaNoLinearUpdatedColumn == viaNoUpdatedColumn {
		t.Errorf("viaNoLinearUpdatedColumn and viaNoUpdatedColumn are the same string, so the two " +
			"providers' structural-zero lines can never be told apart in a report")
	}
	// The branch must be REACHED. Falling through to the generic default renders the anonymous
	// "no <field> value on N issue(s)" subject, which names no column at all — that is what this
	// compares against, so a deleted case arm cannot pass.
	generic := FieldNote{Field: fieldUpdated, Via: "some-via-no-branch-handles"}.render(3)
	if noColumn == generic {
		t.Errorf("the no-column note renders the generic subject %q — its switch arm is not being "+
			"reached, so the sentence naming the column never appears", generic)
	}
}
