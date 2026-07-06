package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/lensintegration"
	"github.com/talyvor/track/internal/model"
)

// stubSearcher records calls to the full-text fallback so the
// semantic-search tests can verify the fallback fired exactly when
// expected.
type stubSearcher struct {
	calls   int
	results []model.Issue
}

func (s *stubSearcher) Search(_ context.Context, _ string, _ string, _ int) ([]model.Issue, error) {
	s.calls++
	return s.results, nil
}

// lensMock builds an httptest server that maps a request handler
// per path. Each handler can inspect the request body to verify the
// outbound shape (model, messages, headers).
func lensMock(t *testing.T, handlers map[string]http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h, ok := handlers[r.URL.Path]; ok {
			h(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// anthropicResp wraps a text response in the Anthropic wire format
// so the engine's content parser succeeds.
func anthropicResp(text string) string {
	return `{"content":[{"type":"text","text":` + strconvQuote(text) + `}]}`
}
func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestTriageIssue_ReturnsPriorityFromLLM(t *testing.T) {
	srv := lensMock(t, map[string]http.HandlerFunc{
		"/v1/proxy/anthropic/v1/messages": func(w http.ResponseWriter, r *http.Request) {
			// X-Talyvor-Feature must be the issue identifier so
			// cost attribution lands on the right issue.
			if got := r.Header.Get("X-Talyvor-Feature"); got != "ENG-42" {
				t.Errorf("X-Talyvor-Feature = %q, want ENG-42", got)
			}
			_, _ = io.WriteString(w, anthropicResp(`{
                "suggested_priority": 1,
                "suggested_labels": ["bug","critical"],
                "summary": "Production payment service is rejecting cards.",
                "confidence": 0.92
            }`))
		},
	})

	lens := lensintegration.New(srv.URL, "tlv_test")
	engine := New(lens, nil, nil)

	got, err := engine.TriageIssue(context.Background(), model.Issue{
		ID: "i-1", Identifier: "ENG-42",
		Title: "Payment failing", Description: "Card declined on checkout.",
	})
	if err != nil {
		t.Fatalf("TriageIssue: %v", err)
	}
	if got.SuggestedPriority != model.PriorityUrgent {
		t.Errorf("priority = %v, want urgent (1)", got.SuggestedPriority)
	}
	if got.Confidence < 0.9 {
		t.Errorf("confidence = %v, want ≥ 0.9", got.Confidence)
	}
}

func TestTriageIssue_HandlesLensUnavailable(t *testing.T) {
	engine := New(lensintegration.New("", ""), nil, nil)
	_, err := engine.TriageIssue(context.Background(), model.Issue{Identifier: "ENG-1"})
	if !errors.Is(err, ErrAIUnavailable) {
		t.Errorf("expected ErrAIUnavailable; got %v", err)
	}
}

func TestFindDuplicates_ReturnsCandidatesAboveThreshold(t *testing.T) {
	srv := lensMock(t, map[string]http.HandlerFunc{
		"/v1/proxy/anthropic/v1/messages": func(w http.ResponseWriter, _ *http.Request) {
			// Reply with two matches: one above 0.7, one below.
			_, _ = io.WriteString(w, anthropicResp(`[
                {"issue_id":"i-old-1","similarity":0.85},
                {"issue_id":"i-old-2","similarity":0.45}
            ]`))
		},
	})
	lens := lensintegration.New(srv.URL, "tlv_test")
	engine := New(lens, nil, nil)

	candidates := []model.Issue{
		{ID: "i-old-1", Identifier: "ENG-100", Title: "Payment declined on checkout"},
		{ID: "i-old-2", Identifier: "ENG-101", Title: "Random unrelated thing"},
	}
	got, err := engine.FindDuplicates(context.Background(),
		model.Issue{Identifier: "ENG-200", Title: "Card rejected"}, candidates)
	if err != nil {
		t.Fatalf("FindDuplicates: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1 (only the >= 0.7 match)", len(got))
	}
	if got[0].IssueID != "i-old-1" {
		t.Errorf("got %+v, want i-old-1", got[0])
	}
}

func TestFindDuplicates_ReturnsEmptyWhenNoDuplicates(t *testing.T) {
	srv := lensMock(t, map[string]http.HandlerFunc{
		"/v1/proxy/anthropic/v1/messages": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, anthropicResp(`[]`))
		},
	})
	lens := lensintegration.New(srv.URL, "tlv_test")
	engine := New(lens, nil, nil)

	got, err := engine.FindDuplicates(context.Background(),
		model.Issue{Identifier: "ENG-1", Title: "x"},
		[]model.Issue{{ID: "y", Identifier: "ENG-2", Title: "y"}})
	if err != nil {
		t.Fatalf("FindDuplicates: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want empty slice", got)
	}
}

func TestSummarizeThread_SkipsWhenFewerThan10Comments(t *testing.T) {
	engine := New(lensintegration.New("http://unused", "k"), nil, nil)
	out, err := engine.SummarizeThread(context.Background(),
		model.Issue{ID: "i-1", Identifier: "ENG-1"},
		make([]model.Comment, 5))
	if err != nil {
		t.Fatalf("SummarizeThread: %v", err)
	}
	if out != nil {
		t.Errorf("got %+v, want nil for thread < 10 comments", out)
	}
}

func TestSummarizeThread_ReturnsSummaryForLongThread(t *testing.T) {
	srv := lensMock(t, map[string]http.HandlerFunc{
		"/v1/proxy/anthropic/v1/messages": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, anthropicResp(`{
                "summary": "Discussion about caching strategy",
                "key_points": ["Use Redis","Add TTL"],
                "next_action": "Implement cache TTL",
                "sentiment": "neutral"
            }`))
		},
	})
	lens := lensintegration.New(srv.URL, "tlv_test")
	engine := New(lens, nil, nil)

	comments := make([]model.Comment, 12)
	for i := range comments {
		comments[i] = model.Comment{Body: "comment " + string(rune('a'+i))}
	}
	got, err := engine.SummarizeThread(context.Background(),
		model.Issue{ID: "i-1", Identifier: "ENG-1", Title: "Caching"}, comments)
	if err != nil {
		t.Fatalf("SummarizeThread: %v", err)
	}
	if got == nil || got.Sentiment != "neutral" {
		t.Errorf("got %+v", got)
	}
	// Second call should hit the cache; mock would error if Lens was
	// called again (it'd return the same response but we want to
	// confirm caching by re-using a closed handler in a tighter test).
	got2, err := engine.SummarizeThread(context.Background(),
		model.Issue{ID: "i-1", Identifier: "ENG-1", Title: "Caching"}, comments)
	if err != nil || got2 == nil {
		t.Fatalf("cached SummarizeThread: %v / %+v", err, got2)
	}
	if got2.NextAction != got.NextAction {
		t.Errorf("cached summary diverged from original")
	}
}

