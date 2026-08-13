package importer

// duplicate_identifier_corpus_census_test.go — the PROVENANCE behind the numbers in
// duplicate_identifier.go, re-runnable rather than a paragraph. Same contract as the other censuses
// in this package: it drives THE SHIPPED COLUMN INDEX over the cached real corpus, it SKIPS only
// when the directory is absent, and a directory that yields no genuine export is a FAILURE.
//
// ⚠ IT COUNTS THE ROWS THAT COLLIDE, NOT THE KEYS THAT REPEAT, and the two differ. A key on six
// rows costs FIVE issues, not one — the population line an operator needs is "how many rows landed
// on an issue an earlier row of this same file already wrote", which is exactly `imported` minus
// the number of issues, and exactly what the note fires on.
//
// ⚠ THE COLUMN SPELLINGS ARE TAKEN FROM THE SHIPPED CONSTANTS ON PURPOSE, and that is the opposite
// choice from csv_dropped_objects_corpus_census_test.go's independent list. The reason is that the
// thing being measured here is NOT the column list: it is a property of the VALUES in one column
// whose name the two provider constants already fix and whose reads two other test files already
// hold apart from their look-alike neighbours (`Issue id`, `Parent key`, `Project ID`, `UUID`). A
// second hand-written copy of "Issue key" would test nothing this census is about and would rot.

import (
	"bytes"
	"encoding/csv"
	"os"
	"path/filepath"
	"testing"
)

const (
	dupIDJiraCorpusDir   = "/tmp/w34-jira-corpus"
	dupIDLinearCorpusDir = "/tmp/w34-linear-corpus-cache"
)

// Floors, not equalities: the corpus is a cache of other people's public exports and can grow. Each
// is set below the measured figure so a shrinking population is a failure and a growing one is not.
// MEASURED at this merge — Jira: 346 files, 305 carrying `Issue key`, 17,923 rows, 71 colliding
// rows in 3 files. Linear: 46 files, 45 carrying `ID`, 3,099 rows, 96 colliding rows in 3 files.
const (
	dupIDMinJiraFiles     = 300
	dupIDMinJiraRows      = 17000
	dupIDMinJiraCollided  = 60
	dupIDMinLinearFiles   = 44
	dupIDMinLinearRows    = 3000
	dupIDMinLinearCollide = 90
)

// censusDuplicateIdentifiers walks a corpus directory and reports, using the SHIPPED buildIndex:
// files carrying the key column, data rows in them, and rows whose key an EARLIER row of the same
// file already carried.
func censusDuplicateIdentifiers(t *testing.T, dir, keyColumn string) (files, rows, collided, filesWithCollision int) {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		t.Skipf("no corpus at %s — this test SKIPS in CI by design; duplicate_identifier_job_test.go "+
			"pins the same shape as a literal fixture and runs everywhere.", dir)
	}
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		// The SHIPPED reader settings, byte for byte with newCSVSource: BOM stripped at the file
		// prefix, ragged rows tolerated, leading space trimmed.
		rd := csv.NewReader(skipUTF8BOM(bytes.NewReader(raw)))
		rd.FieldsPerRecord = -1
		rd.TrimLeadingSpace = true
		header, err := rd.Read()
		if err != nil {
			continue // not a CSV we can read a header from — not an export
		}
		ci := buildIndex(header)
		all, err := rd.ReadAll()
		if err != nil {
			continue // malformed past the header
		}
		// ci.get on a header that does not carry the column returns "" for every row, which is
		// indistinguishable from an export whose key cells are all blank — so the column's presence
		// is decided on the HEADER, through the shipped accessor that exists for exactly that
		// question.
		if !ci.has(keyColumn) {
			continue
		}
		files++
		rows += len(all)
		seen := map[string]bool{}
		fileCollided := 0
		for _, row := range all {
			k := ci.get(row, keyColumn)
			if k == "" {
				continue
			}
			if seen[k] {
				fileCollided++
			}
			seen[k] = true
		}
		collided += fileCollided
		if fileCollided > 0 {
			filesWithCollision++
		}
	}
	return files, rows, collided, filesWithCollision
}

// TestDuplicateIdentifierCorpus_RealExportsNameOneIssueMoreThanOnce is the population behind the
// note. It is a FLOOR on the collisions and a floor on the corpus, so a run that reads a smaller
// corpus than the one measured cannot report a smaller number as reassurance.
func TestDuplicateIdentifierCorpus_RealExportsNameOneIssueMoreThanOnce(t *testing.T) {
	t.Run("jira", func(t *testing.T) {
		files, rows, collided, dupFiles := censusDuplicateIdentifiers(t, dupIDJiraCorpusDir, jiraCSVIssueKeyColumn)
		t.Logf("JIRA: %d files carry %q · %d data rows · %d rows collide with an earlier row of the same file, in %d files",
			files, jiraCSVIssueKeyColumn, rows, collided, dupFiles)
		if files < dupIDMinJiraFiles || rows < dupIDMinJiraRows {
			t.Fatalf("corpus shrank: %d files / %d rows, floors %d / %d — the numbers below are being read off a different population",
				files, rows, dupIDMinJiraFiles, dupIDMinJiraRows)
		}
		if collided < dupIDMinJiraCollided {
			t.Errorf("%d colliding rows, floor %d", collided, dupIDMinJiraCollided)
		}
	})
	t.Run("linear", func(t *testing.T) {
		files, rows, collided, dupFiles := censusDuplicateIdentifiers(t, dupIDLinearCorpusDir, linearCSVIssueIDColumn)
		t.Logf("LINEAR: %d files carry %q · %d data rows · %d rows collide with an earlier row of the same file, in %d files",
			files, linearCSVIssueIDColumn, rows, collided, dupFiles)
		if files < dupIDMinLinearFiles || rows < dupIDMinLinearRows {
			t.Fatalf("corpus shrank: %d files / %d rows, floors %d / %d — the numbers below are being read off a different population",
				files, rows, dupIDMinLinearFiles, dupIDMinLinearRows)
		}
		if collided < dupIDMinLinearCollide {
			t.Errorf("%d colliding rows, floor %d", collided, dupIDMinLinearCollide)
		}
	})
}
