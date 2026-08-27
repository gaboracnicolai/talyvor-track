package issue_test

// value_domain_realpg_test.go — THE ISSUE WRITE PATH VALIDATES THE SHAPE OF A REQUEST AND NOT THE
// DOMAIN OF ITS VALUES.
//
// `updatableFields` is an allowlist of COLUMN NAMES and its own comment says what it is for:
// "protects against SQL injection via map keys". It is not, and does not claim to be, a check on
// what those keys are set TO. Nothing else between the request body and the column looks either.
//
// ⚠ MEASURED THROUGH THE SHIPPED STORE ON REAL POSTGRES, NOT READ. Before this file:
//
//	Create{Priority: 99}            -> 200, column holds 99
//	Create{Priority: -7}            -> 200, column holds -7
//	Update{"priority": 99}          -> 200, column holds 99
//	Update{"title": ""}             -> 200, column holds ""
//
// `model.IssuePriority` declares EXACTLY five values (None 0, Urgent 1, High 2, Medium 3, Low 4),
// `issues.priority` is `INTEGER NOT NULL DEFAULT 0` with no CHECK, and the queue records the
// downstream half: the suite draws a BLANK priority control for an out-of-domain value, and
// pinned that rather than repairing it, because the near fix (`priorityLabel(...) ?? 'None'`)
// answers 99 with "None" — a blank says "I do not know", "None" lies. The honest repair is
// upstream refusal, and it is this repository's.
//
// ⚠ AND THE TITLE HALF IS AN ASYMMETRIC GATE, WHICH IS ITS OWN CLASS. `Create` REFUSES an empty
// title in as many words — "WorkspaceID, TeamID, Title, and CreatorID are required" — and `Update`
// stored one. An issue could not be born without a title and any caller could take its title away,
// leaving a row that renders as a blank line in every list. The rule enforced below is CREATE'S
// OWN, character for character (`title == ""`, no trimming), because inventing a stricter
// whitespace rule here would be a product decision rather than making two doors agree.
//
// ⚠⚠ WHAT THIS FILE DELIBERATELY DOES NOT DO, STATED SO NOBODY READS IT AS AN OVERSIGHT: it does
// not restrict `status`, and V9 below asserts an arbitrary status is STILL accepted and stored
// unchanged. `internal/workflow` ships a per-team status pipeline whose package comment says "any
// team can add custom ones", and metrics_label_bound_realpg_test.go already recorded that
// narrowing the column is an OPEN PRODUCT QUESTION and not a session's. V9 is a scope control: it
// fails if some later tidy-up quietly forecloses that feature while "finishing the validation".
//
// ⚠ WHY IT IS SAFE TO REFUSE AN OUT-OF-DOMAIN PRIORITY, MEASURED RATHER THAN ASSUMED. Every
// production path that produces one produces 0..4: the CSV/Jira/Linear importers map through
// mapLinearPriority / mapJiraPriority, which return a model constant and fall back to
// PriorityNone; the template library's DefaultPriority values are 1..4; the AI engine and the
// dependency store read the column back. The only way an out-of-domain value enters is a caller
// sending one. Existing rows are untouched — this gates WRITES, not reads.

import (
	"context"
	"fmt"
	"testing"

	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/model"
	tt "github.com/talyvor/track/internal/testutil"
)

// outOfDomainPriorities are the two shapes a caller reaches the column with that are not
// priorities: a number past the top of the enum, and a negative.
var outOfDomainPriorities = []int{99, -7, 5, -1}

func vdSetup(t *testing.T) (*tt.DB, context.Context, string, string, *issue.Store) {
	t.Helper()
	d := tt.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	return d, ctx, ws.ID, team.ID, issue.NewStore(d.Pool)
}

func vdCreate(t *testing.T, s *issue.Store, ctx context.Context, wsID, teamID, title string) *model.Issue {
	t.Helper()
	out, err := s.Create(ctx, model.Issue{
		WorkspaceID: wsID, TeamID: teamID, Title: title, CreatorID: "u1",
		Status: model.StatusBacklog, Priority: model.PriorityMedium,
	})
	if err != nil {
		t.Fatalf("premise: a well-formed create was refused: %v", err)
	}
	return out
}

