package importer

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"

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
type SourceRow struct {
	Issue  model.Issue
	RowNum int
	Err    error
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

	out := &ImportResult{Errors: []string{}}
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
		issueModel.CreatorID = "importer"

		if _, err := imp.issues.Create(ctx, issueModel); err != nil {
			out.Skipped++
			out.Errors = append(out.Errors, fmt.Sprintf("row %d: create: %v", row.RowNum, err))
			continue
		}
		out.Imported++
	}
	return out, nil
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
	rd := csv.NewReader(r)
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
	// Catch raggedly-short rows that csv.Read tolerates because of FieldsPerRecord=-1.
	if len(row) < s.expectedCols {
		return SourceRow{RowNum: s.rowNum, Err: fmt.Errorf("row %d: expected %d columns, got %d", s.rowNum, s.expectedCols, len(row))}, true
	}
	issueModel, err := s.mapper(s.ci, row)
	if err != nil {
		return SourceRow{RowNum: s.rowNum, Err: fmt.Errorf("row %d: %v", s.rowNum, err)}, true
	}
	return SourceRow{Issue: issueModel, RowNum: s.rowNum}, true
}
