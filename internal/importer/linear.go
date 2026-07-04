package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/talyvor/track/internal/model"
)

// linear.go — T8 Build C.3: the Linear GraphQL IssueSource.
//
// Auth is the unusual `Authorization: <API_KEY>` (NO "Bearer "). Pagination is Relay cursor
// (issues(first,after) → pageInfo{hasNextPage,endCursor}). Rate-limit is signalled by HTTP 400 with a
// top-level errors[].extensions.code == "RATELIMITED" (NOT 429); other errors can arrive on HTTP 200 in
// errors[], so a 200 is not automatically success — the client parses the body every time.

const defaultLinearURL = "https://api.linear.app/graphql"

const linearIssuesQuery = `query($teamId: String!, $after: String) {
  team(id: $teamId) {
    issues(first: 100, after: $after) {
      pageInfo { hasNextPage endCursor }
      nodes { identifier title description state { name } priority labels { nodes { name } } }
    }
  }
}`

type linearClient struct {
	http  *http.Client
	url   string
	token string // sent verbatim as the Authorization header — NO "Bearer " prefix
	team  string
	retry retryer
}

func newLinearClient(token, teamKey, baseURL string) *linearClient {
	url := baseURL
	if url == "" {
		url = defaultLinearURL
	}
	return &linearClient{
		http:  &http.Client{Timeout: defaultRequestTimeout},
		url:   url,
		token: token,
		team:  teamKey,
		retry: defaultRetryer(),
	}
}

type linearNode struct {
	Identifier  string `json:"identifier"`
	Title       string `json:"title"`
	Description string `json:"description"`
	State       struct {
		Name string `json:"name"`
	} `json:"state"`
	Priority int `json:"priority"`
	Labels   struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
}

type linearResp struct {
	Data struct {
		Team struct {
			Issues struct {
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
				Nodes []linearNode `json:"nodes"`
			} `json:"issues"`
		} `json:"team"`
	} `json:"data"`
	Errors []struct {
		Message    string `json:"message"`
		Extensions struct {
			Code string `json:"code"`
		} `json:"extensions"`
	} `json:"errors"`
}

type linearPage struct {
	issues  []model.Issue
	hasNext bool
	cursor  string
}

// fetchPage issues one paginated query, retrying ONLY on a RATELIMITED response (honoring the reset header).
// A 200 carrying errors[] is a real error, not a silent empty page.
func (c *linearClient) fetchPage(ctx context.Context, after string) (linearPage, error) {
	vars := map[string]any{"teamId": c.team}
	if after != "" {
		vars["after"] = after
	}
	body, _ := json.Marshal(map[string]any{"query": linearIssuesQuery, "variables": vars})

	var lastErr error
	for attempt := 0; attempt < c.retry.attempts(); attempt++ {
		status, hdr, respBody, err := postJSON(ctx, c.http, c.url, map[string]string{"Authorization": c.token}, body)
		if err != nil {
			return linearPage{}, fmt.Errorf("linear: request: %w", err)
		}
		var parsed linearResp
		if e := json.Unmarshal(respBody, &parsed); e != nil {
			return linearPage{}, fmt.Errorf("linear: decode (http %d): %w", status, e)
		}
		// Rate-limit: HTTP 400 whose errors[] carries code=RATELIMITED → retryable, distinct signal.
		if status == http.StatusBadRequest && linearRateLimited(parsed) {
			lastErr = fmt.Errorf("linear: %w", errRateLimited)
			c.retry.wait(linearResetBackoff(hdr))
			continue
		}
		if status != http.StatusOK {
			return linearPage{}, fmt.Errorf("linear: http %d: %s", status, firstLinearError(parsed))
		}
		// A 200 with errors[] is NOT a silent success.
		if len(parsed.Errors) > 0 {
			return linearPage{}, fmt.Errorf("linear: api error: %s", firstLinearError(parsed))
		}
		iss := parsed.Data.Team.Issues
		return linearPage{issues: mapLinearNodes(iss.Nodes), hasNext: iss.PageInfo.HasNextPage, cursor: iss.PageInfo.EndCursor}, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("linear: %w (retries exhausted)", errRateLimited)
	}
	return linearPage{}, lastErr
}

