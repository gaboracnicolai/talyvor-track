package importer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/talyvor/track/internal/model"
)

// jira.go — T8 Build C.3: the Jira Cloud REST IssueSource.
//
// Endpoint is the CURRENT POST /rest/api/3/search/jql (the old /search is 410 Gone). Auth is Basic
// base64("email:api_token") — the C.1-stored token for a Jira integration IS the "email:api_token" pair (C.1
// keeps a single opaque token per provider; Jira's Basic credential is that pair). Pagination is
// nextPageToken/isLast (no total). fields is mandatory. description arrives as ADF JSON → flattened to text.
// Rate-limit is HTTP 429 + Retry-After, honored.

const jiraSearchPath = "/rest/api/3/search/jql"

var jiraFields = []string{"summary", "description", "status", "priority", "labels"}

type jiraClient struct {
	http    *http.Client
	url     string
	auth    string // base64("email:api_token")
	project string
	retry   retryer
}

func newJiraClient(emailAPIToken, projectKey, baseURL string) *jiraClient {
	return &jiraClient{
		http:    &http.Client{Timeout: defaultRequestTimeout},
		url:     strings.TrimRight(baseURL, "/") + jiraSearchPath,
		auth:    base64.StdEncoding.EncodeToString([]byte(emailAPIToken)),
		project: projectKey,
		retry:   defaultRetryer(),
	}
}

type jiraIssue struct {
	Key    string `json:"key"` // PROJ-123 — the identifier (top-level, not in fields)
	Fields struct {
		Summary     string          `json:"summary"`
		Description json.RawMessage `json:"description"` // ADF document
		Status      struct {
			Name string `json:"name"`
		} `json:"status"`
		Priority struct {
			Name string `json:"name"`
		} `json:"priority"`
		Labels []string `json:"labels"`
	} `json:"fields"`
}

type jiraResp struct {
	Issues        []jiraIssue `json:"issues"`
	IsLast        bool        `json:"isLast"`
	NextPageToken string      `json:"nextPageToken"`
	ErrorMessages []string    `json:"errorMessages"`
}

type jiraPage struct {
	issues    []model.Issue
	isLast    bool
	nextToken string
}

// fetchPage issues one search, retrying ONLY on 429 (honoring Retry-After).
func (c *jiraClient) fetchPage(ctx context.Context, pageToken string) (jiraPage, error) {
	reqBody := map[string]any{
		"jql":        fmt.Sprintf("project = %q", c.project),
		"fields":     jiraFields,
		"maxResults": 100,
	}
	if pageToken != "" {
		reqBody["nextPageToken"] = pageToken
	}
	body, _ := json.Marshal(reqBody)

	var lastErr error
	for attempt := 0; attempt < c.retry.attempts(); attempt++ {
		status, hdr, respBody, err := postJSON(ctx, c.http, c.url, map[string]string{"Authorization": "Basic " + c.auth}, body)
		if err != nil {
			return jiraPage{}, fmt.Errorf("jira: request: %w", err)
		}
		if status == http.StatusTooManyRequests {
			lastErr = fmt.Errorf("jira: %w", errRateLimited)
			c.retry.wait(parseRetryAfter(hdr, defaultRetryAfter))
			continue
		}
		if status != http.StatusOK {
			return jiraPage{}, fmt.Errorf("jira: http %d: %s", status, firstJiraError(respBody))
		}
		var parsed jiraResp
		if e := json.Unmarshal(respBody, &parsed); e != nil {
			return jiraPage{}, fmt.Errorf("jira: decode: %w", e)
		}
		if len(parsed.ErrorMessages) > 0 {
			return jiraPage{}, fmt.Errorf("jira: api error: %s", parsed.ErrorMessages[0])
		}
		return jiraPage{issues: mapJiraIssues(parsed.Issues), isLast: parsed.IsLast, nextToken: parsed.NextPageToken}, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("jira: %w (retries exhausted)", errRateLimited)
	}
	return jiraPage{}, lastErr
}

const defaultRetryAfter = time.Second // used when Retry-After is absent

func firstJiraError(body []byte) string {
	var r jiraResp
	if json.Unmarshal(body, &r) == nil && len(r.ErrorMessages) > 0 {
		return r.ErrorMessages[0]
	}
	return "unknown error"
}

func mapJiraIssues(issues []jiraIssue) []model.Issue {
	out := make([]model.Issue, 0, len(issues))
	for _, it := range issues {
		labels := it.Fields.Labels
		if labels == nil {
			labels = []string{}
		}
		out = append(out, model.Issue{
			Identifier:  it.Key, // provider-key (PROJ-123)
			Title:       it.Fields.Summary,
			Description: adfToText(it.Fields.Description),
			Status:      mapJiraStatus(it.Fields.Status.Name),
			Priority:    mapJiraPriority(it.Fields.Priority.Name),
			Labels:      labels,
		})
	}
	return out
}

// ── ADF → plain text ──────────────────────────────────────────────────────────────────────────────────────
// Jira v3 returns description as an Atlassian Document Format tree. We flatten it to readable text: concatenate
// text nodes, newline after block-level nodes. Robust to any shape (nil-safe, unknown types recurse), and
// tolerant of an older plain-string description.

type adfNode struct {
	Type    string    `json:"type"`
	Text    string    `json:"text"`
	Content []adfNode `json:"content"`
}

func adfToText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var doc adfNode
	if err := json.Unmarshal(raw, &doc); err != nil {
		var s string // tolerate a plain-string description (older API)
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
		return ""
	}
	var b strings.Builder
	walkADF(doc, &b)
	return strings.TrimSpace(b.String())
}

func walkADF(n adfNode, b *strings.Builder) {
	if n.Text != "" {
		b.WriteString(n.Text)
	}
	if n.Type == "hardBreak" {
		b.WriteByte('\n')
	}
	for _, c := range n.Content {
		walkADF(c, b)
	}
	switch n.Type { // block-level nodes end a line
	case "paragraph", "heading", "listItem", "blockquote", "codeBlock":
		b.WriteByte('\n')
	}
}

// jiraSource drains nextPageToken/isLast pagination behind Next() — same seam pattern + same fetch-failure
// observability (surface the error once via SourceRow.Err, then stop; never a silent complete-looking stop).
type jiraSource struct {
	client    *jiraClient
	buf       []model.Issue
	pos       int
	nextToken string
	isLast    bool
	started   bool
	done      bool
	rowNum    int
}

func newJiraSource(emailAPIToken, projectKey, baseURL string) *jiraSource {
	return &jiraSource{client: newJiraClient(emailAPIToken, projectKey, baseURL)}
}

func (s *jiraSource) Next() (SourceRow, bool) {
	if s.done {
		return SourceRow{}, false
	}
	if s.pos >= len(s.buf) {
		if s.started && s.isLast {
			s.done = true
			return SourceRow{}, false // clean exhaustion
		}
		page, err := s.client.fetchPage(context.Background(), s.nextToken)
		if err != nil {
			s.done = true
			return SourceRow{RowNum: s.rowNum + 1, Err: fmt.Errorf("jira: fetch page: %w", err)}, true
		}
		s.started, s.buf, s.pos, s.nextToken, s.isLast = true, page.issues, 0, page.nextToken, page.isLast
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
