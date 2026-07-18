package mcp

// White-box tests for handler-level workspace scoping (D11 defense-in-depth).
//
// End-to-end, the tools/call chokepoint already denies a cross-tenant
// issue-keyed call (it resolves the workspace FROM the issue and requires
// membership before dispatch — see authz_integration_test.go). These
// tests exercise the handlers in ISOLATION, with a context authorized for
// workspace A but an issue that lives in workspace B — the inconsistent
// state the chokepoint would never produce — to prove each handler
// re-scopes the read itself instead of trusting a single upstream guard.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/ai"
	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/model"
)

// scopingIssueStore is a minimal issueStoreIface: GetByID/GetByIdentifier
// return whatever issue is registered, REGARDLESS of workspace — exactly
// the unscoped read whose safety must not depend on the caller's context.
type scopingIssueStore struct {
	byID         map[string]*model.Issue
	byIdentifier map[string]*model.Issue
	commentCalls []commentCall // records (issueID, workspaceID) passed to CreateComment
	updateCalls  int           // counts Update calls — proves a foreign write is rejected BEFORE the store
}

// commentCall records one CreateComment invocation so a test can prove the
// handler passed the SERVER-authorized workspace (not the issue's, not a
// client field).
type commentCall struct{ issueID, workspaceID string }

func (f *scopingIssueStore) GetByID(_ context.Context, id string) (*model.Issue, error) {
	if iss, ok := f.byID[id]; ok {
		return iss, nil
	}
	return nil, errors.New("not found")
}
func (f *scopingIssueStore) GetByIdentifier(_ context.Context, ident string) (*model.Issue, error) {
	if iss, ok := f.byIdentifier[ident]; ok {
		return iss, nil
	}
	return nil, errors.New("not found")
}

// unused by these tests — present only to satisfy issueStoreIface.
func (f *scopingIssueStore) Create(context.Context, model.Issue) (*model.Issue, error) {
	return nil, errors.New("unused")
}
func (f *scopingIssueStore) List(context.Context, issue.IssueFilter) ([]model.Issue, error) {
	return nil, nil
}
func (f *scopingIssueStore) Update(_ context.Context, id, workspaceID string, _ map[string]any) (*model.Issue, error) {
	f.updateCalls++
	if iss, ok := f.byID[id]; ok {
		return iss, nil
	}
	return &model.Issue{ID: id, WorkspaceID: workspaceID}, nil
}
func (f *scopingIssueStore) Search(context.Context, string, string, int) ([]model.Issue, error) {
	return nil, nil
}
// CreateComment models the real store's tenancy scoping: the comment lands
// only if its issue is in the passed workspaceID, else issue.ErrNotFound.
func (f *scopingIssueStore) CreateComment(_ context.Context, c model.Comment, workspaceID string) (*model.Comment, error) {
	f.commentCalls = append(f.commentCalls, commentCall{c.IssueID, workspaceID})
	iss, ok := f.byID[c.IssueID]
	if !ok || iss.WorkspaceID != workspaceID {
		return nil, issue.ErrNotFound
	}
	return &model.Comment{ID: "c-1", IssueID: c.IssueID, AuthorID: c.AuthorID, Body: c.Body}, nil
}

// scopingAI is an available AI engine that RECORDS whether it was asked to
// triage — so a test can prove the scoping check rejected a foreign issue
// BEFORE any LLM work (and any data leak) happened.
type scopingAI struct{ triaged int }

func (a *scopingAI) IsAvailable() bool { return true }
func (a *scopingAI) TriageIssue(_ context.Context, i model.Issue) (*ai.TriageResult, error) {
	a.triaged++
	return &ai.TriageResult{Summary: "TRIAGED:" + i.Title, Confidence: 1}, nil
}

func newScopingServer(is issueStoreIface, ae aiEngineIface) *Server {
	return newServer(is, nil, nil, ae, nil, nopMembers{}, "test")
}

// authorizedFor builds a context as the tools/call chokepoint would for a
// caller authorized in wsID (member m-<wsID>).
func authorizedFor(wsID string) context.Context {
	return authz.WithAuthorized(context.Background(), wsID, "m-"+wsID)
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return b
}

// ── get_issue ────────────────────────────────────────────────────────────

