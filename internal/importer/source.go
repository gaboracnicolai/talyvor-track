package importer

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/talyvor/track/internal/model"
)

// source.go — the import SOURCE seam (T8 live-importer, Build A: behaviour-neutral extraction).
//
// An IssueSource yields import rows one at a time, decoupled from WHERE they come from — a CSV file today, a
// paginated Linear/Jira API in Build C. The write pipeline (run) never sees pages or file formats; it just
// pulls a flat stream of rows and writes each via issueCreator. A source paginates INTERNALLY, so pagination
// is expressible without ever holding all rows in memory — a CSV source reads row-by-row; an API source can
// fetch a page, drain it, fetch the next, all behind Next().

// SourceRow is one yielded row: a mapped Issue (Err == nil) OR a per-row parse/map error (Err != nil) that
// the pipeline tallies in Skipped — exactly as the CSV path does today. RowNum labels the row for the
// pipeline's create-error message ("row N: create: ..."), so error strings stay byte-identical after the
// extraction. The Issue carries the mapped fields only (Identifier/LensFeature left unset here — Build C
// populates them); the pipeline stamps WorkspaceID/TeamID/CreatorID.
// Notes carries the fields this row's mapper could not place on Track's scale. The row still
// imports (the fallback value is unchanged); run tallies the notes into ImportResult.Warnings so
// a degraded import stops reporting itself as a clean one.
type SourceRow struct {
	Issue  model.Issue
	RowNum int
	Err    error
	Notes  []FieldNote
}

// IssueSource is the extracted seam. Next returns the next row and ok=false when the source is exhausted.
// Construction-time fatal errors (e.g. an unreadable CSV header) surface from the source's constructor, not
// from Next — so the pipeline only ever deals with per-row outcomes.
type IssueSource interface {
	Next() (row SourceRow, ok bool)
}

// run is the shared write pipeline: pull rows from any source, stamp tenancy + creator, write via
// issueCreator, and tally the result EXACTLY as before. Behaviour is identical to the pre-extraction
// CSV-bound run — the only change is that the row supply is now an IssueSource, not a hard-wired csv.Reader.
func (imp *Importer) run(ctx context.Context, workspaceID, teamID string, src IssueSource) (*ImportResult, error) {
	if workspaceID == "" || teamID == "" {
		return nil, errors.New("importer: workspace_id and team_id are required")
	}

	out := &ImportResult{Errors: []string{}, Warnings: []string{}}
	degraded := map[FieldNote]int{}
	for {
		row, ok := src.Next()
		if !ok {
			break
		}
		if row.Err != nil {
			out.Skipped++
			out.Errors = append(out.Errors, row.Err.Error())
			continue
		}
		issueModel := row.Issue
		issueModel.WorkspaceID = workspaceID
		issueModel.TeamID = teamID
		issueModel.CreatorID = model.ImporterCreatorID // the ONLY provenance a re-import can key on — see the const

		// An issue carrying an Identifier came from a provider that names its issues → route through
		// C.2's re-import upsert (land on the provider-key, INSERT-or-UPDATE). No Identifier → Create,
		// which DERIVES `<team>-<n>`. The upserter is nil for CSV-only backing stores, so that branch
		// is never taken there.
		//
		// ⚠ "CSV rows carry no Identifier" WAS TRUE OF EVERY CSV TRANSPORT AND IS NOW TRUE OF NONE OF
		// THEM. A Jira CSV export's `Issue key` column names the issue and jiraRowMapper reads it
		// (#98); a Linear CSV export's `ID` column names the issue and linearRowMapper reads it. Both
		// CSV transports therefore take the SAME branch as their API twin. Before each of those, a
		// re-import of byte-identical export bytes wrote a second copy of every row and reported the
		// job `succeeded imported=N skipped=0 failed=0` — measured through THIS pipeline on real
		// Postgres, 2 issues to 4, on both transports. See jira_csv_issue_key_job_test.go and
		// linear_csv_issue_id_job_test.go.
		//
		// ⚠ WHAT REMAINS KEYLESS IS A ROW, NOT A TRANSPORT: an export filtered down past its key
		// column yields "" and still takes Create. That is the fail-safe both column constants are
		// written around, not an oversight.
		if issueModel.Identifier != "" && imp.upserter != nil {
			if _, _, err := imp.upserter.UpsertByIdentifier(ctx, issueModel); err != nil {
				// A REFUSAL IS NOT A FAILURE. #71's upsert predicate declines to overwrite an issue a
				// human created; the row not landing is the policy working. It is counted apart so the
				// job row can say which of the two happened — issue.Store exported the sentinel for
				// exactly this and, until now, nobody asked.
				if errors.Is(err, model.ErrIdentifierNotImportOwned) {
					out.Refused++
				} else {
					out.Skipped++
				}
				out.Errors = append(out.Errors, fmt.Sprintf("row %d: upsert: %v", row.RowNum, err))
				continue
			}
		} else if _, err := imp.issues.Create(ctx, issueModel); err != nil {
			out.Skipped++
			out.Errors = append(out.Errors, fmt.Sprintf("row %d: create: %v", row.RowNum, err))
			continue
		}
		// Tallied only AFTER the write succeeded — a row that never landed is a Skipped row,
		// not a degraded one, and counting it in both places would double-report one failure.
		for _, n := range row.Notes {
			degraded[n]++
		}
		out.Imported++
	}
	out.Warnings = renderWarnings(degraded)
	return out, nil
}

