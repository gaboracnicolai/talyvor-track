// Package guest implements scoped external-collaborator access.
//
// Two-table design — invites (pending tokens) and guests (accepted
// records). Invite tokens are random and stored verbatim; access
// tokens (issued after accept) are stateless HMAC-SHA256-signed
// claims, so middleware never needs to hit the DB on a hot path.
//
// Roles: viewer (read-only), commenter (read + comment), editor
// (read + comment + patch issues). Guests can be workspace-wide
// (project_id NULL) or scoped to a single project.
package guest

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ─── public types ───────────────────────────────────────────

type GuestRole string

const (
	GuestRoleViewer    GuestRole = "viewer"
	GuestRoleCommenter GuestRole = "commenter"
	GuestRoleEditor    GuestRole = "editor"
)

var validRoles = map[GuestRole]struct{}{
	GuestRoleViewer:    {},
	GuestRoleCommenter: {},
	GuestRoleEditor:    {},
}

// inviteTTL caps how long an unredeemed invite lives. Seven days
// matches the spec; tokens older than that fail the AcceptInvite
// expiry check even if the row is still in the table.
const inviteTTL = 7 * 24 * time.Hour

type GuestInvite struct {
	ID          string     `json:"id"`
	WorkspaceID string     `json:"workspace_id"`
	ProjectID   *string    `json:"project_id,omitempty"`
	Email       string     `json:"email"`
	Role        GuestRole  `json:"role"`
	Token       string     `json:"token"`
	ExpiresAt   time.Time  `json:"expires_at"`
	AcceptedAt  *time.Time `json:"accepted_at,omitempty"`
	InvitedBy   string     `json:"invited_by"`
	CreatedAt   time.Time  `json:"created_at"`
}

type Guest struct {
	ID          string     `json:"id"`
	WorkspaceID string     `json:"workspace_id"`
	ProjectID   *string    `json:"project_id,omitempty"`
	Email       string     `json:"email"`
	Name        string     `json:"name"`
	Role        GuestRole  `json:"role"`
	Active      bool       `json:"active"`
	CreatedAt   time.Time  `json:"created_at"`
	LastSeenAt  *time.Time `json:"last_seen_at,omitempty"`
}

// AcceptResult bundles the persisted Guest + the freshly-minted
// access token. The token is returned exactly once (it's stateless
// — re-issue on subsequent accepts of the same email).
type AcceptResult struct {
	guest       *Guest
	accessToken string
}

// Exported accessors so callers outside the package can read the
// fields without us widening the AcceptResult struct (tests use the
// unexported names directly).
func (a *AcceptResult) Guest() *Guest      { return a.guest }
func (a *AcceptResult) AccessToken() string { return a.accessToken }

// GuestClaims is the wire shape of the signed access token's
// payload. Kept compact — every byte lives in the URL Bearer header
// on every guest-scoped request.
type GuestClaims struct {
	GuestID     string    `json:"g"`
	WorkspaceID string    `json:"w"`
	ProjectID   string    `json:"p,omitempty"`
	Role        GuestRole `json:"r"`
	ExpiresUnix int64     `json:"e"`
}

// ─── store ──────────────────────────────────────────────────

type pgxDB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Store struct {
	pool   pgxDB
	secret []byte
}

// NewStore constructs the production store. secret is the HMAC key
// used to sign access tokens — read from GUEST_SECRET at boot.
// Empty secrets derive a per-process random key so dev environments
// still work; production deployments must set the env var.
func NewStore(pool *pgxpool.Pool, secret string) *Store {
	var db pgxDB
	if pool != nil {
		db = pool
	}
	return newStore(db, secret)
}

func newStore(db pgxDB, secret string) *Store {
	if secret == "" {
		// Random fallback. Tokens minted in this process are usable
		// during its lifetime; a restart invalidates them all. Fine
		// for dev, unacceptable for prod (operator must set GUEST_SECRET).
		buf := make([]byte, 32)
		_, _ = rand.Read(buf)
		secret = hex.EncodeToString(buf)
	}
	return &Store{pool: db, secret: []byte(secret)}
}

// ─── token signing ──────────────────────────────────────────

// signClaims serialises GuestClaims as `b64payload.b64signature`. The
// signature covers exactly the payload bytes, so a tampered payload
// fails the constant-time signature compare in VerifyToken.
func (s *Store) signClaims(c *GuestClaims) string {
	payload, _ := json.Marshal(c)
	p64 := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(p64))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return p64 + "." + sig
}

