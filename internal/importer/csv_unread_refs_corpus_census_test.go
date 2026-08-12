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

// ⚠⚠ THE INVARIANT IS PER ENTRY, AND IT USED TO BE PER COLUMN. While each entry named ONE spelling
// the two were the same thing; they are not once an entry carries three. `Custom field (Epic Link)`
// is populated on 3,630 rows and NOTED on 1,748 of them — the other 1,882 also populate `Parent`,
// which wins the preference order and is the one note that fires. A per-column equality would call
// that a defect and it is the design (one dropped reference is one line), so the census now counts
// rows where ANY spelling of an entry is populated and compares that to the notes the entry produced
// across ALL its spellings. Per column it keeps only the floor that matters at that grain: a spelling
// no real export ever populates is a dead entry.
type refCensus struct {
	populated      map[string]int // per SPELLING: rows whose cell is non-empty
	noted          map[string]int // per SPELLING: rows that produced the unread-column note
	entryPopulated map[string]int // per ENTRY (keyed by its first spelling): rows where ANY spelling is
	files          int
	rows           int
}

// entryKey names an entry by its first spelling. It cannot be the FIELD: Jira's `Creator` and
// `Reporter` are two entries with one field (they are different people on 8.6% of the rows that
// populate both), so keying by field would merge two deliberately separate reports into one.
func entryKey(r unreadRef) string {
	if len(r.columns) == 0 {
		return "(no spellings)"
	}
	return r.columns[0]
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
	c := refCensus{populated: map[string]int{}, noted: map[string]int{}, entryPopulated: map[string]int{}}
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
				anySpelling := false
				for _, col := range r.columns {
					if ci.has(col) && ci.get(row, col) != "" {
						c.populated[col]++
						anySpelling = true
					}
				}
				// Counted ONCE for the entry however many of its spellings this row populates —
				// which is the whole point of the per-entry grain. 2,926 corpus rows populate two
				// parent spellings and lost ONE reference between them.
				if anySpelling {
					c.entryPopulated[entryKey(r)]++
				}
			}
			for _, n := range m.notes {
				// BOTH unread-column vias. They render different sentences (one reference is
				// stamped rather than left empty — see viaColumnNotReadStamped) but they are the
				// same loss class, and a census that knew only the first would score the fifth
				// reference `populated>0 reported=0` and read as a product defect.
				if n.Via == viaColumnNotRead || n.Via == viaColumnNotReadStamped {
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
//
// ⚠ THEY ARE NOW SPELLING LISTS, AND THE LITERAL IS WHAT MAKES A DELETED SPELLING VISIBLE. C5's
// finding applies one grain finer than it was written: dropping `Custom field (Epic Link)` from the
// parent entry would stop the census asking about it — 3,630 real rows, 1,748 of them reported by
// nothing else — and the per-entry equality below would still balance perfectly, because both sides
// of it would shrink together. The literal is the only thing that reds.
var (
	unreadRefsLinearColumns = [][]string{{"Assignee"}, {"Project"}, {"Cycle Name"}, {"Parent issue"}, {"Creator"}}
	unreadRefsJiraColumns   = [][]string{
		{"Assignee"}, {"Sprint"},
		{"Parent", "Custom field (Epic Link)", "Parent id"},
		{"Creator"}, {"Reporter"},
	}
)

func assertCensus(t *testing.T, name string, c refCensus, refs []unreadRef, want [][]string, minFiles, minRows int) {
	t.Helper()
	if c.files < minFiles || c.rows < minRows {
		t.Fatalf("%s corpus read %d files / %d rows, want at least %d / %d — the corpus is present "+
			"but yielded almost nothing, which is an instrument failure, not a clean answer",
			name, c.files, c.rows, minFiles, minRows)
	}
	// The table's entries, keyed the way the pinned literals are keyed, so the two sets can be
	// compared in BOTH directions: a pinned entry the table lost, and a table entry never measured.
	have := map[string][]string{}
	for _, r := range refs {
		have[entryKey(r)] = r.columns
	}
	for _, entry := range want {
		key := entry[0]
		got, ok := have[key]
		if !ok {
			t.Errorf("%s: the entry for %q is no longer in the shipped table, so nothing reports it — "+
				"the census is pinned to what this merge measured, not to the table", name, key)
			continue
		}
		// ⚠ AND THE SPELLINGS WITHIN THE ENTRY, IN BOTH DIRECTIONS TOO. An entry that keeps its first
		// spelling and quietly loses its third still reports fewer rows, and every count below would
		// agree with itself about it.
		if strings.Join(got, "|") != strings.Join(entry, "|") {
			t.Errorf("%s: entry %q ships spellings %v; this census measured %v — a spelling added or "+
				"removed changes how many rows the report reaches and must be measured, not assumed",
				name, key, got, entry)
		}
		delete(have, key)
	}
	for extra, cols := range have {
		t.Errorf("%s: the table reports entry %q (%v), which this census has never measured — add it "+
			"here with its population or the number in csv_unread_refs.go is unbacked", name, extra, cols)
	}
	for _, entry := range want {
		key := entry[0]
		// PER SPELLING: a spelling no real export ever populates is a dead entry — either the corpus
		// changed or the name in the table does not match what exports emit. This is the floor that
		// caught the whole finding when it was pointed the other way.
		for _, col := range entry {
			if c.populated[col] == 0 {
				t.Errorf("%s: NOT ONE row of %d carries a %q value — either the corpus changed or "+
					"that spelling no longer matches what real exports emit", name, c.rows, col)
			}
		}
		// PER ENTRY, AND THE EQUALITY IS STILL THE POINT. Every row that populates ANY spelling of
		// this reference must produce exactly one note, and no row that populates none may produce
		// any. A count that merely correlated would let a note fire off the header and still look
		// right in aggregate.
		pop := c.entryPopulated[key]
		noted := 0
		for _, col := range entry {
			noted += c.noted[col]
		}
		if pop == 0 {
			t.Errorf("%s: NOT ONE row of %d populates any spelling of entry %q", name, c.rows, key)
			continue
		}
		if noted != pop {
			t.Errorf("%s: entry %q populated on %d rows but reported on %d — the report and the loss "+
				"are not the same set of rows", name, key, pop, noted)
		}
		for _, col := range entry {
			t.Logf("%s   %-26s populated=%6d reported=%6d", name, col, c.populated[col], c.noted[col])
		}
		t.Logf("%s %-14s ENTRY rows=%6d reported=%6d of %d rows", name, key, pop, noted, c.rows)
	}
}

func TestUnreadRefsCorpus_EveryPopulatedReferenceCellIsReported(t *testing.T) {
	lin := censusUnreadRefs(t, unreadRefsLinearCorpusDir, linearRowMapper, linearUnreadRefs, []string{"Title", "Status", "ID"})
	assertCensus(t, "linear", lin, linearUnreadRefs, unreadRefsLinearColumns, unreadRefsLinearMinFiles, unreadRefsLinearMinRows)

	jira := censusUnreadRefs(t, unreadRefsJiraCorpusDir, jiraRowMapper, jiraUnreadRefs, []string{"Summary", "Status"})
	assertCensus(t, "jira", jira, jiraUnreadRefs, unreadRefsJiraColumns, unreadRefsJiraMinFiles, unreadRefsJiraMinRows)
}
