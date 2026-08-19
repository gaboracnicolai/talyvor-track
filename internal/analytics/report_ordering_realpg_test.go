package analytics_test

// THREE OF THE FOUR PRESENTATION `ORDER BY`s IN THE REPORT ENGINE CAN BE DELETED WITH EVERY TEST IN
// THE REPOSITORY GREEN — AND THE ONE ASSERTION THAT LOOKS LIKE IT COVERS ONE OF THEM CATCHES A
// REVERSAL WHILE BEING BLIND TO A DELETION.
//
// Takes tab-b9d7's handed-on lead (a). The measurement corrected the lead in a way worth writing
// down: `[D-ORDER]` in distribution_counting_realpg_test.go is NOT vacuous — it fires when the
// clause is reversed — but its fixture inserts the four statuses in count-DESCENDING order
// (backlog 4, todo 3, in_progress 2, done 1), so the plan returns them already sorted and REMOVING
// the clause moves no number it reads. A guard that catches `DESC`→`ASC` and not `DESC`→`(nothing)`
// is covering the typo and missing the refactor.
//
// MEASURED, NOT INFERRED, at 7481fe2 — one term of the shipped statement at a time, each run over
// the whole analytics package, each restored in a `finally` and sha256-verified
// (scripts/w34-report-ordering-controls-8f3d.py):
//
//	O1  distribution  ORDER BY COUNT(*) DESC -> ASC          ranked backwards   CAUGHT ([D-ORDER])
//	O2  label path    ORDER BY COUNT(*) DESC -> ASC          ranked backwards   NOT CAUGHT
//	O3  workload      ORDER BY open_issues DESC -> ASC       busiest last       NOT CAUGHT
//	O4  distribution  ORDER BY COUNT(*) DESC -> (deleted)    unranked           NOT CAUGHT
//	O5  workload      ORDER BY open_issues DESC -> (deleted) unranked           NOT CAUGHT
//
// Four of five green. The fingerprint regexes on these paths are `GROUP BY status`,
// `GROUP BY priority`, `UNNEST\(labels\)` and `JOIN members m ON m.id = i.assignee_id` — none names
// an ORDER BY — so every control above leaves the matched substring BYTE-IDENTICAL and a red can
// only be an assertion. That is deliberate: #152, #153 and #160 each measured a query-text
// fingerprint being read as coverage, and #160's was the only thing that reddened.
//
// ⚠ WHAT IS AT STAKE, MEASURED RATHER THAN ASSUMED — THIS IS PRESENTATION, NOT MEMBERSHIP, AND THE
// FILE SAYS SO. Neither statement carries a LIMIT, so no row is dropped whichever way it sorts;
// this is a WEAKER finding than #160, where the ORDER BY of two LIMITed sub-queries decided which
// rows shipped at all. What it costs is still real and is in three places: `GetDistribution`'s own
// docstring PROMISES "Returns buckets sorted by count desc"; `ExportDistributionCSV` writes the
// buckets to a downloaded file in wire order, so the row order IS the artefact; and
// `WorkloadView.tsx` renders `workload.map(...)` in wire order with a bar scaled to
// `Math.max(...open_issues)`, so an unranked read draws the busiest member wherever the plan
// happened to put them. A census of production callers (`GetWorkload`, `GetDistribution` —
// handler.go:109, handler.go:156, engine.go:789) found no LIMIT and no top-N slice anywhere.
//
// ⚠ HOW THIS FIXTURE DIFFERS, AND WHY THE OBVIOUS ACCOUNT OF IT IS WRONG. The cohorts are seeded
// count-ASCENDING, and the first draft of this comment said that was what made the clause
// falsifiable. MEASURED, IT IS TRUE OF ONLY ONE OF THE THREE — the three statements come back in
// three DIFFERENT natural orders, and only the third is the insertion order:
//
//	[R-PREMISE-DIST]      backlog, in_progress, in_review, todo   ALPHABETICAL by the group key
//	[R-PREMISE-LABEL]     ord_three, ord_two, ord_four, ord_one   a hash permutation of the key
//	[R-PREMISE-WORKLOAD]  ord-m-a, ord-m-b, ord-m-c, ord-m-d      the order the rows were inserted
//
// So what makes this fixture able to see anything is NOT one tidy property. It is that the count
// assigned to each key is non-monotone in whatever order that particular plan returns — three
// separate facts, one per statement, and no reading of the source could have told you any of them.
//
// The three `[R-PREMISE-*]` probes re-measure exactly that IN-PROCESS on every run: each reads the
// shipped statement's own shape MINUS the ORDER BY and requires the result to come back NOT already
// ranked. Without them this file could pass on a database that sorted the rows anyway and would
// look identical to a file that had caught something — the exact failure mode a storage-order
// fixture has, and the reason #158 wrote the same check for the burndown. Controls O8, O9 and O10
// fire each of the three probes INDEPENDENTLY, with the product untouched, so none of them is
// taken on trust.
//
// ⚠ THE PROBES ARE A FACT ABOUT THE PLAN AND ARE NOT AN ASSERTION ABOUT THE PRODUCT. What the
// unordered read returns is the planner's business; if it ever changes, these fail LOUDLY as a
// broken fixture rather than quietly as a passing guard. Nothing here pins a plan.

