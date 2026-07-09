package automation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/safehttp"
)

// SlackNotifier posts to a Slack incoming-webhook URL using the Block
// Kit message format. Each Send is one HTTP POST — fire-and-forget
// from the caller's perspective; the engine treats failures as
// logged-only warnings.
type SlackNotifier struct {
	httpClient *http.Client
}

func NewSlackNotifier() *SlackNotifier {
	return &SlackNotifier{httpClient: safehttp.Client(5 * time.Second)}
}

// Send posts a message to a Slack incoming webhook. The blocks
// parameter is the Block Kit payload — pass nil for plain-text-only
// messages and the helper will synthesize a single section block.
func (s *SlackNotifier) Send(webhookURL, message string, blocks []map[string]any) error {
	if webhookURL == "" {
		return fmt.Errorf("slack: webhook_url required")
	}
	if blocks == nil {
		blocks = []map[string]any{
			{
				"type": "section",
				"text": map[string]any{"type": "mrkdwn", "text": message},
			},
		}
	}
	body, _ := json.Marshal(map[string]any{
		"text":   message,
		"blocks": blocks,
	})
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("slack: webhook returned %d", resp.StatusCode)
	}
	return nil
}

// IssueUpdated formats a Block Kit message describing the change.
// Includes the issue identifier, title, the diff between fields, and
// a link back to the issue page. The link domain is configurable —
// Phase 6 hard-codes talyvor.com; later phases will pull from config.
func (s *SlackNotifier) IssueUpdated(webhookURL string, issue model.Issue, changes map[string]any) error {
	header := fmt.Sprintf("*%s updated* — %s", issue.Identifier, issue.Title)
	var diff bytes.Buffer
	for k, v := range changes {
		fmt.Fprintf(&diff, "%s: %v\n", k, v)
	}
	body := header
	if diff.Len() > 0 {
		body += "\n" + diff.String()
	}
	body += fmt.Sprintf("<https://app.talyvor.com/issue/%s|View issue>", issue.Identifier)

	blocks := []map[string]any{
		{
			"type": "section",
			"text": map[string]any{"type": "mrkdwn", "text": body},
		},
	}
	return s.Send(webhookURL, body, blocks)
}
