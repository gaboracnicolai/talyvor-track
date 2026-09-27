package guest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Where ResolveSigningKey found the guest-token key; main logs it at boot.
const (
	KeySourceEnv      = "TRACK_GUEST_SECRET"
	KeySourceDatabase = "database (instance_secrets)"
)

const signingKeyName = "guest_token_key"

// ResolveSigningKey returns the HMAC key guest access tokens are signed with, and where it came from.
// TRACK_GUEST_SECRET wins when set. Otherwise the key lives in instance_secrets: the first process to
// boot generates it, and every later process — a restart or a second instance — reads that same key,
// so a guest link keeps working across restarts and instances. Concurrent first boots race on the
// INSERT; ON CONFLICT DO NOTHING plus the read back gives every one of them the winner's key.
func ResolveSigningKey(ctx context.Context, pool *pgxpool.Pool, env string) (key, source string, err error) {
	if env != "" {
		return env, KeySourceEnv, nil
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("guest: generate signing key: %w", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO instance_secrets (name, value) VALUES ($1, $2) ON CONFLICT (name) DO NOTHING`,
		signingKeyName, hex.EncodeToString(buf)); err != nil {
		return "", "", fmt.Errorf("guest: store signing key: %w", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT value FROM instance_secrets WHERE name = $1`, signingKeyName).Scan(&key); err != nil {
		return "", "", fmt.Errorf("guest: read signing key: %w", err)
	}
	return key, KeySourceDatabase, nil
}