func TestToolGetIssue_RejectsForeignWorkspace(t *testing.T) {
	issB := &model.Issue{ID: "iss-B", Identifier: "ENG-9", WorkspaceID: "ws-B", Title: "SECRET-B"}
	is := &scopingIssueStore{byID: map[string]*model.Issue{"iss-B": issB}}
	s := newScopingServer(is, &scopingAI{})

	out, err := s.toolGetIssue(authorizedFor("ws-A"), mustJSON(t, map[string]any{"issue_id": "iss-B"}))
	if err == nil {
		leak, _ := json.Marshal(out)
		t.Fatalf("get_issue on a FOREIGN-workspace issue returned success — cross-tenant leak: %s", leak)
	}
	if out != nil {
		t.Errorf("rejected get_issue must return nil result; got %+v", out)
	}
	if leak, _ := json.Marshal(out); strings.Contains(string(leak), "SECRET-B") {
		t.Errorf("foreign issue content leaked: %s", leak)
	}
}

func TestToolGetIssue_AllowsSameWorkspace(t *testing.T) {
	issA := &model.Issue{ID: "iss-A", Identifier: "ENG-1", WorkspaceID: "ws-A", Title: "own"}
	is := &scopingIssueStore{byID: map[string]*model.Issue{"iss-A": issA}}
	s := newScopingServer(is, &scopingAI{})

	out, err := s.toolGetIssue(authorizedFor("ws-A"), mustJSON(t, map[string]any{"issue_id": "iss-A"}))
	if err != nil {
		t.Fatalf("get_issue on the caller's OWN workspace issue must succeed; got %v", err)
	}
	if out == nil {
		t.Fatal("same-workspace get_issue returned nil")
	}
}

// ── triage_issue ─────────────────────────────────────────────────────────

func TestToolTriageIssue_RejectsForeignWorkspace(t *testing.T) {
	issB := &model.Issue{ID: "iss-B", Identifier: "ENG-9", WorkspaceID: "ws-B", Title: "SECRET-B"}
	is := &scopingIssueStore{byID: map[string]*model.Issue{"iss-B": issB}}
	engine := &scopingAI{}
	s := newScopingServer(is, engine)

	out, err := s.toolTriageIssue(authorizedFor("ws-A"), mustJSON(t, map[string]any{"issue_id": "iss-B"}))
	if err == nil {
		leak, _ := json.Marshal(out)
		t.Fatalf("triage_issue on a FOREIGN-workspace issue returned success — cross-tenant leak: %s", leak)
	}
	if engine.triaged != 0 {
		t.Errorf("the LLM was invoked on a foreign issue (%d triage calls) — the read must be rejected first", engine.triaged)
	}
	if out != nil {
		t.Errorf("rejected triage_issue must return nil result; got %+v", out)
	}
}

// ── add_comment (D11 class, defense-in-depth) ────────────────────────────

func TestToolAddComment_RejectsForeignWorkspace(t *testing.T) {
	issB := &model.Issue{ID: "iss-B", WorkspaceID: "ws-B", Title: "B"}
	is := &scopingIssueStore{byID: map[string]*model.Issue{"iss-B": issB}}
	s := newScopingServer(is, &scopingAI{})

	out, err := s.toolAddComment(authorizedFor("ws-A"),
		mustJSON(t, map[string]any{"issue_id": "iss-B", "body": "cross-tenant"}))
	if err == nil {
		t.Fatalf("add_comment on a FOREIGN issue must be refused; got %+v", out)
	}
	// The handler must scope to the SERVER-authorized workspace (ws-A) — never
	// the issue's ws, never a client field. The store then refuses the write.
	if len(is.commentCalls) != 1 || is.commentCalls[0].workspaceID != "ws-A" {
		t.Errorf("toolAddComment must pass the authorized workspace ws-A to CreateComment; got %+v", is.commentCalls)
	}
}

func TestToolAddComment_AllowsSameWorkspace(t *testing.T) {
	issA := &model.Issue{ID: "iss-A", WorkspaceID: "ws-A", Title: "A"}
	is := &scopingIssueStore{byID: map[string]*model.Issue{"iss-A": issA}}
	s := newScopingServer(is, &scopingAI{})

	if _, err := s.toolAddComment(authorizedFor("ws-A"),
		mustJSON(t, map[string]any{"issue_id": "iss-A", "body": "ok"})); err != nil {
		t.Fatalf("same-workspace add_comment must succeed; got %v", err)
	}
}

