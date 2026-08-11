package importer

// csv_wide_row_corpus_census_test.go — the PROVENANCE behind the numbers in csv_wide_row_test.go,
// re-runnable rather than a paragraph.
//
// ⚠ IT IS NOT A CI GUARD AND MUST NOT BE READ AS ONE — the same statement the two sibling censuses
// make about the same directories. The corpora are directories in /tmp on one machine; in CI this
// SKIPS. csv_wide_row_test.go is the half that runs everywhere.
//
// ⚠ THE SKIP IS NARROW AND LOUD. It fires ONLY when the directory is absent. A directory that exists
// and yields no genuine export is a FAILURE: an instrument that read nothing must not report a clean
// answer.
//
// ⚠ TWO INSTRUMENTS, AND THE GAP BETWEEN THEM IS ITSELF THE MEASUREMENT.
//
//	BYTES    encoding/csv with the settings newCSVSource pins, written out as LITERALS here rather
//	         than read back off the product — a census that reads its subject out of the thing it
//	         measures cannot see that thing being deleted. It counts every row wider than its header.
//	PRODUCT  the shipped ImportJiraCSV / ImportLinearCSV over the same bytes, counting the issues the
//	         rendered warning claims.
//
// The PRODUCT number is SMALLER than the BYTES number, and the difference is a real limit worth
// stating rather than averaging away: csvSource.Next drops a row's notes when the MAPPER returns an
// error, so a wide row that also fails to map — `7f22900e…` carries no `Summary` column at all, so
// all eight of its rows are refused for having no title — reports nothing about its width. That is
// pre-existing behaviour shared with the narrow-row note and is NOT changed here; it is asserted, so
// the next person meets it as a measurement instead of rediscovering it.

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	wideRowLinearCorpusDir = "/tmp/w34-linear-corpus-cache"
	wideRowJiraCorpusDir   = "/tmp/w34-jira-corpus"
)

// Floors on the population as measured at this merge, never equalities: a corpus is a cache that can
// grow, and a test pinning an exact total goes red on a larger sample with nothing wrong.
const (
	wideRowJiraMinFiles     = 300 // 346 files / 340 genuine at this merge
	wideRowJiraMinRows      = 18000
	wideRowJiraMinWideRows  = 11 // in 2 files, from two unrelated instances
	wideRowJiraMinWideFiles = 2
	wideRowJiraMinReported  = 10 // the issues the PRODUCT's warning claims — see the two-instrument note

	wideRowLinearMinFiles = 45
	wideRowLinearMinRows  = 3000
	// The Linear zero, and the positive control that makes it mean something: the SAME instrument
	// must find the 73 narrow rows #102 measured in this corpus, or "0 wide" is a fact about a
	// scanner that read nothing.
	wideRowLinearMinShortRows = 73
)

type widthCensus struct {
	files, genuine, rows int
	wideRows, wideFiles  int
	shortRows            int
	reportedIssues       int // issues named by the PRODUCT's rendered warning
	reportedFiles        int
}

// censusRowWidths runs BOTH instruments over one directory.
func censusRowWidths(t *testing.T, dir string, jira bool) widthCensus {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		t.Skipf("no corpus at %s — this test SKIPS in CI by design; see the file comment. "+
			"csv_wide_row_test.go pins the same shapes as literals and runs everywhere.", dir)
	}
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	c := widthCensus{}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		c.files++

		// ── instrument 1: the bytes. Settings written as literals, matching newCSVSource.
		rd := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})))
		rd.FieldsPerRecord = -1
		rd.TrimLeadingSpace = true
		hdr, err := rd.Read()
		if err != nil {
			continue
		}
		fileRows, fileWide := 0, 0
		for {
			row, err := rd.Read()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				break // a file this reader cannot finish; the rows it did yield still count
			}
			fileRows++
			switch {
			case len(row) > len(hdr):
				fileWide++
			case len(row) < len(hdr):
				c.shortRows++
			}
		}
		c.rows += fileRows
		c.wideRows += fileWide
		if fileWide > 0 {
			c.wideFiles++
		}
		if len(hdr) > 1 && fileRows > 0 {
			c.genuine++
		}

		// ── instrument 2: the shipped importer over the same bytes.
		imp, _ := newTestImporter()
		var out *ImportResult
		if jira {
			out, err = imp.ImportJiraCSV(context.Background(), "ws", "team", bytes.NewReader(b))
		} else {
			out, err = imp.ImportLinearCSV(context.Background(), "ws", "team", bytes.NewReader(b))
		}
		if err != nil || out == nil {
			continue
		}
		for _, w := range out.Warnings {
			if !strings.Contains(w, "wider than the header") {
				continue
			}
			c.reportedFiles++
			var n int
			if _, err := fmt.Sscanf(w, "%d issue(s)", &n); err == nil {
				c.reportedIssues += n
			}
		}
	}
	return c
}