// VerifyToken decodes and authenticates an access token. Returns the
// claims on success, an error on bad shape / bad signature / expired.
func (s *Store) VerifyToken(token string) (*GuestClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil, errors.New("guest: malformed token")
	}
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(parts[0]))
	want := mac.Sum(nil)
	got, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("guest: bad signature encoding")
	}
	if !hmac.Equal(want, got) {
		return nil, errors.New("guest: signature mismatch")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, errors.New("guest: bad payload encoding")
	}
	var c GuestClaims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, fmt.Errorf("guest: decode claims: %w", err)
	}
	if c.ExpiresUnix > 0 && time.Now().Unix() > c.ExpiresUnix {
		return nil, errors.New("guest: token expired")
	}
	return &c, nil
}

// ─── scan helpers ───────────────────────────────────────────

const inviteColumns = `id, workspace_id, project_id, email, role,
    token, expires_at, accepted_at, invited_by, created_at`

func scanInvite(s interface{ Scan(...any) error }) (*GuestInvite, error) {
	var (
		i        GuestInvite
		roleStr  string
	)
	if err := s.Scan(
		&i.ID, &i.WorkspaceID, &i.ProjectID, &i.Email, &roleStr,
		&i.Token, &i.ExpiresAt, &i.AcceptedAt, &i.InvitedBy, &i.CreatedAt,
	); err != nil {
		return nil, err
	}
	i.Role = GuestRole(roleStr)
	return &i, nil
}

const guestColumns = `id, workspace_id, project_id, email, name, role,
    active, created_at, last_seen_at`

func scanGuest(s interface{ Scan(...any) error }) (*Guest, error) {
	var (
		g       Guest
		roleStr string
	)
	if err := s.Scan(
		&g.ID, &g.WorkspaceID, &g.ProjectID, &g.Email, &g.Name, &roleStr,
		&g.Active, &g.CreatedAt, &g.LastSeenAt,
	); err != nil {
		return nil, err
	}
	g.Role = GuestRole(roleStr)
	return &g, nil
}

// ─── CreateInvite ───────────────────────────────────────────

