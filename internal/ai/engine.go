// Package ai implements Talyvor Track's AI-powered features.
//
// Every LLM call routes through Talyvor Lens. Track never talks to
// OpenAI / Anthropic / Google directly — the X-Talyvor-Feature header
// on every request lets Lens attribute the cost back to the
// originating Track issue, which closes the loop: AI features cost
// money, that cost shows up against the issue that triggered it.
//
// Engine methods degrade gracefully when Lens is unreachable: triage
// returns a default low-confidence result, search falls back to
// full-text, summary skips on cache miss + Lens-down.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/talyvor/track/internal/lensintegration"
	"github.com/talyvor/track/internal/model"
)

// lensAccess is the slice of lensintegration.Client the AI engine
// needs. Declared as an interface so tests can construct a tiny
// stand-in instead of a real Client + httptest server (though the
// real tests do spin up httptest for end-to-end coverage).
type lensAccess interface {
	IsConfigured() bool
	BaseURL() string
	APIKey() string
}

type pgxDB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// fullTextSearcher is the fallback search backend the engine drops
// to when no embeddings exist or Lens is unreachable.
type fullTextSearcher interface {
	Search(ctx context.Context, workspaceID, query string, limit int) ([]model.Issue, error)
}

type Engine struct {
	lens        lensAccess
	issueSearch fullTextSearcher
	pool        pgxDB
	httpClient  *http.Client

	// Thread summary cache: issueID → cached entry. Bounded by the
	// number of long threads in the workspace, which is small in
	// practice. Eviction is lazy on read.
	summaryMu    sync.Mutex
	summaryCache map[string]cachedSummary
}

type cachedSummary struct {
	Summary   *ThreadSummary
	Generated time.Time
}

const summaryTTL = time.Hour

// New constructs the AI engine. lensClient may be a zero-value Client
// (LensURL unset) — methods detect that via IsConfigured and return
// graceful "ai_available: false" responses to handlers.
func New(lensClient *lensintegration.Client, issueSearch fullTextSearcher, pool *pgxpool.Pool) *Engine {
	var db pgxDB
	if pool != nil {
		db = pool
	}
	return newEngine(lensClient, issueSearch, db)
}

func newEngine(lens lensAccess, issueSearch fullTextSearcher, db pgxDB) *Engine {
	return &Engine{
		lens:         lens,
		issueSearch:  issueSearch,
		pool:         db,
		httpClient:   &http.Client{Timeout: 30 * time.Second},
		summaryCache: make(map[string]cachedSummary),
	}
}

// ErrAIUnavailable signals that Lens is down or unconfigured. The
// handler turns this into a 503-ish response with {ai_available: false}.
var ErrAIUnavailable = errors.New("ai: Lens is not available")

// IsAvailable reports whether the AI engine can make calls. Cheap —
// just checks the Lens client's configuration state.
func (e *Engine) IsAvailable() bool {
	return e != nil && e.lens != nil && e.lens.IsConfigured()
}

// callAnthropicViaLens POSTs an Anthropic-shaped request to Lens's
// proxy and returns the raw assistant content. featureID is the
// issue identifier or other Track feature label — Lens uses it as
// the X-Talyvor-Feature value so the cost attributes back to the
// right Track entity.
func (e *Engine) callAnthropicViaLens(ctx context.Context, featureID, model, systemPrompt, userPrompt string, maxTokens int) (string, error) {
	if !e.IsAvailable() {
		return "", ErrAIUnavailable
	}
	body, _ := json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"system":     systemPrompt,
		"messages": []map[string]any{
			{"role": "user", "content": userPrompt},
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		e.lens.BaseURL()+"/v1/proxy/anthropic/v1/messages",
		bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.lens.APIKey())
	req.Header.Set("X-Talyvor-Feature", featureID)

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("ai: Lens returned %d: %s", resp.StatusCode, string(raw))
	}
	var out struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	var sb strings.Builder
	for _, c := range out.Content {
		sb.WriteString(c.Text)
	}
	return sb.String(), nil
}

