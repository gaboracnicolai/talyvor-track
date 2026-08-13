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

	// NotesIfUpdated are reported ONLY IF the write turned out to overwrite an issue that already
	// existed. A source knows what its input said; only run() knows whether the row INSERTed or
	// UPDATEd, and the difference decides whether a note is a true sentence or a false alarm. Empty
	// for every API source: both request the fields they map, so no column can be absent.
	NotesIfUpdated []FieldNote
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
	// The identifiers THIS import has already written. A second row under one of them does not
	// create a second issue — it overwrites the first row's — and nothing counted or said so.
	// See duplicate_identifier.go for the whole-population measurement and for what this costs.
	written := map[string]struct{}{}
	lastRow := 0
	for {
		// THE CONTEXT IS CONSULTED HERE AND, UNTIL THIS LINE, WAS CONSULTED NOWHERE. run took
		// ctx and handed it to the STORE; the row loop never read it. runner.go says, right
		// above the call, "an import MUST stop when the process is going down" — and on SIGTERM
		// this loop kept pulling every remaining row, handing each to a store whose every call
		// now fails, and counting each one in `failed`. MEASURED at 977f926: 50 of 50 rows
		// pulled after cancellation and all 50 reported failed; on a linear_api job, 7 further
		// PAGES fetched from the provider after the process had been told to stop, because on
		// that transport "pull the next row" is an HTTP request. See run_context_test.go.
		//
		// ⚠ THE STOP IS RECORDED, AND THAT IS THE LOAD-BEARING HALF. Breaking out quietly leaves
		// Skipped and Refused at zero with rows unread, which is `unlanded == 0` — so
		// terminalStatus would call a truncated import SUCCEEDED and nothing would ever say
		// otherwise. That is a worse defect than the one being fixed, so the stop sets a flag
		// terminalStatus reads and appends the sentence summarise renders.
		if err := ctx.Err(); err != nil {
			out.stopped = true
			out.Errors = append(out.Errors, fmt.Sprintf("stopped after row %d: %v", lastRow, err))
			break
		}
		row, ok := src.Next()
		if !ok {
			break
		}
		lastRow = row.RowNum
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
		//
		// overwroteExisting is the second return of UpsertByIdentifier, inverted. It has been
		// computed by the statement itself (`RETURNING (xmax = 0) AS inserted`) since #71 and thrown
		// away here ever since — and it is the ONLY thing that can tell a report about a deleted
		// value from a false alarm on a first import. See row.NotesIfUpdated.
		overwroteExisting := false
		// dupNote is set when the write landed on an identifier THIS import had already written.
		// It is computed on the write branch and applied below, alongside NotesIfUpdated, for the
		// same reason that one is: only the pipeline knows it, and only a row that actually landed
		// has degraded anything.
		var dupNote []FieldNote
		if issueModel.Identifier != "" && imp.upserter != nil {
			_, inserted, err := imp.upserter.UpsertByIdentifier(ctx, issueModel)
			overwroteExisting = !inserted
			if err != nil {
				// A REFUSAL IS NOT A FAILURE. #71's upsert predicate declines to overwrite an issue a
				// human created; the row not landing is the policy working. It is counted apart so the
				// job row can say which of the two happened — issue.Store exported the sentinel for
				// exactly this and, until now, nobody asked.
				//
				// ⚠ THE SECOND REFUSAL IS COUNTED THE SAME AND REPORTED DIFFERENTLY. A key that
				// resolves to another TEAM's import is refused for the same reason a human's issue
				// is — this import does not own that row — so it belongs in the same counter and
				// must not reach `failed`. What it must not share is the SENTENCE: "was not
				// created by an import" is false of a row the operator's own earlier import
				// created, and it sends them looking for a duplicate that does not exist. The
				// second tally exists for summarise and for nothing else; it is unexported, so the
				// shipped ImportResult JSON shape is unchanged (see its comment).
				switch {
				case errors.Is(err, model.ErrIdentifierOwnedByAnotherTeam):
					out.Refused++
					out.refusedOtherTeam++
				case errors.Is(err, model.ErrIdentifierNotImportOwned):
					out.Refused++
				default:
					out.Skipped++
				}
				out.Errors = append(out.Errors, fmt.Sprintf("row %d: upsert: %v", row.RowNum, err))
				continue
			}
			// ⚠ AFTER the error check, never before it: a row that did not land wrote nothing and
			// overwrote nothing, so its key must not be remembered as written.
			//
			// ⚠ AND THE REACHABLE CASE IS AN ORDINARY WRITE FAILURE, NOT A REFUSAL — measured, not
			// assumed. The first version of this comment named the refusal, and control C2 (moving
			// the two lines above the error check) left the refusal test GREEN: a refusal is decided
			// by the conflicting row's creator and team, neither of which changes during a run, so a
			// key refused once is refused every time and the later row never reaches the note. What
			// DOES reach it is a dropped connection or a statement timeout on one row and a success
			// on a later row of the same key, which the wrong ordering would report to the operator
			// as "your export names this issue twice". See
			// TestRun_ARowThatDidNotLandDoesNotSeedTheDuplicateReport, which drives both.
			if _, again := written[issueModel.Identifier]; again {
				dupNote = []FieldNote{{
					Field: fieldDuplicateIdentifier,
					Value: issueModel.Identifier,
					Via:   viaDuplicateInSameImport,
				}}
			}
			written[issueModel.Identifier] = struct{}{}
		} else if _, err := imp.issues.Create(ctx, issueModel); err != nil {
			out.Skipped++
			out.Errors = append(out.Errors, fmt.Sprintf("row %d: create: %v", row.RowNum, err))
			continue
		}
		// Tallied only AFTER the write succeeded — a row that never landed is a Skipped row,
		// not a degraded one, and counting it in both places would double-report one failure.
		//
		// ⚠ concatNotes, NOT append: appending into row.Notes writes through the mapper's own
		// backing array, which is the aliasing hazard that function was extracted for.
		notes := row.Notes
		if overwroteExisting {
			notes = concatNotes(row.Notes, row.NotesIfUpdated)
		}
		if len(dupNote) > 0 {
			notes = concatNotes(notes, dupNote)
		}
		for _, n := range notes {
			degraded[n]++
		}
		out.Imported++
	}
	out.Warnings = renderWarnings(degraded)
	return out, nil
}

