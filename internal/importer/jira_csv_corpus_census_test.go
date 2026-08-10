package importer

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/model"
)

// jira_csv_corpus_census_test.go — the half of #104's evidence that DECIDES anything, and the only
// thing in this repository that can reproduce the numbers jira_csv_status_category.go's header
// quotes. scripts/w34-jira-csv-status-category-probe.py extracts raw values from #103's cached
// corpus; this classifies them with THE SHIPPED MAPPERS and pins the totals.
//
// ⚠⚠ IT IS NOT A CI GUARD AND MUST NOT BE READ AS ONE. The corpus is 346 files in /tmp on one
// machine; CI has no such directory, so in CI these tests SKIP. A skipping test proves nothing —
// which is exactly why the skip is narrow and loud: it fires ONLY when the extract is absent, and
// an extract that exists but is empty or unparseable is a FAILURE. A census whose instrument read
// nothing must not be able to report a clean answer.
//
// ⚠ WHY PIN NUMBERS AT ALL, GIVEN THAT. Two reasons, and neither is regression protection.
// (1) The header of a shipped product file makes six quantitative claims about real exports. With
// this file, anyone holding the corpus can falsify them in one command; without it they are prose.
// (2) They are the positive control for the VOCABULARY: narrowing mapJiraCSVStatusCategory moves
// `resolvable` immediately, which is how C2 of the control harness was shown to be a real miss
// rather than an argument.

const (
	corpusTriples  = "/tmp/w34-jira-statuscat-triples.json"
	corpusPriority = "/tmp/w34-jira-priority-values.json"
	corpusPerFile  = "/tmp/w34-jira-statuscat-perfile.json"
)

// loadCensus is the ONLY place a missing corpus turns into a skip. Everything past it is a failure,
// including an extract that parses to nothing.
func loadCensus(t *testing.T, path string, into any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		t.Skipf("no corpus extract at %s — run scripts/w34-jira-csv-status-category-probe.py "+
			"(it needs #103's cache at /tmp/w34-jira-corpus). This test SKIPS in CI by design "+
			"and is therefore not a regression guard.", path)
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(b, into); err != nil {
		t.Fatalf("parse %s: %v — an extract that exists must be readable, or the census would "+
			"report a clean answer from an instrument that read nothing", path, err)
	}
}

// THE SIX NUMBERS jira_csv_status_category.go's header quotes, answered by the shipped mappers.
func TestJiraCSVCorpus_StatusCategoryCensusMatchesTheShippedHeader(t *testing.T) {
	var m map[string]int
	loadCensus(t, corpusTriples, &m)
	if len(m) == 0 {
		t.Fatal("the extract is empty — nothing below would be a fact about real exports")
	}

	var total, nameOK, nameMiss, missNoCell, resolvable, unplaceable, doneWithResolved int
	byCategory := map[string]int{}
	for k, n := range m {
		p := strings.SplitN(k, "\t", 3)
		status, category, hasResolved := p[0], p[1], p[2] == "1"
		total += n
		if _, ok := mapJiraStatus(status); ok {
			nameOK += n
			continue
		}
		nameMiss += n
		if category == "" {
			missNoCell += n
			continue
		}
		byCategory[category] += n
		mapped, ok := mapJiraCSVStatusCategory(category)
		if !ok {
			unplaceable += n
			continue
		}
		resolvable += n
		if mapped == model.StatusDone && hasResolved {
			doneWithResolved += n
		}
	}

	for _, c := range []struct {
		name string
		got  int
		want int
	}{
		{"data rows", total, 17657},
		{"status NAME recognised", nameOK, 14490},
		{"status NAME unrecognised", nameMiss, 3167},
		{"unrecognised WITH a category cell", nameMiss - missNoCell, 1424},
		{"unrecognised with NO category cell", missNoCell, 1743},
		{"category present and unplaceable", unplaceable, 0},
		{"category-resolved DONE rows carrying a Resolved date", doneWithResolved, 129},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d — jira_csv_status_category.go's header quotes the second "+
				"number; one of the two is now wrong", c.name, c.got, c.want)
		}
	}

	fmt.Printf("rows %d · name-recognised %d · unrecognised %d (with category %d, without %d) · "+
		"resolvable %d · unplaceable %d\n", total, nameOK, nameMiss, nameMiss-missNoCell,
		missNoCell, resolvable, unplaceable)
	for _, k := range sortedByCount(byCategory) {
		mapped, _ := mapJiraCSVStatusCategory(k)
		_, keyOK := mapJiraStatusCategory(k)
		fmt.Printf("   %6d  %-14q -> %-12s shipped-KEY-fn alone would place it: %v\n",
			byCategory[k], k, mapped, keyOK)
	}
}