// callEmbeddingsViaLens POSTs to Lens's OpenAI proxy and returns the
// embedding vector. featureID labels the cost for attribution.
func (e *Engine) callEmbeddingsViaLens(ctx context.Context, featureID, text string) ([]float32, error) {
	if !e.IsAvailable() {
		return nil, ErrAIUnavailable
	}
	body, _ := json.Marshal(map[string]any{
		"model": "text-embedding-3-small",
		"input": text,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		e.lens.BaseURL()+"/v1/proxy/openai/v1/embeddings",
		bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.lens.APIKey())
	req.Header.Set("X-Talyvor-Feature", featureID)

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("ai: embeddings returned %d", resp.StatusCode)
	}
	var out struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if len(out.Data) == 0 {
		return nil, errors.New("ai: embeddings response empty")
	}
	return out.Data[0].Embedding, nil
}

// ─────────────────────────────────────────────────────────
// Feature 1: AI Issue Triage
// ─────────────────────────────────────────────────────────

type TriageResult struct {
	SuggestedPriority model.IssuePriority `json:"suggested_priority"`
	SuggestedLabels   []string            `json:"suggested_labels"`
	SuggestedAssignee string              `json:"suggested_assignee"`
	Summary           string              `json:"summary"`
	IsDuplicate       bool                `json:"is_duplicate"`
	DuplicateOf       string              `json:"duplicate_of,omitempty"`
	Confidence        float64             `json:"confidence"`
}

const triageSystemPrompt = `You are an engineering issue triage assistant.
Analyze the issue and respond ONLY with JSON in this exact shape:
{
  "suggested_priority": 0,
  "suggested_labels": ["bug","performance"],
  "summary": "one sentence summary",
  "confidence": 0.0
}
Priority values: 0=none, 1=urgent, 2=high, 3=medium, 4=low.`

// TriageIssue asks Claude Haiku to extract priority + labels + a
// one-line summary from the issue body. The cheap model is fine
// here — triage is a classification task, not deep reasoning.
func (e *Engine) TriageIssue(ctx context.Context, issue model.Issue) (*TriageResult, error) {
	if !e.IsAvailable() {
		return nil, ErrAIUnavailable
	}
	user := issue.Title + "\n\n" + issue.Description
	raw, err := e.callAnthropicViaLens(ctx, issue.Identifier, "claude-haiku-4-6", triageSystemPrompt, user, 512)
	if err != nil {
		return nil, err
	}
	jsonOnly := extractJSON(raw)
	var out TriageResult
	if err := json.Unmarshal([]byte(jsonOnly), &out); err != nil {
		return nil, fmt.Errorf("ai: triage parse: %w (raw: %q)", err, raw)
	}
	return &out, nil
}

// ─────────────────────────────────────────────────────────
// Feature 2: Semantic Duplicate Detection
// ─────────────────────────────────────────────────────────

type DuplicateCandidate struct {
	IssueID    string  `json:"issue_id"`
	Identifier string  `json:"identifier"`
	Title      string  `json:"title"`
	Similarity float64 `json:"similarity"`
}

const duplicateThreshold = 0.7

