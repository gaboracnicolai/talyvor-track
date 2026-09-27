package guest_test

import (
	"context"
	"sync"
	"testing"

	"github.com/talyvor/track/internal/guest"
	"github.com/talyvor/track/internal/testutil"
)

// With TRACK_GUEST_SECRET unset, processes booting together agree on one key, and a guest link issued
// before a restart is still accepted after it — by the restarted process and by any other instance.
func TestSigningKey_SharedAcrossProcessesAndRestarts(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)

	// Four processes boot at once on a database that has no key yet.
	keys := make([]string, 4)
	var wg sync.WaitGroup
	for i := range keys {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			k, src, err := guest.ResolveSigningKey(ctx, d.Pool, "")
			if err != nil || src != guest.KeySourceDatabase {
				t.Errorf("process %d: key source %q err %v", i, src, err)
			}
			keys[i] = k
		}(i)
	}
	wg.Wait()
	for i, k := range keys {
		if k == "" || k != keys[0] {
			t.Fatalf("process %d resolved a different key; guest links would not cross instances", i)
		}
	}

	// A guest link issued by one process …
	first := guest.NewStore(d.Pool, keys[0])
	inv, err := first.CreateInvite(ctx, ws.ID, nil, "guest@example.com", guest.GuestRoleViewer, "owner@example.com")
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	accepted, err := first.AcceptInvite(ctx, inv.Token, "Guest")
	if err != nil {
		t.Fatalf("AcceptInvite: %v", err)
	}

	// … still works after a restart (a fresh resolve), and on another instance.
	restartedKey, _, err := guest.ResolveSigningKey(ctx, d.Pool, "")
	if err != nil {
		t.Fatalf("resolve after restart: %v", err)
	}
	if _, err := guest.NewStore(d.Pool, restartedKey).VerifyToken(accepted.AccessToken()); err != nil {
		t.Errorf("a guest link issued before the restart is refused after it: %v", err)
	}

	// An operator's TRACK_GUEST_SECRET still wins.
	if k, src, _ := guest.ResolveSigningKey(ctx, d.Pool, "operator-set"); k != "operator-set" || src != guest.KeySourceEnv {
		t.Errorf("TRACK_GUEST_SECRET set: got key %q from %q, want it used", k, src)
	}
}