import (
	"context"
	"fmt"
	"testing"

	"github.com/talyvor/track/internal/analytics"
	"github.com/talyvor/track/internal/testutil"
)

// ordCohort is one seeded group: a status, a label and an assignee that all share a count, so one
// insertion pass serves all three reports and the three natural orders are the same order.
type ordCohort struct {
	status   string
	label    string
	memberID string
	count    int
}

// ordCohorts is ASCENDING by count, which is what disarms the WORKLOAD plan's insertion order
// (reversing it is control O8, and it reds [R-PREMISE-WORKLOAD] as a broken fixture — which is the
// shape the pre-existing distribution_counting_realpg_test.go fixture has, and why its [D-ORDER]
// cannot see a deletion). The other two statements do NOT return in insertion order, so for them
// what matters is which COUNT lands on which KEY: controls O9 and O10 re-assign the same four
// counts to the same four keys and red [R-PREMISE-DIST] and [R-PREMISE-LABEL] respectively. Change
// a status string, a label string or a count here and all three probes must be re-run — they are
// the only thing standing between this file and a green that means nothing.
var ordCohorts = []ordCohort{
	{status: "backlog", label: "ord_one", memberID: "ord-m-a", count: 1},
	{status: "todo", label: "ord_two", memberID: "ord-m-b", count: 2},
	{status: "in_review", label: "ord_three", memberID: "ord-m-c", count: 3},
	{status: "in_progress", label: "ord_four", memberID: "ord-m-d", count: 4},
}

// ordSeedMember writes a member with an EXPLICIT id. The column defaults to
// gen_random_uuid()::text, and the workload report groups on that id — so with the default the
// group keys are random per run and the plan's unordered output order is random with them. A
// premise check over random keys would red by luck roughly one run in 4!; these ids are fixed so
// the probe below measures the plan rather than the dice.
func ordSeedMember(t *testing.T, d *testutil.DB, wsID, id string) {
	t.Helper()
	if _, err := d.Pool.Exec(context.Background(),
		`INSERT INTO members (id, workspace_id, name, email) VALUES ($1, $2, $1, $1 || '@ordering.example')`,
		id, wsID); err != nil {
		t.Fatalf("seed member %s: %v", id, err)
	}
}

func ordSeedIssue(t *testing.T, d *testutil.DB, wsID, teamID string, n int, c ordCohort) {
	t.Helper()
	if _, err := d.Pool.Exec(context.Background(), `
        INSERT INTO issues (workspace_id, team_id, number, identifier, title, status, priority,
                            assignee_id, creator_id, labels, ai_cost_usd, ai_tokens,
                            created_at, updated_at)
        VALUES ($1, $2, $3::int, 'ORD-' || $3::int, 'ordering ' || $3::int, $4, 0, $5, 'ordprobe',
                $6, 0, 0, NOW() - INTERVAL '1 day', NOW())`,
		wsID, teamID, n, c.status, c.memberID, []string{c.label}); err != nil {
		t.Fatalf("seed issue %d (%s): %v", n, c.status, err)
	}
}

