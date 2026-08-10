package importer

import (
	"encoding/json"
	"os"
	"sort"
	"testing"
)

// crossteam_identifier_census_test.go — the half of the project-key evidence that DECIDES anything.
// scripts/w34-crossteam-identifier-probe.py extracts the raw `Issue key` cells from #103's cached
// corpus and stops; this classifies them with THE SHIPPED MAPPER and pins the totals that
// issue.Store.UpsertByIdentifier's team predicate quotes.
//
// ⚠⚠ IT IS NOT A CI GUARD AND MUST NOT BE READ AS ONE. The corpus is 346 files in /tmp on one
// machine; CI has no such directory, so this SKIPS there. A skipping test proves nothing — which is
// why the skip is narrow and loud: it fires ONLY when the extract is ABSENT, and an extract that
// exists but is empty, unparseable, or missing its owner attribution is a FAILURE. A census whose
// instrument read nothing must not be able to report a clean answer.
//
// ⚠ THE LOADER IS DUPLICATED FROM jira_csv_corpus_census_test.go RATHER THAN CALLED, and that is
// #106's lesson rather than an oversight: loadCensus's skip message names
// w34-jira-csv-status-category-probe.py, which does not write this extract. A helper lends its
// provenance to every caller, and the borrowed sentence would be false here.
//
// ⚠ WHY THE OWNER ATTRIBUTION IS REQUIRED AND NOT OPTIONAL. The cache is keyed by
// sha256(repo TAB path), and the pair is not stored, so two exports carrying key `SCRUM` are
// indistinguishable on disk from one project exported twice. Per-FILE sharing is 70 keys of 172;
// per-OWNER it is 9. Those two numbers answer different questions and only the second one is about
// two unrelated Jira sites landing in one Track workspace — so a census that could report the first
// while silently lacking the second would be quoting the wrong one.

const corpusProjectKeys = "/tmp/w34-crossteam-project-keys.json"

type projectKeyExtract struct {
	PerFile map[string][]string `json:"per_file"`
	Owners  map[string]string   `json:"owners"`
}

// loadProjectKeyExtract is the ONLY place a missing corpus turns into a skip.
func loadProjectKeyExtract(t *testing.T) projectKeyExtract {
	t.Helper()
	b, err := os.ReadFile(corpusProjectKeys)
	if os.IsNotExist(err) {
		t.Skipf("no extract at %s — run `python3 scripts/w34-crossteam-identifier-probe.py --owners` "+
			"(it needs #103's cache at /tmp/w34-jira-corpus). This test SKIPS in CI by design and "+
			"is therefore not a regression guard.", corpusProjectKeys)
	}
	if err != nil {
		t.Fatalf("read %s: %v", corpusProjectKeys, err)
	}
	var x projectKeyExtract
	if err := json.Unmarshal(b, &x); err != nil {
		t.Fatalf("parse %s: %v — an extract that exists must be readable, or the census would "+
			"report a clean answer from an instrument that read nothing", corpusProjectKeys, err)
	}
	if len(x.PerFile) == 0 {
		t.Fatal("the extract is empty — nothing below would be a fact about real exports")
	}
	if len(x.Owners) == 0 {
		t.Fatal("the extract carries no owner attribution: re-run the probe WITH --owners. " +
			"Per-file sharing cannot tell two unrelated Jira sites from one project exported " +
			"twice, and it is the per-OWNER number that UpsertByIdentifier's team predicate quotes.")
	}
	return x
}

