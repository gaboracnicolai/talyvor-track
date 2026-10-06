package lensintegration

import (
	"context"
	"encoding/json"
	"errors"
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

// TokenSource gives a credential Lens admits for one workspace (lenscreds.Provider.TokenFor mints one with
// Lens's LENS_MINT_KEY).
type TokenSource interface {
	TokenFor(ctx context.Context, workspaceID string) (string, error)
}

// ErrNoLensWorkspace: the add did not say which Lens workspace its plan is billed under.
var ErrNoLensWorkspace = errors.New("lens: seats check: no Lens workspace on the request (X-Lens-Workspace)")

// ErrNoSeatsCredential: Track holds no credential Lens admits for the workspace (TRACK_LENS_MINT_KEY is unset).
var ErrNoSeatsCredential = errors.New("lens: seats check: no Lens credential for the workspace (TRACK_LENS_MINT_KEY)")

// Seats asks Lens about the seats of the Lens workspace a Track workspace's plan is billed under.
//
// That is never Track's own workspace id: Track mints those at bootstrap, and a /plans subscription is on the
// user's Lens workspace, which the suite BFF sends as X-Lens-Workspace (B32.73). Lens binds {wsID} on
// /plan/seats to the caller's credential, so each check carries a token minted for that very workspace.
type Seats struct {
	client *Client
	tokens TokenSource // nil when Track holds no mint key: every check is then refused as unchecked
}

// NewSeats returns a seats check against c's Lens, authenticating each check with a token from tokens.
func NewSeats(c *Client, tokens TokenSource) *Seats { return &Seats{client: c, tokens: tokens} }

// CheckSeats asks Lens whether lensWorkspaceID's plan takes members members — the number it would have after an
// add. nil means it does; a *SeatRefusal means Lens refused; any other error means Track could not find out.
// Lens keeps the plans and their seat numbers (B32.12); Track keeps only the member list, so it never holds a
// seat number itself.
func (s *Seats) CheckSeats(ctx context.Context, lensWorkspaceID string, members int) error {
	c := s.client
	if !c.IsConfigured() {
		return ErrNotConfigured
	}
	if lensWorkspaceID == "" {
		return ErrNoLensWorkspace
	}
	if s.tokens == nil {
		return ErrNoSeatsCredential
	}
	token, err := s.tokens.TokenFor(ctx, lensWorkspaceID)
	if err != nil {
		return fmt.Errorf("lens: seats check: %w", err)
	}
	path := "/v1/workspaces/" + url.PathEscape(lensWorkspaceID) + "/plan/seats?members=" + strconv.Itoa(members)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.lensURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
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
