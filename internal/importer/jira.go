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

// jiraFields is the `fields` list every search asks for. duedate and resolutiondate are plain
// scalars nested nowhere, so — like statusCategory before them — they are free: absent ⇒ the zero
// value ⇒ exactly today's nil. Narrowing this list takes them away silently, which is why
// TestJiraRequest_AsksForTheDateFields asserts it at the WIRE, not at the fixture.
var jiraFields = []string{"summary", "description", "status", "priority", "labels", "duedate", "resolutiondate", jiraAPICreatedField, jiraAPIUpdatedField, jiraAPIResolutionField}

// jiraTimeLayouts are pinned BY HAND from what a real Jira actually sends, in the order tried.
//
// ⚠ NOT time.RFC3339, AND THAT IS THE POINT. Measured against jira.atlassian.com (anonymous REST,
// negative-controlled first): duedate arrives as "2027-12-31" — a bare date, no time, no offset —
// and resolutiondate as "2026-08-06T20:06:39.000+0000", whose offset is `+0000`, not `+00:00`.
// time.Parse with time.RFC3339 REFUSES BOTH. Reaching for the obvious constant would have written
// nil into every row of both columns and reported {imported:N, warnings:[]}, while every fabricated
// RFC3339 fixture in the test package passed. RFC3339 is kept in the list anyway, because a shape
// this environment cannot prove absent is not a shape to refuse.
var jiraTimeLayouts = []string{
	"2006-01-02T15:04:05.000-0700", // measured: resolutiondate
	time.RFC3339,                   // tolerated: the same instant with a colon in the offset, or Z
	"2006-01-02",                   // measured: duedate
}

// parseJiraTime returns the instant and true, or false if no pinned layout accepts the value. The
// caller REPORTS a false — it never silently nils, which is what keeps the hand-pinned list honest.
func parseJiraTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range jiraTimeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

type jiraClient struct {
	http    *http.Client
	url     string
	auth    string // base64("email:api_token")
	project string
	retry   retryer
}

// newJiraClient builds the Jira client. Production passes no httpc → the SSRF-guarded client; tests may
// inject a loopback-capable client (httptest binds 127.0.0.1, which the guard blocks by design).
func newJiraClient(emailAPIToken, projectKey, baseURL string, httpc ...*http.Client) *jiraClient {
	return &jiraClient{
		http:    clientOrSafe(httpc),
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
			// The canonical, non-renameable state category. It is NESTED INSIDE the `status` field
			// jiraFields already requests, so it costs no query change and a Jira that omits it
			// simply decodes to the zero value — see resolveJiraStatusCategory.
			StatusCategory struct {
				Key  string `json:"key"`  // undefined | new | indeterminate | done
				Name string `json:"name"` // the display name, e.g. "To Do" — not mapped, only reported
			} `json:"statusCategory"`
		} `json:"status"`
		Priority struct {
			Name string `json:"name"`
		} `json:"priority"`
		Labels []string `json:"labels"`

		// Both plain scalars, both absent-safe: a Jira that sends neither decodes to "" and imports
		// exactly as it did before this merge. See jiraTimeLayouts for the shapes they arrive in.
		DueDate        string `json:"duedate"`
		ResolutionDate string `json:"resolutiondate"`

		// ⚠ NOT ABSENT-SAFE THE WAY THE TWO ABOVE ARE, AND THE DIFFERENCE IS THE WHOLE MERGE. Those
		// two land in NULLABLE columns, so a Jira that sends neither leaves a truthful NULL. This one
		// lands in `created_at`, which is DEFAULT NOW() — an absent value is not an empty column, it
		// is a WRONG one that looks right. So "" is REPORTED here (viaNoCreatedField), never shrugged off.
		Created string `json:"created"`

		// ⚠ THE SAME DEFAULTED-COLUMN TRAP AS `Created`, on the column the product SORTS BY.
		// `updated_at` is DEFAULT NOW() too, so an absent value is a wrong one that looks right —
		// and unlike created_at, whose loss shows up as a number on an analytics page, this one
		// reorders the issue list and prints "updated just now" on every imported row. Reported as
		// viaNoUpdatedField, never shrugged off.
		Updated string `json:"updated"`

		// ⚠ json.RawMessage RATHER THAN A DECODED STRUCT, AND THAT IS THE POINT. Jira sends
		// `"resolution": null` for an unresolved issue and omits the key entirely when the `fields`
		// list did not ask (both MEASURED on the shipped endpoint) — and a *struct is nil for both.
		// Keeping the bytes is the only way jiraAPIResolution can tell "this issue is open" from
		// "this request stopped asking", which are the same database row and opposite reports.
		Resolution json.RawMessage `json:"resolution"`
	} `json:"fields"`
}

