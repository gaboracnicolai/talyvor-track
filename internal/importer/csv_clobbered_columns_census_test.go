package importer

import (
	"encoding/json"
	"os"
	"testing"
)

// csv_clobbered_columns_census_test.go — the half of the clobbered-column numbers that DECIDES
// anything. scripts/w34-csv-clobbered-columns-probe.py writes out the raw header of every cached
// real Jira export and stops; this asks `columnIndex.has` — THE SHIPPED PREDICATE, through the
// SHIPPED buildIndex — which of them carry the two columns a re-import overwrites.
//
// ⚠⚠ IT IS NOT A CI GUARD AND SAYS SO, the same way jira_csv_corpus_census_test.go does. The corpus
// is in /tmp on one machine, so this SKIPS in CI, and the skip is narrow on purpose: an ABSENT
// extract skips, an extract that exists and is empty or unparseable FAILS. A census whose instrument
// read nothing must not be able to report a clean answer.
//
// ⚠ WHY PIN THE NUMBERS. csv_clobbered_columns.go's header states two proportions about real
// exports, and a stated number is not a measurement. With this file anyone holding the corpus can
// falsify them in one command.
const corpusClobberedHeaders = "/tmp/w34-clobbered-column-headers.json"

// loadClobberedHeaders is a DUPLICATE of loadCensus and not a call to it, with one line different:
// the skip names THIS probe. Reusing the neighbour would have sent whoever hits the skip to
// w34-jira-csv-status-category-probe.py, which does not write this extract — a helper lends its
// provenance to every caller, and the borrowed sentence would have been false here.
func loadClobberedHeaders(t *testing.T, into any) {
	t.Helper()
	b, err := os.ReadFile(corpusClobberedHeaders)
	if os.IsNotExist(err) {
		t.Skipf("no extract at %s — run scripts/w34-csv-clobbered-columns-probe.py (it needs "+
			"#103's cache at /tmp/w34-jira-corpus). This test SKIPS in CI by design and is "+
			"therefore not a regression guard.", corpusClobberedHeaders)
	}
	if err != nil {
		t.Fatalf("read %s: %v", corpusClobberedHeaders, err)
	}
	if err := json.Unmarshal(b, into); err != nil {
		t.Fatalf("parse %s: %v — an extract that exists must be readable, or the census would "+
			"report a clean answer from an instrument that read nothing", corpusClobberedHeaders, err)
	}
}

func TestJiraCSVCorpus_ClobberedColumnPresence(t *testing.T) {
	var headers map[string][]string
	loadClobberedHeaders(t, &headers)
	if len(headers) == 0 {
		t.Fatalf("the extract parsed to zero exports — an instrument that read nothing must not " +
			"be able to report a clean answer")
	}

	// PREMISE, asserted rather than assumed: this is the population the probe's N2 counted. A census
	// that silently ran against a different corpus would report numbers true of nothing.
	if len(headers) != 305 {
		t.Fatalf("the extract holds %d exports, not the 305 the probe's N2 pins — every proportion "+
			"below would be about a different set", len(headers))
	}

	var noDescription, noLabels, noBoth int
	for _, h := range headers {
		ci := buildIndex(h) // THE SHIPPED INDEX, not a transcription of it
		d, l := !ci.has(clobberedDescriptionColumn), !ci.has(clobberedLabelsColumn)
		if d {
			noDescription++
		}
		if l {
			noLabels++
		}
		if d && l {
			noBoth++
		}
	}

	// The two figures csv_clobbered_columns.go's header quotes, hardcoded rather than recomputed
	// from the constants they check — a guard that compares a value to itself passes for every value.
	if noLabels != 203 {
		t.Errorf("exports with no %q column = %d of %d, header says 203", clobberedLabelsColumn, noLabels, len(headers))
	}
	if noDescription != 16 {
		t.Errorf("exports with no %q column = %d of %d, header says 16", clobberedDescriptionColumn, noDescription, len(headers))
	}

	// A FLOOR ON THE INSTRUMENT ITSELF, and it is the inverted half: if `has` ever answered false
	// for everything (control C4's mutation, shipped by accident) both counts above would be 305 and
	// the two assertions would simply be wrong rather than obviously broken. This says the predicate
	// still discriminates.
	if noDescription >= len(headers) || noLabels >= len(headers) {
		t.Fatalf("columnIndex.has found NO export carrying either column (%d/%d, %d/%d) — the "+
			"predicate is not reading these headers at all", noDescription, len(headers), noLabels, len(headers))
	}
	t.Logf("305 real Jira exports: no %q %d (%.1f%%) · no %q %d (%.1f%%) · neither %d",
		clobberedLabelsColumn, noLabels, 100*float64(noLabels)/float64(len(headers)),
		clobberedDescriptionColumn, noDescription, 100*float64(noDescription)/float64(len(headers)), noBoth)
}
