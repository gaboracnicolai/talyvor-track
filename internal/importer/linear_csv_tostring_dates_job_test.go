package importer_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/testutil"
)

// linear_csv_tostring_dates_job_test.go — the DATE SERIALISATION a quarter of real Linear CSV
// exports use, driven END TO END through the async runner on real Postgres.
//
// ⚠⚠ THE COLUMN WORK IS DONE AND THE PARSER IS WHAT DROPS THE VALUE. #100 taught linearRowMapper
// to read `Updated`, and #89 taught it `Created`/`Completed`. Both merges land the cell on
// parseLinearCSVTime, whose pinned list is TWO layouts — "2006-01-02" and time.RFC3339. Measured
// against 45 real Linear CSV exports that unrelated tenants committed to public repositories
// (scripts/w34-linear-csv-updated-probe.py, three negative controls first, re-run at b45a39b):
//
//	Created  2,990 non-empty cells   ISO+ms 2,195 · toString 746 · ISO 43 · header leak 6
//	Updated  2,947 non-empty cells   ISO+ms 2,195 · toString 746 ·           header leak 6
//
// 746 of 2,947 `Updated` cells (25.3%) and 746 of 2,990 `Created` cells (24.9%) are ECMAScript's
// Date.prototype.toString shape, which NEITHER pinned layout accepts. So on a quarter of the real
// corpus #89's and #100's merges are INERT: the row imports, the column is not written, and the
// DEFAULT NOW() lands the import instant — the exact loss those two merges were written to stop.
//
// ⚠ THE PROVENANCE IS SIX OWNERS WHO HAVE NEVER MET, which is what makes second-hand bytes
// evidence. `(GMT)` — 454 cells, 4 owners (AlexanderJson, JocoBorghol, kkoocheki, wubin28), header
// widths 30 and 34. `(GMT+00:00)` — 292 cells, 2 owners (gong8, kapishdima), header width 34. The
// ISO shape is 8 owners across widths 29/30/34, so this is not one toolchain against the field.
//
// ⚠ WHAT IS NOT CLAIMED: that Linear's own web export emits this. All 746 carry offset GMT+0000
// and no bare (parenthetical-free) toString appears, which is consistent with a script that read
// Linear's API and re-serialised with a JS Date under TZ=UTC. It is still a file a user HAS and
// uploads, and the instant it carries is unambiguous — the numeric offset is authoritative and the
// parenthetical is ECMA-262's implementation-defined zone NAME, which carries nothing the offset
// does not. That is the whole of the argument for reading it.
//
// ⚠ AND NO REAL CELL EXERCISES A NON-ZERO OFFSET: 734 of 734 distinct toString cells are GMT+0000.
// The layout reads the offset either way and the unit test covers a non-zero one, but the corpus
// does not, and that is stated rather than implied.
//
// ⚠ WHY A COLUMN ASSERTION IS THE WRONG INSTRUMENT ALONE, inherited from #83/#85/#100: both
// `issues.created_at` and `issues.updated_at` are TIMESTAMPTZ DEFAULT NOW(), so a dropped value is
// not a null anybody can spot — it is a plausible timestamp shaped exactly like a correct one.
// The warnings assertion below is the half that says WHY it was dropped.

const (
	// HARDCODED AS THE CORPUS'S BYTES, not built from the layout the fix adds. A fixture that
	// formats with the same constant the code parses with compares the constant to itself and
	// passes for every possible value — including a wrong one. Both variants are real cells:
	//
	//	Fri Feb 06 2026 10:01:29 GMT+0000 (GMT)         wubin28/book-vibe-coding-in-action
	//	Fri Apr 17 2026 04:00:00 GMT+0000 (GMT+00:00)   gong8 / kapishdima
	//
	// The trailing " GMT+0000 (…)" here is LITERAL layout text, not Go's zone form, which is what
	// keeps these strings independent of anything linear_csv_tostring_dates.go declares.
	toStringGMTNameLayout   = "Mon Jan 02 2006 15:04:05 GMT+0000 (GMT)"
	toStringOffsetNameLayou = "Mon Jan 02 2006 15:04:05 GMT+0000 (GMT+00:00)"

	// Days back, computed rather than written down: a hardcoded date ages out of every analytics
	// window and the test stops testing anything while staying green.
	toStringCreatedDaysAgo = 300
	toStringUpdatedDaysAgo = 200

	// A shape NO measured export carries and no layout should ever accept. It is the must-stay-
	// green half: the fix narrows nothing, so a genuinely unknown serialisation must still be
	// refused AND reported rather than silently defaulted.
	toStringUnknownDateCell = "15/01/2026 10:23"
)

