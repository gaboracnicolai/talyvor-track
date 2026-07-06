package automation

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/talyvor/track/internal/model"
)

// issueRefRE matches issue references in PR titles + bodies. Supports
// both numeric (#42) and identifier-prefixed (ENG-42) forms. Verbs
// recognised: fix(es)(ed), close(s)(d), resolve(s)(d).
var issueRefRE = regexp.MustCompile(`(?i)\b(?:fix(?:es|ed)?|close[sd]?|resolve[sd]?)\s+(?:#?\d+|[A-Z]+-\d+)`)

// identifierRE pulls the actual reference out of a matched fragment.
// E.g. "Fixes ENG-42" → "ENG-42".
var identifierRE = regexp.MustCompile(`(#?\d+|[A-Z]+-\d+)`)

// issueLookup is the read+comment side of internal/issue.Store the
// GitHub handler uses. Same interface pattern as other packages:
// keep dependencies narrow, keep tests cheap.
type issueLookup interface {
	GetByIdentifier(ctx context.Context, identifier string) (*model.Issue, error)
	Update(ctx context.Context, id, workspaceID string, updates map[string]any) (*model.Issue, error)
	CreateComment(ctx context.Context, c model.Comment) (*model.Comment, error)
}

type GitHubWebhookHandler struct {
	engine *Engine
	issues issueLookup
	secret string
}

func NewGitHubHandler(engine *Engine, issues issueLookup, secret string) *GitHubWebhookHandler {
	return &GitHubWebhookHandler{engine: engine, issues: issues, secret: secret}
}

func (h *GitHubWebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	if h.secret == "" {
		// Secret unset → refuse all requests rather than accept
		// unsigned input. Same "secure by default" pattern as Lens.
		http.Error(w, "webhook secret not configured", http.StatusUnauthorized)
		return
	}
	if !verifyGitHubSignature(body, r.Header.Get("X-Hub-Signature-256"), h.secret) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	event := r.Header.Get("X-GitHub-Event")
	switch event {
	case "pull_request":
		h.handlePullRequest(r.Context(), body)
	case "push":
		// Push events are no-ops in Phase 6. The infrastructure is
		// here for Phase 7 to add commit-references-issue support.
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

type pullRequestPayload struct {
	Action      string `json:"action"`
	PullRequest struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		Merged bool   `json:"merged"`
	} `json:"pull_request"`
}

// handlePullRequest fans out based on the PR action. The two events
// we care about today are "opened" and "closed" (with merged=true).
func (h *GitHubWebhookHandler) handlePullRequest(ctx context.Context, body []byte) {
	var pl pullRequestPayload
	if err := json.Unmarshal(body, &pl); err != nil {
		slog.Warn("automation: github PR payload parse failed",
			slog.String("err", err.Error()))
		return
	}
	refs := ExtractIssueReferences(pl.PullRequest.Title + " " + pl.PullRequest.Body)
	if len(refs) == 0 {
		return
	}

	switch {
	case pl.Action == "closed" && pl.PullRequest.Merged:
		h.handleMerged(ctx, refs, pl)
	case pl.Action == "opened":
		h.handleOpened(ctx, refs, pl)
	}
}

// handleMerged closes referenced issues and fires the pr.merged
// trigger so workspace automation rules can run their own actions
// (notify Slack, move to next cycle, etc.).
func (h *GitHubWebhookHandler) handleMerged(ctx context.Context, refs []string, pl pullRequestPayload) {
	for _, ref := range refs {
		iss, err := h.issues.GetByIdentifier(ctx, ref)
		if err != nil || iss == nil {
			continue
		}
		if _, err := h.issues.Update(ctx, iss.ID, iss.WorkspaceID, map[string]any{
			"status": string(model.StatusDone),
		}); err != nil {
			slog.Warn("automation: PR-merged auto-close failed",
				slog.String("issue", ref),
				slog.String("err", err.Error()))
			continue
		}
		_, _ = h.issues.CreateComment(ctx, model.Comment{
			IssueID:  iss.ID,
			AuthorID: "github-automation",
			Body:     fmt.Sprintf("Closed by PR #%d: %s", pl.PullRequest.Number, pl.PullRequest.Title),
		})
		if h.engine != nil {
			_ = h.engine.Fire(ctx, TriggerPRMerged, iss.WorkspaceID, *iss, nil)
		}
	}
}

func (h *GitHubWebhookHandler) handleOpened(ctx context.Context, refs []string, pl pullRequestPayload) {
	for _, ref := range refs {
		iss, err := h.issues.GetByIdentifier(ctx, ref)
		if err != nil || iss == nil {
			continue
		}
		_, _ = h.issues.CreateComment(ctx, model.Comment{
			IssueID:  iss.ID,
			AuthorID: "github-automation",
			Body:     fmt.Sprintf("PR #%d opened: %s", pl.PullRequest.Number, pl.PullRequest.Title),
		})
		if h.engine != nil {
			_ = h.engine.Fire(ctx, TriggerPROpened, iss.WorkspaceID, *iss, nil)
		}
	}
}

// ExtractIssueReferences scans free text for "Fixes #123",
// "Closes ENG-42", etc. and returns the bare identifiers found.
// Numeric-only references ("#123") aren't useful in a multi-team
// workspace without a team prefix, so they're dropped — only the
// "ENG-42" style is returned.
func ExtractIssueReferences(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range issueRefRE.FindAllString(text, -1) {
		ref := identifierRE.FindString(m)
		// Strip leading # if any; we only return prefixed identifiers.
		ref = strings.TrimPrefix(ref, "#")
		if !strings.Contains(ref, "-") {
			// Pure-numeric reference without team prefix is ambiguous;
			// skip silently.
			continue
		}
		if seen[ref] {
			continue
		}
		seen[ref] = true
		out = append(out, ref)
	}
	return out
}

func verifyGitHubSignature(body []byte, header, secret string) bool {
	if header == "" || !strings.HasPrefix(header, "sha256=") {
		return false
	}
	provided := strings.TrimPrefix(header, "sha256=")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(provided))
}

// signForTest is used by tests + the GitHub sender side; not part
// of the public API in production.
func signForTest(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

var _ = errors.New
