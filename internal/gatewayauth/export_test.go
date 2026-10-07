package gatewayauth

import "time"

// SetTransitClock lets a test verify an assertion minted at a fixed time.
func SetTransitClock(v *TransitVerifier, now func() time.Time) { v.now = now }
