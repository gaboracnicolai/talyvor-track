package issue_test

// metrics_label_bound_realpg_test.go — THE ONE LABEL ON THESE COLLECTORS THAT A CALLER CHOOSES.
//
// internal/metrics/metrics.go opens with the rule this file enforces:
//
//	"Keep label cardinality bounded — workspace ID is fine (one workspace = one tenant), but
//	 never use issue ID or arbitrary user-supplied values."
//
// `workspace` and `team` are server-generated ids and are bounded by the tenant count, exactly as
// that sentence says. `status` is the third label on both issue collectors and it is NOT bounded:
// `issues.status` is `TEXT NOT NULL DEFAULT 'backlog'` with NO CHECK constraint (measured against
// the live schema), `status` is a member of `updatableFields`, and nothing between the request body
// and the column compares the value to model.IssueStatus's six constants.
//
// ⚠ MEASURED THROUGH THE SHIPPED STORE ON REAL POSTGRES, NOT READ: `Create` with
// Status: "Deployed to prod 🚀" is ACCEPTED, and produces the series
// `track_issues_created_total{status="Deployed to prod 🚀"}`. `Update` with
// `{"status": "'; DROP TABLE issues; --"}` is ACCEPTED and stores it. Both doors, both counters.
//
// ⚠⚠ WHY THAT IS A DEFECT AND NOT AN UNTIDY LABEL. `/metrics` is mounted at the TOP LEVEL of
// cmd/track/main.go's router — `r.Handle("/metrics", metrics.Handler())` sits above every
// `r.Group`/`r.Route` that installs gatewayauth + authz, and the only top-level `r.Use` calls are
// RequestID, Recoverer, Timeout, metricsMiddleware and BodyLimit. So the endpoint is
// UNAUTHENTICATED, and this label lets any authenticated workspace member mint an unbounded number
// of Prometheus time-series carrying text they chose, readable by anyone who can reach it. The
// cardinality half is what metrics.go's rule is about; the publication half is worse and the rule
// does not mention it.
//
// ⚠ WHAT THIS FILE DOES NOT DECIDE, STATED SO NOBODY READS IT AS SETTLED. It does not restrict
// what may be WRITTEN to issues.status, and the tests below assert the raw value still reaches the
// column. Whether an arbitrary status is legal is an OPEN PRODUCT QUESTION and not a session's:
// internal/workflow ships a per-team status pipeline whose package comment says "any team can add
// custom ones", so narrowing the column would foreclose a feature this repository already has code
// for. The rule being enforced here is the one metrics.go already wrote down, and its scope is the
// LABEL.
//
// The bucket is model.StatusLabelOther. It preserves the total — an unknown status still counts as
// a create or an update, which is what these counters are for — while collapsing every unknown
// spelling onto one series. Dropping the increment instead would UNDERCOUNT, and undercounting is
// the exact defect countCreated and countUpdatedLabels were each written to end.

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/metrics"
	"github.com/talyvor/track/internal/model"
	tt "github.com/talyvor/track/internal/testutil"
)

// The two spellings a caller can reach the column with that are not statuses. The first is what a
// well-meaning client sends when it mistakes the workflow_statuses display name for the column's
// vocabulary; the second is what an unfriendly one sends when it has noticed the endpoint is
// unauthenticated.
const (
	unknownStatusInnocent = "Deployed to prod 🚀"
	unknownStatusHostile  = "'; DROP TABLE issues; --"
)

func createdCountRaw(ws, team, status string) float64 {
	return testutil.ToFloat64(metrics.IssuesCreated.WithLabelValues(ws, team, status))
}

func updatedCountRaw(ws, team, status string) float64 {
	return testutil.ToFloat64(metrics.IssuesUpdated.WithLabelValues(ws, team, status))
}

