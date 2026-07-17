package automation

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/talyvor/track/internal/model"
)

// fakeIssueLookup is the GitHub handler's stand-in for issue.Store.
// Records every Update/CreateComment so tests can assert side
// effects after a webhook fires.
type fakeIssueLookup struct {
	mu                 sync.Mutex
	issuesByIdentifier map[string]*model.Issue
	updates            []map[string]any
	comments           []model.Comment
}

func (f *fakeIssueLookup) GetByIdentifier(_ context.Context, ident string) (*model.Issue, error) {
	if i, ok := f.issuesByIdentifier[ident]; ok {
		return i, nil
	}
	return nil, nil
}
func (f *fakeIssueLookup) Update(_ context.Context, _, _ string, updates map[string]any) (*model.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, updates)
	return &model.Issue{ID: "i-1"}, nil
}
func (f *fakeIssueLookup) CreateComment(_ context.Context, c model.Comment, _ string) (*model.Comment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.comments = append(f.comments, c)
	return &c, nil
}

func signedGitHubReq(t *testing.T, secret string, event string, body []byte) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/github", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-Hub-Signature-256", signForTest(body, secret))
	return req
}

func TestGitHub_ValidSignatureAccepted(t *testing.T) {
	body := []byte(`{"action":"opened","pull_request":{"number":1,"title":"x","body":"","merged":false}}`)
	h := NewGitHubHandler(nil, &fakeIssueLookup{}, "topsecret")

	req := signedGitHubReq(t, "topsecret", "pull_request", body)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
}

func TestGitHub_InvalidSignatureReturns401(t *testing.T) {
	body := []byte(`{"action":"opened"}`)
	h := NewGitHubHandler(nil, &fakeIssueLookup{}, "topsecret")

	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", "sha256=wrong")
	req.Header.Set("X-GitHub-Event", "pull_request")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestExtractIssueReferences_ParsesCommonForms(t *testing.T) {
	got := ExtractIssueReferences(
		"This PR fixes ENG-42 and closes ENG-43. Also resolves #999 in old format.",
	)
	if len(got) != 2 {
		t.Fatalf("got %d refs, want 2 (ENG-42, ENG-43); got %+v", len(got), got)
	}
	if got[0] != "ENG-42" || got[1] != "ENG-43" {
		t.Errorf("refs = %+v, want [ENG-42 ENG-43]", got)
	}
}

func TestGitHub_PRMergedSetsIssueStatusToDone(t *testing.T) {
	body, _ := json.Marshal(pullRequestPayload{
		Action: "closed",
		PullRequest: struct {
			Number int    `json:"number"`
			Title  string `json:"title"`
			Body   string `json:"body"`
			Merged bool   `json:"merged"`
		}{Number: 7, Title: "Fix auth bug", Body: "Fixes ENG-42", Merged: true},
	})
	issues := &fakeIssueLookup{
		issuesByIdentifier: map[string]*model.Issue{
			"ENG-42": {ID: "i-1", Identifier: "ENG-42", WorkspaceID: "ws-1"},
		},
	}
	h := NewGitHubHandler(nil, issues, "s")

	req := signedGitHubReq(t, "s", "pull_request", body)
	h.ServeHTTP(httptest.NewRecorder(), req)

	if len(issues.updates) != 1 {
		t.Fatalf("got %d updates, want 1", len(issues.updates))
	}
	if issues.updates[0]["status"] != "done" {
		t.Errorf("status = %v, want done", issues.updates[0]["status"])
	}
}

func TestGitHub_PRMergedAddsClosingComment(t *testing.T) {
	body, _ := json.Marshal(pullRequestPayload{
		Action: "closed",
		PullRequest: struct {
			Number int    `json:"number"`
			Title  string `json:"title"`
			Body   string `json:"body"`
			Merged bool   `json:"merged"`
		}{Number: 7, Title: "Fix auth", Body: "Fixes ENG-42", Merged: true},
	})
	issues := &fakeIssueLookup{
		issuesByIdentifier: map[string]*model.Issue{
			"ENG-42": {ID: "i-1", Identifier: "ENG-42", WorkspaceID: "ws-1"},
		},
	}
	h := NewGitHubHandler(nil, issues, "s")

	h.ServeHTTP(httptest.NewRecorder(), signedGitHubReq(t, "s", "pull_request", body))

	if len(issues.comments) != 1 {
		t.Fatalf("got %d comments, want 1", len(issues.comments))
	}
	c := issues.comments[0]
	if c.AuthorID != "github-automation" {
		t.Errorf("author = %q, want github-automation", c.AuthorID)
	}
	if c.IssueID != "i-1" {
		t.Errorf("issue id = %q, want i-1", c.IssueID)
	}
}

func TestGitHub_EmptySecretRefuses(t *testing.T) {
	body := []byte(`{}`)
	h := NewGitHubHandler(nil, &fakeIssueLookup{}, "")

	req := signedGitHubReq(t, "", "pull_request", body)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("empty-secret webhook should refuse all requests; got %d", w.Code)
	}
}
