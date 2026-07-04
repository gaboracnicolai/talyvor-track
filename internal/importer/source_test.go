package importer

import (
	"context"
	"testing"

	"github.com/talyvor/track/internal/model"
)

// sliceSource is a trivial in-memory IssueSource — the seam's first NON-CSV exercise. Builds B (async job)
// and C (Linear/Jira API) plug real sources into the same run + tenancy path this proves works.
type sliceSource struct {
	rows []SourceRow
	i    int
}

func (s *sliceSource) Next() (SourceRow, bool) {
	if s.i >= len(s.rows) {
		return SourceRow{}, false
	}
	r := s.rows[s.i]
	s.i++
	return r, true
}

// TestRun_NonCSVSource_WritesEachRow — a non-CSV source flows through the exact same write pipeline: rows are
// created via issueCreator, and run stamps WorkspaceID/TeamID/CreatorID just like the CSV path.
func TestRun_NonCSVSource_WritesEachRow(t *testing.T) {
	imp, store := newTestImporter()
	src := &sliceSource{rows: []SourceRow{
		{Issue: model.Issue{Title: "from-api-1"}, RowNum: 1},
		{Issue: model.Issue{Title: "from-api-2"}, RowNum: 2},
	}}

	out, err := imp.run(context.Background(), "ws-x", "team-x", src)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out.Imported != 2 || out.Skipped != 0 {
		t.Fatalf("Imported=%d Skipped=%d, want 2/0 (a non-CSV source must flow through the same pipeline)", out.Imported, out.Skipped)
	}
	if len(store.created) != 2 {
		t.Fatalf("store got %d issues, want 2", len(store.created))
	}
	for _, iss := range store.created {
		if iss.WorkspaceID != "ws-x" || iss.TeamID != "team-x" || iss.CreatorID != "importer" {
			t.Errorf("run must stamp ws/team/creator identically to the CSV path: %+v", iss)
		}
	}
	if store.created[0].Title != "from-api-1" || store.created[1].Title != "from-api-2" {
		t.Errorf("rows not written in order: %+v", store.created)
	}
}