// TestCorpus_JiraWideRows is the Jira half of the population claim.
func TestCorpus_JiraWideRows(t *testing.T) {
	c := censusRowWidths(t, wideRowJiraCorpusDir, true)
	t.Logf("jira corpus: files=%d genuine=%d rows=%d wideRows=%d wideFiles=%d shortRows=%d "+
		"reportedIssues=%d reportedFiles=%d",
		c.files, c.genuine, c.rows, c.wideRows, c.wideFiles, c.shortRows, c.reportedIssues, c.reportedFiles)

	if c.genuine < wideRowJiraMinFiles || c.rows < wideRowJiraMinRows {
		t.Fatalf("the corpus at %s yielded %d genuine exports / %d rows, below the measured floor "+
			"%d/%d — an instrument that read nothing must not report a clean answer",
			wideRowJiraCorpusDir, c.genuine, c.rows, wideRowJiraMinFiles, wideRowJiraMinRows)
	}
	if c.wideRows < wideRowJiraMinWideRows || c.wideFiles < wideRowJiraMinWideFiles {
		t.Errorf("bytes instrument found %d wide rows in %d files, want at least %d in %d",
			c.wideRows, c.wideFiles, wideRowJiraMinWideRows, wideRowJiraMinWideFiles)
	}
	if c.reportedIssues < wideRowJiraMinReported {
		t.Errorf("the SHIPPED importer reported %d issues on wide rows, want at least %d — this is "+
			"the number the operator actually sees, and it is what this merge added",
			c.reportedIssues, wideRowJiraMinReported)
	}
	// THE GAP, ASSERTED RATHER THAN AVERAGED AWAY. The product must report FEWER than the bytes
	// instrument counts, because a wide row whose mapper also fails carries no note. If these two
	// ever agree, that limit has been closed and this comment is stale — which is worth being told.
	if c.reportedIssues >= c.wideRows {
		t.Errorf("product reported %d issues but the bytes instrument counted %d wide rows; they were "+
			"measured as UNEQUAL (a wide row that fails to map drops its notes in csvSource.Next). "+
			"If that is no longer true, csv_wide_row_corpus_census_test.go's two-instrument note is stale",
			c.reportedIssues, c.wideRows)
	}
}

// TestCorpus_LinearHasNoWideRows is the Linear half — a ZERO, and a zero is only a measurement if
// the instrument that produced it can be shown to see anything at all. It is positive-controlled by
// the 73 narrow rows the same scan must find in the same corpus.
func TestCorpus_LinearHasNoWideRows(t *testing.T) {
	c := censusRowWidths(t, wideRowLinearCorpusDir, false)
	t.Logf("linear corpus: files=%d genuine=%d rows=%d wideRows=%d shortRows=%d reportedIssues=%d",
		c.files, c.genuine, c.rows, c.wideRows, c.shortRows, c.reportedIssues)

	if c.genuine < wideRowLinearMinFiles || c.rows < wideRowLinearMinRows {
		t.Fatalf("the corpus at %s yielded %d genuine exports / %d rows, below the measured floor %d/%d",
			wideRowLinearCorpusDir, c.genuine, c.rows, wideRowLinearMinFiles, wideRowLinearMinRows)
	}
	// The positive control FIRST — before the zero is allowed to mean anything.
	if c.shortRows < wideRowLinearMinShortRows {
		t.Fatalf("the row-width scan found %d narrow rows in the Linear corpus, want at least %d "+
			"(#102's measurement). Until this passes, the zero below is a fact about a dead scanner",
			c.shortRows, wideRowLinearMinShortRows)
	}
	if c.wideRows != 0 {
		t.Errorf("the Linear corpus now carries %d wide rows; csv_wide_row_test.go records ZERO and "+
			"that number is quoted as a population claim — re-measure before trusting it", c.wideRows)
	}
	if c.reportedIssues != 0 {
		t.Errorf("the importer reported %d Linear issues on wide rows against a corpus with none", c.reportedIssues)
	}
}
