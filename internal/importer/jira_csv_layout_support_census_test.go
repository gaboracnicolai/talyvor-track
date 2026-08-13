package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// jira_csv_layout_support_census_test.go — jira_csv_dates.go says of jiraCSVTimeLayouts that "each
// entry carries the number of cells IN THE CORPUS that it is the first to accept, so no line here is
// a guess", and that three shapes were dropped for scoring zero. That was a claim checked by hand at
// one merge. This census re-derives it, and turns the load-bearing half into an assertion.
//
// ⚠ THE UNIT IS A DISTINCT EXPORT, NOT A CELL, AND THAT IS THE POINT OF THIS FILE. The counts in
// jira_csv_dates.go were taken over cache entries, and the cache holds byte-identical copies of the
// same export (see jira_csv_two_digit_year.go). A layout supported by 3,000 cells from one file
// copied three times has ONE instance behind it, and "the CSV date format is a per-instance
// preference" is the sentence this whole line of work exists to answer.
//
// ⚠ IT IS NOT A CI GUARD, for the reason every corpus census here says so: the corpus is a directory
// in /tmp on one machine and CI has no such directory, so in CI this SKIPS. jira_csv_dates_test.go
// pins the list itself and runs everywhere.
func TestJiraCSVLayoutSupport_EveryPinnedLayoutHasADistinctExportBehindIt(t *testing.T) {
	ents, err := os.ReadDir(jiraCSVDateCorpusDir)
	if os.IsNotExist(err) {
		t.Skipf("no corpus at %s — this census SKIPS in CI by design; jira_csv_dates_test.go pins "+
			"the layout list itself and runs everywhere.", jiraCSVDateCorpusDir)
	}
	if err != nil {
		t.Fatalf("read %s: %v", jiraCSVDateCorpusDir, err)
	}
	distinct, err := distinctByContent(jiraCSVDateCorpusDir)
	if err != nil {
		t.Fatalf("distinctByContent: %v", err)
	}

	cells := make([]int, len(jiraCSVTimeLayouts))
	cellsDistinct := make([]int, len(jiraCSVTimeLayouts))
	exports := make([]map[string]bool, len(jiraCSVTimeLayouts))
	for i := range exports {
		exports[i] = map[string]bool{}
	}
	files := 0
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		rows, err := readCorpusCSV(filepath.Join(jiraCSVDateCorpusDir, e.Name()))
		if err != nil || len(rows) < 2 {
			continue
		}
		have := map[string]bool{}
		for _, h := range rows[0] {
			have[strings.ToLower(strings.TrimSpace(h))] = true
		}
		if !have["summary"] || !have["issue key"] || !have["status"] {
			continue
		}
		files++
		first := distinct[e.Name()]
		ci := buildIndex(rows[0])
		for _, col := range jiraCSVDateCorpusColumns {
			for _, r := range rows[1:] {
				v := strings.TrimSpace(ci.get(r, col))
				if v == "" {
					continue
				}
				for i, layout := range jiraCSVTimeLayouts {
					// FIRST to accept, exactly as jira_csv_dates.go defines the counts it carries.
					if _, err := time.Parse(layout, v); err != nil {
						continue
					}
					cells[i]++
					if first {
						cellsDistinct[i]++
						exports[i][e.Name()] = true
					}
					break
				}
			}
		}
	}
	if files == 0 {
		t.Fatalf("%s exists but yielded ZERO genuine Jira exports — a census whose instrument read "+
			"nothing must not report a clean answer", jiraCSVDateCorpusDir)
	}

	for i, layout := range jiraCSVTimeLayouts {
		t.Logf("%-22q %6d cells (%6d distinct) from %3d distinct exports",
			layout, cells[i], cellsDistinct[i], len(exports[i]))
	}
	// The assertion, at zero and not at a floor: jira_csv_dates.go's own rule is that a layout with
	// no bytes behind it must not be in this list, and a layout whose only bytes are a copy of
	// another layout's file is not a second instance either. A floor would be a second calibrated
	// number to maintain; zero is the rule the file already states.
	for i, layout := range jiraCSVTimeLayouts {
		if len(exports[i]) == 0 {
			t.Errorf("layout %q is the first to accept %d cells in the cache but is supported by "+
				"ZERO distinct exports — jira_csv_dates.go's own rule is that an entry with no bytes "+
				"behind it does not belong in the list", layout, cells[i])
		}
	}
}