// V1 — Create must refuse a priority the enum does not have.
func TestValueDomain_CreateRefusesOutOfDomainPriority_RealPG(t *testing.T) {
	d, ctx, wsID, teamID, s := vdSetup(t)
	for _, p := range outOfDomainPriorities {
		out, err := s.Create(ctx, model.Issue{
			WorkspaceID: wsID, TeamID: teamID, Title: fmt.Sprintf("p%d", p), CreatorID: "u1",
			Status: model.StatusBacklog, Priority: model.IssuePriority(p),
		})
		if err == nil {
			var stored int
			_ = d.Pool.QueryRow(ctx, `SELECT priority FROM issues WHERE id=$1`, out.ID).Scan(&stored)
			t.Errorf("Create stored priority=%d; model.IssuePriority declares exactly 0..4 and the "+
				"column has no CHECK, so this value renders as a blank control in the product", stored)
		}
	}
}

// V2 — Update must refuse the same, through the other door.
func TestValueDomain_UpdateRefusesOutOfDomainPriority_RealPG(t *testing.T) {
	d, ctx, wsID, teamID, s := vdSetup(t)
	for _, p := range outOfDomainPriorities {
		iss := vdCreate(t, s, ctx, wsID, teamID, fmt.Sprintf("u%d", p))
		if _, err := s.Update(ctx, iss.ID, wsID, map[string]any{"priority": p}); err == nil {
			var stored int
			_ = d.Pool.QueryRow(ctx, `SELECT priority FROM issues WHERE id=$1`, iss.ID).Scan(&stored)
			t.Errorf("Update stored priority=%d — updatableFields allowlists the KEY and nothing "+
				"looks at the VALUE", stored)
		}
	}
}

// V3 — UpsertByIdentifier is the third door and the one the importers use. A door that is not
// gated is the whole gate: this repository's own clockguard records five separate occasions when a
// rule was enforced one site at a time.
func TestValueDomain_UpsertRefusesOutOfDomainPriority_RealPG(t *testing.T) {
	_, ctx, wsID, teamID, s := vdSetup(t)
	_, _, err := s.UpsertByIdentifier(ctx, model.Issue{
		WorkspaceID: wsID, TeamID: teamID, Identifier: "EXT-1", Title: "imported",
		CreatorID: "u1", Status: model.StatusBacklog, Priority: model.IssuePriority(99),
	})
	if err == nil {
		t.Errorf("UpsertByIdentifier accepted priority=99 — the importer path is a door too")
	}
}

// V4 — THE ASYMMETRIC GATE. Create already refuses an empty title; Update stored one.
func TestValueDomain_UpdateRefusesEmptyTitle_RealPG(t *testing.T) {
	d, ctx, wsID, teamID, s := vdSetup(t)
	iss := vdCreate(t, s, ctx, wsID, teamID, "has a title")
	if _, err := s.Update(ctx, iss.ID, wsID, map[string]any{"title": ""}); err == nil {
		var stored string
		_ = d.Pool.QueryRow(ctx, `SELECT title FROM issues WHERE id=$1`, iss.ID).Scan(&stored)
		t.Errorf("Update stored title=%q. Create refuses exactly this value in as many words, so "+
			"an issue could not be born without a title and any caller could take its title away",
			stored)
	}
}