// THE TRAP, MEASURED RATHER THAN ARGUED: what the API's KEY vocabulary alone would reach on this
// column. This is the number that makes the display-name half of #104 a fix rather than a
// preference, and it is the assertion that moves the moment anyone narrows that vocabulary.
func TestJiraCSVCorpus_TheAPIKeyVocabularyAloneMissesMostOfIt(t *testing.T) {
	var m map[string]int
	loadCensus(t, corpusTriples, &m)
	var byKeyFn, byCSVFn int
	for k, n := range m {
		p := strings.SplitN(k, "\t", 3)
		if _, ok := mapJiraStatus(p[0]); ok || p[1] == "" {
			continue
		}
		if _, ok := mapJiraStatusCategory(p[1]); ok {
			byKeyFn += n
		}
		if _, ok := mapJiraCSVStatusCategory(p[1]); ok {
			byCSVFn += n
		}
	}
	if byKeyFn != 130 || byCSVFn != 1424 {
		t.Errorf("key-vocabulary alone reaches %d rows and the CSV vocabulary reaches %d; "+
			"want 130 and 1424 — the gap IS the finding", byKeyFn, byCSVFn)
	}
}

// THE COMPANION ZERO, AND IT IS WORTH AS MUCH AS THE DEFECT. The same corpus, the same instrument,
// pointed at the OTHER vocabulary the same mapper applies: `Priority`. If it read like `Status` did
// — 17.9% unplaceable — Jira CSV priority would be the next merge. It does not:
//
//	17,657 rows · recognised 17,317 (98.1%) · UNRECOGNISED 340 (1.9%)
//	  P4 108 · "2" 48 · "1" 46 · Yellow 24 · "P1 - Critical" 16 · "Px - Unprioritized" 15 ·
//	  "P2 - Major" 14 · "Show Stopper" 14 · P3 13 · "p3 - Low" 12 · … 18 distinct values
//	12 of 304 files carry any; 6 of those carry nothing else
//
// Every miss is a PER-INSTANCE SCHEME — Pn ladders, bare integers, a colour — not a rename of a
// word Track knows. There is no canonical, non-renameable twin for priority the way statusCategory
// is for status (Jira's priority scheme is defined per instance), so the only fix available is
// guessing what another company's P4 means, which is the move #75's `undefined` refusal and #76's
// triage/duplicate refusal both exist to prevent. Recorded so the next session does not re-derive
// it, and so the CONTRAST is on the record: 17.9% with a canonical answer in the file is a fix;
// 1.9% with no canonical answer anywhere is a decision nobody needs to take yet.
func TestJiraCSVCorpus_ThePriorityVocabularyIsMeasuredClean(t *testing.T) {
	var m map[string]int
	loadCensus(t, corpusPriority, &m)
	if len(m) == 0 {
		t.Fatal("the extract is empty")
	}
	var total, miss int
	unrec := map[string]int{}
	for v, n := range m {
		total += n
		if _, ok := mapJiraPriority(v); !ok {
			miss += n
			unrec[v] += n
		}
	}
	if total != 17657 || miss != 340 {
		t.Errorf("rows = %d (want 17657), unrecognised = %d (want 340) — this test's own comment "+
			"quotes both", total, miss)
	}
	fmt.Printf("priority: rows %d · unrecognised %d (%.1f%%) · %d distinct\n",
		total, miss, 100*float64(miss)/float64(total), len(unrec))
	for _, k := range sortedByCount(unrec) {
		fmt.Printf("   %6d  %q\n", unrec[k], k)
	}
}

// PER FILE, BECAUSE AN IMPORT IS ONE FILE. A row proportion across 304 exports says what a fleet
// loses; this says what ONE tenant loses, which is the number an operator experiences.
func TestJiraCSVCorpus_PerFileReachOfTheCategoryRead(t *testing.T) {
	var pf map[string]map[string]int
	loadCensus(t, corpusPerFile, &pf)
	if len(pf) != 304 {
		t.Fatalf("extract holds %d files, want 304 — a different corpus", len(pf))
	}
	var any, half, all int
	for _, rows := range pf {
		var tot, fixable int
		for k, n := range rows {
			p := strings.SplitN(k, "\t", 2)
			tot += n
			if _, ok := mapJiraStatus(p[0]); ok {
				continue
			}
			if _, ok := mapJiraCSVStatusCategory(p[1]); ok {
				fixable += n
			}
		}
		if fixable == 0 || tot == 0 {
			continue
		}
		any++
		if fixable*2 >= tot {
			half++
		}
		if fixable == tot {
			all++
		}
	}
	if any != 57 || half != 23 || all != 5 {
		t.Errorf("files gaining rows = %d (want 57), >=50%% of their rows = %d (want 23), "+
			"100%% = %d (want 5) — jira_csv_status_category.go's header quotes all three",
			any, half, all)
	}
}

func sortedByCount(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if m[out[i]] != m[out[j]] {
			return m[out[i]] > m[out[j]]
		}
		return out[i] < out[j]
	})
	return out
}