// ordCounts reads a two-column (key, count) result into the order the database returned it.
func ordCounts(t *testing.T, d *testutil.DB, tag, sql string, args ...any) []int {
	t.Helper()
	rows, err := d.Pool.Query(context.Background(), sql, args...)
	if err != nil {
		t.Fatalf("%s probe query: %v", tag, err)
	}
	defer rows.Close()
	var (
		out  []int
		keys []string
	)
	for rows.Next() {
		var k string
		var c int
		if err := rows.Scan(&k, &c); err != nil {
			t.Fatalf("%s probe scan: %v", tag, err)
		}
		out = append(out, c)
		keys = append(keys, fmt.Sprintf("%s=%d", k, c))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%s probe rows: %v", tag, err)
	}
	// `note:` prefix, deliberately. Go prints a t.Logf line and a t.Errorf line in the SAME shape
	// (`file.go:NN: text`), so a harness scanning output for [TAGS] scores this log as a failure of
	// the assertion it names — measured, and it made the first control run report that every
	// premise had fired when none had. The prefix is the discriminator, and
	// scripts/w34-report-ordering-controls-8f3d.py keys on it generically rather than on this
	// sentence.
	t.Logf("note: %s the UNORDERED plan returned: %v", tag, keys)
	return out
}

// ordNonIncreasing reports whether the counts are already ranked biggest-first.
func ordNonIncreasing(counts []int) bool {
	for i := 1; i < len(counts); i++ {
		if counts[i] > counts[i-1] {
			return false
		}
	}
	return true
}

func ordRequirePremise(t *testing.T, tag string, counts []int) {
	t.Helper()
	if len(counts) != len(ordCohorts) {
		t.Fatalf("%s the unordered read returned %d rows, want %d — the FIXTURE is broken, not the "+
			"product: %v", tag, len(counts), len(ordCohorts), counts)
	}
	if ordNonIncreasing(counts) {
		t.Fatalf("%s the read WITHOUT an ORDER BY came back already ranked biggest-first (%v), so "+
			"this fixture cannot tell an ordered implementation from an unordered one. This is a "+
			"BROKEN FIXTURE, NOT A PASSING GUARD — the plan's unordered output order has changed "+
			"and the assertions below are now vacuous. Re-measure with "+
			"scripts/w34-report-ordering-controls-8f3d.py before trusting any green in this file.",
			tag, counts)
	}
}