func linearRateLimited(r linearResp) bool {
	for _, e := range r.Errors {
		if e.Extensions.Code == "RATELIMITED" {
			return true
		}
	}
	return false
}

func firstLinearError(r linearResp) string {
	if len(r.Errors) > 0 {
		return r.Errors[0].Message
	}
	return "unknown error"
}

// linearResetBackoff derives a wait from X-RateLimit-Requests-Reset (epoch ms). Absent/invalid ⇒ 1s.
func linearResetBackoff(h http.Header) time.Duration {
	for _, key := range []string{"X-RateLimit-Requests-Reset", "X-RateLimit-Complexity-Reset"} {
		if v := h.Get(key); v != "" {
			if ms, err := strconv.ParseInt(v, 10, 64); err == nil {
				d := time.Until(time.UnixMilli(ms))
				if d > 0 {
					return d
				}
			}
		}
	}
	return time.Second
}

func mapLinearNodes(nodes []linearNode) []model.Issue {
	out := make([]model.Issue, 0, len(nodes))
	for _, n := range nodes {
		labels := make([]string, 0, len(n.Labels.Nodes))
		for _, l := range n.Labels.Nodes {
			labels = append(labels, l.Name)
		}
		out = append(out, model.Issue{
			Identifier:  n.Identifier, // the provider-key (ENG-123) — what C.2's upsert + PR #30 resolve on
			Title:       n.Title,
			Description: n.Description,
			Status:      mapLinearStatus(n.State.Name),
			Priority:    linearPriorityFromInt(n.Priority),
			Labels:      labels,
		})
	}
	return out
}

// linearPriorityFromInt maps Linear's numeric priority (0 none, 1 urgent, 2 high, 3 medium/normal, 4 low) to
// Track's scale. Unknown ⇒ none.
func linearPriorityFromInt(p int) model.IssuePriority {
	switch p {
	case 1:
		return model.PriorityUrgent
	case 2:
		return model.PriorityHigh
	case 3:
		return model.PriorityMedium
	case 4:
		return model.PriorityLow
	default:
		return model.PriorityNone
	}
}

// linearSource drains the Linear cursor pagination behind Next() — the seam pattern from Build A: buffer a
// page, yield its issues one by one, fetch the next page on exhaustion. A fetch failure is surfaced ONCE as a
// SourceRow.Err (so run() records it and the job ends partial/failed) and then the source stops — NEVER a
// silent stop that would look like a complete import.
type linearSource struct {
	client  *linearClient
	buf     []model.Issue
	pos     int
	cursor  string
	hasNext bool
	started bool
	done    bool
	rowNum  int
}

func newLinearSource(token, teamKey, baseURL string) *linearSource {
	return &linearSource{client: newLinearClient(token, teamKey, baseURL)}
}

func (s *linearSource) Next() (SourceRow, bool) {
	if s.done {
		return SourceRow{}, false
	}
	if s.pos >= len(s.buf) {
		if s.started && !s.hasNext {
			s.done = true
			return SourceRow{}, false // clean exhaustion
		}
		page, err := s.client.fetchPage(context.Background(), s.cursor)
		if err != nil {
			s.done = true
			return SourceRow{RowNum: s.rowNum + 1, Err: fmt.Errorf("linear: fetch page: %w", err)}, true
		}
		s.started, s.buf, s.pos, s.cursor, s.hasNext = true, page.issues, 0, page.cursor, page.hasNext
		if len(s.buf) == 0 {
			s.done = true
			return SourceRow{}, false
		}
	}
	iss := s.buf[s.pos]
	s.pos++
	s.rowNum++
	return SourceRow{Issue: iss, RowNum: s.rowNum}, true
}