// toStringDatesFixture returns a THREE-row Linear export:
//
//	LIN-1  both dates in the `(GMT)` variant
//	LIN-2  both dates in the `(GMT+00:00)` variant
//	LIN-3  a date shape no export in the corpus carries — the must-stay-green row
//
// The two variants are SEPARATE ROWS rather than one row with one of them, because a single fixture
// carrying only one variant cannot tell a fix that handles the parenthetical generically from one
// that pins the four bytes "(GMT)".
func toStringDatesFixture() (body string, created, updated time.Time) {
	now := time.Now().UTC()
	// Truncate to the second: the toString shape carries no fractional part, so a round trip
	// loses anything finer and an equality assertion would fail for a reason unrelated to the
	// finding.
	created = now.Add(-toStringCreatedDaysAgo * 24 * time.Hour).Truncate(time.Second)
	updated = now.Add(-toStringUpdatedDaysAgo * 24 * time.Hour).Truncate(time.Second)
	body = "ID,Team,Title,Description,Status,Priority,Labels,Created,Updated,Completed\n" +
		fmt.Sprintf("LIN-1,Eng,widget gmt-name variant,d,In Progress,High,,%s,%s,\n",
			created.Format(toStringGMTNameLayout), updated.Format(toStringGMTNameLayout)) +
		fmt.Sprintf("LIN-2,Eng,widget offset-name variant,d,In Progress,High,,%s,%s,\n",
			created.Format(toStringOffsetNameLayou), updated.Format(toStringOffsetNameLayou)) +
		fmt.Sprintf("LIN-3,Eng,widget unknown shape,d,In Progress,High,,%s,%s,\n",
			toStringUnknownDateCell, toStringUnknownDateCell)
	return body, created, updated
}