// maxWarningExemplars bounds how many DISTINCT values of one note kind are listed individually.
//
// ⚠ IT BOUNDS ENUMERATION, NEVER COUNTING. Beyond it a single summary line reports how many further
// distinct values there were and how many issues they covered, so an import is never quieter about
// what it could not place than it is today — only less repetitive about it.
//
// TEN because the exemplars exist to show a SHAPE, not to inventory a column: a tenant whose date
// serialisation or status vocabulary differs shows that in the first few values, and the counts
// carry the size. The number is deliberately a named constant with this reasoning attached rather
// than a literal in a loop, because the right value is a judgement and the next person should be
// able to see what it was judged against.
const maxWarningExemplars = 10

// warningSummaryPrefix opens the one line that stands for everything past the bound. It sorts before
// every rendered sentence (which start with a field name or "unrecognised"/"no"), so the summaries
// group at the top of a sorted report instead of hiding among their own exemplars.
const warningSummaryPrefix = "+ "

// renderWarnings turns the tally into one sorted, self-describing line per distinct note. The note
// keys on the PATH as well as the value (FieldNote.Via), so a tenant whose categories resolved some
// rows and were absent on others gets both truths in one report instead of one averaged sentence.
// Sorted because an unordered report cannot be diffed between two runs of the same import.
func renderWarnings(degraded map[FieldNote]int) []string {
	// Group by everything EXCEPT the value, so "this kind of finding" is one group however many
	// different values arrived under it. The bound is applied per group: a status column full of
	// free text must not crowd a real date finding out of the report, and vice versa.
	type kind struct {
		Field, Mapped, Via, ViaValue string
		ViaResolved                  bool
	}
	groups := map[kind][]FieldNote{}
	for n := range degraded {
		k := kind{n.Field, n.Mapped, n.Via, n.ViaValue, n.ViaResolved}
		groups[k] = append(groups[k], n)
	}

	out := make([]string, 0, len(degraded))
	for _, notes := range groups {
		// Sorted so the exemplars are the SAME ones on every run of the same import — the bound
		// picks a subset, and a subset chosen by map iteration order would make two runs of one
		// import undiffable, which is the property this function sorts for in the first place.
		sort.Slice(notes, func(i, j int) bool { return notes[i].Value < notes[j].Value })

		shown := notes
		if len(notes) > maxWarningExemplars {
			shown = notes[:maxWarningExemplars]
		}
		for _, n := range shown {
			out = append(out, n.render(degraded[n]))
		}
		if len(notes) > maxWarningExemplars {
			restValues, restIssues := 0, 0
			for _, n := range notes[maxWarningExemplars:] {
				restValues++
				restIssues += degraded[n]
			}
			out = append(out, fmt.Sprintf(
				"%s%d further distinct %s value(s) across %d issue(s) not listed individually (%d shown above)",
				warningSummaryPrefix, restValues, notes[0].Field, restIssues, len(shown)))
		}
	}
	sort.Strings(out)
	return out
}

// ─── CSV source ─────────────────────────────────────────────
// csvSource wraps today's CSV parsing behind IssueSource. Its Next replicates the exact per-row logic the
// old run inlined: EOF ends the stream, a read error / a raggedly-short row / a mapper error each become a
// skipped SourceRow with the identical "row N: ..." message.

type csvSource struct {
	rd           *csv.Reader
	ci           columnIndex
	mapper       rowMapper
	expectedCols int
	rowNum       int
	done         bool
}

// newCSVSource reads the header (fatal errors surface here, matching the old run: EOF header ⇒ an empty
// source that yields nothing; a non-EOF read error ⇒ returned as an error).
func newCSVSource(r io.Reader, mapper rowMapper) (*csvSource, error) {
	// A leading UTF-8 BOM is consumed HERE, at the one seam both CSV transports pass through, and
	// not in either mapper. Neither csv.Reader nor strings.TrimSpace removes it, so without this the
	// three bytes stay glued to the first header cell and buildIndex files that column under a name
	// no lookup can spell — which on a Jira export is `Summary`, so every row is refused for having
	// no title and the whole file imports nothing. MEASURED at 66 of 304 real Jira exports; see
	// csv_bom.go for the population and for why the strip is the file prefix only.
	rd := csv.NewReader(skipUTF8BOM(r))
	rd.FieldsPerRecord = -1
	rd.TrimLeadingSpace = true

	header, err := rd.Read()
	if errors.Is(err, io.EOF) {
		return &csvSource{done: true}, nil // empty input ⇒ zero rows, no error
	}
	if err != nil {
		return nil, fmt.Errorf("importer: read header: %w", err)
	}
	return &csvSource{
		rd:           rd,
		ci:           buildIndex(header),
		mapper:       mapper,
		expectedCols: len(header),
		rowNum:       1, // header was row 1
	}, nil
}