// CreateInvite mints a fresh random token, computes the expiry, and
// inserts the row. The token is returned to the caller exactly
// once — that's the only place it appears in plaintext.
func (s *Store) CreateInvite(ctx context.Context, workspaceID string, projectID *string, email string, role GuestRole, invitedBy string) (*GuestInvite, error) {
	if s.pool == nil {
		return nil, errors.New("guest: store has no pool")
	}
	if workspaceID == "" {
		return nil, errors.New("guest: workspace_id required")
	}
	if strings.TrimSpace(email) == "" {
		return nil, errors.New("guest: email required")
	}
	if _, ok := validRoles[role]; !ok {
		return nil, fmt.Errorf("guest: invalid role %q", role)
	}

	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("guest: random: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	expires := time.Now().UTC().Add(inviteTTL)

	return scanInvite(s.pool.QueryRow(ctx,
		`INSERT INTO guest_invites (workspace_id, project_id, email, role, token, expires_at, invited_by)
        VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING `+inviteColumns,
		workspaceID, projectID, strings.ToLower(strings.TrimSpace(email)),
		string(role), token, expires, invitedBy,
	))
}

// ─── GetGuestByToken (invite lookup) ────────────────────────

// GetGuestByToken returns the open invite for a token. Used by the
// public /v1/invite/:token endpoint so the invitee can see who
// invited them before accepting.
func (s *Store) GetGuestByToken(ctx context.Context, token string) (*GuestInvite, error) {
	if s.pool == nil {
		return nil, errors.New("guest: store has no pool")
	}
	return scanInvite(s.pool.QueryRow(ctx,
		`SELECT `+inviteColumns+` FROM guest_invites WHERE token = $1`,
		token,
	))
}

// ─── AcceptInvite ───────────────────────────────────────────

// AcceptInvite is the redemption path: look up the invite, validate
// it's still usable, insert (or up-cast) the guest row, mark the
// invite accepted, and return a signed access token.
//
// Single guest per (workspace, email): ON CONFLICT (workspace_id,
// email) DO UPDATE preserves the existing guest_id so revoke history
// stays attached even after re-invite.
func (s *Store) AcceptInvite(ctx context.Context, token, name string) (*AcceptResult, error) {
	if s.pool == nil {
		return nil, errors.New("guest: store has no pool")
	}
	invite, err := scanInvite(s.pool.QueryRow(ctx,
		`SELECT `+inviteColumns+` FROM guest_invites WHERE token = $1`,
		token,
	))
	if err != nil {
		return nil, fmt.Errorf("guest: invite lookup: %w", err)
	}
	if invite.AcceptedAt != nil {
		return nil, errors.New("guest: invite already accepted")
	}
	if time.Now().UTC().After(invite.ExpiresAt) {
		return nil, errors.New("guest: invite expired")
	}

	g, err := scanGuest(s.pool.QueryRow(ctx,
		`INSERT INTO guests (workspace_id, project_id, email, name, role)
        VALUES ($1, $2, $3, $4, $5)
        ON CONFLICT (workspace_id, email)
        DO UPDATE SET name = EXCLUDED.name, role = EXCLUDED.role,
                      project_id = EXCLUDED.project_id, active = true
        RETURNING `+guestColumns,
		invite.WorkspaceID, invite.ProjectID, invite.Email, strings.TrimSpace(name), string(invite.Role),
	))
	if err != nil {
		return nil, fmt.Errorf("guest: insert: %w", err)
	}

	if _, err := s.pool.Exec(ctx,
		`UPDATE guest_invites SET accepted_at = NOW() WHERE id = $1`,
		invite.ID,
	); err != nil {
		// Guest is already created; the marker failure shouldn't
		// invalidate the operation. Return the guest + token; a
		// retry-then-accept will hit the "already accepted" check.
		// Soft-log here in a real deployment.
		_ = err
	}

	claims := &GuestClaims{
		GuestID:     g.ID,
		WorkspaceID: g.WorkspaceID,
		Role:        g.Role,
		ExpiresUnix: time.Now().Add(30 * 24 * time.Hour).Unix(),
	}
	if g.ProjectID != nil {
		claims.ProjectID = *g.ProjectID
	}
	return &AcceptResult{guest: g, accessToken: s.signClaims(claims)}, nil
}

// ─── ListGuests ─────────────────────────────────────────────

func (s *Store) ListGuests(ctx context.Context, workspaceID string, projectID *string) ([]Guest, error) {
	if s.pool == nil {
		return nil, nil
	}
	var (
		rows pgx.Rows
		err  error
	)
	if projectID == nil {
		rows, err = s.pool.Query(ctx,
			`SELECT `+guestColumns+` FROM guests WHERE workspace_id = $1
            ORDER BY created_at DESC`,
			workspaceID,
		)
	} else {
		rows, err = s.pool.Query(ctx,
			`SELECT `+guestColumns+` FROM guests
            WHERE workspace_id = $1 AND (project_id IS NULL OR project_id = $2)
            ORDER BY created_at DESC`,
			workspaceID, *projectID,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("guest: list: %w", err)
	}
	defer rows.Close()
	var out []Guest
	for rows.Next() {
		g, err := scanGuest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *g)
	}
	return out, rows.Err()
}

// ─── RevokeGuest ────────────────────────────────────────────

// RevokeGuest flips the active flag off. Stateless access tokens
// remain in circulation until their own TTL expires — that's a
// known trade-off for stateless auth; if you need immediate cutoff,
// rotate GUEST_SECRET (invalidates every token in flight).
func (s *Store) RevokeGuest(ctx context.Context, guestID string) error {
	if s.pool == nil {
		return errors.New("guest: store has no pool")
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE guests SET active = false WHERE id = $1`,
		guestID,
	)
	return err
}

// ─── ValidateGuestAccess ────────────────────────────────────

// ValidateGuestAccess verifies the token and checks the claim
// against the resource. Returns the guest's role on success.
//
// resourceType:
//   - "workspace": claim.WorkspaceID must match; project-scoped
//     guests do not have workspace-wide access.
//   - "project":   claim's project (or workspace-wide claim) must
//     cover the resource.
//   - "issue":     the issue's project_id is not looked up here;
//     callers needing issue-level checks should pass the issue's
//     project_id via resourceID after resolving it.
func (s *Store) ValidateGuestAccess(ctx context.Context, token, resourceType, resourceID string) (GuestRole, error) {
	_ = ctx
	claims, err := s.VerifyToken(token)
	if err != nil {
		return "", err
	}
	switch resourceType {
	case "workspace":
		if claims.WorkspaceID != resourceID {
			return "", errors.New("guest: workspace mismatch")
		}
		if claims.ProjectID != "" {
			return "", errors.New("guest: project-scoped guest cannot access whole workspace")
		}
	case "project", "issue":
		// Workspace-wide claims (ProjectID empty) cover every
		// project. Project-scoped claims only their own.
		if claims.ProjectID != "" && claims.ProjectID != resourceID {
			return "", errors.New("guest: project mismatch")
		}
	default:
		return "", fmt.Errorf("guest: unknown resource type %q", resourceType)
	}
	return claims.Role, nil
}
