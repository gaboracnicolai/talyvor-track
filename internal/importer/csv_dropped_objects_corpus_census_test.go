package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// csv_dropped_objects_corpus_census_test.go — the PROVENANCE behind the numbers in
// csv_dropped_objects.go, re-runnable rather than a paragraph. Same contract as the other two
// censuses in this package: it drives THE SHIPPED MAPPER over the cached real corpus, it SKIPS only
// when the directory is absent, and a directory that yields no genuine export is a FAILURE.
//
// ⚠ ITS COLUMN LIST IS ITS OWN AND IS DELIBERATELY NOT jiraObjectColumns — control C5's finding
// from the unread-reference census: both sides of an equality whose subject comes from the thing
// being measured shrink together.
//
// ⚠ AND THE STRONGER-SOUNDING VERSION OF THAT SENTENCE IS NOT TRUE OF THIS FILE, WHICH IS WHY IT IS
// NOT WRITTEN HERE. Control D11 built the derived census — column list taken from jiraObjectColumns,
// the `Time Spent` entry then deleted — and this test went RED anyway, on the PER-COLUMN floor
// below. That is the difference between this census and the issue-link one: its floor is on the
// TOTAL, so a small entry can leave under it (measured there as C12), while these floors name
// `Comment` and `Time Spent` individually. The independent list is what keeps the per-column
// EQUALITY honest; the named floors are what see an entry leave. Neither claim is worth writing
// without the mutation that separates them.
//
// ⚠ AND IT CENSUSES THE TWO EXCLUSIONS TOO, which is the half a per-entry equality cannot express.
// `Σ Time Spent` is populated MORE OFTEN than `Time Spent` and must produce NO note; a rule that
// matched it by substring would look like a bigger, better report and would be counting a parent's
// children twice.
var (
	censusDroppedObjectColumns = []string{"Comment", "Time Spent"}
	censusNeverReportedColumns = []string{"Σ Time Spent", "Σ Original Estimate", "Original estimate",
		"Remaining Estimate", "Work Ratio"}
)

const (
	droppedObjMinFiles       = 300   // 302 genuine exports at this merge
	droppedObjMinRows        = 17000 // 18,807 rows
	droppedObjMinCommentRows = 2000  // 2,317 rows carry a comment
	droppedObjMinTimeRows    = 250   // 283 rows carry logged work
)

func TestDroppedObjectsCorpus_EveryPopulatedCellIsReportedAndTheExclusionsAreSilent(t *testing.T) {
	dir := unreadRefsJiraCorpusDir
	ents, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		t.Skipf("no corpus at %s — this test SKIPS in CI by design; csv_dropped_objects_test.go "+
			"pins the same shapes as literals and runs everywhere.", dir)
	}
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	populated, noted := map[string]int{}, map[string]int{}
	excludedPopulated := map[string]int{}
	files, rows := 0, 0
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		recs, err := readCorpusCSV(filepath.Join(dir, e.Name()))
		if err != nil || len(recs) < 2 {
			continue
		}
		have := map[string]bool{}
		for _, h := range recs[0] {
			have[strings.ToLower(strings.TrimSpace(h))] = true
		}
		if !have["summary"] || !have["status"] {
			continue
		}
		files++
		ci := buildIndex(recs[0])
		for _, row := range recs[1:] {
			rows++
			m, err := jiraRowMapper(ci, row)
			if err != nil {
				continue // refused rows import nothing at all; a different class
			}
			for _, col := range censusDroppedObjectColumns {
				if len(ci.getAll(row, col)) > 0 {
					populated[col]++
				}
			}
			for _, col := range censusNeverReportedColumns {
				if len(ci.getAll(row, col)) > 0 {
					excludedPopulated[col]++
				}
			}
			for _, n := range m.notes {
				if n.Via == viaObjectNotCreated {
					noted[n.Value]++
				}
			}
		}
	}

	if files < droppedObjMinFiles || rows < droppedObjMinRows {
		t.Fatalf("corpus read %d files / %d rows, want at least %d / %d — the corpus is present but "+
			"yielded almost nothing, which is an instrument failure, not a clean answer",
			files, rows, droppedObjMinFiles, droppedObjMinRows)
	}
	if populated["Comment"] < droppedObjMinCommentRows || populated["Time Spent"] < droppedObjMinTimeRows {
		t.Errorf("populations fell: Comment=%d (want ≥%d) Time Spent=%d (want ≥%d) — either the "+
			"corpus changed or the census stopped finding the columns",
			populated["Comment"], droppedObjMinCommentRows, populated["Time Spent"], droppedObjMinTimeRows)
	}
	for _, col := range censusDroppedObjectColumns {
		if noted[col] != populated[col] {
			t.Errorf("%q populated on %d rows but reported on %d — the report and the loss are not "+
				"the same set of rows", col, populated[col], noted[col])
		}
	}
	// THE EXCLUSIONS, ASSERTED RATHER THAN ASSUMED. Each is populated on real rows; none may be
	// reported. A rule widened to `contains("time spent")` passes every check above and fails here.
	for _, col := range censusNeverReportedColumns {
		if excludedPopulated[col] == 0 {
			t.Errorf("NOT ONE row populates %q, so its exclusion is untested — either the corpus "+
				"changed or this census is asking for a column real exports do not carry", col)
		}
		if noted[col] != 0 {
			t.Errorf("%q produced %d dropped-object note(s); it is populated on %d rows and has no "+
				"Track object (or is a roll-up over subtasks)", col, noted[col], excludedPopulated[col])
		}
	}
	t.Logf("files=%d rows=%d | Comment populated=%d reported=%d | Time Spent populated=%d reported=%d",
		files, rows, populated["Comment"], noted["Comment"], populated["Time Spent"], noted["Time Spent"])
	for _, col := range censusNeverReportedColumns {
		t.Logf("   excluded %-22s populated=%6d reported=%d", col, excludedPopulated[col], noted[col])
	}
}