func (s *csvSource) Next() (SourceRow, bool) {
	if s.done {
		return SourceRow{}, false
	}
	row, err := s.rd.Read()
	if errors.Is(err, io.EOF) {
		s.done = true
		return SourceRow{}, false
	}
	s.rowNum++
	if err != nil {
		return SourceRow{RowNum: s.rowNum, Err: fmt.Errorf("row %d: %v", s.rowNum, err)}, true
	}
	// A raggedly-short row that csv.Read tolerates because of FieldsPerRecord=-1 is REPORTED and
	// mapped, not refused.
	//
	// ⚠ THIS USED TO BE A REFUSAL AND THE REFUSAL WAS THE DEFECT. It read:
	//
	//	if len(row) < s.expectedCols { return ...Err: "row N: expected X columns, got Y" }, true
	//
	// which put a whole-header width test in front of a mapper that reads twelve columns, all of
	// them in the first two thirds of every measured header. Two things make that wrong rather than
	// strict, and both are measured:
	//
	//  · columnIndex.get and getAll are ALREADY bounds-safe and say so — get's doc comment reads
	//    "Returns "" if the column doesn't exist OR THE ROW IS TOO SHORT — that lets row-level
	//    validation focus on what's required (title) rather than how the export was shaped". Since
	//    every index comes from the header (so is < expectedCols) and no narrower row could reach a
	//    mapper, the bounds guard in BOTH accessors was structurally unreachable. The package
	//    documented the tolerant behaviour and three lines of this function prevented it.
	//
	//  · scripts/w34-linear-csv-short-row-probe.py, whole-population over the same corpus of REAL
	//    Linear exports #99/#100/#101 used: 45 files · 3,099 data rows · 73 rows (2.4%) narrower
	//    than their header, from TWO UNRELATED OWNERS, every one short by EXACTLY ONE column, and in
	//    all 73 the missing header is `Roadmaps` — the last column of the 30-wide shape, empty in
	//    every file that does emit it. In BOTH files that carry them the short rows are 100% of the
	//    data, so those two exports imported NOTHING and were reported to the operator as
	//    {status:"failed", imported:0, failed:N}.
	//
	// ⚠ ALIGNMENT WAS THE THING THAT HAD TO BE MEASURED, because importing a MISALIGNED row would
	// write garbage and refusing it would be right. For all 73, at their header index: ID matches
	// ^PREFIX-<int>$ 73/73 · Title non-empty 73/73 · Status in mapLinearStatus 73/73 · Priority in
	// mapLinearPriority 73/73 · Created DATE-SHAPED at its index 73/73.
	//
	// ⚠ THAT LAST ONE IS THE ALIGNMENT QUESTION AND NOT THE PARSING ONE, and the two are worth
	// keeping apart because the answers differ: `Created` PARSES under a pinned layout on 0 of the
	// 73. All 73 carry JavaScript's Date.toString — the shape #101 measured at 25.3% of real
	// `Updated` cells and deliberately did NOT add to linearCSVTimeLayouts. So every one of these
	// rows now imports AND reports an unpinned date, which is two true sentences where there used
	// to be one false one ("row 2: expected 30 columns, got 29"). An earlier draft of the probe
	// measured date-SHAPEDNESS and wrote the number down under the parsing predicate's name; the
	// two are separate lines in scripts/w34-linear-csv-short-row-probe.py for that reason.
	//
	// ⚠ THE REFUSAL IS NARROWED, NOT DELETED, and the narrowing is the mapper's own required-field
	// check: a row cut back past `Title` still does not land, refused by errEmptyTitle. That case is
	// pinned by TestLinearCSV_ARowTruncatedPastTheTitleIsStillRefused, which asserts the ERROR
	// MESSAGE rather than the skip count — both this code and the old code skip that row, and only
	// the message says which one did it.
	//
	// ⚠ AND IT IS NEVER SILENT. The row carries a note, so an export whose truncation DID reach a
	// column the mapper reads says so, once, with a count, instead of the caller seeing a column of
	// empties that looks exactly like a column the provider left blank. That also moves this class
	// out of the UNBOUNDED ImportResult.Errors and into the bounded Warnings #80 built.
	var notes []FieldNote
	if len(row) < s.expectedCols {
		notes = append(notes, FieldNote{
			Field: fieldRowWidth,
			Value: fmt.Sprintf("%d of %d columns", len(row), s.expectedCols),
			Via:   viaShortRow,
		})
	}
	mapped, err := s.mapper(s.ci, row)
	if err != nil {
		return SourceRow{RowNum: s.rowNum, Err: fmt.Errorf("row %d: %v", s.rowNum, err)}, true
	}
	return SourceRow{Issue: mapped.issue, RowNum: s.rowNum, Notes: concatNotes(notes, mapped.notes)}, true
}
