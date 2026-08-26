package gatewayauth_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The README's curl recipes are the ONLY executable instructions this repository ships,
// and every one of them addresses /v1 — which is behind gwAuth (this package) and wsAuthz.
// A recipe that omits the transit proof does not "mostly work": it returns 401 before it
// reaches a handler, and the reader has no way to know why, because `docker compose up`
// publishes track directly on :3000 with no gateway in front of it.
//
// MEASURED 2026-08-26 on a from-zero container (26 migrations, seeded workspace/member/team),
// which is why this test exists rather than a comment:
//
//	README's `curl -X POST ".../v1/import/linear?workspace_id=WS&team_id=TEAM" -F file=@…`
//	    VERBATIM                                     -> 401 GATEWAY_AUTH_REQUIRED
//	    + X-Gateway-Auth + X-User-Email              -> 200 {"imported":1,…}
//	    + X-Gateway-Auth, no X-User-Email            -> 403 WORKSPACE_FORBIDDEN
//	    + X-User-Email, wrong X-Gateway-Auth         -> 401 GATEWAY_AUTH_REQUIRED
//
// Both headers, or nothing. So both are required here.
//
// WHY THE EXEMPT LIST IS READ FROM cmd/track/main.go RATHER THAN COPIED: gwExempt is the
// single definition of which paths skip the boundary. A second copy in this file would be
// a guard that goes stale the first time somebody exempts a route — the failure mode would
// be this test demanding a header on a path that does not want one, i.e. a false red that
// teaches its reader to delete it. The parse REFUSES on an empty result (see below), so a
// rename cannot quietly turn the exemption set into "nothing is exempt" either.

const (
	hdrProof = "x-gateway-auth"
	hdrEmail = "x-user-email"
)

func repoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

var (
	// A curl invocation, joined across backslash line continuations by joinContinuations.
	reCurlLine = regexp.MustCompile(`(?m)^\s*curl\b`)
	// The URL: first http(s) token, with or without surrounding quotes.
	reURL = regexp.MustCompile(`https?://[^\s"']+`)
	// `-H "Name: value"` / `--header 'Name: value'` / `-H Name:value`
	reHeader = regexp.MustCompile(`(?:-H|--header)\s+["']?([A-Za-z0-9-]+)\s*:`)
	// The prefixes inside gwExempt's body.
	reExempt = regexp.MustCompile(`strings\.HasPrefix\(p,\s*"([^"]+)"\)`)
)

// joinContinuations folds `foo \` + newline into one logical line so a curl whose headers
// sit on later lines is read as one command. Without this every multi-line recipe in the
// README would look header-less and this guard would be loud and wrong.
func joinContinuations(s string) []string {
	var out []string
	var cur strings.Builder
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimRight(line, " \t")
		if strings.HasSuffix(trimmed, `\`) {
			cur.WriteString(strings.TrimSuffix(trimmed, `\`))
			cur.WriteString(" ")
			continue
		}
		cur.WriteString(trimmed)
		out = append(out, cur.String())
		cur.Reset()
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// exemptPrefixes reads gwExempt's body out of cmd/track/main.go. It REFUSES rather than
// returning an empty set: "no prefixes found" and "nothing is exempt" are the same value
// and opposite meanings, and this queue has shipped the empty-population trap three times.
func exemptPrefixes(t *testing.T) []string {
	t.Helper()
	src := repoFile(t, filepath.Join("cmd", "track", "main.go"))
	start := strings.Index(src, "gwExempt := func(")
	if start < 0 {
		t.Fatal("REFUSE: cannot find `gwExempt := func(` in cmd/track/main.go — this guard " +
			"cannot tell an exempt path from a guarded one, so it must not report a verdict. " +
			"If gwExempt was renamed, rename it here too.")
	}
	end := strings.Index(src[start:], "\n\t}")
	if end < 0 {
		t.Fatal("REFUSE: found gwExempt but not the end of its body in cmd/track/main.go")
	}
	body := src[start : start+end]
	var out []string
	for _, m := range reExempt.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	if len(out) == 0 {
		t.Fatal("REFUSE: gwExempt's body yielded ZERO strings.HasPrefix prefixes. An empty " +
			"exemption set would make every /v1 example in the README require the proof, " +
			"which is a verdict this parse has not earned.")
	}
	return out
}

func TestREADMECurlExamplesCarryTheGatewayTransitProof(t *testing.T) {
	readme := repoFile(t, "README.md")
	exempt := exemptPrefixes(t)

	checked := 0
	for _, cmd := range joinContinuations(readme) {
		if !reCurlLine.MatchString(cmd) {
			continue
		}
		raw := reURL.FindString(cmd)
		if raw == "" {
			continue
		}
		// Path only: strip scheme://host and any ?query.
		path := raw
		if i := strings.Index(path, "://"); i >= 0 {
			path = path[i+3:]
		}
		if i := strings.Index(path, "/"); i >= 0 {
			path = path[i:]
		} else {
			path = "/"
		}
		if i := strings.Index(path, "?"); i >= 0 {
			path = path[:i]
		}
		if !strings.HasPrefix(path, "/v1/") {
			continue
		}
		skip := false
		for _, p := range exempt {
			if strings.HasPrefix(path, p) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		checked++

		got := map[string]bool{}
		for _, m := range reHeader.FindAllStringSubmatch(cmd, -1) {
			got[strings.ToLower(m[1])] = true
		}
		for _, want := range []string{hdrProof, hdrEmail} {
			if !got[want] {
				t.Errorf("README curl for %s omits %s — as written this returns 401/403 "+
					"before reaching a handler.\n  command: %s", path, want, strings.TrimSpace(cmd))
			}
		}
	}

	if checked == 0 {
		t.Fatal("REFUSE: the README contains no curl example addressing a NON-exempt /v1 path, " +
			"so this test asserted nothing. A green run here would mean 'we found no recipes', " +
			"not 'every recipe is correct'. If the recipes moved, point this test at them.")
	}
	t.Logf("checked %d non-exempt /v1 curl recipe(s) against %d exempt prefix(es)", checked, len(exempt))
}
