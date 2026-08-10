package importer

import (
	"encoding/csv"
	"os"
	"regexp"
	"sort"
)

// jira_csv_date_corpus_helpers_test.go — the two helpers the date corpus census needs.

// readCorpusCSV reads a corpus file with THE SAME csv settings newCSVSource uses, so a file the
// product would parse one way is never censused another way.
func readCorpusCSV(path string) ([][]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	rd := csv.NewReader(skipUTF8BOM(f))
	rd.FieldsPerRecord = -1
	rd.TrimLeadingSpace = true
	return rd.ReadAll()
}

var (
	corpusDigits  = regexp.MustCompile(`[0-9]`)
	corpusLetters = regexp.MustCompile(`[A-Za-z]`)
)

// dateShape reduces a cell to a digit/letter skeleton, so thousands of distinct instants collapse
// to the handful of FORMATS that is the actual unit of this census.
//
// ⚠ THE DIGIT PLACEHOLDER IS `9` AND NOT A LETTER, WHICH IS NOT A STYLE CHOICE. Written the obvious
// way — digits to "N", then letters to "A" — the second pass rewrites the FIRST pass's own output,
// because "N" is a letter. Every shape comes out all-A, digits and month names become
// indistinguishable, and the caller's "does this shape contain a month NAME" test is then true of
// `22/08/2024`. That is a predicate that reads its own instrument's artefact rather than the input.
func dateShape(s string) string {
	return corpusLetters.ReplaceAllString(corpusDigits.ReplaceAllString(s, "9"), "A")
}

func topShapes(m map[string]int, n int) []string {
	type kv struct {
		k string
		v int
	}
	var ks []kv
	for k, v := range m {
		ks = append(ks, kv{k, v})
	}
	sort.Slice(ks, func(i, j int) bool { return ks[i].v > ks[j].v })
	var out []string
	for i, e := range ks {
		if i >= n {
			break
		}
		out = append(out, e.k)
	}
	return out
}