type jiraResp struct {
	Issues        []jiraIssue `json:"issues"`
	IsLast        bool        `json:"isLast"`
	NextPageToken string      `json:"nextPageToken"`
	ErrorMessages []string    `json:"errorMessages"`
}

type jiraPage struct {
	issues    []mappedIssue
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

func mapJiraIssues(issues []jiraIssue) []mappedIssue {
	out := make([]mappedIssue, 0, len(issues))
	for _, it := range issues {
		labels := it.Fields.Labels
		if labels == nil {
			labels = []string{}
		}
		status, statusOK := mapJiraStatus(it.Fields.Status.Name)
		var fallback statusFallback
		if !statusOK {
			status, fallback = resolveJiraStatusCategory(it.Fields.Status.StatusCategory.Key, status)
		}
		prio, prioOK := mapJiraPriority(it.Fields.Priority.Name)
		// `resolution` says whether closed work was FINISHED or ABANDONED, and it runs HERE for two
		// reasons that are both load-bearing. AFTER the category fallback above, because on the
		// measured instance 138 of 3,000 resolved issues reach `done` only through statusCategory —
		// a route the CSV mapper does not have. And BEFORE jiraCompletedAt below, because that
		// mapping gates on the status this line can change, and on THIS transport the mapper's gate
		// is the only one: UpsertByIdentifier passes CompletedAt through ungated, where Create nils
		// it itself. Reversing these two lines lands a cancelled issue carrying a completion time,
		// which analytics counts as delivered work. See api_resolution.go.
		status, resolutionNotes := jiraAPIResolution(it.Fields.Resolution, status)
		due, dueNotes := jiraDueDate(it.Fields.DueDate)
		completed, completedNotes := jiraCompletedAt(it.Fields.ResolutionDate, status)
		created, createdNotes := jiraAPICreated(it.Fields.Created)
		updated, updatedNotes := jiraAPIUpdated(it.Fields.Updated)
		// `description` is the only field on this transport that is a DOCUMENT rather than a
		// scalar, and its losses are per-NODE rather than per-value: see adf_attrs.go.
		description, descriptionNotes := adfToText(it.Fields.Description)
		out = append(out, mappedIssue{
			issue: model.Issue{
				Identifier:  it.Key, // provider-key (PROJ-123)
				Title:       it.Fields.Summary,
				Description: description,
				Status:      status,
				Priority:    prio,
				Labels:      labels,
				DueDate:     due,
				CompletedAt: completed,
				CreatedAt:   created,
				UpdatedAt:   updated,
			},
			notes: append(collectNotes(it.Fields.Status.Name, status, statusOK, fallback, it.Fields.Priority.Name, prio, prioOK),
				append(resolutionNotes, append(dueNotes, append(completedNotes, append(createdNotes,
					append(updatedNotes, descriptionNotes...)...)...)...)...)...),
		})
	}
	return out
}

// jiraDueDate maps Jira's `duedate`. Absent is not a loss and is not reported; a value in a shape no
// pinned layout accepts IS a loss, and is.
func jiraDueDate(raw string) (*time.Time, []FieldNote) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	t, ok := parseJiraTime(raw)
	if !ok {
		return nil, []FieldNote{{Field: fieldDueDate, Value: raw, Via: viaUnparseableDate}}
	}
	return &t, nil
}

// jiraCompletedAt maps Jira's `resolutiondate` — and refuses it unless the issue imported as done.
//
// ⚠ THE DECISION THIS MERGE OWED, DERIVED FROM SHIPPED MECHANICS RATHER THAN TASTE. Jira resolves
// "Won't Do" as well as "Done", so a CANCELLED issue carries a resolutiondate. Track's own rule is
// issue.Store.Update: a status transition onto "done" stamps completed_at and any transition away
// CLEARS it. So completed_at on a non-done row is a state no Track path can produce, and the first
// status edit through the API would erase it without a word. It is not free either — analytics'
// resolution-stats query selects on `completed_at IS NOT NULL` with NO status predicate, so an
// abandoned issue carrying one is counted as delivered work in cycle time and throughput.
// The refusal is REPORTED, because a deliberate drop nobody is told about is indistinguishable from
// the silent ones #71, #72 and #73 each found one field over.
//
// ⚠ THE COARSENESS INHERITED FROM #73 IS ON THE REPORT, NOT HIDDEN: a cancellation-shaped name the
// mapper does not know resolves via statusCategory `done` to Track "done", so it DOES get a
// completion time. That is #73's stated trade (Jira files "Won't Do" under the `done` category), and
// the status warning names the path that decided it.
func jiraCompletedAt(raw string, status model.IssueStatus) (*time.Time, []FieldNote) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	t, ok := parseJiraTime(raw)
	if !ok {
		return nil, []FieldNote{{Field: fieldResolutionDate, Value: raw, Via: viaUnparseableDate}}
	}
	if status != model.StatusDone {
		return nil, []FieldNote{{Field: fieldResolutionDate, Value: raw, Mapped: string(status), Via: viaStatusNotDone}}
	}
	return &t, nil
}

