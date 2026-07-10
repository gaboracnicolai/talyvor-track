package webhookdedup_test

import (
	"context"
	"testing"

	"github.com/talyvor/track/internal/testutil"
	"github.com/talyvor/track/internal/webhookdedup"
)

// SEC-7: the durable claim must be exactly-once — the first claim of a delivery id wins, a repeat loses.
func TestClaim_DedupsDeliveryID(t *testing.T) {
	ctx := context.Background()
	d := testutil.New(t) // SKIPs without TRACK_TEST_DATABASE_URL
	s := webhookdedup.New(d.Pool)

	first, err := s.Claim(ctx, "github", "delivery-xyz")
	if err != nil {
		t.Fatalf("first Claim: %v", err)
	}
	if !first {
		t.Errorf("first Claim = false, want true (first delivery must be processed)")
	}

	second, err := s.Claim(ctx, "github", "delivery-xyz")
	if err != nil {
		t.Fatalf("second Claim: %v", err)
	}
	if second {
		t.Errorf("second Claim = true, want false (a replay must be deduped)")
	}

	// A different id, and the same id under a different source, both still claim.
	if ok, _ := s.Claim(ctx, "github", "delivery-other"); !ok {
		t.Errorf("distinct delivery id should claim")
	}
	if ok, _ := s.Claim(ctx, "lens", "delivery-xyz"); !ok {
		t.Errorf("same id under a different source should claim (keyed on source+id)")
	}
}
