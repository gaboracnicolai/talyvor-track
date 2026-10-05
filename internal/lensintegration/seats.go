package lensintegration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// SeatRefusal is Lens's 402 from the seats check: the workspace's plan does not take the members it would have.
// Message is Lens's error, unchanged — it names LENS_PLAN_GATES, the plan and the plan that would allow the add.
type SeatRefusal struct {
	Message string `json:"error"`
	Plan    string `json:"plan"`
	Gate    string `json:"gate"`
	Limit   *int64 `json:"limit,omitempty"`
	Allows  string `json:"allows,omitempty"`
}

func (r *SeatRefusal) Error() string { return r.Message }

// CheckSeats asks Lens whether workspaceID's plan takes members members — the number it would have after an add.
// nil means it does; a *SeatRefusal means Lens refused; any other error means Track could not find out.
// Lens keeps the plans and their seat numbers (B32.12); Track keeps only the member list, so it never holds a
// seat number itself. The Lens workspace is the Track workspace's own id, as the syncer and lenscreds use it.
func (c *Client) CheckSeats(ctx context.Context, workspaceID string, members int) error {
	if !c.IsConfigured() {
		return ErrNotConfigured
	}
	path := "/v1/workspaces/" + url.PathEscape(workspaceID) + "/plan/seats?members=" + strconv.Itoa(members)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.lensURL+path, nil)
	if err != nil {
		return err
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("lens: seats check: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusPaymentRequired:
		var r SeatRefusal
		if err := json.Unmarshal(body, &r); err != nil || r.Message == "" {
			return fmt.Errorf("lens: seats check: 402 without a refusal: %s", body)
		}
		return &r
	default:
		return fmt.Errorf("lens: seats check returned %d: %s", resp.StatusCode, body)
	}
}
