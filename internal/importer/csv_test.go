package importer

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/talyvor/track/internal/model"
)

// fakeIssueStore records every Create call so the test can assert on
// the mapped fields. Returning a stable shape (id derived from title)
// keeps assertions independent of insertion order.
type fakeIssueStore struct {
	mu       sync.Mutex
	created  []model.Issue
	failNext bool
}

func (f *fakeIssueStore) Create(_ context.Context, in model.Issue) (*model.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext {
		f.failNext = false
		return nil, ErrTestForcedFailure
	}
	f.created = append(f.created, in)
	out := in
	out.ID = "i-" + in.Title
	out.Identifier = "TEST-" + in.Title
	return &out, nil
}

// ErrTestForcedFailure is used in tests to simulate a store error
// without depending on pgx-specific error types.
var ErrTestForcedFailure = &testErr{msg: "forced failure"}

type testErr struct{ msg string }

func (e *testErr) Error() string { return e.msg }

func newTestImporter() (*Importer, *fakeIssueStore) {
	store := &fakeIssueStore{}
	return New(store), store
}

const linearCSV = `ID,Title,Description,Status,Priority,Assignee,Labels,Created,Completed
LIN-1,Bug in cache layer,Cache invalidation is broken,Backlog,Urgent,alice,"bug,perf",2026-01-01,
LIN-2,Add dark mode,Long-requested,Done,Medium,bob,ui,2026-01-02,2026-02-15
LIN-3,Refactor auth,Token rotation,In Progress,High,,refactor,2026-01-03,
`

func TestImportLinearCSV_ImportsThreeIssues(t *testing.T) {
	imp, store := newTestImporter()
	out, err := imp.ImportLinearCSV(context.Background(), "ws-1", "team-1", strings.NewReader(linearCSV))
	if err != nil {
		t.Fatalf("ImportLinearCSV: %v", err)
	}
	if out.Imported != 3 {
		t.Errorf("Imported = %d, want 3", out.Imported)
	}
	if out.Skipped != 0 {
		t.Errorf("Skipped = %d, want 0", out.Skipped)
	}
	if len(store.created) != 3 {
		t.Fatalf("store got %d issues, want 3", len(store.created))
	}
	first := store.created[0]
	if first.WorkspaceID != "ws-1" || first.TeamID != "team-1" {
		t.Errorf("workspace/team not propagated: %+v", first)
	}
	if first.Title != "Bug in cache layer" {
		t.Errorf("title = %q", first.Title)
	}
}

func TestImportLinearCSV_MapsStatusCorrectly(t *testing.T) {
	imp, store := newTestImporter()
	csv := `ID,Title,Description,Status,Priority,Assignee,Labels,Created,Completed
A,Backlog item,d,Backlog,No priority,,,2026-01-01,
B,Todo item,d,Todo,No priority,,,2026-01-01,
C,Active item,d,In Progress,No priority,,,2026-01-01,
D,Done item,d,Done,No priority,,,2026-01-01,2026-01-05
E,Cancelled item,d,Cancelled,No priority,,,2026-01-01,
`
	if _, err := imp.ImportLinearCSV(context.Background(), "ws", "team", strings.NewReader(csv)); err != nil {
		t.Fatalf("ImportLinearCSV: %v", err)
	}
	if len(store.created) != 5 {
		t.Fatalf("got %d issues, want 5", len(store.created))
	}
	wantStatus := []model.IssueStatus{
		model.StatusBacklog,
		model.StatusTodo,
		model.StatusInProgress,
		model.StatusDone,
		model.StatusCancelled,
	}
	for i, got := range store.created {
		if got.Status != wantStatus[i] {
			t.Errorf("row %d status = %q, want %q", i, got.Status, wantStatus[i])
		}
	}
}

func TestImportLinearCSV_ParsesLabelsAsArray(t *testing.T) {
	imp, store := newTestImporter()
	csv := `ID,Title,Description,Status,Priority,Assignee,Labels,Created,Completed
A,T,d,Backlog,No priority,,"bug,perf,critical",2026-01-01,
`
	if _, err := imp.ImportLinearCSV(context.Background(), "ws", "team", strings.NewReader(csv)); err != nil {
		t.Fatalf("ImportLinearCSV: %v", err)
	}
	if len(store.created) != 1 {
		t.Fatalf("got %d, want 1", len(store.created))
	}
	got := store.created[0].Labels
	wantSet := map[string]bool{"bug": true, "perf": true, "critical": true}
	if len(got) != 3 {
		t.Errorf("labels = %v, want 3 entries", got)
	}
	for _, l := range got {
		if !wantSet[l] {
			t.Errorf("unexpected label %q", l)
		}
	}
}