func TestToolTriageIssue_AllowsSameWorkspace(t *testing.T) {
	issA := &model.Issue{ID: "iss-A", Identifier: "ENG-1", WorkspaceID: "ws-A", Title: "own"}
	is := &scopingIssueStore{byID: map[string]*model.Issue{"iss-A": issA}}
	engine := &scopingAI{}
	s := newScopingServer(is, engine)

	out, err := s.toolTriageIssue(authorizedFor("ws-A"), mustJSON(t, map[string]any{"issue_id": "iss-A"}))
	if err != nil {
		t.Fatalf("triage_issue on the caller's OWN workspace issue must succeed; got %v", err)
	}
	if engine.triaged != 1 {
		t.Errorf("same-workspace triage must invoke the LLM once; got %d", engine.triaged)
	}
	if out == nil {
		t.Fatal("same-workspace triage returned nil")
	}
}

// ── update_issue / move_to_cycle (D11 class, Phase B defense-in-depth) ──────
// These handlers were already store-scoped via Update(id, wsID); the added
// scopeIssueToCaller pre-check rejects a foreign issue at the HANDLER, before the
// write is ever attempted. The fake records Update calls, so the test proves the
// rejection happens BEFORE the store (teeth: remove scopeIssueToCaller → Update runs).

func TestToolUpdateIssue_RejectsForeignWorkspace(t *testing.T) {
	issB := &model.Issue{ID: "iss-B", WorkspaceID: "ws-B", Title: "B"}
	is := &scopingIssueStore{byID: map[string]*model.Issue{"iss-B": issB}}
	s := newScopingServer(is, &scopingAI{})

	if _, err := s.toolUpdateIssue(authorizedFor("ws-A"),
		mustJSON(t, map[string]any{"issue_id": "iss-B", "title": "hijack"})); err == nil {
		t.Fatal("update_issue on a FOREIGN-workspace issue must be refused")
	}
	if is.updateCalls != 0 {
		t.Errorf("Update must NOT be reached for a foreign issue; updateCalls=%d", is.updateCalls)
	}
}

func TestToolUpdateIssue_AllowsSameWorkspace(t *testing.T) {
	issA := &model.Issue{ID: "iss-A", WorkspaceID: "ws-A", Title: "A"}
	is := &scopingIssueStore{byID: map[string]*model.Issue{"iss-A": issA}}
	s := newScopingServer(is, &scopingAI{})

	if _, err := s.toolUpdateIssue(authorizedFor("ws-A"),
		mustJSON(t, map[string]any{"issue_id": "iss-A", "title": "ok"})); err != nil {
		t.Fatalf("same-workspace update_issue must succeed; got %v", err)
	}
	if is.updateCalls != 1 {
		t.Errorf("same-workspace update must reach Update once; got %d", is.updateCalls)
	}
}

func TestToolMoveToCycle_RejectsForeignWorkspace(t *testing.T) {
	issB := &model.Issue{ID: "iss-B", WorkspaceID: "ws-B"}
	is := &scopingIssueStore{byID: map[string]*model.Issue{"iss-B": issB}}
	s := newScopingServer(is, &scopingAI{})

	if _, err := s.toolMoveToCycle(authorizedFor("ws-A"),
		mustJSON(t, map[string]any{"issue_id": "iss-B", "cycle_id": "cyc-1"})); err == nil {
		t.Fatal("move_to_cycle on a FOREIGN-workspace issue must be refused")
	}
	if is.updateCalls != 0 {
		t.Errorf("Update must NOT be reached for a foreign issue; updateCalls=%d", is.updateCalls)
	}
}

func TestToolMoveToCycle_AllowsSameWorkspace(t *testing.T) {
	issA := &model.Issue{ID: "iss-A", WorkspaceID: "ws-A"}
	is := &scopingIssueStore{byID: map[string]*model.Issue{"iss-A": issA}}
	s := newScopingServer(is, &scopingAI{})

	if _, err := s.toolMoveToCycle(authorizedFor("ws-A"),
		mustJSON(t, map[string]any{"issue_id": "iss-A", "cycle_id": "cyc-1"})); err != nil {
		t.Fatalf("same-workspace move_to_cycle must succeed; got %v", err)
	}
	if is.updateCalls != 1 {
		t.Errorf("same-workspace move must reach Update once; got %d", is.updateCalls)
	}
}
