package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// csv_issue_links_corpus_census_test.go — the PROVENANCE behind the numbers in csv_issue_links.go,
// re-runnable rather than a paragraph. It drives THE SHIPPED MAPPERS over every real export in the
// two cached corpora and compares, per link column, the rows whose cell is populated against the
// rows whose note fired.
//
// ⚠ IT IS NOT A CI GUARD — the same statement csv_unread_refs_corpus_census_test.go makes about the
// same directories. The corpora are directories in /tmp on one machine; in CI this SKIPS.
// csv_issue_links_test.go is the half that runs everywhere.
//
// ⚠ THE SKIP IS NARROW AND LOUD. It fires ONLY when the directory is absent. A directory that
// exists and yields no genuine export is a FAILURE: an instrument that read nothing must not report
// a clean answer.
//
// ⚠⚠ THE PREDICATE BELOW IS WRITTEN OUT HERE AND IS DELIBERATELY NOT jiraIssueLinkSpellings /
// linearIssueLinkColumns. That is control C5's finding from the unread-reference census applied
// before it could bite again: a census that asks the shipped rule which columns to look at cannot
// see that rule shrink, because both sides of its equality shrink together. This one finds the link
// columns itself, so narrowing the prefix rule leaves `populated` at 3,789 and takes `noted` down
// with the rule.
//
// ⚠ AND THE SHARPER VERSION OF THAT SENTENCE WAS OVERSTATED UNTIL A CONTROL MEASURED IT. It read
// "a derived census goes QUIET rather than RED"; control C10 built exactly that census — the
// predicate pointed at jiraIssueLinkSpellings, the shipped prefix rule emptied — and this test went
// RED anyway, on the `linkRows` FLOOR rather than on the equality. Both mechanisms are load-bearing
// and they catch different mutations: the FLOOR catches a rule deleted wholesale (linkRows falls to
// 0), the INDEPENDENT PREDICATE catches a rule narrowed to a subset, where the floor can still be
// met while whole spellings go unreported. Neither on its own covers the other.

// censusIssueLinkPrefixes / censusLinearIssueLinkColumns — the census's OWN copy of the rule.
var (
	censusIssueLinkPrefixes      = []string{"outward issue link (", "inward issue link ("}
	censusLinearIssueLinkColumns = []string{"blocked by", "related to", "duplicate of"}
)

// Floors on the population as measured at this merge, never equalities: the corpora are caches that
// can grow, and a test pinning an exact total goes red on a larger sample with nothing wrong.
const (
	issueLinksJiraMinFiles     = 300   // 302 genuine exports at this merge
	issueLinksJiraMinRows      = 17000 // 18,807 rows
	issueLinksJiraMinLinkRows  = 3500  // 3,789 rows carry a link
	issueLinksJiraMinLinkFiles = 180   // 188 exports carry at least one
	// The prefix rule's reason for existing, as a floor: an exact list of literals would have to
	// have named this many spellings to cover the sample, and the next instance names its own.
	issueLinksJiraMinSpellings = 40 // 55 distinct link-column spellings at this merge

	issueLinksLinearMinFiles    = 45   // 45 genuine exports
	issueLinksLinearMinRows     = 3000 // 3,099 rows
	issueLinksLinearMinLinkRows = 1300 // 1,403 rows carry a link
)

type linkCensus struct {
	files, rows      int
	linkRows         int
	linkFiles        int
	populated        map[string]int // per spelling: rows with ANY occurrence non-empty
	noted            map[string]int // per spelling: rows that produced the issue-link note
	blocksFamilyRows int
	spellings        map[string]bool
}

func censusIssueLinks(t *testing.T, dir string, mapper rowMapper, fingerprint []string, jira bool) linkCensus {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		t.Skipf("no corpus at %s — this test SKIPS in CI by design; see the file comment. "+
			"csv_issue_links_test.go pins the same shapes as literals and runs everywhere.", dir)
	}
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	c := linkCensus{populated: map[string]int{}, noted: map[string]int{}, spellings: map[string]bool{}}
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
		genuine := true
		for _, f := range fingerprint {
			if !have[strings.ToLower(f)] {
				genuine = false
			}
		}
		if !genuine {
			continue
		}
		c.files++
		ci := buildIndex(recs[0])

		// The census's own discovery of the link columns — see the file comment.
		var cols []string
		for column := range ci {
			if jira {
				for _, p := range censusIssueLinkPrefixes {
					if strings.HasPrefix(column, p) {
						cols = append(cols, column)
						break
					}
				}
				continue
			}
			for _, l := range censusLinearIssueLinkColumns {
				if column == l {
					cols = append(cols, column)
				}
			}
		}
		for _, col := range cols {
			c.spellings[col] = true
		}

		fileHasLink := false
		for _, row := range recs[1:] {
			c.rows++
			m, err := mapper(ci, row)
			if err != nil {
				continue // refused rows import nothing at all; a different class, counted elsewhere
			}
			rowHasLink, rowBlocks := false, false
			for _, col := range cols {
				if len(ci.getAll(row, col)) == 0 {
					continue
				}
				c.populated[col]++
				rowHasLink = true
				if strings.Contains(col, "block") {
					rowBlocks = true
				}
			}
			if rowHasLink {
				c.linkRows++
				fileHasLink = true
			}
			if rowBlocks {
				c.blocksFamilyRows++
			}
			for _, n := range m.notes {
				if n.Via == viaIssueLinkNotRead {
					c.noted[n.Value]++
				}
			}
		}
		if fileHasLink {
			c.linkFiles++
		}
	}
	return c
}