// TestJiraCSVCorpus_ProjectKeyNamespace answers, with the SHIPPED mapper, the question the team
// predicate rests on: does the pipeline carry any namespace at all into `issues.identifier`?
//
// ⚠ THE MAPPER IS RUN, NOT DESCRIBED. A census that asserted `"SCRUM-1" == "SCRUM-1"` would be a
// fact about Go's string comparison. jiraRowMapper is what decides what reaches the UNIQUE
// (workspace_id, identifier) constraint, and it is the thing that must be shown to pass the
// provider's key through unchanged — because a mapper that DID namespace by team or project would
// make the whole cross-team collision unreachable.
func TestJiraCSVCorpus_ProjectKeyNamespace(t *testing.T) {
	x := loadProjectKeyExtract(t)

	// Every distinct key in the corpus, and which owners carry it.
	ownersOfKey := map[string]map[string]bool{}
	filesOfKey := map[string]int{}
	for blob, keys := range x.PerFile {
		owner := x.Owners[blob]
		for _, k := range keys {
			filesOfKey[k]++
			if owner == "" {
				continue
			}
			if ownersOfKey[k] == nil {
				ownersOfKey[k] = map[string]bool{}
			}
			ownersOfKey[k][owner] = true
		}
	}

	if got, want := len(x.PerFile), 305; got != want {
		t.Errorf("exports in the extract = %d, want %d — a different corpus, so every number below "+
			"is about a different set", got, want)
	}
	if got, want := len(filesOfKey), 172; got != want {
		t.Errorf("distinct project keys = %d, want %d", got, want)
	}

	// THE SHIPPED MAPPER, ONE SYNTHETIC ROW PER REAL KEY. The header is the minimum jiraRowMapper
	// needs to accept a row; the assertion is that Identifier comes out as the provider's own key
	// with nothing prepended, appended or replaced.
	ci := buildIndex([]string{"Issue key", "Summary"})
	unnamespaced := 0
	for k := range filesOfKey {
		mapped, err := jiraRowMapper(ci, []string{k + "-1", "a title"})
		if err != nil {
			t.Fatalf("the shipped mapper refused a row built from a real key %q: %v", k, err)
		}
		if mapped.issue.Identifier != k+"-1" {
			t.Fatalf("the mapper changed the provider key: %q -> %q", k+"-1", mapped.issue.Identifier)
		}
		unnamespaced++
	}
	if unnamespaced != len(filesOfKey) {
		t.Fatalf("mapped %d of %d keys", unnamespaced, len(filesOfKey))
	}

	// THE NUMBERS THE PREDICATE'S COMMENT QUOTES. Per-file first, then the one that matters.
	sharedFiles := 0
	for _, n := range filesOfKey {
		if n >= 2 {
			sharedFiles++
		}
	}
	sharedOwners := map[string]int{}
	for k, os_ := range ownersOfKey {
		if len(os_) >= 2 {
			sharedOwners[k] = len(os_)
		}
	}
	if got, want := sharedFiles, 70; got != want {
		t.Errorf("keys carried by >= 2 exports = %d, want %d", got, want)
	}
	if got, want := len(sharedOwners), 9; got != want {
		names := make([]string, 0, len(sharedOwners))
		for k := range sharedOwners {
			names = append(names, k)
		}
		sort.Strings(names)
		t.Errorf("keys carried by >= 2 DISTINCT OWNERS = %d, want %d: %v", got, want, names)
	}
	// SCRUM and KAN are the keys Jira Software's own Scrum and Kanban project templates hand out,
	// which is why they lead a list of collisions between unrelated instances. That is the sharp
	// end of the finding: the most-shared keys in real data are the ones the provider itself
	// assigns by default, so two teams importing "their" Jira in one workspace is not an exotic
	// coincidence.
	if got := sharedOwners["SCRUM"]; got != 9 {
		t.Errorf("`SCRUM` carried by %d distinct owners, want 9", got)
	}
	if got := sharedOwners["KAN"]; got != 3 {
		t.Errorf("`KAN` carried by %d distinct owners, want 3", got)
	}

	// Median key length — the reason a collision is cheap. Sorted here rather than taken from the
	// probe, so the number is this package's own.
	lens := make([]int, 0, len(filesOfKey))
	for k := range filesOfKey {
		lens = append(lens, len(k))
	}
	sort.Ints(lens)
	median := lens[len(lens)/2]
	if len(lens)%2 == 0 {
		median = (lens[len(lens)/2-1] + lens[len(lens)/2]) / 2
	}
	if median != 4 {
		t.Errorf("median project-key length = %d, want 4", median)
	}
}
