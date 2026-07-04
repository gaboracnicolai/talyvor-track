package integrations_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/integrations"
	"github.com/talyvor/track/internal/testutil"
)

func testStore(t *testing.T, d *testutil.DB) *integrations.Store {
	t.Helper()
	c, err := integrations.NewCipher(bytes.Repeat([]byte("k"), 32))
	if err != nil {
		t.Fatal(err)
	}
	return integrations.NewStore(d.Pool, c)
}

// (a) THE LOAD-BEARING SECURITY PROOF — plaintext never persisted. After Upsert(token=marker), the RAW row
// contains the plaintext marker in NO column; GetDecrypted round-trips it back (plaintext only in memory).
func TestStore_PlaintextNeverPersisted(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	s := testStore(t, d)
	const token = "secret-xyz-PLAINTEXT-marker"

	if _, err := s.Upsert(ctx, ws.ID, "linear", token, "ENG", ""); err != nil {
		t.Fatal(err)
	}

	var id, provider, projectKey, baseURL string
	var ciphertext, nonce []byte
	if err := d.Pool.QueryRow(ctx,
		`SELECT id, provider, token_ciphertext, token_nonce, project_or_team_key, base_url
		 FROM workspace_integrations WHERE workspace_id=$1`, ws.ID).
		Scan(&id, &provider, &ciphertext, &nonce, &projectKey, &baseURL); err != nil {
		t.Fatal(err)
	}
	for name, col := range map[string]string{
		"id": id, "provider": provider, "project_or_team_key": projectKey, "base_url": baseURL,
		"token_ciphertext": string(ciphertext), "token_nonce": string(nonce),
	} {
		if strings.Contains(col, token) {
			t.Fatalf("PLAINTEXT LEAK: the token appears in column %q", name)
		}
	}
	if len(ciphertext) == 0 {
		t.Fatal("ciphertext is empty")
	}
	got, pk, _, err := s.GetDecrypted(ctx, ws.ID, "linear")
	if err != nil || got != token {
		t.Fatalf("GetDecrypted = %q, %v; want %q", got, err, token)
	}
	if pk != "ENG" {
		t.Fatalf("projectKey = %q, want ENG", pk)
	}
}

// (c, store) TENANCY: workspace A's GetDecrypted cannot read workspace B's integration.
func TestStore_GetDecrypted_TenancyScoped(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	s := testStore(t, d)
	if _, err := s.Upsert(ctx, wsB.ID, "linear", "B-token", "", ""); err != nil {
		t.Fatal(err)
	}

	if _, _, _, err := s.GetDecrypted(ctx, wsA.ID, "linear"); err != integrations.ErrNotConfigured {
		t.Fatalf("A reading its own (absent) integration = %v, want ErrNotConfigured — must NOT see B's", err)
	}
	tok, _, _, err := s.GetDecrypted(ctx, wsB.ID, "linear")
	if err != nil || tok != "B-token" {
		t.Fatalf("B reading B's = %q, %v; want B-token", tok, err)
	}
}

// (b, store) re-upsert of the SAME token rotates the stored ciphertext (fresh nonce) — no deterministic leak
// at rest.
func TestStore_ReUpsert_RotatesCiphertext(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	s := testStore(t, d)
	readCT := func() []byte {
		var ct []byte
		_ = d.Pool.QueryRow(ctx, `SELECT token_ciphertext FROM workspace_integrations WHERE workspace_id=$1`, ws.ID).Scan(&ct)
		return ct
	}
	if _, err := s.Upsert(ctx, ws.ID, "linear", "same-token", "", ""); err != nil {
		t.Fatal(err)
	}
	ct1 := readCT()
	if _, err := s.Upsert(ctx, ws.ID, "linear", "same-token", "", ""); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(ct1, readCT()) {
		t.Fatal("re-upsert of the same token produced identical ciphertext (nonce not fresh)")
	}
}
