package tenancy_test

// suppression_census_test.go — EVERY `nosemgrep` IS A PERMANENT HOLE IN A TENANCY LOCK, AND THE
// ONLY RECORD OF HOW MANY THERE ARE WAS A SENTENCE IN A RULE FILE THAT WAS WRONG.
//
// .semgrep/operate-by-id-tenancy.yml said: "The only remaining suppressions are three INLINE
// `nosemgrep` lines, each with a written justification at the code". MEASURED at 74ca01b:
//
//	operate-by-id-write-requires-workspace-scope           4   (workspace, guest, featureboard x2)
//	operate-by-id-write-requires-workspace-scope-sprintf   1   (workspace/store.go:276)
//	child-insert-requires-parent-workspace-guard           3   (ai, workflow, automation)
//	caller-workspace-id-query-needs-authorization          1   (member/handler.go:55)
//	                                                     ---
//	                                                       9
//
// FIVE in the family that sentence describes, not three — and its named list omits the sprintf
// one at workspace/store.go:276 entirely. A prose census of holes goes stale the first time
// someone adds a hole, and nothing was checking it.
//
// ⚠ THE OTHER HALF OF THAT SENTENCE IS TRUE AND IS NOW ENFORCED RATHER THAN ASSERTED: all NINE
// carry a justification with an INVALIDATED IF clause naming what would void it. That is the
// property worth keeping — a suppression whose justification cannot be checked against the code
// is indistinguishable from one nobody thought about.
//
// ⚠ A CEILING, NOT AN EQUALITY, AND THE ASYMMETRY IS THE POINT. Adding a suppression is the
// dangerous direction and must be a deliberate, visible edit here. REMOVING one closes a hole and
// must never red a build — so fewer is always fine. (The mounted-route sweep in
// analytics/authz_refusal_sweep_test.go takes the opposite trade for the opposite reason: there,
// growth is free and shrinkage is the danger.)
//
// ⚠ AND THE SCAN IS CHECKED DIRECTLY. A walk that reads nothing, or a matcher whose token has been
// renamed, would report a clean product: zero suppressions is under every ceiling and vacuously
// satisfies the justification rule. A files-parsed floor catches the broken walk and a matcher
// self-test catches the broken pattern. Both are positive-controlled in
// scripts/w34-suppression-census-controls-8x2m.py, along with the removal case that this file's
// first cut got wrong.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Measured at 74ca01b. A ceiling per rule id: a new suppression on any of these locks is an edit
// to this map, in the same commit, where a reviewer sees it.
var suppressionCeiling = map[string]int{
	"operate-by-id-write-requires-workspace-scope":         4,
	"operate-by-id-write-requires-workspace-scope-sprintf": 1,
	"child-insert-requires-parent-workspace-guard":         3,
	"caller-workspace-id-query-needs-authorization":        1,
}

// Floor against a blinded WALK. 114 non-test .go files under internal/ + cmd/ at this merge.
//
// ⚠ THERE IS DELIBERATELY NO FLOOR ON THE NUMBER OF SUPPRESSIONS, AND THE FIRST CUT OF THIS FILE
// HAD ONE. `minSuppressions = 9` plus the ceilings below made the census an EQUALITY in disguise:
// removing a suppression — closing a hole — dropped the count to 8 and RED the build, which is
// exactly what the header promises will never happen. Control S5 scored it CAUGHT against a
// predicted NOT CAUGHT and that is how it was found. A count of holes is the wrong instrument for
// "is the scan working"; the scan is checked directly instead, by the self-test below.
const minSuppressionFiles = 100

// sampleDirective exercises the matcher itself, independently of how many suppressions the tree
// happens to contain. A renamed or broken token makes the scan match nothing, and nothing is under
// every ceiling and vacuously satisfies the justification rule — a blinded census reports a clean
// product. This is what catches that, without pinning the population.
const sampleDirective = `	// nosemgrep: some-rule-id -- INVALIDATED IF nothing.`

