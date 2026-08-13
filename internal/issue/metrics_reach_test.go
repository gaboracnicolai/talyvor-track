package issue_test

// metrics_reach_test.go — WHO INCREMENTS the two issue counters, enumerated from the source rather
// than remembered.
//
// ⚠ THIS GUARD EXISTS BECAUSE THE DEFECT IT LOCKS WAS A QUESTION OF PLACE, NOT OF LOGIC. Each
// increment sat at ONE route while many production paths wrote issues, so the counter under-reported
// the product by every import, every MCP tool call, every automation rule and — for updates — every
// kanban drag. They now live in issue.countCreated and issue.countUpdatedLabels, on the store doors
// every writer passes through. Both failure directions are real and neither is visible in a diff of
// the file that causes it:
//
//   - a SECOND site (a route "also" counting, the way these did) DOUBLE-counts that path, and the
//     counter is then wrong in the opposite direction with no test asserting either number;
//   - moving the increment back OUT of the store re-opens the original hole for whichever writer is
//     added next — the ones that already existed did not forget deliberately, they never had a
//     reason to look.
//
// ⚠ WHAT IT DOES NOT CLAIM: it does not prove every writer counts. Nothing textual can — a new store
// method that writes issues and calls neither helper would satisfy this guard. What it pins is that
// there is exactly ONE increment site per counter and which file it is in; the per-door proofs are
// created_metric_realpg_test.go and updated_metric_realpg_test.go, and the end-to-end proofs are
// internal/importer/created_metric_job_test.go and internal/importer/updated_metric_job_test.go.
// Three instruments per counter, three different questions.
//
// ⚠ IT COUNTS TEXT, INCLUDING COMMENTS. A comment that spells the collector's Go name adds to the
// count and reds this guard — which is why the two handler comments that hand the increment to the
// store name the METRIC (track_issues_updated_total) and not the identifier. That is a real
// constraint on prose, stated here so the next reader does not read a red as a bug in the walk.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// theOneIncrementSite is the file that may increment either counter. Relative to this package's
// directory, which is where `go test` runs.
const theOneIncrementSite = "store.go"

// incrementSites walks the whole production tree — this repository's Go code is internal/ + cmd/ —
// and returns every non-test file naming `ref`, with its occurrence count. A guard that searched
// only this package would be blind to precisely the shape it exists to catch: a handler in ANOTHER
// package counting as well.
func incrementSites(t *testing.T, ref string) map[string]int {
	t.Helper()
	found := map[string]int{}
	for _, root := range []string{"../../internal", "../../cmd"} {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			if n := strings.Count(string(b), ref); n > 0 {
				found[filepath.ToSlash(path)] = n
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v — the guard read nothing, which is not the same as finding nothing", root, err)
		}
	}
	return found
}

// assertExactlyOneSite is the shared body of both cases below. `why` is the sentence a reader gets
// when the count is not one — the two counters were broken by the same mistake and are held to the
// same rule, but the census behind each is its own.
func assertExactlyOneSite(t *testing.T, ref, helper, why string) {
	t.Helper()
	found := incrementSites(t, ref)
	// A zero here means the walk found no reference AT ALL, i.e. the counter is dead or the walk is
	// broken. Reported apart from the count so "nothing increments it" cannot read as "one thing does".
	if len(found) == 0 {
		t.Fatalf("NO production file references %s. Either the counter is now dead (it is published "+
			"at /metrics and its Help claims to total the product) or this guard is reading the "+
			"wrong tree.", ref)
	}
	if len(found) != 1 {
		t.Fatalf("%s is referenced in %d production files: %v.\n"+
			"It must be incremented in exactly one place — internal/issue/%s (%s), the door every "+
			"writer passes through. %s", ref, len(found), found, theOneIncrementSite, helper, why)
	}
	for path, n := range found {
		if !strings.HasSuffix(path, "internal/issue/"+theOneIncrementSite) {
			t.Errorf("%s is referenced in %s, not internal/issue/%s. The increment belongs on the "+
				"store's write path, not on a caller of it — a caller-side counter is what left "+
				"almost every writer uncounted. %s", ref, path, theOneIncrementSite, why)
		}
		if n != 1 {
			t.Errorf("%s references %s %d times, want 1 — two increments in one file double-count "+
				"just as surely as two in two files", path, ref, n)
		}
	}
}

func TestMetrics_IssuesCreatedIsIncrementedInExactlyOnePlace(t *testing.T) {
	assertExactlyOneSite(t, "metrics.IssuesCreated", "countCreated",
		"A second site double-counts that path. The census: FIVE production paths create an issue "+
			"(POST /v1/issues, the importer's two branches, the MCP tool surface, the automation "+
			"engine) and four of them moved no counter until the increment came here.")
}

// ⚠ THE SECOND COUNTER IS PINNED HERE NOW, AND THE NOTE THIS REPLACES SAID IT WAS NOT. It read:
// "track_issues_updated_total IS DELIBERATELY NOT PINNED HERE and is NOT fixed ... Store.BulkUpdate
// returns a COUNT and not the rows, so a per-(workspace, team, status) increment there needs the
// updated rows read back or the statement changed. That is a decision about a SQL path, not a
// counter." The statement was changed: the per-item UPDATE gained `RETURNING team_id, status` and
// tx.Exec became tx.QueryRow. It touches at most one row (the WHERE is the primary key AND the
// workspace), so pgx.ErrNoRows and RowsAffected()==0 are the same fact and the returned count is
// unchanged — what the command tag could not give was two of the three labels.
func TestMetrics_IssuesUpdatedIsIncrementedInExactlyOnePlace(t *testing.T) {
	assertExactlyOneSite(t, "metrics.IssuesUpdated", "countUpdatedLabels",
		"A second site double-counts that path. The census: FIFTEEN production paths update an issue "+
			"(PATCH /v1/issues/{id}, TWELVE other Store.Update callers, the bulk route, and the "+
			"importer's upsert-UPDATE branch) and FOURTEEN of them moved no counter until the "+
			"increment came here.")
}
