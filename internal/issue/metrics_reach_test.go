package issue_test

// metrics_reach_test.go — WHO INCREMENTS track_issues_created_total, enumerated from the source
// rather than remembered.
//
// ⚠ THIS GUARD EXISTS BECAUSE THE DEFECT IT LOCKS WAS A QUESTION OF PLACE, NOT OF LOGIC. The
// increment sat at ONE route (POST /v1/issues) while five production paths created issues, so the
// counter under-reported the product by every import, every MCP tool call and every automation
// rule. It now lives in issue.countCreated, on the two store doors all five paths pass through.
// Both failure directions are real and neither is visible in a diff of the file that causes it:
//
//   - a SECOND site (a route "also" counting, the way this one did) DOUBLE-counts that path, and
//     the counter is then wrong in the opposite direction with no test asserting either number;
//   - moving the increment back OUT of the store re-opens the original hole for whichever writer
//     is added next — the four that already existed did not forget deliberately, they never had a
//     reason to look.
//
// ⚠ WHAT IT DOES NOT CLAIM: it does not prove every writer counts. Nothing textual can — a new
// store method that writes issues and does not call countCreated would satisfy this guard. What it
// pins is that there is exactly ONE increment site and which file it is in; the per-door proof is
// created_metric_realpg_test.go and the end-to-end proof is
// internal/importer/created_metric_job_test.go. Three instruments, three different questions.
//
// ⚠ track_issues_updated_total IS DELIBERATELY NOT PINNED HERE and is NOT fixed: it has the SAME
// defect, MEASURED — one production site (internal/issue/handler.go's PATCH route) against TWELVE
// other Store.Update call sites (internal/mcp/server.go ×3, internal/automation/engine.go ×7,
// internal/automation/github.go ×1, internal/ai/handler.go ×1), plus the bulk route, plus the
// importer's upsert-UPDATE branch — and one complication this merge does not settle:
// Store.BulkUpdate returns a COUNT and not the rows, so a per-(workspace, team, status) increment
// there needs the updated rows read back or the statement changed. That is a decision about a SQL
// path, not a counter. Locking the counter to today's single site would PIN the defect, so it is
// measured and written up in the queue instead of half-closed here.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// theOneIncrementSite is the file that may increment track_issues_created_total. Relative to this
// package's directory, which is where `go test` runs.
const theOneIncrementSite = "store.go"

func TestMetrics_IssuesCreatedIsIncrementedInExactlyOnePlace(t *testing.T) {
	// The whole production tree: this repository's Go code is internal/ + cmd/, and both are walked
	// from here. A guard that searched only this package would be blind to precisely the shape it
	// exists to catch — a handler in ANOTHER package counting as well.
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
			if n := strings.Count(string(b), "metrics.IssuesCreated"); n > 0 {
				found[filepath.ToSlash(path)] = n
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v — the guard read nothing, which is not the same as finding nothing", root, err)
		}
	}
	// A zero here means the walk found no reference AT ALL, i.e. the counter is dead or the walk is
	// broken. Reported apart from the count so "nothing increments it" cannot read as "one thing does".
	if len(found) == 0 {
		t.Fatalf("NO production file references metrics.IssuesCreated. Either the counter is now dead " +
			"(it is published at /metrics and its Help claims to total issue creation) or this guard " +
			"is reading the wrong tree.")
	}
	if len(found) != 1 {
		t.Fatalf("metrics.IssuesCreated is referenced in %d production files: %v.\n"+
			"It must be incremented in exactly one place — internal/issue/%s (countCreated), the door "+
			"every writer passes through. A second site double-counts that path; see this file's header.",
			len(found), found, theOneIncrementSite)
	}
	for path, n := range found {
		if !strings.HasSuffix(path, "internal/issue/"+theOneIncrementSite) {
			t.Errorf("metrics.IssuesCreated is referenced in %s, not internal/issue/%s. The increment "+
				"belongs on the store's write path, not on a caller of it — a caller-side counter is "+
				"what left four of five writers uncounted.", path, theOneIncrementSite)
		}
		if n != 1 {
			t.Errorf("%s references metrics.IssuesCreated %d times, want 1 — two increments in one "+
				"file double-count just as surely as two in two files", path, n)
		}
	}
}