var nosemgrepRule = regexp.MustCompile(`nosemgrep:\s*([\w-]+)`)

type suppression struct {
	file, rule string
	line       int
	justified  bool
}

func TestSemgrepSuppressions_AreCountedAndJustified(t *testing.T) {
	found, parsed := scanSuppressions(t)

	if parsed < minSuppressionFiles {
		t.Fatalf("the scan read %d non-test .go files, floor is %d — the walk is not reading the "+
			"tree, so a clean result here would mean nothing", parsed, minSuppressionFiles)
	}
	if m := nosemgrepRule.FindStringSubmatch(sampleDirective); m == nil || m[1] != "some-rule-id" {
		t.Fatalf("the directive matcher does not match its own sample %q — the token has been renamed "+
			"or the pattern broken, so this census would scan the whole tree and find nothing. Nothing "+
			"is under every ceiling and vacuously justified: a blinded census reports a clean product.",
			strings.TrimSpace(sampleDirective))
	}

	counts := map[string]int{}
	for _, s := range found {
		counts[s.rule]++
		if !s.justified {
			t.Errorf("%s:%d suppresses %q with no INVALIDATED IF clause.\n"+
				"\tEvery nosemgrep is a PERMANENT hole in a tenancy lock. The justification must say "+
				"what would VOID it, inline after `--` or in the comment block directly above, so the "+
				"next reader can check the reason against the code instead of trusting that someone "+
				"once had one.", s.file, s.line, s.rule)
		}
	}

	for rule, n := range counts {
		ceiling, known := suppressionCeiling[rule]
		if !known {
			t.Errorf("%d suppression(s) of %q, a rule this census has never seen. Add it to "+
				"suppressionCeiling in the same commit that adds the suppression — a hole nobody "+
				"counted is the thing this test exists to prevent.", n, rule)
			continue
		}
		if n > ceiling {
			t.Errorf("%q is suppressed %d times, ceiling is %d. A new suppression is a new hole in a "+
				"tenancy lock: raise this number deliberately, in the same commit, with the "+
				"justification at the code. FEWER is always fine — this is a ceiling, not an equality, "+
				"because closing a hole must never red a build.", rule, n, ceiling)
		}
	}
}

func scanSuppressions(t *testing.T) ([]suppression, int) {
	t.Helper()
	root := suppressionRepoRoot(t)
	var out []suppression
	parsed := 0

	for _, sub := range []string{"internal", "cmd"} {
		err := filepath.Walk(filepath.Join(root, sub), func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			b, rerr := os.ReadFile(p)
			if rerr != nil {
				return rerr
			}
			parsed++
			lines := strings.Split(string(b), "\n")
			for i, l := range lines {
				m := nosemgrepRule.FindStringSubmatch(l)
				if m == nil {
					continue
				}
				rel, _ := filepath.Rel(root, p)
				out = append(out, suppression{
					file: rel, line: i + 1, rule: m[1],
					justified: hasInvalidatedIf(lines, i),
				})
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", sub, err)
		}
	}
	return out, parsed
}

// hasInvalidatedIf looks on the directive line and in the contiguous comment block above it. The
// block is JOINED before searching: "INVALIDATED" and "IF" are wrapped across two lines at
// automation/engine.go, and a line-by-line check scores that one unjustified when it is not.
// (Measured — the first cut of this scan did exactly that.)
func hasInvalidatedIf(lines []string, at int) bool {
	parts := []string{lines[at]}
	for j := at - 1; j >= 0 && strings.HasPrefix(strings.TrimSpace(lines[j]), "//"); j-- {
		parts = append([]string{lines[j]}, parts...)
	}
	joined := strings.Join(parts, " ")
	joined = strings.ReplaceAll(joined, "//", " ")
	return strings.Contains(strings.Join(strings.Fields(joined), " "), "INVALIDATED IF")
}

func suppressionRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s — cannot locate the repo root to scan", dir)
		}
		dir = parent
	}
}
