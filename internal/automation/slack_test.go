package automation

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/model"
)

func TestSlack_SendPostsToCorrectWebhookURL(t *testing.T) {
	var (
		gotPath string
		gotBody []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	notif := NewSlackNotifier()
	notif.httpClient = srv.Client() // reach the loopback test server; the SSRF-guarded client blocks 127.0.0.1 by design
	if err := notif.Send(srv.URL+"/incoming/hook", "hello", nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotPath != "/incoming/hook" {
		t.Errorf("upstream path = %q, want /incoming/hook", gotPath)
	}
	var payload map[string]any
	_ = json.Unmarshal(gotBody, &payload)
	if payload["text"] != "hello" {
		t.Errorf("payload text = %v, want hello", payload["text"])
	}
	// Default block should be a section with mrkdwn.
	blocks, _ := payload["blocks"].([]any)
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1 (default section)", len(blocks))
	}
	first := blocks[0].(map[string]any)
	if first["type"] != "section" {
		t.Errorf("first block type = %v, want section", first["type"])
	}
}

func TestSlack_IssueUpdatedFormatsMessage(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	notif := NewSlackNotifier()
	notif.httpClient = srv.Client() // reach the loopback test server; the SSRF-guarded client blocks 127.0.0.1 by design
	err := notif.IssueUpdated(srv.URL, model.Issue{
		Identifier: "ENG-42", Title: "Authentication bug",
	}, map[string]any{"status": "done"})
	if err != nil {
		t.Fatalf("IssueUpdated: %v", err)
	}
	for _, want := range []string{"ENG-42", "Authentication bug", "status: done", "View issue"} {
		if !strings.Contains(got, want) {
			t.Errorf("message missing %q; body=%s", want, got)
		}
	}
}

func TestSlack_SendErrorsWhenWebhookEmpty(t *testing.T) {
	notif := NewSlackNotifier()
	if err := notif.Send("", "msg", nil); err == nil {
		t.Error("Send with empty webhook URL should error")
	}
}