// FindDuplicates asks the LLM which of the candidate issues describe
// the same problem as the new one. Returns only matches above 0.7
// similarity. Capped at 20 candidates (most recent) to keep the
// prompt budget bounded.
func (e *Engine) FindDuplicates(ctx context.Context, issue model.Issue, candidates []model.Issue) ([]DuplicateCandidate, error) {
	if !e.IsAvailable() {
		return nil, ErrAIUnavailable
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	if len(candidates) > 20 {
		candidates = candidates[:20]
	}

	var candidateList strings.Builder
	for _, c := range candidates {
		fmt.Fprintf(&candidateList, "- %s (%s): %s\n", c.Identifier, c.ID, c.Title)
	}

	system := `You compare a new engineering issue against existing ones and identify potential duplicates.
Return ONLY a JSON array of objects, no prose. Schema:
[{"issue_id":"...","similarity":0.0}]
Return [] if no candidate describes the same problem.`

	user := fmt.Sprintf("New issue:\n%s\n\n%s\n\nExisting issues:\n%s",
		issue.Title, issue.Description, candidateList.String())

	raw, err := e.callAnthropicViaLens(ctx, issue.Identifier, "claude-haiku-4-6", system, user, 1024)
	if err != nil {
		return nil, err
	}

	type rawMatch struct {
		IssueID    string  `json:"issue_id"`
		Similarity float64 `json:"similarity"`
	}
	var matches []rawMatch
	if err := json.Unmarshal([]byte(extractJSON(raw)), &matches); err != nil {
		return nil, fmt.Errorf("ai: duplicates parse: %w (raw: %q)", err, raw)
	}

	// Index candidates by ID so we can return Identifier + Title.
	byID := make(map[string]model.Issue, len(candidates))
	for _, c := range candidates {
		byID[c.ID] = c
	}
	var out []DuplicateCandidate
	for _, m := range matches {
		if m.Similarity < duplicateThreshold {
			continue
		}
		c, ok := byID[m.IssueID]
		if !ok {
			continue
		}
		out = append(out, DuplicateCandidate{
			IssueID:    c.ID,
			Identifier: c.Identifier,
			Title:      c.Title,
			Similarity: m.Similarity,
		})
	}
	return out, nil
}

// ─────────────────────────────────────────────────────────
// Feature 3: Auto-summarize Long Threads
// ─────────────────────────────────────────────────────────

type ThreadSummary struct {
	Summary    string   `json:"summary"`
	KeyPoints  []string `json:"key_points"`
	NextAction string   `json:"next_action"`
	Sentiment  string   `json:"sentiment"`
}

const summaryMinComments = 10

// SummarizeThread is a no-op (returns nil) for short threads. Long
// threads run through Claude with a structured-output prompt; the
// result is cached for an hour so repeated dashboard loads don't
// burn the AI budget.
func (e *Engine) SummarizeThread(ctx context.Context, issue model.Issue, comments []model.Comment) (*ThreadSummary, error) {
	if len(comments) < summaryMinComments {
		return nil, nil
	}
	// Cache check before Lens check — even when Lens is down, a
	// cached summary stays valid.
	if cached, ok := e.cachedSummary(issue.ID); ok {
		return cached, nil
	}
	if !e.IsAvailable() {
		return nil, ErrAIUnavailable
	}

	var thread strings.Builder
	for _, c := range comments {
		fmt.Fprintf(&thread, "[%s] %s: %s\n", c.CreatedAt.Format("2006-01-02"), c.AuthorID, c.Body)
	}

	system := `Summarize this issue discussion. Return ONLY JSON:
{
  "summary": "2-3 sentence overview",
  "key_points": ["...","..."],
  "next_action": "what should happen next",
  "sentiment": "positive|neutral|negative|blocked"
}`
	user := "Issue: " + issue.Title + "\n\nThread:\n" + thread.String()

	raw, err := e.callAnthropicViaLens(ctx, issue.Identifier, "claude-haiku-4-6", system, user, 1024)
	if err != nil {
		return nil, err
	}
	var out ThreadSummary
	if err := json.Unmarshal([]byte(extractJSON(raw)), &out); err != nil {
		return nil, fmt.Errorf("ai: summary parse: %w (raw: %q)", err, raw)
	}
	e.putCachedSummary(issue.ID, &out)
	return &out, nil
}

func (e *Engine) cachedSummary(issueID string) (*ThreadSummary, bool) {
	e.summaryMu.Lock()
	defer e.summaryMu.Unlock()
	c, ok := e.summaryCache[issueID]
	if !ok {
		return nil, false
	}
	if time.Since(c.Generated) > summaryTTL {
		delete(e.summaryCache, issueID)
		return nil, false
	}
	return c.Summary, true
}

func (e *Engine) putCachedSummary(issueID string, s *ThreadSummary) {
	e.summaryMu.Lock()
	defer e.summaryMu.Unlock()
	e.summaryCache[issueID] = cachedSummary{Summary: s, Generated: time.Now().UTC()}
}

// ─────────────────────────────────────────────────────────
// Feature 4: AI Sprint Planning
// ─────────────────────────────────────────────────────────

type SprintSuggestion struct {
	RecommendedIssues  []string `json:"recommended_issues"`
	TotalEstimatedDays float64  `json:"total_estimated_days"`
	Reasoning          string   `json:"reasoning"`
	AITokensCostUSD    float64  `json:"ai_tokens_cost_usd"`
}

// SuggestSprintIssues picks a subset of the backlog suitable for the
// next cycle. Uses Sonnet (not Haiku) — picking with attention to
// priorities, dependencies, and team capacity needs real reasoning.
func (e *Engine) SuggestSprintIssues(ctx context.Context, teamID string, backlog []model.Issue, cycleDays, teamSize int) (*SprintSuggestion, error) {
	if !e.IsAvailable() {
		return nil, ErrAIUnavailable
	}
	if len(backlog) == 0 {
		return &SprintSuggestion{Reasoning: "Backlog is empty."}, nil
	}

	var backlogList strings.Builder
	for _, i := range backlog {
		fmt.Fprintf(&backlogList, "- %s [p=%d, labels=%v] %s\n",
			i.ID, i.Priority, i.Labels, i.Title)
	}

	system := `You are a sprint planning assistant. Given a team's backlog, select
a sensible set of issues for the next cycle. Consider priority, labels (bug/
feature/epic), and a rough estimate of how much work fits in a {team_size}-
person team in {cycle_days} days.
Return ONLY JSON:
{"recommended_issues": ["id1","id2"], "total_estimated_days": 12.5, "reasoning": "..."}`

	user := fmt.Sprintf("Team size: %d\nCycle length: %d days\n\nBacklog:\n%s",
		teamSize, cycleDays, backlogList.String())

	raw, err := e.callAnthropicViaLens(ctx, "track-sprint-planning", "claude-sonnet-4-6", system, user, 2048)
	if err != nil {
		return nil, err
	}
	var out SprintSuggestion
	if err := json.Unmarshal([]byte(extractJSON(raw)), &out); err != nil {
		return nil, fmt.Errorf("ai: sprint suggestion parse: %w (raw: %q)", err, raw)
	}
	return &out, nil
}

// ─────────────────────────────────────────────────────────
// Feature 5: Semantic Issue Search
// ─────────────────────────────────────────────────────────

// SemanticSearch returns issues by vector similarity to the query.
// Falls back to full-text search via the issue store when:
//   - Lens isn't configured / available
//   - The embeddings table is empty
//   - Any step of the vector pipeline fails
//
// The fallback path is invisible to callers — they always get a
// useful result.
func (e *Engine) SemanticSearch(ctx context.Context, workspaceID, query string, limit int) ([]model.Issue, error) {
	if limit <= 0 {
		limit = 25
	}
	// Hard fallback: no Lens, no embeddings — just full-text.
	if !e.IsAvailable() || e.pool == nil {
		return e.fullTextFallback(ctx, workspaceID, query, limit)
	}

	vec, err := e.callEmbeddingsViaLens(ctx, "track-search", query)
	if err != nil {
		return e.fullTextFallback(ctx, workspaceID, query, limit)
	}
	// Build the pgvector literal "[0.1,0.2,...]".
	var vec64 strings.Builder
	vec64.WriteByte('[')
	for i, f := range vec {
		if i > 0 {
			vec64.WriteByte(',')
		}
		vec64.WriteString(strconv.FormatFloat(float64(f), 'f', -1, 32))
	}
	vec64.WriteByte(']')

	rows, err := e.pool.Query(ctx,
		`SELECT i.id, i.workspace_id, i.team_id, i.project_id, i.number, i.identifier,
            i.title, i.description, i.status, i.priority,
            i.assignee_id, i.creator_id, i.cycle_id, i.parent_id,
            i.due_date, i.completed_at,
            i.lens_feature, i.ai_cost_usd, i.ai_tokens,
            i.labels, i.sort_order, i.created_at, i.updated_at
        FROM issues i
        JOIN issue_embeddings e ON e.issue_id = i.id
        WHERE i.workspace_id = $1
        ORDER BY e.embedding <-> $2::vector
        LIMIT $3`,
		workspaceID, vec64.String(), limit,
	)
	if err != nil {
		return e.fullTextFallback(ctx, workspaceID, query, limit)
	}
	defer rows.Close()

	var out []model.Issue
	for rows.Next() {
		var (
			i        model.Issue
			status   string
			priority int
		)
		if err := rows.Scan(
			&i.ID, &i.WorkspaceID, &i.TeamID, &i.ProjectID, &i.Number, &i.Identifier,
			&i.Title, &i.Description, &status, &priority,
			&i.AssigneeID, &i.CreatorID, &i.CycleID, &i.ParentID,
			&i.DueDate, &i.CompletedAt,
			&i.LensFeature, &i.AICostUSD, &i.AITokens,
			&i.Labels, &i.SortOrder, &i.CreatedAt, &i.UpdatedAt,
		); err != nil {
			return nil, err
		}
		i.Status = model.IssueStatus(status)
		i.Priority = model.IssuePriority(priority)
		out = append(out, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// If no embeddings have been indexed yet the JOIN returns zero
	// rows even when issues exist. Fall through to full-text.
	if len(out) == 0 {
		return e.fullTextFallback(ctx, workspaceID, query, limit)
	}
	return out, nil
}

func (e *Engine) fullTextFallback(ctx context.Context, workspaceID, query string, limit int) ([]model.Issue, error) {
	if e.issueSearch == nil {
		return nil, nil
	}
	return e.issueSearch.Search(ctx, workspaceID, query, limit)
}

// IndexIssue generates an embedding for an issue's title+description
// and upserts it into issue_embeddings. Called after every Create
// and Update so vector search stays current.
func (e *Engine) IndexIssue(ctx context.Context, issue model.Issue) error {
	if !e.IsAvailable() || e.pool == nil {
		return ErrAIUnavailable
	}
	text := issue.Title + " " + issue.Description
	vec, err := e.callEmbeddingsViaLens(ctx, issue.Identifier, text)
	if err != nil {
		return err
	}
	var vecLit strings.Builder
	vecLit.WriteByte('[')
	for i, f := range vec {
		if i > 0 {
			vecLit.WriteByte(',')
		}
		vecLit.WriteString(strconv.FormatFloat(float64(f), 'f', -1, 32))
	}
	vecLit.WriteByte(']')
	_, err = e.pool.Exec(ctx,
		`INSERT INTO issue_embeddings (issue_id, embedding, updated_at)
        VALUES ($1, $2::vector, NOW())
        ON CONFLICT (issue_id) DO UPDATE SET
            embedding = EXCLUDED.embedding, updated_at = NOW()`,
		issue.ID, vecLit.String(),
	)
	return err
}

// ─────────────────────────────────────────────────────────
// Feature 6: AI Cost per Workload (pure heuristic, no AI call)
// ─────────────────────────────────────────────────────────

// EstimateIssueCost returns a rough USD estimate of the AI tokens
// the issue is likely to consume across its lifetime. Heuristic
// only — fast, free, deterministic. The real AI cost is recorded
// later by the Lens syncer once the issue is actually worked on.
func (e *Engine) EstimateIssueCost(_ context.Context, issue model.Issue) float64 {
	hasLabel := func(name string) bool {
		for _, l := range issue.Labels {
			if strings.EqualFold(l, name) {
				return true
			}
		}
		return false
	}

	switch {
	case hasLabel("epic"):
		return 100.0
	case hasLabel("feature"):
		return 20.0
	case hasLabel("bug"):
		return 3.50
	default:
		// Fall back to a priority-based estimate.
		switch issue.Priority {
		case model.PriorityUrgent:
			return 15.0
		case model.PriorityHigh:
			return 10.0
		case model.PriorityMedium:
			return 5.0
		case model.PriorityLow, model.PriorityNone:
			return 2.0
		}
	}
	return 5.0
}

// extractJSON pulls the first {...} or [...] block out of a model
// response. LLMs occasionally add prose around the structured output
// — this lets us recover gracefully without strict-mode prompting.
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	for i, ch := range s {
		if ch == '{' || ch == '[' {
			// Find matching close by counting depth.
			open := ch
			close := byte('}')
			if open == '[' {
				close = ']'
			}
			depth := 0
			for j := i; j < len(s); j++ {
				switch s[j] {
				case byte(open):
					depth++
				case close:
					depth--
					if depth == 0 {
						return s[i : j+1]
					}
				}
			}
		}
	}
	return s
}
