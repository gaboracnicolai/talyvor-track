package importer

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

// linear_csv_date_cells_census_test.go — applies THE SHIPPED parseLinearCSVTime to every distinct
// date cell scripts/w34-linear-csv-updated-probe.py extracted from 45 real Linear CSV exports.
//
// ⚠⚠ IT EXISTS BECAUSE THE PROBE ALREADY TOLD FOUR SESSIONS TO RUN IT AND IT HAD NEVER BEEN
// WRITTEN. The probe's last line of output reads "-> run TestRealLinearExportDateCellsParse to
// apply the REAL parseLinearCSVTime to them"; `grep -rn TestRealLinearExportDateCellsParse` over
// the whole repository at b45a39b matched exactly one line — that print statement. An instruction
// naming a test that does not exist reads like coverage from the outside, which is the shape this
// item keeps finding one layer down. The name below is the one the probe prints.
//
// ⚠⚠ IT IS NOT A CI GUARD AND MUST NOT BE READ AS ONE, for the same reason
// jira_csv_corpus_census_test.go says so: the extract is a file in /tmp on one machine and CI has
// no such file, so in CI this SKIPS. A skipping test proves nothing — which is exactly why the skip
// is narrow and loud. It fires ONLY when the extract is absent; an extract that exists and is empty
// is a FAILURE, because a census whose instrument read nothing must not report a clean answer.
//
// ⚠ WHAT IT PINS IS THE SHAPE OF THE REMAINDER, NOT A PASS MARK. After this merge the parser
// accepts every date-shaped cell in the corpus and refuses exactly the 8 header rows that leaked
// into the data — so the assertion is that every refusal is a HEADER NAME, and any new refusal
// names itself in the failure message. That is what makes it re-runnable evidence for
// linear_csv_tostring_dates.go's numbers rather than a green tick.
const linearCSVDateCellsExtract = "/tmp/w34-linear-csv-date-cells.txt"

// linearExportDateColumnNames are the eight column headers the probe reads. A header row that
// leaked into a file's data rows arrives as a cell whose value is one of these — 8 of the 5,440
// distinct cells, from one owner (ray-abhishek), and they are correctly refused: a column called
// "Created" is not an instant.
var linearExportDateColumnNames = map[string]bool{
	"Created": true, "Updated": true, "Started": true, "Triaged": true,
	"Completed": true, "Canceled": true, "Archived": true, "Due Date": true,
}

func TestRealLinearExportDateCellsParse(t *testing.T) {
	f, err := os.Open(linearCSVDateCellsExtract)
	if os.IsNotExist(err) {
		t.Skipf("no corpus extract at %s — run scripts/w34-linear-csv-updated-probe.py (it needs "+
			"an authenticated `gh`). This test SKIPS in CI by design; see the file comment.",
			linearCSVDateCellsExtract)
	}
	if err != nil {
		t.Fatalf("open %s: %v", linearCSVDateCellsExtract, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	var accepted int
	var refusedNonHeader []string
	var refusedHeaders int
	for sc.Scan() {
		cell := strings.TrimSpace(sc.Text())
		if cell == "" {
			continue
		}
		if _, ok := parseLinearCSVTime(cell); ok {
			accepted++
			continue
		}
		if linearExportDateColumnNames[cell] {
			refusedHeaders++
			continue
		}
		refusedNonHeader = append(refusedNonHeader, cell)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read %s: %v", linearCSVDateCellsExtract, err)
	}

	// THE FLOOR. An empty or truncated extract parses to zero cells, which under the assertions
	// below would score a clean sweep. Zero is "the instrument read nothing", not "everything
	// parsed" — the same distinction claimable.py's ERROR state exists for, one repository over.
	total := accepted + refusedHeaders + len(refusedNonHeader)
	if total < 1000 {
		t.Fatalf("the extract yielded only %d cells — it is empty or truncated, not clean. "+
			"Re-run scripts/w34-linear-csv-updated-probe.py.", total)
	}

	if len(refusedNonHeader) > 0 {
		shown := refusedNonHeader
		if len(shown) > 10 {
			shown = shown[:10]
		}
		t.Errorf("parseLinearCSVTime refused %d of %d real cells that are NOT a leaked header row.\n"+
			"first %d: %q\n\n"+
			"Every refusal defaults `issues.created_at`/`updated_at` to the import instant, which is "+
			"a plausible timestamp rather than a null — so this is the only place the loss is visible.",
			len(refusedNonHeader), total, len(shown), shown)
	}
	t.Logf("census: %d cells · accepted %d · refused %d (all leaked header rows)",
		total, accepted, refusedHeaders)
}