const jiraCSV = `Issue Key,Summary,Description,Status,Priority,Assignee,Labels,Created,Resolved
PROJ-1,Login broken,Repro: 500 on POST,To Do,Highest,charlie,backend,2026-01-01,
PROJ-2,Slow query,N+1 on profile load,In Progress,Major,dave,backend,2026-01-02,
PROJ-3,Update docs,Add MCP section,Done,Low,erin,docs,2026-01-03,2026-02-01
`

func TestImportJiraCSV_ImportsIssuesCorrectly(t *testing.T) {
	imp, store := newTestImporter()
	out, err := imp.ImportJiraCSV(context.Background(), "ws-1", "team-1", strings.NewReader(jiraCSV))
	if err != nil {
		t.Fatalf("ImportJiraCSV: %v", err)
	}
	if out.Imported != 3 {
		t.Errorf("Imported = %d, want 3", out.Imported)
	}
	if len(store.created) != 3 {
		t.Fatalf("store got %d, want 3", len(store.created))
	}
	if store.created[0].Title != "Login broken" {
		t.Errorf("title = %q", store.created[0].Title)
	}
	if store.created[0].Description != "Repro: 500 on POST" {
		t.Errorf("desc = %q", store.created[0].Description)
	}
}

func TestImportJiraCSV_MapsJiraPriorityToTrackPriority(t *testing.T) {
	imp, store := newTestImporter()
	csv := `Issue Key,Summary,Description,Status,Priority,Assignee,Labels,Created,Resolved
A,t,d,To Do,Highest,,,2026-01-01,
B,t,d,To Do,High,,,2026-01-01,
C,t,d,To Do,Medium,,,2026-01-01,
D,t,d,To Do,Low,,,2026-01-01,
E,t,d,To Do,Lowest,,,2026-01-01,
F,t,d,To Do,Major,,,2026-01-01,
G,t,d,To Do,Trivial,,,2026-01-01,
H,t,d,To Do,(unset),,,2026-01-01,
`
	if _, err := imp.ImportJiraCSV(context.Background(), "ws", "team", strings.NewReader(csv)); err != nil {
		t.Fatalf("ImportJiraCSV: %v", err)
	}
	want := []model.IssuePriority{
		model.PriorityUrgent, // Highest → urgent
		model.PriorityHigh,   // High → high
		model.PriorityMedium, // Medium → medium
		model.PriorityLow,    // Low → low
		model.PriorityLow,    // Lowest → low
		model.PriorityHigh,   // Major → high (Jira variant)
		model.PriorityLow,    // Trivial → low
		model.PriorityNone,   // unknown → none
	}
	if len(store.created) != len(want) {
		t.Fatalf("got %d rows, want %d", len(store.created), len(want))
	}
	for i, got := range store.created {
		if got.Priority != want[i] {
			t.Errorf("row %d priority = %d, want %d", i, got.Priority, want[i])
		}
	}
}

func TestImportJiraCSV_MapsStatusCorrectly(t *testing.T) {
	imp, store := newTestImporter()
	csv := `Issue Key,Summary,Description,Status,Priority,Assignee,Labels,Created,Resolved
A,t,d,To Do,Medium,,,2026-01-01,
B,t,d,In Progress,Medium,,,2026-01-01,
C,t,d,In Review,Medium,,,2026-01-01,
D,t,d,Done,Medium,,,2026-01-01,2026-01-05
E,t,d,Closed,Medium,,,2026-01-01,2026-01-05
F,t,d,Resolved,Medium,,,2026-01-01,2026-01-05
G,t,d,Backlog,Medium,,,2026-01-01,
`
	if _, err := imp.ImportJiraCSV(context.Background(), "ws", "team", strings.NewReader(csv)); err != nil {
		t.Fatalf("ImportJiraCSV: %v", err)
	}
	want := []model.IssueStatus{
		model.StatusTodo,
		model.StatusInProgress,
		model.StatusInReview,
		model.StatusDone,
		model.StatusDone,
		model.StatusDone,
		model.StatusBacklog,
	}
	if len(store.created) != len(want) {
		t.Fatalf("got %d, want %d", len(store.created), len(want))
	}
	for i, got := range store.created {
		if got.Status != want[i] {
			t.Errorf("row %d status = %q, want %q", i, got.Status, want[i])
		}
	}
}

func TestImporter_EmptyCSVReturnsZeroImported(t *testing.T) {
	imp, _ := newTestImporter()
	out, err := imp.ImportLinearCSV(context.Background(), "ws", "team", strings.NewReader(""))
	if err != nil {
		t.Fatalf("empty CSV should not error: %v", err)
	}
	if out.Imported != 0 {
		t.Errorf("Imported = %d, want 0", out.Imported)
	}
	if out.Skipped != 0 {
		t.Errorf("Skipped = %d, want 0", out.Skipped)
	}
}

