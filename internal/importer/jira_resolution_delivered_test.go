package importer

import (
	"encoding/json"
	"testing"

	"github.com/talyvor/track/internal/model"
)

// jira_resolution_delivered_test.go — the resolution word that says DELIVERED, on both Jira
// transports.
//
// ⚠ WHAT THIS IS NOT. It is NOT the decision #82 deferred. That decision asks whether an
// abandoned-SHAPED resolution ("Duplicate", "Rejected", "Cannot Reproduce", …) should move a row
// from done to cancelled — a change to what analytics counts as delivered work, with numbers
// attached, which stays exactly where #82 left it and is re-measured in the queue rather than made
// here. "Fixed" is not in that question at all: it means the work WAS delivered, the row is already
// `done`, and there is nothing for it to change. It sits in the refused-and-reported class only
// because the classifier is the STATUS table, which has never heard of the word.
//
// ⚠ THE CHANGE MOVES NO DATA AND THAT IS THE PROPERTY THAT MAKES IT A SESSION CALL. Both arms of
// applyJiraResolution's switch that this word can reach return the SAME status: `case StatusDone`
// returns (status, nil) and `default` returns (status, note), and the guard above them has already
// established status == done. So the only observable difference is whether a warning line exists.
// TestFixedResolutionMovesNoData below asserts that as its own case, so a future edit cannot quietly
// turn a report change into a reclassification.

// ── the measurement this file exists for ─────────────────────────────────────────────────────────
//
// MEASURED 2026-08-10 on a REAL Jira CLOUD site through the endpoint jiraSearchPath names
// (hibernate.atlassian.net, anonymous, POST /rest/api/3/search/jql and
// /rest/api/3/search/approximate-count). Whole-population counts, not a sample, and the vocabulary
// is CLOSED — the nine names below sum to 29,512, which is exactly the instance's resolved
// population, so nothing is hiding in an "other" bucket:
//
//	resolution IS NOT EMPTY (= statusCategory Done)          29,512
//	  Fixed                        19,698   66.7%   reported as unreadable   ← this file
//	  Rejected                      3,721   12.6%   reported as unreadable   ← #82's open decision
//	  Out of Date                   2,619    8.9%   reported as unreadable   ← #82's open decision
//	  Duplicate                     1,544    5.2%   reported as unreadable   ← #82's open decision
//	  Won't Fix                       951    3.2%   acted on → cancelled
//	  Cannot Reproduce                518    1.8%   reported as unreadable   ← #82's open decision
//	  Incomplete                      259    0.9%   reported as unreadable   ← #82's open decision
//	  Won't Do                        149    0.5%   acted on → cancelled
//	  Done                             53    0.18%  silent
//
// So the single loudest line in the import report — two thirds of all resolved work on a real
// instance — told the operator Track could not tell whether that work was finished, about the one
// word that says it was. The word Track's table DID know covers 0.18%.
//
// ⚠ THE CLASSIFICATION IS THE PROVIDER'S OWN, READ AT THE WIRE, NOT THIS SESSION'S OPINION. The
// `resolution` object the mapper already decodes carries a `description`, and on that instance it
// reads:
//
//	Fixed              "A fix for this issue is checked into the tree and tested."
//	Done               "Work has been completed on this issue."
//	Rejected           "The bug report describes expected behavior, or a feature will not be implemented"
//	Duplicate          "The problem is a duplicate of an existing issue."
//	Incomplete         "The problem is not completely described."
//	Cannot Reproduce   "All attempts at reproducing this issue failed …"
//	Out of Date        "The issue is either fixed by another issue or is in some other way no longer relevant"
//
// Fixed and Done are one class in the provider's own words; the rest are not, which is why exactly
// one word is added and the other five stay reported.
//
// ⚠ THE NUMBER WAS ALREADY WRITTEN DOWN IN THIS PACKAGE AND READ AS SOMETHING ELSE.
// TestPinned_TheMeasuredResolutionVocabularyStillClassifiesAsShipped has carried
// `"Fixed": {StatusDone, viaResolutionUnreadable, 13411}` since #82 — the largest row in its own
// table — under the heading "the decision this merge deliberately did not make". Two different
// questions were filed under one class. That table is updated by this merge and the count is left
// as #82 measured it, because it is a fact about a different instance.

// TestFixedIsDeliveredWorkAndIsNotReported is the RED case. Before this merge applyJiraResolution
// classifies "Fixed" through mapJiraStatus, which does not know the word, so it falls to
// StatusBacklog and takes the "cannot read it" arm.
func TestFixedIsDeliveredWorkAndIsNotReported(t *testing.T) {
	for _, raw := range []string{"Fixed", "fixed", "FIXED", "  Fixed  "} {
		gotStatus, notes := applyJiraResolution(raw, model.StatusDone)
		if gotStatus != model.StatusDone {
			t.Errorf("applyJiraResolution(%q, done): status = %q, want %q — the word means the work "+
				"was delivered and the row is already done; nothing may move", raw, gotStatus, model.StatusDone)
		}
		if len(notes) != 0 {
			t.Errorf("applyJiraResolution(%q, done): reported %#v, want SILENCE — on the measured "+
				"instance this line covered 19,698 of 29,512 resolved issues (66.7%%) and told the "+
				"operator Track could not tell whether delivered work was delivered", raw, notes)
		}
	}
}