func TestIssueMetrics_ACreateWithAnUnknownStatus_MintsNoLabelSeries_RealPG(t *testing.T) {
	d := tt.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	s := issue.NewStore(d.Pool)

	beforeRaw := createdCountRaw(ws.ID, team.ID, unknownStatusInnocent)
	beforeOther := createdCountRaw(ws.ID, team.ID, model.StatusLabelOther)

	out, err := s.Create(ctx, model.Issue{
		WorkspaceID: ws.ID, TeamID: team.ID, Title: "unknown status", CreatorID: "u1",
		Status: model.IssueStatus(unknownStatusInnocent),
	})
	if err != nil {
		t.Fatalf("PREMISE FAILED: the store refused an unknown status (%v) — this file measures the "+
			"LABEL of a write that is currently accepted; if the write is now refused, the finding "+
			"has changed shape and this test must be rewritten, not deleted", err)
	}

	// The write is unchanged: the column still holds exactly what the caller sent. This assertion
	// is what makes the fix provably label-only.
	if string(out.Status) != unknownStatusInnocent {
		t.Errorf("stored status = %q, want %q — the bound belongs on the metric label, not on the "+
			"column; narrowing the column is an open product question (see the header)",
			out.Status, unknownStatusInnocent)
	}

	if got := createdCountRaw(ws.ID, team.ID, unknownStatusInnocent) - beforeRaw; got != 0 {
		t.Errorf("track_issues_created_total{status=%q} moved by %v, want 0 — a caller-chosen string "+
			"became a Prometheus label on an UNAUTHENTICATED /metrics endpoint. metrics.go's own "+
			"header forbids exactly this: \"never use issue ID or arbitrary user-supplied values\"",
			unknownStatusInnocent, got)
	}
	if got := createdCountRaw(ws.ID, team.ID, model.StatusLabelOther) - beforeOther; got != 1 {
		t.Errorf("track_issues_created_total{status=%q} moved by %v, want 1 — the total must be "+
			"preserved. Dropping the increment for an unknown status would UNDERCOUNT, which is the "+
			"defect countCreated exists to end", model.StatusLabelOther, got)
	}
}

func TestIssueMetrics_AnUpdateToAnUnknownStatus_MintsNoLabelSeries_RealPG(t *testing.T) {
	d := tt.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	s := issue.NewStore(d.Pool)

	iss, err := s.Create(ctx, model.Issue{
		WorkspaceID: ws.ID, TeamID: team.ID, Title: "to be moved", CreatorID: "u1",
		Status: model.StatusTodo,
	})
	if err != nil {
		t.Fatalf("PREMISE FAILED: seed: %v", err)
	}

	beforeRaw := updatedCountRaw(ws.ID, team.ID, unknownStatusHostile)
	beforeOther := updatedCountRaw(ws.ID, team.ID, model.StatusLabelOther)

	out, err := s.Update(ctx, iss.ID, ws.ID, map[string]any{"status": unknownStatusHostile})
	if err != nil {
		t.Fatalf("PREMISE FAILED: the store refused an unknown status on update (%v) — see the note "+
			"on the create test above", err)
	}
	if string(out.Status) != unknownStatusHostile {
		t.Errorf("stored status = %q, want %q — the bound belongs on the label, not the column",
			out.Status, unknownStatusHostile)
	}

	if got := updatedCountRaw(ws.ID, team.ID, unknownStatusHostile) - beforeRaw; got != 0 {
		t.Errorf("track_issues_updated_total{status=%q} moved by %v, want 0 — same rule, the other "+
			"collector. countUpdatedLabels is the single increment site for FIFTEEN production update "+
			"paths, so an unbounded label here is unbounded on every one of them",
			unknownStatusHostile, got)
	}
	if got := updatedCountRaw(ws.ID, team.ID, model.StatusLabelOther) - beforeOther; got != 1 {
		t.Errorf("track_issues_updated_total{status=%q} moved by %v, want 1", model.StatusLabelOther, got)
	}
}

