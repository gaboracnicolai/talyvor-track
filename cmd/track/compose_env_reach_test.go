package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// compose_env_reach_test.go — a credential the compose file does not forward is MUTE: it can be
// set in .env, look correct in every review, and never reach the process.
//
// WHY A TEST AND NOT A REVIEW HABIT. `.env` is NOT passed into containers. Compose reads it only
// for ${VAR} substitution inside the compose file itself, and the `track:` service declares an
// explicit `environment:` list and NO `env_file:` — so a variable reaches the process if and only
// if it appears in that list. Nothing about editing .env tells you otherwise.
//
// ⚠ AND THE GAP IS INVISIBLE FROM INSIDE THE PROCESS. An unset variable and an unforwarded one
// are the same empty string, so no behavioural test can find this. It is only visible from
// outside, in the file that forwards. That is why this guard reads a YAML file from a Go test
// instead of asserting on behaviour.
//
// ⚠ THE INPUT IS THE SOURCE TREE, NOT A LIST SOMEBODY TYPED. A curated list catches only what its
// author remembered, which is the failure being guarded against. Adding os.Getenv("X_TOKEN")
// anywhere in the tree is what makes this fire; nobody has to remember to update it.
//
// WHAT IT GUARDS AND WHAT IT DELIBERATELY DOES NOT. The process reads 17 environment variables and
// compose forwards 11. Most of the difference is fine — a tuning knob's absence is its documented
// default. A CREDENTIAL's absence is different: it is a dead feature or an unarmed control, with
// no error anywhere. So the class is credentials, chosen by a MECHANICAL name-suffix rule that
// needs no judgement to apply and no memory to maintain. Judgement lives only in the exemptions,
// and each one states its reason.

// credentialExemptions are credential-shaped names that must NOT be forwarded to the `track:`
// service, with the reason. An exemption is a decision; it is written here so the next person can
// disagree with it rather than guess at it.
//
// Empty today, and that is a real statement: every credential the process reads belongs in the
// container. If you are about to add an entry, the bar is "forwarding it would be WRONG", not
// "I do not want to set it" — an unused credential forwarded as `${VAR:-}` costs nothing.
var credentialExemptions = map[string]string{}

// credentialSuffixes is the mechanical class test.
var credentialSuffixes = []string{"_KEY", "_SECRET", "_TOKEN", "_PASSWORD", "_CREDENTIALS"}

func looksLikeCredential(name string) bool {
	for _, s := range credentialSuffixes {
		if strings.HasSuffix(name, s) {
			return true
		}
	}
	return false
}

// envReadPat matches every way this tree reads an environment variable. getEnv/getEnvDuration are
// internal/config's own helpers; missing them would make the guard blind to exactly the file that
// reads the most variables.
var envReadPat = regexp.MustCompile(`(?:os\.Getenv|os\.LookupEnv|getEnv|getEnvDuration)\(\s*"([A-Z][A-Z0-9_]*)"`)

// readsEnvVars enumerates every environment variable the tree reads, from the SOURCE, mapped to
// the first file that reads it so a failure can be acted on without a search.
//
// ⚠ _test.go IS SKIPPED. A test reading an env var says nothing about what the deployment needs,
// and counting them would produce exemptions for names no container ever wants.
func readsEnvVars(t *testing.T) map[string]string {
	t.Helper()
	found := map[string]string{}
	for _, root := range []string{"..", "../../internal"} {
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				return nil
			}
			for _, m := range envReadPat.FindAllStringSubmatch(string(b), -1) {
				if _, seen := found[m[1]]; !seen {
					found[m[1]] = path
				}
			}
			return nil
		})
	}
	return found
}

func readCompose(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../docker-compose.yaml")
	if err != nil {
		t.Fatalf("read docker-compose.yaml: %v", err)
	}
	return string(raw)
}

// trackServiceEnv returns the text of the `track:` service block. Scoped deliberately: forwarding
// a variable to the one-shot `migrate` service does not help the process that serves traffic.
func trackServiceEnv(t *testing.T, compose string) string {
	t.Helper()
	start := strings.Index(compose, "\n  track:")
	if start < 0 {
		t.Fatal("no `track:` service in docker-compose.yaml — this guard has drifted from the " +
			"file it protects, and would otherwise scan an empty string and pass everything")
	}
	rest := compose[start+1:]
	// The block ends at the next top-level service key (two-space indent, not four, not a comment).
	end := len(rest)
	for i := 1; i < len(rest)-3; i++ {
		if rest[i] == '\n' && rest[i+1] == ' ' && rest[i+2] == ' ' && rest[i+3] != ' ' && rest[i+3] != '#' {
			end = i
			break
		}
	}
	return rest[:end]
}

// forwards reports whether the block actually FORWARDS name — i.e. contains a real list entry
// `- NAME=` — rather than merely mentioning it.
//
// ⚠ THIS DISTINCTION IS THE WHOLE GUARD. A strings.Contains version would be satisfied by the
// COMMENT above a forwarding line, so deleting the line while keeping the comment explaining why
// the variable matters would stay green. That is a green light wired to nothing, and it is the
// documented way this exact guard failed in the sibling repo.
func forwards(block, name string) bool {
	re := regexp.MustCompile(`(?m)^\s*-\s*` + regexp.QuoteMeta(name) + `=`)
	return re.MatchString(block)
}

