package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// jira_csv_date_corpus_census_test.go — applies THE SHIPPED parseJiraCSVTime to every non-empty
// cell of the four date columns jiraRowMapper reads, across the real Jira CSV export corpus, so the
// numbers in jira_csv_two_digit_year.go are re-runnable evidence rather than a paragraph.
//
// ⚠ IT IS NOT A CI GUARD AND MUST NOT BE READ AS ONE, for exactly the reason
// jira_csv_corpus_census_test.go says so: the corpus is a directory in /tmp on one machine and CI
// has no such directory, so in CI this SKIPS. jira_csv_two_digit_year_test.go is the half that runs
// everywhere — every shape below is also pinned there as a hardcoded literal.
//
// ⚠ THE SKIP IS NARROW AND LOUD ON PURPOSE. It fires ONLY when the directory is absent. A directory
// that exists but yields zero genuine exports is a FAILURE: an instrument that read nothing must
// not report a clean answer.
const jiraCSVDateCorpusDir = "/tmp/w34-jira-corpus"

// jiraCSVDateCorpusColumns are the four columns jiraRowMapper reads a date out of.
var jiraCSVDateCorpusColumns = []string{"Created", "Updated", "Due Date", "Resolved"}

// The census as measured at this merge. Written as a FLOOR on acceptance and a CEILING on refusal
// rather than as equality: the corpus is a cache that can grow, and a test that pins an exact total
// would go red on a larger sample without anything being wrong. What must not happen is the
// acceptance rate falling back.
const (
	jiraCSVDateCorpusMinFiles    = 300   // 301 genuine exports at this merge
	jiraCSVDateCorpusMinAccepted = 34000 // 34,358 cells accepted after the six added layouts
	jiraCSVDateCorpusMaxRefused  = 6500  // 6,165 remain, and they are the ambiguous day/month class
)

// ⚠⚠ THE THREE ABOVE COUNT CACHE ENTRIES, AND A CACHE ENTRY IS NOT AN EXPORT. The corpus is keyed on
// `sha256(repo \t path)`, so the same export committed to two repositories is two entries; 301
// "genuine exports" are 285 distinct byte-contents, and the 16 surplus copies carry 2,188 of the
// 17,898 data rows. The three below are the same census over DISTINCT EXPORTS — the population the
// probe's own evidentiary claim ("instances that have never met") is about. Both are reported,
// because they answer different questions and the failure mode of this file was that one number was
// being read as the other.
//
// ⚠ THE OLD THREE ARE LEFT EXACTLY AS THEY WERE. They still hold of the cache and moving them is a
// recalibration of every floor in this package at once — seven of the eight corpus censuses here
// have at least one floor ABOVE the deduplicated population, so switching them all is a separate,
// larger decision, measured and written down in the queue rather than taken quietly inside a
// date-census change.
const (
	jiraCSVDateCorpusMinDistinctFiles = 275 // 285 distinct exports at this merge (301 cache entries)
	jiraCSVDateCorpusMinDistinctAcc   = 30000
	// ⚠ 5,200 IS BELOW THE AS-COUNTED 6,165 ON PURPOSE, AND THAT IS WHAT MAKES THIS LINE A GUARD
	// RATHER THAN A RESTATEMENT: a distinctByContent that silently stopped collapsing anything would
	// hand this pass the whole cache and 6,165 > 5,200 reds it. A ceiling a broken instrument can
	// still satisfy would be measuring nothing. 4,827 refused at this merge.
	jiraCSVDateCorpusMaxDistinctRef = 5200
)