func assertLinkCensus(t *testing.T, name string, c linkCensus, minFiles, minRows, minLinkRows int) {
	t.Helper()
	if c.files < minFiles || c.rows < minRows {
		t.Fatalf("%s corpus read %d files / %d rows, want at least %d / %d — the corpus is present "+
			"but yielded almost nothing, which is an instrument failure, not a clean answer",
			name, c.files, c.rows, minFiles, minRows)
	}
	if c.linkRows < minLinkRows {
		t.Errorf("%s: %d rows carry an issue link, want at least %d — either the corpus changed or "+
			"the census stopped finding the columns", name, c.linkRows, minLinkRows)
	}
	// THE EQUALITY IS THE POINT, and it is per spelling: every row that populates a link column
	// must produce exactly one note for that column, and no row that populates none may produce
	// any. A count that merely correlated would let a note fire off the header.
	for col, pop := range c.populated {
		if c.noted[col] != pop {
			t.Errorf("%s: %q populated on %d rows but reported on %d — the report and the loss are "+
				"not the same set of rows", name, col, pop, c.noted[col])
		}
	}
	for col, noted := range c.noted {
		if c.populated[col] == 0 {
			t.Errorf("%s: %d note(s) fired for %q, which NO row populates — the report is firing off "+
				"something other than a populated cell", name, noted, col)
		}
	}
	t.Logf("%s: files=%d rows=%d linkRows=%d linkFiles=%d spellings=%d blocksFamilyRows=%d",
		name, c.files, c.rows, c.linkRows, c.linkFiles, len(c.spellings), c.blocksFamilyRows)
	for col, pop := range c.populated {
		if pop >= 50 {
			t.Logf("%s   %-42s populated=%6d reported=%6d", name, col, pop, c.noted[col])
		}
	}
}

func TestIssueLinksCorpus_EveryPopulatedLinkCellIsReported(t *testing.T) {
	jira := censusIssueLinks(t, unreadRefsJiraCorpusDir, jiraRowMapper, []string{"Summary", "Status"}, true)
	assertLinkCensus(t, "jira", jira, issueLinksJiraMinFiles, issueLinksJiraMinRows, issueLinksJiraMinLinkRows)
	if jira.linkFiles < issueLinksJiraMinLinkFiles {
		t.Errorf("jira: only %d exports carry a populated link, want at least %d",
			jira.linkFiles, issueLinksJiraMinLinkFiles)
	}
	// The prefix rule's justification, measured rather than argued: a literal list would need this
	// many entries to cover the sample alone.
	if len(jira.spellings) < issueLinksJiraMinSpellings {
		t.Errorf("jira: %d distinct link-column spellings, want at least %d — if the corpus really "+
			"carries few, the argument for a prefix rule over a list weakens and should be re-made",
			len(jira.spellings), issueLinksJiraMinSpellings)
	}
	// The half of the population whose consequence is the sharpest: these rows arrive saying the
	// issue blocks or is blocked, and import with Issue.IsBlocked false.
	if jira.blocksFamilyRows == 0 {
		t.Errorf("jira: NOT ONE row carries a blocks-family link, which contradicts the sentence " +
			"csv.go renders about them")
	}

	lin := censusIssueLinks(t, unreadRefsLinearCorpusDir, linearRowMapper, []string{"Title", "Status", "ID"}, false)
	assertLinkCensus(t, "linear", lin, issueLinksLinearMinFiles, issueLinksLinearMinRows, issueLinksLinearMinLinkRows)
	// Per shipped spelling: a column no real export ever populates is a dead entry.
	for _, col := range linearIssueLinkColumns {
		if lin.populated[strings.ToLower(col)] == 0 {
			t.Errorf("linear: NOT ONE row of %d carries a %q value — either the corpus changed or "+
				"that spelling no longer matches what real exports emit", lin.rows, col)
		}
	}
}