// runToStringDatesImport drives the fixture through the SHIPPED async path — JobStore.Create, then
// Runner.RunOnce — so the assertions below are about the transport a user reaches, not about a
// mapper called directly. Returns the workspace and the finished job row.
func runToStringDatesImport(t *testing.T, d *testutil.DB, body string) (wsID string, job *importer.Job) {
	t.Helper()
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	js := importer.NewJobStore(d.Pool)
	runner := importer.NewRunner(js, importer.New(issue.NewStore(d.Pool)))
	jobID, err := js.Create(ctx, ws.ID, team.ID, "linear_csv", []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if did, err := runner.RunOnce(ctx); err != nil || !did {
		t.Fatalf("RunOnce did=%v err=%v", did, err)
	}
	job, err = js.Get(ctx, jobID)
	if err != nil || job == nil {
		t.Fatalf("read job row: job=%v err=%v", job, err)
	}
	return ws.ID, job
}

// readDates returns (created_at, updated_at) for one identifier. A missing row is fatal HERE rather
// than through a zero-value comparison downstream, so "the row never landed" can never be read as
// "the timestamp is wrong".
func readDates(t *testing.T, d *testutil.DB, wsID, identifier string) (created, updated time.Time) {
	t.Helper()
	err := d.Pool.QueryRow(context.Background(),
		`SELECT created_at, updated_at FROM issues WHERE workspace_id = $1 AND identifier = $2`,
		wsID, identifier).Scan(&created, &updated)
	if err != nil {
		t.Fatalf("read dates for %s: %v", identifier, err)
	}
	return created.UTC(), updated.UTC()
}

func assertInstant(t *testing.T, label string, got, want time.Time) {
	t.Helper()
	if delta := got.Sub(want); delta > time.Minute || delta < -time.Minute {
		t.Errorf("%s = %s, want %s — off by %s.\n"+
			"The cell is ECMAScript Date.prototype.toString, which 746 of 2,947 real `Updated` "+
			"cells (25.3%%, six unrelated owners) use. parseLinearCSVTime pins only \"2006-01-02\" "+
			"and RFC3339, so the value is refused and the column takes its DEFAULT NOW() — the "+
			"import instant. #89 and #100 read these columns; on a quarter of the real corpus "+
			"they land nothing.",
			label, got.Format(time.RFC3339), want.Format(time.RFC3339), delta)
	}
}

// TestJobRow_LinearCSV_ToStringDatesLandOnTheirColumns is the column half, asserted on BOTH
// measured zone-name variants and on BOTH date columns — four independent assertions, so a fix
// that reaches one variant and not the other cannot pass.
func TestJobRow_LinearCSV_ToStringDatesLandOnTheirColumns(t *testing.T) {
	d := testutil.New(t)
	body, created, updated := toStringDatesFixture()
	wsID, _ := runToStringDatesImport(t, d, body)

	gotCreated, gotUpdated := readDates(t, d, wsID, "LIN-1")
	assertInstant(t, `LIN-1 created_at ("(GMT)" variant)`, gotCreated, created)
	assertInstant(t, `LIN-1 updated_at ("(GMT)" variant)`, gotUpdated, updated)

	gotCreated, gotUpdated = readDates(t, d, wsID, "LIN-2")
	assertInstant(t, `LIN-2 created_at ("(GMT+00:00)" variant)`, gotCreated, created)
	assertInstant(t, `LIN-2 updated_at ("(GMT+00:00)" variant)`, gotUpdated, updated)
}

// TestJobRow_LinearCSV_ToStringDatesAreNoLongerReportedUnparseable is the half a column read cannot
// do. A defaulted timestamp and a correct one are the same shape in the column; the WARNING is the
// only place the product says which one happened, and today it says "not a date shape this
// importer recognises" for a quarter of every real export's rows.
//
// ⚠ THE ASSERTION IS ANCHORED TO THE CELL BYTES, not to the count of warnings: the fixture's third
// row is DESIGNED to keep producing one, so a test that merely counted would pass on a fix that
// silenced the channel instead of parsing the date.
func TestJobRow_LinearCSV_ToStringDatesAreNoLongerReportedUnparseable(t *testing.T) {
	d := testutil.New(t)
	body, _, _ := toStringDatesFixture()
	_, job := runToStringDatesImport(t, d, body)

	if job.Imported != 3 {
		t.Fatalf("job imported=%d, want 3 — every row of this fixture must land, or the warning "+
			"assertions below are about rows that never arrived (status=%q summary=%q)",
			job.Imported, job.Status, job.ErrorSummary)
	}
	joined := strings.Join(job.Warnings, "\n")
	for _, variant := range []string{"GMT+0000 (GMT)", "GMT+0000 (GMT+00:00)"} {
		if strings.Contains(joined, variant) {
			t.Errorf("the job row still reports a %s cell as unrecognised:\n%s\n\n"+
				"That sentence is correct today and is the defect: the numeric offset makes the "+
				"instant unambiguous, and the parenthetical is ECMA-262's implementation-defined "+
				"zone NAME.", variant, joined)
		}
	}
}

// TestJobRow_LinearCSV_AnUnknownDateShapeIsStillRefusedAndReported is the MUST-STAY-GREEN half, and
// it is what stops the fix from becoming a tolerant date parser.
//
// ⚠ IT IS GREEN BEFORE THE FIX AS WELL, AND THAT IS THE POINT rather than a weakness: it pins the
// property the fix must not break. linear_csv_dates.go's own argument is that the REFUSAL is the
// load-bearing part, not the list — a tenant whose export matches no pinned shape must learn it in
// the warnings channel instead of receiving a column of import-instant timestamps. A normalisation
// that stripped any parenthetical, or a layout that accepted a bare `dd/mm/yyyy`, would take that
// away, and only this row says so.
func TestJobRow_LinearCSV_AnUnknownDateShapeIsStillRefusedAndReported(t *testing.T) {
	d := testutil.New(t)
	body, _, _ := toStringDatesFixture()
	wsID, job := runToStringDatesImport(t, d, body)

	joined := strings.Join(job.Warnings, "\n")
	if !strings.Contains(joined, toStringUnknownDateCell) {
		t.Errorf("no warning names the unknown cell %q. Warnings were:\n%s\n\n"+
			"A date shape this importer does not know must be REPORTED, never silently defaulted.",
			toStringUnknownDateCell, joined)
	}

	// And it must genuinely not be recorded: the row lands with the import instant, which is what
	// the warning is warning about. Asserted so "reported" cannot be satisfied by a fix that
	// reports AND writes some guessed value.
	gotCreated, _ := readDates(t, d, wsID, "LIN-3")
	if time.Since(gotCreated) > time.Hour {
		t.Errorf("LIN-3 created_at = %s — an unparseable cell must fall through to DEFAULT NOW(), "+
			"not to a parsed value", gotCreated.Format(time.RFC3339))
	}
}
