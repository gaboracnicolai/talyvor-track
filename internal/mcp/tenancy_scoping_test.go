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
}

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
func (f *scopingIssueStore) Update(context.Context, string, string, map[string]any) (*model.Issue, error) {
	return nil, errors.New("unused")
}
func (f *scopingIssueStore) Search(context.Context, string, string, int) ([]model.Issue, error) {
	return nil, nil
}
func (f *scopingIssueStore) CreateComment(context.Context, model.Comment, string) (*model.Comment, error) {
	return nil, errors.New("unused")
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
