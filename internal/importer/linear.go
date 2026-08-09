package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
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
      nodes { identifier title description state { name type } priority labels { nodes { name } } }
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

// newLinearClient builds the Linear client. Production passes no httpc → the SSRF-guarded client; tests
// may inject a loopback-capable client (httptest binds 127.0.0.1, which the guard blocks by design).
func newLinearClient(token, teamKey, baseURL string, httpc ...*http.Client) *linearClient {
	url := baseURL
	if url == "" {
		url = defaultLinearURL
	}
	return &linearClient{
		http:  clientOrSafe(httpc),
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
		// Type is Linear's CANONICAL state category — the value a team cannot rename. Absent ⇒ ""
		// ⇒ today's name-only behaviour, which is what makes the query change fail-safe on the
		// decoding side. The query change itself is not fail-safe (an unknown field 400s the whole
		// document), which is why it was measured against the live schema before it was made; see
		// mapLinearStateType and scripts/w34-linear-schema-probe.py.
		Type string `json:"type"`
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
	issues  []mappedIssue
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

func mapLinearNodes(nodes []linearNode) []mappedIssue {
	out := make([]mappedIssue, 0, len(nodes))
	for _, n := range nodes {
		labels := make([]string, 0, len(n.Labels.Nodes))
		for _, l := range n.Labels.Nodes {
			labels = append(labels, l.Name)
		}
		status, statusOK := mapLinearStatus(n.State.Name)
		var fallback statusFallback
		if !statusOK {
			status, fallback = resolveLinearStateType(n.State.Type, status)
		}
		prio, prioOK := linearPriorityFromInt(n.Priority)
		out = append(out, mappedIssue{
			issue: model.Issue{
				Identifier:  n.Identifier, // the provider-key (ENG-123) — what C.2's upsert + PR #30 resolve on
				Title:       n.Title,
				Description: n.Description,
				Status:      status,
				Priority:    prio,
				Labels:      labels,
			},
			notes: collectNotes(n.State.Name, status, statusOK, fallback, strconv.Itoa(n.Priority), prio, prioOK),
		})
	}
	return out
}

// linearPriorityFromInt maps Linear's numeric priority (0 none, 1 urgent, 2 high, 3 medium/normal, 4 low) to
// Track's scale, and reports whether the value was ON that scale. 0 is a REAL value the user chose
// ("No priority"), so it is recognised; anything outside 0..4 is not a Linear priority and falls
// back to none — now reported rather than assumed.
func linearPriorityFromInt(p int) (model.IssuePriority, bool) {
	switch p {
	case 0:
		return model.PriorityNone, true
	case 1:
		return model.PriorityUrgent, true
	case 2:
		return model.PriorityHigh, true
	case 3:
		return model.PriorityMedium, true
	case 4:
		return model.PriorityLow, true
	default:
		return model.PriorityNone, false
	}
}

// resolveLinearStateType is the second chance an unrecognised Linear state NAME gets. It never runs
// for a name mapLinearStatus knows, so a recognised import is byte-for-byte what it was.
//
// It returns the status to use plus the note material describing WHICH of the three things happened,
// because a type that never arrived must not be reportable as one that arrived and resolved — that is
// the only way a real tenant's first import can tell anyone whether this code executed. Exactly
// #73's argument for the Jira half; the failure shape is the provider-independent one.
func resolveLinearStateType(typ string, unresolved model.IssueStatus) (model.IssueStatus, statusFallback) {
	if strings.TrimSpace(typ) == "" {
		return unresolved, statusFallback{via: viaNoStateType}
	}
	mapped, ok := mapLinearStateType(typ)
	if !ok {
		return unresolved, statusFallback{via: viaStateType, value: typ}
	}
	return mapped, statusFallback{via: viaStateType, value: typ, resolved: true}
}

// linearSource drains the Linear cursor pagination behind Next() — the seam pattern from Build A: buffer a
// page, yield its issues one by one, fetch the next page on exhaustion. A fetch failure is surfaced ONCE as a
// SourceRow.Err (so run() records it and the job ends partial/failed) and then the source stops — NEVER a
// silent stop that would look like a complete import.
type linearSource struct {
	client  *linearClient
	buf     []mappedIssue
	pos     int
	cursor  string
	hasNext bool
	started bool
	done    bool
	rowNum  int
}

func newLinearSource(token, teamKey, baseURL string, httpc ...*http.Client) *linearSource {
	return &linearSource{client: newLinearClient(token, teamKey, baseURL, httpc...)}
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
	m := s.buf[s.pos]
	s.pos++
	s.rowNum++
	return SourceRow{Issue: m.issue, RowNum: s.rowNum, Notes: m.notes}, true
}
