package migrate

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// toolchain_pin_test.go — the Go version this repository ships, and why it is pinned.
//
// ⚠ THE PIN IS A SECURITY CONTROL, NOT HOUSEKEEPING. Before W6.34 track's go.mod carried no
// `toolchain` directive at all, so it built on whatever Go was installed. Measured on clean main
// with go1.26.3: `govulncheck ./...` reported **11 CALLED vulnerabilities** — not the "present in a
// module you require but not called" bucket, which it counts separately. Adding `toolchain
// go1.26.6` took that to **2**, measured back-to-back fourteen seconds apart so the advisory
// database could not have moved between the two runs.
//
// ⚠ THE DIAGNOSIS ALREADY EXISTED IN THIS ESTATE. talyvor-lens gates on govulncheck every build,
// reports zero, and pinned this exact version for this exact reason — its go.mod names the
// advisories >= 1.26.6 clears. lens hit the problem, went red, and fixed it. track has no
// govulncheck job (measured: zero references in ci.yaml), so it never got the signal and never got
// the fix. This test is the smallest thing that keeps the fix from being undone silently.
//
// ⚠ WHAT IT DOES NOT COVER, SAID PLAINLY. Two called advisories remain and BOTH are dependency
// bumps this test cannot speak to:
//
//	GO-2026-5004  SQL injection via placeholder confusion — github.com/jackc/pgx/v5
//	              v5.7.1 → v5.9.2. talyvor-lens and talyvor-docs are BOTH already on v5.9.2;
//	              track is the only repo in the estate still on 5.7.1.
//	GO-2026-5970  infinite loop on invalid input — golang.org/x/text v0.18.0 → v0.39.0.
//
// ⚠ I PREDICTED ONLY THE pgx ONE WOULD REMAIN AND I WAS WRONG — x/text was lagging too. The
// prediction is recorded here rather than quietly dropped, because "the toolchain pin explains the
// whole gap" was the tidier story and it is not the true one.

var toolchainRe = regexp.MustCompile(`(?m)^toolchain go(\d+)\.(\d+)\.(\d+)$`)

var advisoryRe = regexp.MustCompile(`GO-\d{4}-\d+`)

// goVersionRe matches ci.yaml pins: `go-version: "1.26.6"` or `go-version: "1.25"`.
var goVersionRe = regexp.MustCompile(`go-version:\s*"(\d+)\.(\d+)(\.\d+)?"`)

func goMod(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	return string(b)
}

// ⚠ THE GUARD. Removing the directive silently restores eight reachable stdlib advisories.
func TestGoModPinsTheToolchainAtOrAboveTheSecurityFloor(t *testing.T) {
	src := goMod(t)
	m := toolchainRe.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("go.mod has no `toolchain goX.Y.Z` directive.\n\n" +
			"    Without it this repository builds on whatever Go the runner happens to install. " +
			"Measured at go1.26.3, that was 11 CALLED vulnerabilities; at go1.26.6 it is 2, and the " +
			"remaining two are dependency bumps, not stdlib.\n" +
			"    ci.yaml pins `go-version: \"1.25\"`; a toolchain directive is honoured ABOVE a lower " +
			"setup-go pin, which is the mechanism this repository relies on to ship a patched " +
			"runtime. Removing the line does not fail the build — it just stops fixing anything.")
	}
	// Floor: 1.26.6. Compare numerically so 1.27.0 or 1.26.10 pass and 1.26.5 does not.
	major, minor, patch := atoi(t, m[1]), atoi(t, m[2]), atoi(t, m[3])
	const wantMajor, wantMinor, wantPatch = 1, 26, 6
	older := major < wantMajor ||
		(major == wantMajor && minor < wantMinor) ||
		(major == wantMajor && minor == wantMinor && patch < wantPatch)
	if older {
		t.Errorf("go.mod pins toolchain go%d.%d.%d; the security floor is go%d.%d.%d.\n"+
			"    >= 1.26.6 clears GO-2026-6218/6091/6090/6089/6088/5972/5026 and >= 1.26.5 clears "+
			"GO-2026-5856 — eight reachable stdlib advisories. Lowering the pin restores them.",
			major, minor, patch, wantMajor, wantMinor, wantPatch)
	}
}

