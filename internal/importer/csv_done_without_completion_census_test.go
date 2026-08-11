package importer

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/talyvor/track/internal/model"
)

// csv_done_without_completion_census_test.go — the measurement behind csv_done_without_completion.go.
//
// ⚠⚠ IT IS NOT A CI GUARD AND MUST NOT BE READ AS ONE, the same rule and the same wording as
// jira_csv_corpus_census_test.go: the corpora are directories in /tmp on one machine, CI has none,
// so in CI this SKIPS. A skipping test proves nothing — which is why the skip is narrow and loud
// (it fires only when the DIRECTORY is absent) and why a corpus that exists but yields no rows is a
// FAILURE. A census whose instrument read nothing must not be able to report a clean answer.
//
// ⚠ EVERY ROW GOES THROUGH THE SHIPPED newCSVSource, not through a reader configured here. An
// earlier draft of this probe set LazyQuotes on its own csv.Reader; the product does not, so that
// instrument was reading rows the importer refuses and its counts were about a file the importer
// never sees. The classification comes from the SHIPPED FieldNotes, so the census cannot disagree
// with the code it is quoted in.
const (
	linearCorpusDir = "/tmp/w34-linear-corpus-cache"
	jiraCorpusDir   = "/tmp/w34-jira-corpus"
)

func censusDoneWithoutCompletion(t *testing.T, dir string, mapper rowMapper, viaColumn, viaValue string) map[string]int {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		t.Skipf("no corpus at %s — this census SKIPS off the machine that holds it and is therefore "+
			"NOT a regression guard", dir)
	}
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	tally := map[string]int{}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		f, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("open %s: %v", e.Name(), err)
		}
		src, err := newCSVSource(f, mapper)
		if err != nil {
			f.Close()
			tally["file: header unreadable"]++
			continue
		}
		tally["files"]++
		for {
			row, ok := src.Next()
			if !ok {
				break
			}
			tally["rows"]++
			if row.Err != nil {
				tally["rows refused"]++
				continue
			}
			if row.Issue.Status != model.StatusDone {
				continue
			}
			tally["done"]++
			if row.Issue.CompletedAt != nil {
				tally["done, completion recorded"]++
				continue
			}
			seen := ""
			for _, n := range row.Notes {
				switch n.Via {
				case viaColumn:
					seen = "done, NULL — no completion column"
				case viaValue:
					seen = "done, NULL — empty cell"
				case viaUnparseableDate:
					if n.Field == fieldResolutionDate || n.Field == fieldCompletionTime {
						seen = "done, NULL — value refused"
					}
				case viaStatusNotDone:
					seen = "done, NULL — status-not-done"
				}
			}
			if seen == "" {
				seen = "done, NULL — SILENT"
			}
			tally[seen]++
		}
		f.Close()
	}
	if tally["rows"] == 0 {
		t.Fatalf("%s yielded ZERO rows — a census whose instrument read nothing must not report a clean answer", dir)
	}
	keys := make([]string, 0, len(tally))
	for k := range tally {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("  %-38s %d\n", k, tally[k])
	}
	return tally
}

// THE FINDING, AS A NUMBER: after this merge, NO done issue reaches Postgres with a NULL completion
// time and no explanation. Before it, 2,425 of 7,186 Jira done rows and 34 of 1,153 Linear ones did.
func TestDoneWithoutCompletionCensus_Jira(t *testing.T) {
	tally := censusDoneWithoutCompletion(t, jiraCorpusDir, jiraRowMapper, viaNoResolvedColumn, viaNoResolvedValue)
	if got := tally["done, NULL — SILENT"]; got != 0 {
		t.Errorf("%d done issue(s) still import with a NULL completion time and no note", got)
	}
	if tally["done"] < 1000 {
		t.Fatalf("only %d done rows — the corpus is not the one these numbers were taken from", tally["done"])
	}
}

func TestDoneWithoutCompletionCensus_Linear(t *testing.T) {
	tally := censusDoneWithoutCompletion(t, linearCorpusDir, linearRowMapper, viaNoLinearCompletedColumn, viaNoLinearCompletedValue)
	if got := tally["done, NULL — SILENT"]; got != 0 {
		t.Errorf("%d done issue(s) still import with a NULL completion time and no note", got)
	}
	if tally["done"] < 500 {
		t.Fatalf("only %d done rows — the corpus is not the one these numbers were taken from", tally["done"])
	}
}