func TestReportOrdering_TheRankIsAPromise_RealPG(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()

	ws := d.Workspace(t)
	tm := d.Team(t, ws.ID)

	n := 0
	for _, c := range ordCohorts {
		ordSeedMember(t, d, ws.ID, c.memberID)
		for i := 0; i < c.count; i++ {
			n++
			ordSeedIssue(t, d, ws.ID, tm.ID, n, c)
		}
	}

	// ── [R-PREMISE-DIST] / [R-PREMISE-LABEL] / [R-PREMISE-WORKLOAD]. Each probe is the shipped
	// statement's own FROM/WHERE/GROUP BY with the ORDER BY removed and the projection narrowed to
	// (key, count) — i.e. exactly the statement each control below produces. Anything further from
	// the original could take a different plan, and then the premise would be about a query the
	// product does not run.
	ordRequirePremise(t, "[R-PREMISE-DIST]", ordCounts(t, d, "[R-PREMISE-DIST]",
		`SELECT status::text, COUNT(*)
        FROM issues
        WHERE workspace_id = $1
          AND created_at > NOW() - (INTERVAL '1 day' * $2::int)
        GROUP BY status`, ws.ID, 30))

	ordRequirePremise(t, "[R-PREMISE-LABEL]", ordCounts(t, d, "[R-PREMISE-LABEL]",
		`SELECT label, COUNT(*)
        FROM (
            SELECT UNNEST(labels) AS label
            FROM issues
            WHERE workspace_id = $1
              AND created_at > NOW() - (INTERVAL '1 day' * $2::int)
        ) t
        GROUP BY label`, ws.ID, 30))

	ordRequirePremise(t, "[R-PREMISE-WORKLOAD]", ordCounts(t, d, "[R-PREMISE-WORKLOAD]",
		`SELECT m.id,
            COUNT(*) FILTER (WHERE i.status NOT IN ('done','cancelled')) AS open_issues
        FROM issues i
        JOIN members m ON m.id = i.assignee_id
        WHERE i.workspace_id = $1
        GROUP BY m.id, m.name, m.avatar_url`, ws.ID))

	e := analytics.New(d.Pool)

	// ── [R-DIST-ORDER]. The docstring's promise: "Returns buckets sorted by count desc". The
	// assertion is NON-INCREASING rather than an exact sequence, because that is the whole of what
	// `ORDER BY COUNT(*) DESC` guarantees — ties are free to land either way and pinning them would
	// red on a plan change that broke no promise.
	byStatus, err := e.GetDistribution(ctx, ws.ID, "status", 30)
	if err != nil {
		t.Fatalf("GetDistribution(status): %v", err)
	}
	if len(byStatus) != len(ordCohorts) {
		t.Fatalf("[R-DIST-ORDER] got %d status buckets, want %d: %+v", len(byStatus), len(ordCohorts), byStatus)
	}
	for i := 1; i < len(byStatus); i++ {
		if byStatus[i].Count > byStatus[i-1].Count {
			t.Errorf("[R-DIST-ORDER] bucket %d (%q, %d) outranks bucket %d (%q, %d) — the report is "+
				"documented as sorted by count descending, and ExportDistributionCSV writes these rows "+
				"to a downloaded file in exactly this order: %+v",
				i, byStatus[i].Label, byStatus[i].Count, i-1, byStatus[i-1].Label, byStatus[i-1].Count, byStatus)
			break
		}
	}

	// ── [R-LABEL-ORDER]. The UNNEST path is a SEPARATE statement with its own ORDER BY, which is
	// why it is a separate assertion: a fix applied to one would leave the other silently unranked.
	byLabel, err := e.GetDistribution(ctx, ws.ID, "label", 30)
	if err != nil {
		t.Fatalf("GetDistribution(label): %v", err)
	}
	if len(byLabel) != len(ordCohorts) {
		t.Fatalf("[R-LABEL-ORDER] got %d label buckets, want %d: %+v", len(byLabel), len(ordCohorts), byLabel)
	}
	for i := 1; i < len(byLabel); i++ {
		if byLabel[i].Count > byLabel[i-1].Count {
			t.Errorf("[R-LABEL-ORDER] bucket %d (%q, %d) outranks bucket %d (%q, %d) — the label path "+
				"carries its own ORDER BY and it is not ranking: %+v",
				i, byLabel[i].Label, byLabel[i].Count, i-1, byLabel[i-1].Label, byLabel[i-1].Count, byLabel)
			break
		}
	}

	// ── [R-WORKLOAD-ORDER]. WorkloadView.tsx renders these in wire order, so this is the assertion
	// that the busiest member is drawn first.
	workload, err := e.GetWorkload(ctx, ws.ID, "")
	if err != nil {
		t.Fatalf("GetWorkload: %v", err)
	}
	if len(workload) != len(ordCohorts) {
		t.Fatalf("[R-WORKLOAD-ORDER] got %d members, want %d: %+v", len(workload), len(ordCohorts), workload)
	}
	for i := 1; i < len(workload); i++ {
		if workload[i].OpenIssues > workload[i-1].OpenIssues {
			t.Errorf("[R-WORKLOAD-ORDER] member %d (%q, %d open) outranks member %d (%q, %d open) — "+
				"WorkloadView.tsx renders this slice in wire order, so an unranked read draws the "+
				"busiest member wherever the plan put them: %+v",
				i, workload[i].Name, workload[i].OpenIssues, i-1, workload[i-1].Name, workload[i-1].OpenIssues, workload)
			break
		}
	}
}