// ⚠ A PIN WITH NO REASON IS A NUMBER SOMEBODY WILL BUMP DOWN TO FIX A BUILD. The advisory ids are
// what let the next person tell a security floor from a preference.
func TestTheToolchainPinCarriesItsReason(t *testing.T) {
	src := goMod(t)
	idx := toolchainRe.FindStringIndex(src)
	if idx == nil {
		t.Skip("no toolchain directive — TestGoModPinsTheToolchainAtOrAboveTheSecurityFloor reports that")
	}
	// The reason must be in the comment block immediately above the directive.
	above := src[:idx[0]]
	for _, needle := range []string{"govulncheck", "toolchain"} {
		if !strings.Contains(above, needle) {
			t.Errorf("the toolchain pin's rationale does not mention %q. It is a security floor, and "+
				"without that the next person cannot tell it from a preference.", needle)
		}
	}

	// ⚠ COUNTED, NOT MERELY PRESENT — CONTROL V4 IS WHY. The first version of this test asked only
	// whether the string "GO-2026-" appeared. Deleting the whole justifying list left one stray id
	// behind further down the comment, and the check still passed: it was asserting that the
	// rationale mentioned AN advisory, when the claim being justified is that this floor clears
	// EIGHT. A floor defended by one advisory is a different claim from a floor defended by eight.
	ids := map[string]bool{}
	for _, m := range advisoryRe.FindAllString(above, -1) {
		ids[m] = true
	}
	const wantIDs = 4
	if len(ids) < wantIDs {
		t.Errorf("the toolchain pin's rationale names %d distinct advisory id(s); it justifies a "+
			"floor that clears eight, so it must name at least %d.\n"+
			"    They must be written out in full, not compressed to GO-2026-{6218,6091,...}: that "+
			"form is not greppable, and an advisory nobody can search for is one nobody will find "+
			"when it matters. Whoever lowers this pin needs to see what they are giving up.",
			len(ids), wantIDs)
	}
}

// ⚠ NON-VACUITY, AND IT IS NOT DECORATIVE HERE. If the test binary is somehow built by a toolchain
// older than the pin, the directive is not being honoured and every assertion above is describing a
// file rather than a build.
func TestTheRunningToolchainActuallyHonoursThePin(t *testing.T) {
	v := runtime.Version() // e.g. "go1.26.6"
	if !strings.HasPrefix(v, "go") {
		t.Skipf("unexpected runtime.Version() %q", v)
	}
	got := strings.TrimPrefix(v, "go")
	parts := strings.Split(got, ".")
	if len(parts) < 3 {
		t.Skipf("runtime.Version() = %q has no patch component — a devel or beta toolchain; the "+
			"go.mod assertions above still hold", v)
	}
	major, minor, patch := atoiStr(t, parts[0]), atoiStr(t, parts[1]), atoiStr(t, parts[2])
	if major < 1 || (major == 1 && minor < 26) || (major == 1 && minor == 26 && patch < 6) {
		t.Errorf("this test binary was built by %s, below the go1.26.6 floor go.mod pins.\n"+
			"    The `toolchain` directive is supposed to upgrade a lower setup-go pin. If it is not "+
			"doing so here, it is not doing so in CI either, and the pin is a comment rather than a "+
			"control.", v)
	}
}

func atoi(t *testing.T, s string) int { t.Helper(); return atoiStr(t, s) }
func atoiStr(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			t.Fatalf("non-numeric version component %q", s)
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// ⚠ THE LOCKSTEP, AND CI TAUGHT ME THIS ONE. go.mod's `toolchain` does NOT govern CI:
// actions/setup-go exports GOTOOLCHAIN=local, so each job runs exactly the version its
// `go-version:` pin installed. With go.mod at go1.26.6 and ci.yaml at "1.25", the test binary came
// out go1.25.14 and golangci-lint refused to run at all — "the Go language version (go1.25) used
// to build golangci-lint is lower than the targeted Go version (1.26.6)".
//
// So there are two numbers and nothing but a human keeps them equal. talyvor-lens carries a whole
// package for this (internal/toolchainaudit) and its ci.yaml says the same thing in a comment.
// This is track's version of that instrument: every go-version pin must be at least go.mod's
// toolchain floor.
func TestCIGoVersionPinsAreAtLeastTheToolchainFloor(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	ci, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yaml"))
	if err != nil {
		t.Fatalf("read ci.yaml: %v", err)
	}
	pins := goVersionRe.FindAllStringSubmatch(string(ci), -1)
	if len(pins) == 0 {
		t.Fatal("ci.yaml declares no `go-version:` pin — either the workflow stopped installing Go " +
			"or this parse is broken, and a broken parse reports perfect lockstep")
	}

	m := toolchainRe.FindStringSubmatch(goMod(t))
	if m == nil {
		t.Skip("no toolchain directive — TestGoModPinsTheToolchainAtOrAboveTheSecurityFloor reports it")
	}
	fMaj, fMin, fPat := atoi(t, m[1]), atoi(t, m[2]), atoi(t, m[3])

	for _, p := range pins {
		maj, min := atoi(t, p[1]), atoi(t, p[2])
		pat := 0
		if p[3] != "" {
			pat = atoi(t, strings.TrimPrefix(p[3], "."))
		}
		below := maj < fMaj ||
			(maj == fMaj && min < fMin) ||
			(maj == fMaj && min == fMin && pat < fPat)
		if below {
			t.Errorf("ci.yaml pins go-version %q, below go.mod's toolchain floor go%d.%d.%d.\n"+
				"    setup-go exports GOTOOLCHAIN=local, so this pin — not the directive — is what "+
				"the job runs. Below the floor, golangci-lint refuses to start and the tests "+
				"exercise a runtime the release is not built with.",
				p[0], fMaj, fMin, fPat)
		}
	}
	t.Logf("MEASURED: %d go-version pin(s) in ci.yaml, all >= go%d.%d.%d.", len(pins), fMaj, fMin, fPat)
}