// TestFixedResolutionMovesNoData is the invariant that keeps this a REPORT change. It is stated
// separately from the case above because the two can fail apart: silencing the note is what this
// merge does, and moving the status is what it must never do. Written so that a future edit which
// promotes "Fixed" into the cancelled arm — or which lets it escape the not-done guard — reds here
// rather than on a warning string.
func TestFixedResolutionMovesNoData(t *testing.T) {
	// Every status a row can arrive at the rule with. For anything but done the guard returns
	// early and the word is irrelevant; for done the word must leave it done.
	for _, in := range []model.IssueStatus{
		model.StatusBacklog, model.StatusTodo, model.StatusInProgress,
		model.StatusInReview, model.StatusDone, model.StatusCancelled,
	} {
		got, notes := applyJiraResolution("Fixed", in)
		if got != in {
			t.Errorf(`applyJiraResolution("Fixed", %q) = %q — this merge changes the REPORT and `+
				`must change no row's status`, in, got)
		}
		if len(notes) != 0 {
			t.Errorf(`applyJiraResolution("Fixed", %q): reported %#v, want silence`, in, notes)
		}
	}
}

// TestTheDeferredDecisionIsStillDeferred is the floor. Silencing one word must not silence the
// class it was wrongly filed under: those lines are the only thing telling an operator that 8,661
// issues (29.3% of the measured instance's resolved work) were imported as DELIVERED without Track
// being able to check. A fix that widened to "any resolution ⇒ delivered" would pass the two cases
// above and empty the report.
func TestTheDeferredDecisionIsStillDeferred(t *testing.T) {
	for raw, want := range map[string]struct {
		status model.IssueStatus
		via    string
	}{
		// measured on the real Cloud instance; still refused, still reported
		"Rejected":         {model.StatusDone, viaResolutionUnreadable},
		"Out of Date":      {model.StatusDone, viaResolutionUnreadable},
		"Duplicate":        {model.StatusDone, viaResolutionUnreadable},
		"Cannot Reproduce": {model.StatusDone, viaResolutionUnreadable},
		"Incomplete":       {model.StatusDone, viaResolutionUnreadable},
		// and the two Track's own vocabulary does act on stay acted on
		"Won't Fix": {model.StatusCancelled, viaResolutionCancelled},
		"Won't Do":  {model.StatusCancelled, viaResolutionCancelled},
	} {
		got, notes := applyJiraResolution(raw, model.StatusDone)
		if got != want.status {
			t.Errorf("resolution %q: status = %q, want %q — unchanged by this merge", raw, got, want.status)
		}
		if len(notes) != 1 || notes[0].Via != want.via {
			t.Errorf("resolution %q: notes = %#v, want exactly one via %q — unchanged by this merge",
				raw, notes, want.via)
		}
	}
	// "Done" was already silent and must stay silent, so the table above cannot be satisfied by a
	// classifier that reports everything.
	if _, notes := applyJiraResolution("Done", model.StatusDone); len(notes) != 0 {
		t.Errorf(`resolution "Done": reported %#v, want silence`, notes)
	}
}

// TestJiraAPITransportAlsoStopsReportingFixed — THE SECOND COPY OF THE SEAM. applyJiraResolution has
// two call sites: jiraRowMapper (csv.go) and jiraAPIResolution (api_resolution.go). They share the
// rule by CALL rather than by copy, so the fix reaches both — but "reaches both" is a claim about
// this package's wiring, and this package has been wrong about exactly that five times (#74, #78,
// #83, #84, #85 were all one seam found again at the next copy). It is asserted through the API
// entry point, on the bytes Jira actually sends, rather than assumed from the call graph.
func TestJiraAPITransportAlsoStopsReportingFixed(t *testing.T) {
	// The shape measured on the wire: a resolution object with a name and the provider's own
	// description beside it.
	raw := json.RawMessage(`{"self":"https://x/rest/api/3/resolution/1","id":"1",` +
		`"description":"A fix for this issue is checked into the tree and tested.","name":"Fixed"}`)

	got, notes := jiraAPIResolution(raw, model.StatusDone)
	if got != model.StatusDone {
		t.Errorf("jiraAPIResolution(Fixed, done): status = %q, want %q", got, model.StatusDone)
	}
	if len(notes) != 0 {
		t.Errorf("jiraAPIResolution(Fixed, done): reported %#v, want silence", notes)
	}

	// And the API transport's own two states stay exactly as #87 left them: an ABSENT key is still
	// the structural zero, a NULL is still an unresolved issue. Silencing a word must not silence
	// either of those — they are what tells an operator the `fields` list stopped working.
	if _, n := jiraAPIResolution(nil, model.StatusDone); len(n) != 1 || n[0].Via != viaNoResolutionField {
		t.Errorf("absent resolution key: notes = %#v, want exactly one via %q", n, viaNoResolutionField)
	}
	if s, n := jiraAPIResolution(json.RawMessage(`null`), model.StatusDone); s != model.StatusDone || len(n) != 0 {
		t.Errorf("null resolution: (%q, %#v), want (done, silence)", s, n)
	}
}