// resolveJiraStatusCategory is the second chance an unrecognised Jira status NAME gets. It never
// runs for a name mapJiraStatus knows, so a recognised import is byte-for-byte what it was.
//
// It returns the status to use plus the note material describing WHICH of the three things happened,
// because a category that never arrives must not be reportable as one that arrived and resolved —
// that is the only way a real tenant's first import can tell anyone whether this code executed.
func resolveJiraStatusCategory(key string, unresolved model.IssueStatus) (model.IssueStatus, statusFallback) {
	if strings.TrimSpace(key) == "" {
		return unresolved, statusFallback{via: viaNoCategory}
	}
	mapped, ok := mapJiraStatusCategory(key)
	if !ok {
		return unresolved, statusFallback{via: viaCategory, value: key}
	}
	return mapped, statusFallback{via: viaCategory, value: key, resolved: true}
}

// ── ADF → plain text ──────────────────────────────────────────────────────────────────────────────────────
// Jira v3 returns description as an Atlassian Document Format tree. We flatten it to readable text: concatenate
// text nodes, newline after block-level nodes. Robust to any shape (nil-safe, unknown types recurse), and
// tolerant of an older plain-string description.

type adfNode struct {
	Type string `json:"type"`
	Text string `json:"text"`

	// Attrs is where a whole class of ADF nodes keeps its content — a link's URL, a mention's
	// display name, an emoji's character. Decoded per key rather than into a struct because the
	// attribute that matters differs per node type (see adfAttrText) and a value that is not a
	// string must not become a decode error that loses the entire description.
	Attrs   map[string]json.RawMessage `json:"attrs"`
	Content []adfNode                  `json:"content"`
}

// adfToText flattens the document and returns, alongside the text, one FieldNote per node type whose
// payload it could not place. See adf_attrs.go for what is pinned and why the notes exist at all.
func adfToText(raw json.RawMessage) (string, []FieldNote) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var doc adfNode
	if err := json.Unmarshal(raw, &doc); err != nil {
		var s string // tolerate a plain-string description (older API)
		if json.Unmarshal(raw, &s) == nil {
			return s, nil
		}
		return "", nil
	}
	var b strings.Builder
	var dropped droppedTypes
	walkADF(doc, &b, &dropped)
	return strings.TrimSpace(b.String()), dropped.notes()
}

func walkADF(n adfNode, b *strings.Builder, dropped *droppedTypes) {
	if n.Text != "" {
		b.WriteString(n.Text)
	}
	// A node whose content lives in an attribute. If the pinned attribute is absent or is not a
	// string, the node carried something this importer did not place — which is the same report as
	// an attachment, and for the same reason.
	if attr, ok := adfAttrText[n.Type]; ok {
		if s := adfAttrString(n.Attrs, attr); s != "" {
			b.WriteString(s)
		} else {
			dropped.add(n.Type)
		}
	}
	if _, ok := adfNoTextEquivalent[n.Type]; ok {
		dropped.add(n.Type)
	}
	if n.Type == "hardBreak" {
		b.WriteByte('\n')
	}
	for _, c := range n.Content {
		walkADF(c, b, dropped)
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
	buf       []mappedIssue
	pos       int
	nextToken string
	isLast    bool
	started   bool
	done      bool
	rowNum    int
}

func newJiraSource(emailAPIToken, projectKey, baseURL string, httpc ...*http.Client) *jiraSource {
	return &jiraSource{client: newJiraClient(emailAPIToken, projectKey, baseURL, httpc...)}
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
	m := s.buf[s.pos]
	s.pos++
	s.rowNum++
	return SourceRow{Issue: m.issue, RowNum: s.rowNum, Notes: m.notes}, true
}