// V5 — POSITIVE CONTROL, THE OTHER DIRECTION. Every declared priority still works, on every door.
// Without this, "refuse every priority" satisfies V1-V3.
func TestValueDomain_EveryDeclaredPriorityStillWrites_RealPG(t *testing.T) {
	d, ctx, wsID, teamID, s := vdSetup(t)
	declared := []model.IssuePriority{
		model.PriorityNone, model.PriorityUrgent, model.PriorityHigh,
		model.PriorityMedium, model.PriorityLow,
	}
	for _, p := range declared {
		out, err := s.Create(ctx, model.Issue{
			WorkspaceID: wsID, TeamID: teamID, Title: fmt.Sprintf("ok%d", p), CreatorID: "u1",
			Status: model.StatusBacklog, Priority: p,
		})
		if err != nil {
			t.Fatalf("Create refused a DECLARED priority %d: %v — the gate is too tight", p, err)
		}
		var stored int
		if err := d.Pool.QueryRow(ctx, `SELECT priority FROM issues WHERE id=$1`, out.ID).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if stored != int(p) {
			t.Errorf("Create stored priority=%d, want %d", stored, p)
		}
		if _, err := s.Update(ctx, out.ID, wsID, map[string]any{"priority": int(p)}); err != nil {
			t.Errorf("Update refused a DECLARED priority %d: %v", p, err)
		}
		if _, _, err := s.UpsertByIdentifier(ctx, model.Issue{
			WorkspaceID: wsID, TeamID: teamID, Identifier: fmt.Sprintf("EXT-%d", p),
			Title: "imported", CreatorID: "u1", Status: model.StatusBacklog, Priority: p,
		}); err != nil {
			t.Errorf("UpsertByIdentifier refused a DECLARED priority %d: %v", p, err)
		}
	}
}

// V6 — POSITIVE CONTROL. A non-empty title still updates, so V4 is not satisfied by refusing every
// title.
func TestValueDomain_ANonEmptyTitleStillUpdates_RealPG(t *testing.T) {
	d, ctx, wsID, teamID, s := vdSetup(t)
	iss := vdCreate(t, s, ctx, wsID, teamID, "before")
	if _, err := s.Update(ctx, iss.ID, wsID, map[string]any{"title": "after"}); err != nil {
		t.Fatalf("Update refused a real title: %v", err)
	}
	var stored string
	if err := d.Pool.QueryRow(ctx, `SELECT title FROM issues WHERE id=$1`, iss.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "after" {
		t.Errorf("stored title=%q, want %q", stored, "after")
	}
}

// V7 — PREMISE CONTROL. Create's rule is the one V4 mirrors; if Create stops refusing an empty
// title, V4 is enforcing a rule this repository no longer holds and must be revisited, not kept.
func TestValueDomain_CreateStillRefusesAnEmptyTitle_RealPG(t *testing.T) {
	_, ctx, wsID, teamID, s := vdSetup(t)
	if _, err := s.Create(ctx, model.Issue{
		WorkspaceID: wsID, TeamID: teamID, Title: "", CreatorID: "u1", Status: model.StatusBacklog,
	}); err == nil {
		t.Errorf("Create accepted an empty title — the asymmetry V4 closes has been closed from " +
			"the wrong end; decide which rule this product wants before deleting either test")
	}
}

// V8 — SCOPE CONTROL, AND THE MOST IMPORTANT TEST IN THIS FILE. An arbitrary STATUS is still
// accepted and stored VERBATIM. internal/workflow ships per-team custom statuses; narrowing this
// column is an open product question that metrics_label_bound_realpg_test.go already declined to
// settle, and it must not be settled by accident while somebody is "finishing the validation".
func TestValueDomain_AnArbitraryStatusIsStillAcceptedVerbatim_RealPG(t *testing.T) {
	d, ctx, wsID, teamID, s := vdSetup(t)
	const custom = "Deployed to prod 🚀"
	out, err := s.Create(ctx, model.Issue{
		WorkspaceID: wsID, TeamID: teamID, Title: "custom status", CreatorID: "u1",
		Status: model.IssueStatus(custom),
	})
	if err != nil {
		t.Fatalf("a custom status was REFUSED at Create: %v — internal/workflow's per-team status "+
			"pipeline says any team can add custom ones. If this is now intended, it is a product "+
			"decision and belongs in the queue, not in a validation patch", err)
	}
	var stored string
	if err := d.Pool.QueryRow(ctx, `SELECT status FROM issues WHERE id=$1`, out.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != custom {
		t.Errorf("stored status=%q, want the caller's value verbatim %q", stored, custom)
	}
	iss := vdCreate(t, s, ctx, wsID, teamID, "another")
	if _, err := s.Update(ctx, iss.ID, wsID, map[string]any{"status": custom}); err != nil {
		t.Errorf("a custom status was REFUSED at Update: %v", err)
	}
}