// TestEveryCredentialTheProcessReadsIsForwarded is the guard proper.
func TestEveryCredentialTheProcessReadsIsForwarded(t *testing.T) {
	trackEnv := trackServiceEnv(t, readCompose(t))
	reads := readsEnvVars(t)

	// Non-vacuity: the enumeration must actually find things. A broken regex or a bad walk root
	// would make every assertion below vacuously green, which is the failure mode this whole file
	// exists to prevent in the compose file.
	if len(reads) < 15 {
		t.Fatalf("only %d environment reads found in the tree — the enumeration is broken, and a "+
			"guard that enumerates nothing passes everything. Found: %v", len(reads), sortedKeys(reads))
	}

	var missing []string
	for name, file := range reads {
		if !looksLikeCredential(name) {
			continue
		}
		if _, exempt := credentialExemptions[name]; exempt {
			continue
		}
		if !forwards(trackEnv, name) {
			missing = append(missing, name+"  (read at "+file+")")
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("%s is a CREDENTIAL the process reads and docker-compose.yaml does not forward "+
			"to the `track:` service.\n"+
			"    It can be set in .env, look right in review, and reach nothing: compose reads "+
			".env only for ${} substitution and this service declares no env_file. The container "+
			"starts healthy, passes its healthcheck, and the feature is inert with no log line "+
			"that an operator would read as an error.\n"+
			"    Add `- NAME=${NAME:-}` to the `track:` service's environment, or add it to "+
			"credentialExemptions WITH ITS REASON.", m)
	}
}

// TestCredentialClassDistinguishes — the class must actually SEPARATE. If the suffix rule matched
// nothing, or matched every variable, the guard above would be theatre either way: one direction
// passes unconditionally, the other floods the operator with names they cannot act on and gets
// the guard deleted.
func TestCredentialClassDistinguishes(t *testing.T) {
	reads := readsEnvVars(t)

	var creds, plain []string
	for name := range reads {
		if looksLikeCredential(name) {
			creds = append(creds, name)
		} else {
			plain = append(plain, name)
		}
	}
	if len(creds) == 0 {
		t.Fatal("the credential class matched NOTHING — the guard above cannot fail")
	}
	if len(plain) == 0 {
		t.Fatal("the credential class matched EVERY variable — it is not a class, it is a tautology")
	}

	// Pin the rule's behaviour on two known names, so a future edit to credentialSuffixes that
	// broadens or narrows it fails here rather than silently changing what is guarded.
	if !looksLikeCredential("TRACK_INTEGRATION_ENCRYPTION_KEY") {
		t.Error("TRACK_INTEGRATION_ENCRYPTION_KEY is not classified as a credential — it is the " +
			"AES-256 key that gates live Jira/Linear API import")
	}
	if looksLikeCredential("TRACK_LISTEN_ADDR") {
		t.Error("TRACK_LISTEN_ADDR is classified as a credential — the rule is too broad")
	}
}

// TestForwardedCredentialsUseASafeDefault — an OPTIONAL capability must be forwarded as `${VAR:-}`,
// not `${VAR:?}`. `:?` makes the whole stack refuse to render when the variable is absent, turning
// a capability a deployment never wanted into a hard boot dependency. That is the same mistake as
// the mute one, pointed the other way: instead of failing silently it fails everything.
//
// The exceptions are listed with their justification rather than pattern-matched, so adding one is
// a decision somebody writes down.
func TestForwardedCredentialsUseASafeDefault(t *testing.T) {
	requiredIsCorrect := map[string]string{
		"GATEWAY_AUTH_SECRET": "the edge gateway transit proof — the root of Track's entire auth " +
			"boundary: whoever knows it can present ANY identity. It shipped with a committed " +
			"default that satisfied the >=16 length check while being public, so the stack booted " +
			"\"fail-closed\" on a secret anyone could read. `:?` is correct precisely because there " +
			"must be NOTHING to fall back to.",
		"POSTGRES_PASSWORD": "shipped as `${POSTGRES_PASSWORD:-changeme}` in three places for the " +
			"life of the file. A `:-` fallback SUPPLIES the value, so `docker compose up` with no " +
			"operator input started Postgres on a password published in this repo.",
	}

	trackEnv := trackServiceEnv(t, readCompose(t))
	re := regexp.MustCompile(`\$\{([A-Z][A-Z0-9_]*):\?`)
	for _, m := range re.FindAllStringSubmatch(trackEnv, -1) {
		if _, ok := requiredIsCorrect[m[1]]; ok {
			continue
		}
		t.Errorf("%s is forwarded with `:?` (required). Use `:-` (empty default) so a deployment "+
			"that does not use this capability still boots — or add it to requiredIsCorrect with "+
			"the reason it genuinely cannot start without a value.", m[1])
	}
}

// TestExemptionsAreStillRead keeps the exemption list honest in the other direction: an entry
// naming a variable the process no longer reads is a line that guards nothing while reading as
// though it does — and it would hide the next real one.
func TestExemptionsAreStillRead(t *testing.T) {
	reads := readsEnvVars(t)
	for name := range credentialExemptions {
		if _, ok := reads[name]; !ok {
			t.Errorf("credentialExemptions names %s, which no non-test file reads. Stale entry — "+
				"remove it, or fix the name.", name)
		}
	}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
