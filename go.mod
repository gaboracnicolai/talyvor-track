module github.com/talyvor/track

go 1.25.0

// ⚠ THE VERSION THIS REPO SHIPS — AND ci.yaml MUST BE KEPT IN LOCKSTEP WITH IT BY HAND.
//
// ⚠ AN EARLIER DRAFT OF THIS COMMENT SAID A `toolchain` line "is honoured ABOVE a lower setup-go
// pin". THAT IS FALSE IN THIS REPO'S CI AND CI SAID SO: actions/setup-go exports
// GOTOOLCHAIN=local, so the directive is NOT auto-fetched and the job runs exactly the version
// setup-go installed. The test binary came out as go1.25.14 against a go1.26.6 floor, and
// golangci-lint refused outright — "the Go language version (go1.25) used to build golangci-lint
// is lower than the targeted Go version (1.26.6)". The directive governs LOCAL builds and the
// Docker build (golang:1.25-alpine, GOTOOLCHAIN unset ⇒ auto ⇒ it fetches this); ci.yaml's
// go-version governs CI. Both are now 1.26.6 and internal/migrate/toolchain_pin_test.go asserts
// the lockstep.
//
// Without this directive track built on whatever was installed (go1.26.3) and govulncheck
// reported 11 CALLED vulnerabilities on main — while talyvor-lens, which gates on govulncheck every
// build, reported zero and had already pinned this same version for the same reason. lens's go.mod
// names the advisories. Written out in full rather than as GO-2026-{6218,6091,...}: the compressed
// form is not greppable, so someone searching this estate for a specific advisory id would find
// nothing here. >= 1.26.6 clears GO-2026-6218, GO-2026-6091, GO-2026-6090, GO-2026-6089,
// GO-2026-6088, GO-2026-5972 and GO-2026-5026; >= 1.26.5 clears GO-2026-5856 (crypto/tls ECH
// privacy leak).
//
// ⚠ TRACK NEVER GOT THE SIGNAL, SO IT NEVER GOT THE FIX — the diagnosis existed in a sibling repo
// the whole time. Adding the CI gate that would have said so is W6.33 and needs the remaining pgx
// advisory decided first, or the gate lands red on main.
toolchain go1.26.6

require (
	github.com/alicebob/miniredis/v2 v2.38.0
	github.com/go-chi/chi/v5 v5.3.0
	github.com/google/uuid v1.6.0
	github.com/gorilla/websocket v1.5.3
	github.com/jackc/pgx/v5 v5.9.2
	github.com/pashagolub/pgxmock/v4 v4.3.0
	github.com/prometheus/client_golang v1.20.5
	github.com/redis/go-redis/v9 v9.21.0
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/klauspost/compress v1.17.9 // indirect
	github.com/kylelemons/godebug v1.1.0 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/prometheus/client_model v0.6.1 // indirect
	github.com/prometheus/common v0.55.0 // indirect
	github.com/prometheus/procfs v0.15.1 // indirect
	github.com/yuin/gopher-lua v1.1.1 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	golang.org/x/sync v0.21.0 // indirect
	golang.org/x/sys v0.30.0 // indirect
	golang.org/x/text v0.39.0 // indirect
	google.golang.org/protobuf v1.34.2 // indirect
)
