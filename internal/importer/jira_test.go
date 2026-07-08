package importer

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/talyvor/track/internal/model"
)

// ── T8 Build C.3 — Jira REST source proofs (httptest canned responses). ──

func adfDoc(text string) string {
	return fmt.Sprintf(`{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":%q}]}]}`, text)
}

func jiraIssueJSON(key, summary, adf, status, prio string) string {
	desc := adf
	if desc == "" {
		desc = "null"
	}
	return fmt.Sprintf(`{"key":%q,"fields":{"summary":%q,"description":%s,"status":{"name":%q},"priority":{"name":%q},"labels":["x"]}}`,
		key, summary, desc, status, prio)
}

// (c) JIRA PAGINATION: nextPageToken then isLast=true → all issues yielded, Identifier=issue.key.
func TestJiraSource_Paginates(t *testing.T) {
	page1 := fmt.Sprintf(`{"issues":[%s],"isLast":false,"nextPageToken":"tok2"}`,
		jiraIssueJSON("PROJ-1", "First", adfDoc("hello"), "To Do", "High"))
	page2 := fmt.Sprintf(`{"issues":[%s],"isLast":true}`,
		jiraIssueJSON("PROJ-2", "Second", "", "Done", "Low"))
	srv := httptest.NewServer(cannedPages([]string{page1, page2}, `{"issues":[],"isLast":true}`))
	defer srv.Close()

	src := newJiraSource("me@corp.com:api-token", "PROJ", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep

	var got []model.Issue
	for {
		row, ok := src.Next()
		if !ok {
			break
		}
		if row.Err != nil {
			t.Fatalf("unexpected err: %v", row.Err)
		}
		got = append(got, row.Issue)
	}
	if len(got) != 2 || got[0].Identifier != "PROJ-1" || got[1].Identifier != "PROJ-2" {
		t.Fatalf("jira identifiers = %v, want [PROJ-1 PROJ-2]", []string{got[0].Identifier, got[1].Identifier})
	}
	if got[0].Description != "hello" {
		t.Fatalf("ADF description = %q, want hello", got[0].Description)
	}
}

// (d) JIRA ADF: a canned ADF → expected text; and weird/empty shapes don't crash + a plain-string passes.
func TestADFToText(t *testing.T) {
	multi := `{"type":"doc","content":[
		{"type":"paragraph","content":[{"type":"text","text":"Hello "},{"type":"text","text":"world"}]},
		{"type":"bulletList","content":[{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"item"}]}]}]}
	]}`
	if got := adfToText(json.RawMessage(multi)); got != "Hello world\nitem" {
		t.Fatalf("adf multi = %q, want \"Hello world\\nitem\"", got)
	}
	// must not crash on any shape
	for _, weird := range []string{``, `null`, `{}`, `{"type":"doc"}`, `{"type":"doc","content":[{"type":"rule"}]}`, `[]`, `123`} {
		_ = adfToText(json.RawMessage(weird))
	}
	if got := adfToText(json.RawMessage(`"plain description"`)); got != "plain description" {
		t.Fatalf("plain-string description = %q, want passthrough", got)
	}
}

// (e) JIRA RATE-LIMIT: HTTP 429 + Retry-After honored (waits then retries, succeeds).
func TestJiraSource_RateLimit_HonorsRetryAfter(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		writeRaw(w, fmt.Sprintf(`{"issues":[%s],"isLast":true}`, jiraIssueJSON("PROJ-9", "S", "", "Done", "Low")))
	}))
	defer srv.Close()

	src := newJiraSource("e:t", "PROJ", srv.URL, srv.Client())
	var waited time.Duration
	src.client.retry.sleep = func(d time.Duration) { waited = d }

	row, ok := src.Next()
	if !ok || row.Err != nil {
		t.Fatalf("429+Retry-After must be retried then succeed: ok=%v err=%v", ok, row.Err)
	}
	if waited != 2*time.Second {
		t.Fatalf("Retry-After: 2 must be honored, waited %v", waited)
	}
	if row.Issue.Identifier != "PROJ-9" {
		t.Fatalf("identifier = %q, want PROJ-9", row.Issue.Identifier)
	}
}
