package importer

import (
	"bufio"
	"bytes"
	"io"
)

// csv_bom.go — the three bytes a fifth of real Jira CSV exports begin with, and which made every
// one of those files import NOTHING.
//
// ⚠ THE DEFECT. A UTF-8 byte-order mark is EF BB BF at offset 0 of a file. encoding/csv does not
// strip it and neither does strings.TrimSpace — unicode.IsSpace(U+FEFF) is FALSE, because U+FEFF is
// category Cf (format), not whitespace. So the mark stays glued to the first header cell,
// buildIndex files that column under "\ufeffsummary", and jiraRowMapper's first act
//
//	title := ci.get(row, "Summary")
//	if title == "" { title = ci.get(row, "Title") }
//	if title == "" { return mappedIssue{}, errEmptyTitle }
//
// misses twice and refuses the row. Every row. The file lands zero issues and the operator is told
// {status:"failed", imported:0, skipped:0, failed:N} — #102's outcome exactly, on the other
// provider, and the one result nobody re-runs and nobody investigates as a Track bug.
//
// ⚠ MEASURED WHOLE-POPULATION over REAL Jira CSV exports committed to PUBLIC repositories
// (scripts/w34-jira-csv-corpus-probe.py, three negative controls first, raw bytes never utf-8-sig):
//
//	304 files · 130 distinct owners · 17,921 data rows
//	 66/304 files (21.7%) begin with EF BB BF
//	 63 of those carry it on `Summary` — the column the mapper reads for the title
//	 64/304 files (21.1%) have NO title column jiraRowMapper can find; 63 of them for this reason
//	 4,826/17,921 rows (26.9%) are in those files, and in every one the loss is 100% of the data
//
// The 64th file is the one honest refusal in the set: a filtered export carrying no title column at
// all, which errEmptyTitle is right to reject. This change does not touch it.
//
// ⚠ WHY NO EXISTING MEASUREMENT COULD SEE IT, and it is two separate blindnesses.
// scripts/w34-jira-csv-export-probe.py reads FIRST-HAND bytes from a real Jira's own export
// endpoint — better provenance than this corpus — but it is ONE instance, and that instance emits
// no BOM (re-measured this pass: its export begins `Summary,Issue ke`). One clean instance is not a
// population. And that probe decodes with `body.decode("utf-8-sig")`, the codec a probe reaches for
// by reflex, which eats the byte silently: had the instance emitted one, the probe would still have
// printed a clean header. A lenient decoder cannot see the byte that breaks the product.
//
// ⚠ THE LINEAR HALF WAS ALREADY MEASURED CLEAN — 0 of 45 files, from raw bytes, #102 — and that
// block wrote down precisely what a BOM WOULD cost that transport: buildIndex would key the first
// column "\ufeffid", `ID` would go unread, and #99's re-import duplication would come back. It was
// right about the mechanism and looked at the wrong provider. The fix here is at the SHARED seam,
// so both transports are covered by construction rather than by a second patch later.
//
// ⚠ THE STRIP IS THE FILE PREFIX AND NOTHING ELSE, and that restraint is the point. U+FEFF is a
// byte-order mark ONLY at offset 0; anywhere else it is ZERO WIDTH NO-BREAK SPACE, a character a
// Jira summary can legitimately contain. Stripping it from every header cell, or from cell values,
// would silently rewrite a customer's data to make a parsing problem go away.
// TestCSVBOM_OnlyTheFILEStartIsStripped holds both halves of that line.
//
// ⚠ IT IS DELIBERATELY NOT A DECODER. A `golang.org/x/text/encoding/unicode` BOM-override reader
// would also transcode UTF-16, which is a different claim about a different population and is not
// measured: 0 of the 304 files are UTF-16. Handling three bytes is what the measurement supports.

// utf8BOM is EF BB BF — U+FEFF encoded in UTF-8.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// skipUTF8BOM returns a reader positioned past a leading UTF-8 BOM, or over the original bytes if
// there is none.
//
// ⚠ IT MUST NOT CONSUME ANYTHING WHEN THERE IS NO BOM, which is why it Peeks rather than Reads. A
// short file is the boundary that makes that non-obvious: bufio.Peek(3) returns a non-nil error
// when fewer than three bytes are available, so a one-byte file takes the no-BOM path and its byte
// survives. TestCSVBOM_AFileThatIsOnlyABOMIsNotAnError pins the three cases around that edge.
//
// The returned *bufio.Reader is handed straight to csv.NewReader, which wraps its argument in a
// bufio.Reader of its own only when the argument is not already one — so this adds no second layer
// of buffering.
func skipUTF8BOM(r io.Reader) io.Reader {
	br := bufio.NewReader(r)
	if b, err := br.Peek(len(utf8BOM)); err == nil && bytes.Equal(b, utf8BOM) {
		_, _ = br.Discard(len(utf8BOM))
	}
	return br
}