func TestImporter_HeaderOnlyReturnsZeroImported(t *testing.T) {
	imp, _ := newTestImporter()
	out, err := imp.ImportLinearCSV(context.Background(), "ws", "team",
		strings.NewReader("ID,Title,Description,Status,Priority,Assignee,Labels,Created,Completed\n"))
	if err != nil {
		t.Fatalf("header-only CSV: %v", err)
	}
	if out.Imported != 0 || out.Skipped != 0 {
		t.Errorf("got Imported=%d Skipped=%d, want both 0", out.Imported, out.Skipped)
	}
}

func TestImporter_InvalidRowIsSkippedAndLogged(t *testing.T) {
	imp, store := newTestImporter()
	// Mid-stream malformed row: second row has too few columns.
	csv := `ID,Title,Description,Status,Priority,Assignee,Labels,Created,Completed
A,Good row,d,Backlog,No priority,,,2026-01-01,
B,bad
C,Another good,d,Backlog,No priority,,,2026-01-02,
`
	out, err := imp.ImportLinearCSV(context.Background(), "ws", "team", strings.NewReader(csv))
	if err != nil {
		t.Fatalf("ImportLinearCSV should not error on a single bad row: %v", err)
	}
	if out.Imported != 2 {
		t.Errorf("Imported = %d, want 2", out.Imported)
	}
	if out.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", out.Skipped)
	}
	if len(out.Errors) != 1 {
		t.Errorf("Errors = %v, want one entry", out.Errors)
	}
	if len(store.created) != 2 {
		t.Errorf("store got %d, want 2", len(store.created))
	}
}

func TestImporter_RowsWithEmptyTitleAreSkipped(t *testing.T) {
	imp, _ := newTestImporter()
	csv := `ID,Title,Description,Status,Priority,Assignee,Labels,Created,Completed
A,,d,Backlog,No priority,,,2026-01-01,
B,Real title,d,Backlog,No priority,,,2026-01-01,
`
	out, err := imp.ImportLinearCSV(context.Background(), "ws", "team", strings.NewReader(csv))
	if err != nil {
		t.Fatalf("ImportLinearCSV: %v", err)
	}
	if out.Imported != 1 {
		t.Errorf("Imported = %d, want 1", out.Imported)
	}
	if out.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", out.Skipped)
	}
}

func TestImportLinearCSV_MapsLinearPriorityToTrackPriority(t *testing.T) {
	imp, store := newTestImporter()
	csv := `ID,Title,Description,Status,Priority,Assignee,Labels,Created,Completed
A,t,d,Backlog,Urgent,,,2026-01-01,
B,t,d,Backlog,High,,,2026-01-01,
C,t,d,Backlog,Medium,,,2026-01-01,
D,t,d,Backlog,Low,,,2026-01-01,
E,t,d,Backlog,No priority,,,2026-01-01,
`
	if _, err := imp.ImportLinearCSV(context.Background(), "ws", "team", strings.NewReader(csv)); err != nil {
		t.Fatalf("ImportLinearCSV: %v", err)
	}
	want := []model.IssuePriority{
		model.PriorityUrgent,
		model.PriorityHigh,
		model.PriorityMedium,
		model.PriorityLow,
		model.PriorityNone,
	}
	if len(store.created) != len(want) {
		t.Fatalf("got %d, want %d", len(store.created), len(want))
	}
	for i, got := range store.created {
		if got.Priority != want[i] {
			t.Errorf("row %d priority = %d, want %d", i, got.Priority, want[i])
		}
	}
}

func TestImporter_StoreFailurePropagatesToErrors(t *testing.T) {
	imp, store := newTestImporter()
	store.failNext = true // first row will fail, rest should succeed
	csv := `ID,Title,Description,Status,Priority,Assignee,Labels,Created,Completed
A,Will fail,d,Backlog,No priority,,,2026-01-01,
B,Will succeed,d,Backlog,No priority,,,2026-01-01,
`
	out, err := imp.ImportLinearCSV(context.Background(), "ws", "team", strings.NewReader(csv))
	if err != nil {
		t.Fatalf("ImportLinearCSV: %v", err)
	}
	if out.Imported != 1 {
		t.Errorf("Imported = %d, want 1", out.Imported)
	}
	if out.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", out.Skipped)
	}
	if len(out.Errors) != 1 {
		t.Errorf("Errors = %v, want one entry", out.Errors)
	}
}
