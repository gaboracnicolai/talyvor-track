package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// csv_unread_refs_corpus_census_test.go — the PROVENANCE behind the numbers in csv_unread_refs.go,
// re-runnable rather than a paragraph. It drives THE SHIPPED MAPPERS over every real export in the
// two cached corpora and counts, per reference column, the rows whose cell is populated and whose
// note fired.
//
// ⚠ IT IS NOT A CI GUARD AND MUST NOT BE READ AS ONE — the same statement
// jira_csv_date_corpus_census_test.go makes about the same directories. The corpora are directories
// in /tmp on one machine; in CI this SKIPS. csv_unread_refs_test.go is the half that runs
// everywhere.
//
// ⚠ THE SKIP IS NARROW AND LOUD. It fires ONLY when the directory is absent. A directory that
// exists and yields no genuine export is a FAILURE: an instrument that read nothing must not report
// a clean answer.
const (
	unreadRefsLinearCorpusDir = "/tmp/w34-linear-corpus-cache"
	unreadRefsJiraCorpusDir   = "/tmp/w34-jira-corpus"
)

// Floors on the population as measured at this merge, never equalities: a corpus is a cache that
// can grow, and a test pinning an exact total goes red on a larger sample with nothing wrong. What
// must not happen is a column's count falling to nothing — that is the shape a mapper change makes
// when it stops seeing the column at all.
const (
	unreadRefsLinearMinFiles = 45   // 45 genuine exports at this merge (a 46th file in the cache is
	unreadRefsLinearMinRows  = 3000 // every line quoted whole — one CSV field — and yields no rows)
	unreadRefsJiraMinFiles   = 300  // 302 at this merge
	unreadRefsJiraMinRows    = 18000
)

type refCensus struct {
	populated map[string]int // rows whose cell is non-empty
	noted     map[string]int // rows that produced the unread-column note
	files     int
	rows      int
}

func censusUnreadRefs(t *testing.T, dir string, mapper rowMapper, refs []unreadRef, fingerprint []string) refCensus {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		t.Skipf("no corpus at %s — this test SKIPS in CI by design; see the file comment. "+
			"csv_unread_refs_test.go pins the same shapes as literals and runs everywhere.", dir)
	}
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	c := refCensus{populated: map[string]int{}, noted: map[string]int{}}
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
		for _, row := range recs[1:] {
			c.rows++
			m, err := mapper(ci, row)
			if err != nil {
				continue // refused rows import nothing at all; a different class, counted elsewhere
			}
			for _, r := range refs {
				if ci.has(r.column) && ci.get(row, r.column) != "" {
					c.populated[r.column]++
				}
			}
			for _, n := range m.notes {
				if n.Via == viaColumnNotRead {
					c.noted[n.Value]++
				}
			}
		}
	}
	return c
}

// unreadRefsLinearColumns / unreadRefsJiraColumns are PINNED LITERALS, deliberately not derived from
// linearUnreadRefs / jiraUnreadRefs.
//
// ⚠ THIS IS CONTROL C5's FINDING ABOUT THIS FILE RATHER THAN ABOUT THE PRODUCT. The census first
// iterated the tables themselves, so deleting `Project` from linearUnreadRefs simply stopped the
// census asking about `Project` — 3,099 real rows, 2,491 of them carrying a project, and the
// instrument went quiet instead of red. A census that reads its subject out of the thing it is
// measuring cannot see a deletion. The literals below can, and assertCensus also fails when the
// table grows past them, so the two cannot drift apart in either direction.
var (
	unreadRefsLinearColumns = []string{"Assignee", "Project", "Cycle Name", "Parent issue"}
	unreadRefsJiraColumns   = []string{"Assignee", "Sprint", "Parent"}
)

func assertCensus(t *testing.T, name string, c refCensus, refs []unreadRef, want []string, minFiles, minRows int) {
	t.Helper()
	if c.files < minFiles || c.rows < minRows {
		t.Fatalf("%s corpus read %d files / %d rows, want at least %d / %d — the corpus is present "+
			"but yielded almost nothing, which is an instrument failure, not a clean answer",
			name, c.files, c.rows, minFiles, minRows)
	}
	have := map[string]bool{}
	for _, r := range refs {
		have[r.column] = true
	}
	for _, col := range want {
		if !have[col] {
			t.Errorf("%s: %q is no longer in the shipped table, so nothing reports it — "+
				"the census is pinned to the columns this merge measured, not to the table", name, col)
		}
		delete(have, col)
	}
	for extra := range have {
		t.Errorf("%s: the table reports %q, which this census has never measured — add it here with "+
			"its population or the number in csv_unread_refs.go is unbacked", name, extra)
	}
	for _, col := range want {
		pop, noted := c.populated[col], c.noted[col]
		if pop == 0 {
			t.Errorf("%s: NOT ONE row of %d carries a %q value — either the corpus changed or the "+
				"column name in the table no longer matches what real exports emit", name, c.rows, col)
			continue
		}
		// THE EQUALITY IS THE POINT. Every populated cell must produce exactly one note and no
		// unpopulated cell may produce any; a count that merely correlates would let a note fire
		// off the header and still look right in aggregate.
		if noted != pop {
			t.Errorf("%s: %q populated on %d rows but reported on %d — the report and the loss are "+
				"not the same set of rows", name, col, pop, noted)
		}
		t.Logf("%s %-14s populated=%6d reported=%6d of %d rows", name, col, pop, noted, c.rows)
	}
}

func TestUnreadRefsCorpus_EveryPopulatedReferenceCellIsReported(t *testing.T) {
	lin := censusUnreadRefs(t, unreadRefsLinearCorpusDir, linearRowMapper, linearUnreadRefs, []string{"Title", "Status", "ID"})
	assertCensus(t, "linear", lin, linearUnreadRefs, unreadRefsLinearColumns, unreadRefsLinearMinFiles, unreadRefsLinearMinRows)

	jira := censusUnreadRefs(t, unreadRefsJiraCorpusDir, jiraRowMapper, jiraUnreadRefs, []string{"Summary", "Status"})
	assertCensus(t, "jira", jira, jiraUnreadRefs, unreadRefsJiraColumns, unreadRefsJiraMinFiles, unreadRefsJiraMinRows)
}
