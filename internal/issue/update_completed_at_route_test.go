package issue_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/testutil"
)

// A COMPLETION TIME IS RECORDED ONLY ON A ROW THAT IS DONE — the invariant this repository
// states in FOUR production comments, asserted at the door that can break it and through the
// real /v1 chain rather than against the store alone.
//
// WHERE THE HOLE WAS. `Update`'s field-map gate (store.go:1075) reads
//
//	if _, ok := updatableFields[k]; !ok && k != "completed_at" { continue }
//
// so `completed_at` is admitted BY NAME, outside the allowlist, WHOEVER put it in the map.
// The server puts it there on a status transition — but that block (store.go:1053) runs only
// `if rawStatus, ok := updates["status"]; ok`, and `updates` is the raw PATCH body decoded
// straight off the request (handler.go:337, `map[string]any`). A body carrying `completed_at`
// and NO `status` therefore reached `SET completed_at = $1` with the caller's value while the
// row's status never moved.
//
// WHY THAT IS A DEFECT AND NOT A TASTE ARGUMENT — the repository had already decided this, at
// the other write path, and wrote the decision down. `Create` gates the same column explicitly
// (store.go:244: `if issue.Status != model.StatusDone { completedAt = nil }`) and its comment
// says why: naming the column with no rule "would newly let any client file BACKLOG work
// carrying a completion time — a row no Track path can produce (Update stamps completed_at only
// on a transition ONTO done and CLEARS it on any transition away) and one that analytics'
// resolution stats count as delivered, because that query selects on `completed_at IS NOT NULL`
// with no status predicate." The parenthesis is a claim about Update, and MEASURED at
// `1c0323a` it was false: `PATCH {"completed_at":"2020-01-02T03:04:05Z"}` on a backlog issue
// answered 200 and stored the value. The same sentence appears at importer/jira.go:261,
// importer/linear.go:89 and importer/linear_csv_dates.go:163, each inheriting it as settled.
//
// THE FIX IS THE SMALLEST ONE THAT MAKES THE STATED RULE TRUE: `serverStamped` records that
// Update itself put the value in the map, and the by-name exemption now applies only then. No
// production caller is affected — every internal `issueStore.Update` call site passes a fixed
// key set (automation/engine.go, automation/github.go, mcp/server.go's typed field list,
// ai/handler.go), and none of them names `completed_at`. What changes is that an API client
// PATCHing `completed_at` directly now has it dropped, exactly as `Create` has always nil'd a
// CompletedAt supplied on a non-done row.
//
// The four subtests are four different ways this can be got wrong, and each is here because a
// plausible "fix" passes the others: dropping the column from the write path entirely passes
// (1) and fails (2); keeping the caller's value on a done transition passes (1) and (2) and
// fails (3); and (4) states the invariant in the database's own terms over every row the
// earlier subtests left behind, so a future write path that reaches the column another way has
// something to fail.
func TestUpdateRoute_ACompletionTimeIsRecordedOnlyOnARowThatIsDone_RealPG(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	sec5Member(t, d, ws.ID, "alice@corp.com")
	tm := d.Team(t, ws.ID)
	chain := sec5Chain(d)

	// Driven through gatewayauth + authz + the chi route, not against the store: the question
	// is what an API caller can put in the column, and the body is the only thing under test.
	patch := func(t *testing.T, issueID, body string) int {
		t.Helper()
		w := httptest.NewRecorder()
		chain.ServeHTTP(w, patchAs(ws.ID, "/issues/"+issueID, "alice@corp.com", body))
		return w.Code
	}

	const callerTime = "2020-01-02T03:04:05Z"

	t.Run("a body carrying only completed_at leaves a backlog row with none", func(t *testing.T) {
		iss := d.Issue(t, ws.ID, tm.ID)
		if got := issueStatus(t, d, iss.ID); got != string(model.StatusBacklog) {
			t.Fatalf("PREMISE FAILED: seeded status is %q, want %q — this subtest needs a row "+
				"that is NOT done for the assertion to mean anything", got, model.StatusBacklog)
		}
		if code := patch(t, iss.ID, `{"completed_at":"`+callerTime+`"}`); code != http.StatusOK {
			t.Fatalf("PATCH completed_at = %d, want 200", code)
		}
		if got := issueStatus(t, d, iss.ID); got != string(model.StatusBacklog) {
			t.Errorf("status is %q after a body that named no status, want %q", got, model.StatusBacklog)
		}
		if got := readCompletedAt(t, d, iss.ID); got != nil {
			t.Errorf("completed_at = %s on a %s issue — the caller's value reached SET, and "+
				"analytics' resolution query selects on `completed_at IS NOT NULL` with NO status "+
				"predicate, so this row is counted as delivered work",
				got.UTC().Format(time.RFC3339), model.StatusBacklog)
		}
	})

	t.Run("the server still stamps on done and clears on the way back", func(t *testing.T) {
		iss := d.Issue(t, ws.ID, tm.ID)
		if code := patch(t, iss.ID, `{"status":"done"}`); code != http.StatusOK {
			t.Fatalf("PATCH status=done = %d, want 200", code)
		}
		stamped := readCompletedAt(t, d, iss.ID)
		if stamped == nil {
			t.Fatalf("completed_at is NULL after a transition ONTO done — the stamp itself is gone, " +
				"which is a bigger break than the one this file exists for")
		}
		if age := time.Since(*stamped); age < 0 || age > time.Hour {
			t.Errorf("completed_at = %s, want the server's own clock", stamped.UTC().Format(time.RFC3339))
		}
		if code := patch(t, iss.ID, `{"status":"todo"}`); code != http.StatusOK {
			t.Fatalf("PATCH status=todo = %d, want 200", code)
		}
		if got := readCompletedAt(t, d, iss.ID); got != nil {
			t.Errorf("completed_at = %s after a transition AWAY from done, want NULL",
				got.UTC().Format(time.RFC3339))
		}
	})

	t.Run("a completed_at supplied with a done transition does not become the completion time", func(t *testing.T) {
		iss := d.Issue(t, ws.ID, tm.ID)
		if code := patch(t, iss.ID, `{"status":"done","completed_at":"`+callerTime+`"}`); code != http.StatusOK {
			t.Fatalf("PATCH status=done+completed_at = %d, want 200", code)
		}
		got := readCompletedAt(t, d, iss.ID)
		if got == nil {
			t.Fatalf("completed_at is NULL after a transition onto done")
		}
		if got.UTC().Year() == 2020 {
			t.Errorf("completed_at = %s — the CALLER's value survived alongside the status "+
				"transition, so cycle time is whatever the client says it is",
				got.UTC().Format(time.RFC3339))
		}
	})

	// The invariant in the database's own terms, over every row the subtests above left behind.
	// It is deliberately NOT scoped to one issue: a future write path that reaches the column
	// some other way has something here to fail.
	t.Run("no row in this workspace holds a completion time without being done", func(t *testing.T) {
		var n int
		if err := d.Pool.QueryRow(ctx,
			`SELECT count(*) FROM issues WHERE workspace_id = $1 AND status <> $2 AND completed_at IS NOT NULL`,
			ws.ID, string(model.StatusDone)).Scan(&n); err != nil {
			t.Fatalf("census: %v", err)
		}
		if n != 0 {
			t.Errorf("%d issue(s) in this workspace are not done and carry a completion time — "+
				"the state four production comments say Track cannot produce", n)
		}
	})
}