// maxConsecutiveEmptyPages bounds how far a source will walk through pages the provider says are
// not the last but that carry no rows, before it stops and REPORTS that it stopped.
//
// ⚠ IT IS A BOUND ON A BROKEN PROVIDER, NOT A PAGE BUDGET. A page can legitimately come back short
// — Jira's own search doc says issues appear "where the user has Browse projects permission ...
// issue-level security permission to view the issue", i.e. the page is filtered AFTER it is cut, and
// zero is short — so an empty non-final page must be walked past, not treated as the end. What must
// not happen is walking for ever, so the walk is bounded and the bound is observable: hitting it
// yields a SourceRow.Err, which run() counts and the job row shows. Contrast the behaviour this
// replaces, where the FIRST empty page ended the import and the job recorded `succeeded imported=0`.
//
// 200 because at the shipped page size (100) it tolerates 20,000 consecutive rows the credential
// cannot see before it calls the pagination broken — well past any filtering an import is likely to
// meet, and still a hard stop. Like maxWarningExemplars, the number is a judgement and is written
// down with what it was judged against rather than left as a literal in a loop.
const maxConsecutiveEmptyPages = 200

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
	// Group by everything EXCEPT the values, so "this kind of finding" is one group however many
	// different values arrived under it. The bound is applied per group: a status column full of
	// free text must not crowd a real date finding out of the report, and vice versa.
	//
	// ⚠⚠ ViaValue IS A VALUE AND WAS IN THIS KEY, WHICH IS THE WHOLE OF THE BOUND'S ONE HOLE. It is
	// a provider cell copied verbatim — jiraCSVStatusCategory passes the uploaded CSV's "Status
	// Category" text straight through — so a distinct cell per row was a distinct KIND per row, each
	// holding one note, each under the bound, each rendering its own line. MEASURED through the
	// shipped jiraRowMapper: 5,000 rows ⇒ 5,002 lines with that column, 13 without it.
	// See warning_kind_key_test.go for the census and warning_kind_key_job_test.go for the
	// cardinality it put in a TEXT[].
	//
	// ⚠ IT IS STILL RENDERED, ONLY NOT GROUPED ON. The exemplars name their category exactly as
	// before; what changes is that ten of them stand for the rest instead of all of them being
	// listed. Nothing merges that carried a distinct MEANING: for a RESOLVED note the category is
	// what decided Mapped, and Mapped is still in the key, so `To Do` and `In Progress` remain two
	// findings with their own counts. For an UNRESOLVED one the category decided nothing — which is
	// precisely why it must not mint a kind.
	type kind struct {
		Field, Mapped, Via string
		ViaResolved        bool
	}
	groups := map[kind][]FieldNote{}
	for n := range degraded {
		k := kind{n.Field, n.Mapped, n.Via, n.ViaResolved}
		groups[k] = append(groups[k], n)
	}

	out := make([]string, 0, len(degraded))
	for _, notes := range groups {
		// Sorted so the exemplars are the SAME ones on every run of the same import — the bound
		// picks a subset, and a subset chosen by map iteration order would make two runs of one
		// import undiffable, which is the property this function sorts for in the first place.
		//
		// ⚠ THE TIE-BREAK IS LOAD-BEARING NOW THAT ViaValue IS OUT OF THE KEY. Two notes can share
		// a Value and differ only in ViaValue (one unrecognised status under many unplaceable
		// categories is the reachable case), and Value alone would order them by map iteration.
		sort.Slice(notes, func(i, j int) bool {
			if notes[i].Value != notes[j].Value {
				return notes[i].Value < notes[j].Value
			}
			return notes[i].ViaValue < notes[j].ViaValue
		})

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
			// ⚠ "finding(s)", NOT "value(s)" — THE NUMBER IS THE COUNT OF UNLISTED NOTES AND THAT IS
			// NO LONGER THE SAME AS A COUNT OF DISTINCT FIELD VALUES. A group may now vary along
			// Value or ViaValue or both, so one unrecognised status under 3,000 unplaceable
			// categories would have this line claim 2,990 distinct STATUS VALUES where there is
			// exactly one. The count is unchanged and every other clause is unchanged; the noun is
			// what the count has always actually been.
			out = append(out, fmt.Sprintf(
				"%s%d further distinct %s finding(s) across %d issue(s) not listed individually (%d shown above)",
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
	//
	// ⚠ AND THE OTHER DIRECTION HAD NO BRANCH AT ALL. `len(row) > s.expectedCols` was the one
	// row/header mismatch this parser KNOWS about and said nothing about, and it is the worse of
	// the two: a narrow row reads as empty (a loss you can see), a WIDE row shifts every column
	// after the surplus cell, so the mapper reads a NEIGHBOUR'S value — present, plausible, wrong,
	// and invisible to everything downstream. MEASURED whole-population in Go under these exact
	// reader settings: 11 of 31,103 rows across 2 of 340 real Jira exports, from two unrelated
	// instances, 0 of 3,164 Linear rows. In `0347210d…` it is 10 of 10 rows — the export carries
	// two `Labels` cells against a header declaring one — so `Description`, the next column the
	// mapper reads, holds a LABEL on every issue and the import reports
	// {imported:10, skipped:0, failed:0} with seven warnings, none of them about the row.
	// See csv_wide_row_test.go for the population, the second instance, and the provenance limit.
	//
	// ⚠ IT REPORTS, IT DOES NOT REFUSE — the precedent is four paragraphs up: refusing the narrow
	// row was itself the defect. No row changes where it lands.
	var notes []FieldNote
	switch {
	case len(row) < s.expectedCols:
		notes = append(notes, FieldNote{
			Field: fieldRowWidth,
			Value: fmt.Sprintf("%d of %d columns", len(row), s.expectedCols),
			Via:   viaShortRow,
		})
	case len(row) > s.expectedCols:
		notes = append(notes, FieldNote{
			Field: fieldRowWidth,
			Value: fmt.Sprintf("%d of %d columns", len(row), s.expectedCols),
			Via:   viaWideRow,
		})
	}
	mapped, err := s.mapper(s.ci, row)
	if err != nil {
		return SourceRow{RowNum: s.rowNum, Err: fmt.Errorf("row %d: %v", s.rowNum, err)}, true
	}
	return SourceRow{
		Issue:  mapped.issue,
		RowNum: s.rowNum,
		Notes:  concatNotes(notes, mapped.notes),
		// Only the pipeline can decide whether these apply — they are true of a row that OVERWROTE
		// an existing issue and of no other. See csv_clobbered_columns.go.
		NotesIfUpdated: mapped.onUpdate,
	}, true
}