func TestJiraCSVDateCorpus_TheShippedParserAcceptsWhatRealExportsEmit(t *testing.T) {
	ents, err := os.ReadDir(jiraCSVDateCorpusDir)
	if os.IsNotExist(err) {
		t.Skipf("no corpus at %s — this test SKIPS in CI by design; see the file comment. "+
			"jira_csv_two_digit_year_test.go pins the same shapes as literals and runs everywhere.",
			jiraCSVDateCorpusDir)
	}
	if err != nil {
		t.Fatalf("read %s: %v", jiraCSVDateCorpusDir, err)
	}
	// The same walk, counted twice: once per cache entry, once per distinct export. One read of one
	// directory, so the two populations cannot drift apart through two different traversals.
	distinct, err := distinctByContent(jiraCSVDateCorpusDir)
	if err != nil {
		t.Fatalf("distinctByContent %s: %v", jiraCSVDateCorpusDir, err)
	}

	var files, accepted, refused int
	var distinctFiles, distinctAccepted, distinctRefused int
	refusedShapes := map[string]int{}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		isFirstOfItsContent := distinct[e.Name()]
		rows, err := readCorpusCSV(filepath.Join(jiraCSVDateCorpusDir, e.Name()))
		if err != nil || len(rows) < 2 {
			continue
		}
		have := map[string]bool{}
		for _, h := range rows[0] {
			have[strings.ToLower(strings.TrimSpace(h))] = true
		}
		// The genuine-export predicate. It is what keeps the ~39 non-Jira CSVs in the cache (ML
		// datasets headed `title_Xi`, `deepseek_fewshot_sp`) out of the denominator.
		if !have["summary"] || !have["issue key"] || !have["status"] {
			continue
		}
		files++
		if isFirstOfItsContent {
			distinctFiles++
		}
		ci := buildIndex(rows[0])
		for _, col := range jiraCSVDateCorpusColumns {
			for _, r := range rows[1:] {
				v := strings.TrimSpace(ci.get(r, col))
				if v == "" {
					continue
				}
				if _, ok := parseJiraCSVTime(v); ok {
					accepted++
					if isFirstOfItsContent {
						distinctAccepted++
					}
				} else {
					refused++
					refusedShapes[dateShape(v)]++
					if isFirstOfItsContent {
						distinctRefused++
					}
				}
			}
		}
	}

	if files == 0 {
		t.Fatalf("%s exists but yielded ZERO genuine Jira exports — a census whose instrument read "+
			"nothing must not report a clean answer", jiraCSVDateCorpusDir)
	}
	if files < jiraCSVDateCorpusMinFiles {
		t.Fatalf("genuine exports = %d cache entries, want >= %d — a different corpus; the numbers in "+
			"jira_csv_two_digit_year.go were measured over 301 entries / 285 distinct exports",
			files, jiraCSVDateCorpusMinFiles)
	}
	t.Logf("cache entries=%d accepted=%d refused=%d (%.1f%% accepted)",
		files, accepted, refused, 100*float64(accepted)/float64(accepted+refused))
	t.Logf("distinct exports=%d accepted=%d refused=%d (%.1f%% accepted) — %d entries were copies",
		distinctFiles, distinctAccepted, distinctRefused,
		100*float64(distinctAccepted)/float64(distinctAccepted+distinctRefused), files-distinctFiles)

	// The deduplicated pass. Its ceiling is the load-bearing line: see the constant's own note.
	if distinctFiles > files {
		t.Fatalf("distinct exports = %d over %d cache entries — the helper returned names the walk "+
			"never saw, so the two counts are not two readings of one directory", distinctFiles, files)
	}
	if distinctFiles < jiraCSVDateCorpusMinDistinctFiles {
		t.Errorf("distinct exports = %d, want >= %d. The deduplicated figures in "+
			"jira_csv_two_digit_year.go were measured over 285.",
			distinctFiles, jiraCSVDateCorpusMinDistinctFiles)
	}
	if distinctAccepted < jiraCSVDateCorpusMinDistinctAcc {
		t.Errorf("accepted over distinct exports = %d cells, want >= %d. The layout list has lost "+
			"ground against the population it was measured on.",
			distinctAccepted, jiraCSVDateCorpusMinDistinctAcc)
	}
	if distinctRefused > jiraCSVDateCorpusMaxDistinctRef {
		t.Errorf("refused over distinct exports = %d cells, want <= %d (as-counted: %d). Either the "+
			"ambiguous remainder grew, or the copies stopped collapsing and this pass is reading the "+
			"whole cache.", distinctRefused, jiraCSVDateCorpusMaxDistinctRef, refused)
	}

	if accepted < jiraCSVDateCorpusMinAccepted {
		t.Errorf("accepted = %d cells, want >= %d. The layout list has lost ground against the "+
			"population it was measured on.", accepted, jiraCSVDateCorpusMinAccepted)
	}
	if refused > jiraCSVDateCorpusMaxRefused {
		t.Errorf("refused = %d cells, want <= %d. Top refused shapes: %v",
			refused, jiraCSVDateCorpusMaxRefused, topShapes(refusedShapes, 8))
	}

	// ⚠ AND THE REMAINDER IS ASSERTED TO BE THE CLASS IT WAS DIAGNOSED AS, not merely small. If a
	// month-NAME shape ever shows up here it is an unambiguous serialisation that could have been
	// added and was not — a different finding from "the day/month order is undecidable", and it
	// must not hide inside the same tolerated total.
	for sh, n := range refusedShapes {
		if strings.Contains(sh, "AAA") && n > 25 {
			t.Errorf("refused shape %q (%d cells) contains a month NAME — that is unambiguous and "+
				"should be a pinned layout, not part of the tolerated ambiguous remainder", sh, n)
		}
	}
}