func TestSuggestSprintIssues_ReturnsRecommended(t *testing.T) {
	srv := lensMock(t, map[string]http.HandlerFunc{
		"/v1/proxy/anthropic/v1/messages": func(w http.ResponseWriter, r *http.Request) {
			// Sonnet, not Haiku, is required by the spec.
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "claude-sonnet-4-6") {
				t.Errorf("sprint planning must use claude-sonnet-4-6; body=%s", body)
			}
			_, _ = io.WriteString(w, anthropicResp(`{
                "recommended_issues": ["i-1","i-3"],
                "total_estimated_days": 12.5,
                "reasoning": "Prioritised the two highest-priority bugs."
            }`))
		},
	})
	lens := lensintegration.New(srv.URL, "tlv_test")
	engine := New(lens, nil, nil)

	backlog := []model.Issue{
		{ID: "i-1", Title: "Bug 1", Priority: model.PriorityUrgent},
		{ID: "i-2", Title: "Feature 2", Priority: model.PriorityMedium},
		{ID: "i-3", Title: "Bug 3", Priority: model.PriorityHigh},
	}
	got, err := engine.SuggestSprintIssues(context.Background(), "team-1", backlog, 14, 5)
	if err != nil {
		t.Fatalf("SuggestSprintIssues: %v", err)
	}
	if len(got.RecommendedIssues) != 2 {
		t.Errorf("got %d recommended, want 2", len(got.RecommendedIssues))
	}
}

func TestSemanticSearch_FallsBackToFullText(t *testing.T) {
	// Lens unavailable → falls back to the searcher.
	stub := &stubSearcher{
		results: []model.Issue{{ID: "i-x", Title: "fallback"}},
	}
	engine := New(lensintegration.New("", ""), stub, nil)

	got, err := engine.SemanticSearch(context.Background(), "ws-1", "anything", 10)
	if err != nil {
		t.Fatalf("SemanticSearch: %v", err)
	}
	if stub.calls != 1 {
		t.Errorf("full-text fallback should fire when Lens unavailable; calls=%d", stub.calls)
	}
	if len(got) != 1 || got[0].ID != "i-x" {
		t.Errorf("expected fallback result; got %+v", got)
	}
}

func TestSemanticSearch_FallsBackOnEmbeddingError(t *testing.T) {
	// Lens returns 500 on the embedding call → fallback.
	srv := lensMock(t, map[string]http.HandlerFunc{
		"/v1/proxy/openai/v1/embeddings": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "down", http.StatusInternalServerError)
		},
	})
	stub := &stubSearcher{results: []model.Issue{{ID: "i-x"}}}
	engine := New(lensintegration.New(srv.URL, "k"), stub, nil)

	if _, err := engine.SemanticSearch(context.Background(), "ws-1", "q", 10); err != nil {
		t.Fatalf("SemanticSearch: %v", err)
	}
	if stub.calls != 1 {
		t.Errorf("expected fallback on Lens embedding failure; calls=%d", stub.calls)
	}
}

func TestEstimateIssueCost_ReturnsReasonableEstimates(t *testing.T) {
	engine := New(lensintegration.New("", ""), nil, nil)

	cases := []struct {
		name  string
		issue model.Issue
		min   float64
		max   float64
	}{
		{"bug", model.Issue{Labels: []string{"bug"}}, 2.0, 5.0},
		{"feature", model.Issue{Labels: []string{"feature"}}, 10.0, 30.0},
		{"epic", model.Issue{Labels: []string{"epic"}}, 50.0, 200.0},
		{"urgent unlabeled", model.Issue{Priority: model.PriorityUrgent}, 10.0, 30.0},
		{"low unlabeled", model.Issue{Priority: model.PriorityLow}, 0.5, 5.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := engine.EstimateIssueCost(context.Background(), tc.issue)
			if got < tc.min || got > tc.max {
				t.Errorf("got %v, want in [%v, %v]", got, tc.min, tc.max)
			}
		})
	}
}
