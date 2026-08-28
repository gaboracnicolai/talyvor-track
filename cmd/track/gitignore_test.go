package main

import (
	"os/exec"
	"strings"
	"testing"
)

// gitignore_test.go — the artefacts this repo's own build commands produce must be
// unstageable.
//
// ⚠ THIS IS NOT TIDINESS, AND THE EVIDENCE IS A NEAR-MISS RATHER THAN AN OPINION. W3.47
// (tab-j4q7, 2026-08-28): `go build ./cmd/track` drops a `track` binary in the repo root,
// and during that merge `git add -A` STAGED IT. It was caught by reading
// `git status --porcelain` before committing; had `git commit -a` been used, a ~40 MB
// binary would be in main. At that point the repo had NO .gitignore at all — the only one
// anywhere in the tree belonged to a different tool (.remember/) and ignored its own logs.
//
// Nothing was committed, so this guards a latent hazard rather than repairing a live
// defect, and it says so.

// buildArtefacts are paths this repo's OWN documented commands produce:
//
//	go build ./cmd/track      -> track
//	make build                -> bin/track       (Makefile: go build -o bin/track)
//	npm run build  (frontend) -> frontend/dist
//	npm ci         (frontend) -> frontend/node_modules
//
// plus .env, which is not a build artefact but is the one file whose accidental commit
// would be worst: docker-compose reads it for ${VAR} substitution, and .env.example now
// documents TRACK_GUEST_SECRET, TRACK_MEMBER_SYNC_SECRET and
// TRACK_INTEGRATION_ENCRYPTION_KEY (W3.44), so a real .env holds every credential this
// service has.
var buildArtefacts = []string{
	"track",
	"bin/track",
	"frontend/dist",
	"frontend/node_modules",
	".env",
}

// mustBeTracked are paths that must NOT be ignored. Without them this whole file could be
// satisfied by a .gitignore containing a single `*`, which would pass every assertion above
// while making the repository unusable.
var mustBeTracked = []string{
	"cmd/track/main.go",
	"go.mod",
	".env.example",
	"docker-compose.yaml",
}

func gitCheckIgnore(t *testing.T, path string) bool {
	t.Helper()
	// -q: exit 0 when the path IS ignored, 1 when it is not. Any other code is a real error.
	cmd := exec.Command("git", "check-ignore", "-q", "--no-index", path)
	cmd.Dir = "../.."
	err := cmd.Run()
	if err == nil {
		return true
	}
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
		return false
	}
	t.Fatalf("git check-ignore %s: %v", path, err)
	return false
}

func TestBuildArtefactsAreUnstageable(t *testing.T) {
	// ⚠ ASKED UNCONDITIONALLY, ABOUT PATHS RATHER THAN ABOUT FILES THAT HAPPEN TO EXIST.
	// A guard that only checked artefacts present in the working tree would go vacuously
	// green on a clean checkout — which is every CI run, i.e. exactly when it matters least
	// to the person running it and most to the repository.
	for _, p := range buildArtefacts {
		if !gitCheckIgnore(t, p) {
			t.Errorf("%q is not ignored: a routine build (or `npm ci`) drops it in the tree and "+
				"`git add -A` will stage it. W3.47 nearly committed a 40MB binary this way.", p)
		}
	}

	// ⚠ THE COUNTERWEIGHT. `*` in .gitignore satisfies every assertion above.
	for _, p := range mustBeTracked {
		if gitCheckIgnore(t, p) {
			t.Errorf("%q IS ignored — the ignore rules are too broad. A pattern wide enough to "+
				"hide source makes the assertions above meaningless.", p)
		}
	}
}

// TestNoBuildArtefactIsAlreadyTracked — an ignore rule does NOTHING for a path git already
// tracks. Without this, the test above could report protection the repository does not have.
func TestNoBuildArtefactIsAlreadyTracked(t *testing.T) {
	cmd := exec.Command("git", "ls-files")
	cmd.Dir = "../.."
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	tracked := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(tracked) < 100 {
		t.Fatalf("git ls-files returned only %d paths — this enumeration is broken, and a guard "+
			"that enumerates nothing passes everything", len(tracked))
	}
	for _, f := range tracked {
		for _, p := range buildArtefacts {
			if f == p || strings.HasPrefix(f, p+"/") {
				t.Errorf("%q is TRACKED. Ignoring an already-tracked path has no effect, so the "+
					"ignore rule for %q protects nothing until it is `git rm --cached`d.", f, p)
			}
		}
	}
}