// MUST STAY GREEN, AND IT IS HALF THE POINT. A bound that collapsed everything onto one series
// would satisfy both tests above and destroy the metric. Each of the six real statuses must still
// get its OWN series, spelled exactly as the column spells it, and must not touch the bucket.
func TestIssueMetrics_TheSixKnownStatusesKeepTheirOwnSeries_RealPG(t *testing.T) {
	d := tt.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	s := issue.NewStore(d.Pool)

	known := []model.IssueStatus{
		model.StatusBacklog, model.StatusTodo, model.StatusInProgress,
		model.StatusInReview, model.StatusDone, model.StatusCancelled,
	}
	if len(known) != len(model.IssueStatuses()) {
		t.Fatalf("this test enumerates %d statuses and model.IssueStatuses() has %d — a status was "+
			"added or removed and the case for it is missing here", len(known), len(model.IssueStatuses()))
	}

	for _, st := range known {
		beforeOwn := createdCountRaw(ws.ID, team.ID, string(st))
		beforeOther := createdCountRaw(ws.ID, team.ID, model.StatusLabelOther)

		if _, err := s.Create(ctx, model.Issue{
			WorkspaceID: ws.ID, TeamID: team.ID, Title: "known " + string(st), CreatorID: "u1",
			Status: st,
		}); err != nil {
			t.Fatalf("create %s: %v", st, err)
		}

		if got := createdCountRaw(ws.ID, team.ID, string(st)) - beforeOwn; got != 1 {
			t.Errorf("track_issues_created_total{status=%q} moved by %v, want 1 — a real status lost "+
				"its own series to the bound", st, got)
		}
		if got := createdCountRaw(ws.ID, team.ID, model.StatusLabelOther) - beforeOther; got != 0 {
			t.Errorf("%q leaked into the %q bucket (moved by %v) — the bound is too wide and the "+
				"metric no longer distinguishes the statuses it exists to distinguish",
				st, model.StatusLabelOther, got)
		}
	}
}

// THE PREMISE OF THE TWO UNKNOWN-STATUS TESTS, ASSERTED RATHER THAN ASSUMED — AND A CONTROL IS WHAT
// SAID IT WAS MISSING. Control C8a fed unknownStatusInnocent = "done" to the create test with the
// bound INTACT and it went red: the raw series legitimately moves for a real status, so the
// assertion "the raw series must not move" is only meaningful while the input is outside the closed
// set. The failure direction is safe — a wrong input reds rather than passing quietly — but nothing
// said WHY it reds, and a reader repairing that red would have had no way to know the constant was
// load-bearing. Now it is written down.
func TestIssueMetrics_TheUnknownInputsAreActuallyOutsideTheClosedSet(t *testing.T) {
	for _, in := range []string{unknownStatusInnocent, unknownStatusHostile} {
		for _, st := range model.IssueStatuses() {
			if string(st) == in {
				t.Fatalf("the test input %q IS one of the six statuses — the assertions above compare "+
					"the raw series against 0, which a real status moves legitimately. Pick an input "+
					"outside model.IssueStatuses(); do not relax the assertion", in)
			}
		}
		if model.BoundStatusLabel(in) != model.StatusLabelOther {
			t.Fatalf("BoundStatusLabel(%q) = %q, want %q — the input is not being bucketed, so the "+
				"tests above are measuring something other than the bound", in,
				model.BoundStatusLabel(in), model.StatusLabelOther)
		}
	}
}

// The bucket must not be a real status, or an unknown spelling would be indistinguishable from a
// genuine one and the two tests above would be measuring the same series.
func TestIssueMetrics_TheBucketIsNotItselfAStatus(t *testing.T) {
	for _, st := range model.IssueStatuses() {
		if string(st) == model.StatusLabelOther {
			t.Fatalf("model.StatusLabelOther is %q, which is also a real status — an unknown status "+
				"would then be counted as that one", model.StatusLabelOther)
		}
	}
}
