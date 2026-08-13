package importer

import (
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

// jira_csv_date_corpus_helpers_test.go — the helpers the date corpus census needs.

// distinctByContent answers, for one corpus directory, WHICH file names are the first occurrence of
// their byte-content — so a census can count cache entries and distinct exports in the same walk
// and never confuse the two.
//
// ⚠⚠ IT EXISTS BECAUSE THE CACHE KEY IS NOT THE EXPORT. scripts/w34-jira-csv-corpus-probe.py caches
// each fetch under `sha256(repo \t path)` (:105), so THE SAME export committed to two repositories —
// a fork, a vendored copy, a re-upload — lands as two cache entries. The probe's own header says
// what makes second-hand bytes into evidence is "agreement — or disagreement — ACROSS INSTANCES THAT
// HAVE NEVER MET", and two copies of one file have not met anybody.
//
// ⚠ CONTENT, NOT NAME AND NOT SIZE. Two exports from unrelated instances that happen to be the same
// length are different evidence and both are kept; only identical bytes collapse. Byte-identical
// multi-kilobyte CSVs do not arise independently, so identity of content IS identity of export.
// corpus_copies_test.go asserts both directions over a directory it builds, so the rule is checked
// everywhere the suite runs and not only on the machine that holds the corpus.
//
// The survivor is the first name in sorted order, so a census that logs a file name logs the same
// one on every run.
//
// ⚠ THERE IS NO e.IsDir() SKIP AND THAT IS MEASURED, NOT AN OVERSIGHT. The obvious first line of the
// loop was one; control C8 of scripts/w34-corpus-copies-controls.py shows os.ReadFile already
// answers a directory with an error, so the `continue` below is what excludes it and the IsDir
// branch could never be the reason. Two mechanisms for one behaviour means the test naming that
// behaviour pins neither; TestDistinctByContent_SubdirectoriesAreNotExports pins the one that
// actually produces it, and C8 mutates exactly that line.
func distinctByContent(dir string) (map[string]bool, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	seen := make(map[string]bool, len(names))
	first := make(map[string]bool, len(names))
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			continue
		}
		sum := sha256.Sum256(b)
		k := hex.EncodeToString(sum[:])
		if seen[k] {
			continue
		}
		seen[k] = true
		first[n] = true
	}
	return first, nil
}

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
