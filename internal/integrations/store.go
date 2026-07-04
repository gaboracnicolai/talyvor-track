package integrations

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// store.go — the per-workspace provider-credential store. The token is encrypted IN GO (Cipher) before it
// touches the DB and decrypted IN GO at read; the plaintext is a function arg / return value that never
// touches a column or a log. Every read/write is scoped by workspace_id (the tenancy anchor).

// ErrNotConfigured is returned by GetDecrypted when a workspace has no integration for the provider.
var ErrNotConfigured = errors.New("integrations: no integration configured for this workspace/provider")

// Integration is the NON-SECRET view of an integration (for status reads). It has NO token/ciphertext field —
// so it is structurally impossible to leak the credential through a JSON response of this type.
type Integration struct {
	ID         string `json:"id"`
	Provider   string `json:"provider"`
	ProjectKey string `json:"project_or_team_key"`
	BaseURL    string `json:"base_url"`
	Configured bool   `json:"configured"`
}

type Store struct {
	pool   *pgxpool.Pool
	cipher *Cipher
}

func NewStore(pool *pgxpool.Pool, c *Cipher) *Store { return &Store{pool: pool, cipher: c} }

// Upsert encrypts plaintextToken IN GO and writes ciphertext+nonce — one integration per (workspace,
// provider). plaintextToken is an ARG that NEVER touches a DB column or a log. Tenancy: keyed on workspaceID.
func (s *Store) Upsert(ctx context.Context, workspaceID, provider, plaintextToken, projectKey, baseURL string) (string, error) {
	if workspaceID == "" || provider == "" || plaintextToken == "" {
		return "", errors.New("integrations: Upsert requires workspace_id, provider, token")
	}
	ciphertext, nonce, err := s.cipher.Encrypt([]byte(plaintextToken))
	if err != nil {
		return "", fmt.Errorf("integrations: encrypt: %w", err)
	}
	var id string
	err = s.pool.QueryRow(ctx, `
		INSERT INTO workspace_integrations
		    (workspace_id, provider, token_ciphertext, token_nonce, project_or_team_key, base_url)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (workspace_id, provider) DO UPDATE SET
		    token_ciphertext    = EXCLUDED.token_ciphertext,
		    token_nonce         = EXCLUDED.token_nonce,
		    project_or_team_key = EXCLUDED.project_or_team_key,
		    base_url            = EXCLUDED.base_url,
		    updated_at          = NOW()
		RETURNING id`,
		workspaceID, provider, ciphertext, nonce, projectKey, baseURL).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("integrations: upsert: %w", err)
	}
	return id, nil
}

// GetDecrypted reads the row and decrypts the token IN GO — the ONLY place the plaintext exists, in memory,
// at use time (the runner, Build C.3). Tenancy: scoped by workspaceID. ErrNotConfigured when absent.
func (s *Store) GetDecrypted(ctx context.Context, workspaceID, provider string) (token, projectKey, baseURL string, err error) {
	var ciphertext, nonce []byte
	err = s.pool.QueryRow(ctx,
		`SELECT token_ciphertext, token_nonce, project_or_team_key, base_url
		 FROM workspace_integrations WHERE workspace_id=$1 AND provider=$2`,
		workspaceID, provider).Scan(&ciphertext, &nonce, &projectKey, &baseURL)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", "", ErrNotConfigured
	}
	if err != nil {
		return "", "", "", fmt.Errorf("integrations: read: %w", err)
	}
	plaintext, err := s.cipher.Decrypt(ciphertext, nonce)
	if err != nil {
		return "", "", "", err
	}
	return string(plaintext), projectKey, baseURL, nil
}

// Get returns the NON-SECRET status view — provider/project/base_url/configured, NEVER the token. Tenancy
// scoped. (nil, nil) when the workspace has no integration for the provider.
func (s *Store) Get(ctx context.Context, workspaceID, provider string) (*Integration, error) {
	var in Integration
	err := s.pool.QueryRow(ctx,
		`SELECT id, provider, project_or_team_key, base_url
		 FROM workspace_integrations WHERE workspace_id=$1 AND provider=$2`,
		workspaceID, provider).Scan(&in.ID, &in.Provider, &in.ProjectKey, &in.BaseURL)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("integrations: get: %w", err)
	}
	in.Configured = true
	return &in, nil
}
