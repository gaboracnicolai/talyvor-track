package importer

import (
	"fmt"
	"testing"

	"github.com/talyvor/track/internal/model"
)

// api_resolution_test.go — whether closed work was FINISHED or ABANDONED, on the JIRA API TRANSPORT.
//
// ⚠⚠ THE FINDING IS THAT #82 FIXED THIS ON ONE TRANSPORT AND ITS OWN HEADER ENUMERATED WHICH PATHS
// IT LEFT ALONE — AND THE JIRA API TRANSPORT IS NOT IN THAT LIST. jira_csv_resolution.go names the
// LINEAR CSV path explicitly ("deliberately untouched … nothing in this environment can fetch a
// Linear CSV export") and says nothing whatsoever about jira_api, which is the transport a customer
// with credentials actually runs. The rule — then named `applyJiraCSVResolution` — had exactly ONE
// call site, csv.go:496.
// mapJiraIssues never asks the question, and `jiraFields` never asks for the field. This is #84's
// shape one field over: a fix landing on the transport where the defect was FOUND, and the sibling
// transport inheriting neither the fix nor the evidence.
//
// MEASURED 2026-08-10 ON THE SHIPPED ENDPOINT — POST /rest/api/3/search/jql on a real Jira CLOUD
// site (hibernate.atlassian.net, project HHH, anonymous), which is the endpoint jiraSearchPath names
// and the host #84 found to answer it without credentials. Re-run it with
// scripts/w34-jira-api-resolution-probe.py, which FAILS rather than reports if a control answers.
//
//	project HHH, issues                                          20,550
//	  ... resolved (statusCategory = Done) — every one of these
//	      imports as Track `done` today                          18,267
//	  ... whose resolution Track's OWN vocabulary reads as
//	      ABANDONED ("Won't Fix" / "Won't Do")                      539
//	  ... of those, carrying a resolutiondate                       539   (all of them)
//	issues whose STATUS is "Cancelled"/"Canceled" — the only
//	  cancellation signal mapJiraStatus can see today                  0
//
// So an import of that project reports {imported:18267, warnings:[]} while 539 rows it recorded as
// DELIVERED were abandoned, each carrying a completion time that analytics' resolution-stats query
// counts as throughput and cycle time (it selects on `completed_at IS NOT NULL` with NO status
// predicate). #82's eleventh instance of this item's "data loss reported as success" shape, in the
// transport that needs credentials rather than the one that does not.
//
// ⚠ THE API TRANSPORT'S EXPOSURE IS STRICTLY LARGER THAN THE CSV TRANSPORT'S, AND THAT IS MEASURED
// RATHER THAN ASSUMED. mapJiraIssues has a SECOND route onto `done` that jiraRowMapper does not:
// resolveJiraStatusCategory. In a 3,000-issue sample of that project's resolved work, 2,862 landed
// on done by status NAME ("Closed") and 138 landed there ONLY through statusCategory — from the
// status "Release pending", a name Track has never heard of. Every one of those 138 is a row the
// CSV rule could never have reached even if it had been called.
//
// ⚠ AND THE OPEN DECISION #82 LEFT IS BIGGER HERE, WITH NUMBERS: 7,214 further issues in that one
// project carry resolutions that plainly describe abandoned work — "Rejected", "Out of Date",
// "Duplicate", "Cannot Reproduce", "Incomplete" — and Track's vocabulary reads none of them. THIS
// MERGE INVENTS NO VOCABULARY AND DOES NOT DECIDE THEM. They are REPORTED, per issue-count, on the
// first import. That refusal is #82's and #76's, inherited rather than re-litigated.

// jiraIssueResolutionJSON shapes a v3 issue the way the real Cloud instance serialises one.
//
// `resolution` is injected VERBATIM so a case can express the three states the wire actually has,
// which a *string parameter could not: the key ABSENT (the fields list never asked — measured: an
// unknown field name is answered HTTP 200 with the key simply missing), the key present and NULL
// (an unresolved issue — measured, both sampled unresolved issues came back `"resolution": null`),
// and an object carrying a name.
func jiraIssueResolutionJSON(key, summary, status, categoryKey, resolutionJSON string) string {
	statusObj := fmt.Sprintf(`{"name":%q}`, status)
	if categoryKey != "" {
		statusObj = fmt.Sprintf(`{"name":%q,"id":"11772","statusCategory":{"id":3,"key":%q,"colorName":"green","name":"Done"}}`,
			status, categoryKey)
	}
	res := ""
	if resolutionJSON != "" {
		res = fmt.Sprintf(`,"resolution":%s`, resolutionJSON)
	}
	return fmt.Sprintf(`{"key":%q,"fields":{"summary":%q,"description":null,"status":%s,`+
		`"priority":{"name":"Medium"},"labels":[],"resolutiondate":%q,"created":%q,"updated":%q%s}}`,
		key, summary, statusObj, fixtureJiraResolutionDate, fixtureJiraCreated, fixtureJiraUpdated, res)
}

// The instant the sampled instance actually returned for resolutiondate, in the serialisation #84
// pinned for Cloud (three fractional digits, numeric offset). A completion time only lands if this
// parses, so a fabricated RFC3339 here would test the wrong thing.
const fixtureJiraResolutionDate = "2026-08-09T09:00:10.130-0700"

// oneJiraRow drains exactly one canned issue through the real jiraSource and returns its row. It
// FAILS on any count other than one rather than indexing — a fixture that stopped producing a row
// would otherwise panic in a way that reads as a broken test rather than a broken fixture.
func oneJiraRow(t *testing.T, issueJSON string) SourceRow {
	t.Helper()
	rows := jiraRowsFrom(t, issueJSON)
	if len(rows) != 1 {
		t.Fatalf("canned page yielded %d rows, want exactly 1", len(rows))
	}
	return rows[0]
}

// notesWithVia returns the notes on a row that took a given path. Keyed on Via rather than on the
// rendered sentence so a wording change does not red these tests, and so a test cannot pass by
// finding SOME note.
func notesWithVia(row SourceRow, via string) []FieldNote {
	var out []FieldNote
	for _, n := range row.Notes {
		if n.Via == via {
			out = append(out, n)
		}
	}
	return out
}

// ── THE WIRE: the shipped request must ASK for the field ─────────────────────────────────────────
//
// ⚠ THIS IS THE ONLY INSTRUMENT IN THE PACKAGE THAT CAN SEE THE REQUEST, AND #86 PROVED IT THE HARD
// WAY. Every canned server here answers ANY body with the same page, so a fixture supplies
// `resolution` whether or not the request asked for it — every behavioural test below would stay
// green with the field removed from jiraFields. And a real Jira does NOT complain: measured on the
// shipped endpoint, `fields:["summary","talyvorTotallyFakeField"]` returns HTTP 200 with only
// summary. A misspelling here produces a perfectly successful import in which every abandoned issue
// silently reverts to importing as delivered.
//
// ⚠ THE LITERAL IS HARDCODED, NOT TAKEN FROM THE CONSTANT — #75's C6. A test written against the
// same constant the code uses compares the constant to itself and passes for EVERY value, "" included.
func TestJiraRequest_AsksForTheResolutionField(t *testing.T) {
	const wireName = "resolution" // measured on the shipped endpoint; NOT jiraAPIResolutionField
	for _, f := range jiraFields {
		if f == wireName {
			return
		}
	}
	t.Errorf("jiraFields = %v does not request %q. Jira answers HTTP 200 and omits an unknown or "+
		"unrequested field, so every abandoned issue imports as delivered work carrying a completion "+
		"time, and the import reports complete success.", jiraFields, wireName)
}

// ── THE DEFECT ───────────────────────────────────────────────────────────────────────────────────

// The measured shape: Status "Closed" — which mapJiraStatus maps to done — with the abandonment
// carried entirely in `resolution`. On the sampled project this is 539 issues, all of them dated.
func TestJiraAPIResolution_AbandonedWorkImportsAsCancelledWithNoCompletionTime(t *testing.T) {
	for _, word := range []string{"Won't Fix", "Won't Do"} {
		t.Run(word, func(t *testing.T) {
			row := oneJiraRow(t, jiraIssueResolutionJSON("PROJ-1", "abandoned", "Closed", "",
				fmt.Sprintf(`{"self":"https://x/rest/api/3/resolution/1","id":"1","description":"d","name":%q}`, word)))

			if row.Issue.Status != model.StatusCancelled {
				t.Errorf("status = %q, want %q — Jira resolved this issue %q and Track's own "+
					"mapJiraStatus reads that word as cancelled; importing it as done puts abandoned "+
					"work in the delivered column", row.Issue.Status, model.StatusCancelled, word)
			}
			if row.Issue.CompletedAt != nil {
				t.Errorf("completed_at = %v, want nil — analytics' resolution-stats query selects on "+
					"`completed_at IS NOT NULL` with no status predicate, so a completion time here is "+
					"counted as throughput and as cycle time", row.Issue.CompletedAt)
			}
			if got := notesWithVia(row, viaResolutionCancelled); len(got) != 1 {
				t.Errorf("notes carrying %s = %v, want exactly one — an import that silently "+
					"reclassifies is as unreadable as one that silently does not",
					viaResolutionCancelled, got)
			}
		})
	}
}

// ⚠ THE ROUTE ONTO `done` THAT THE CSV TRANSPORT DOES NOT HAVE, and the reason this merge is not
// simply "call the CSV function". "Release pending" is a status name Track has never heard of; the
// row reaches done only through resolveJiraStatusCategory. 138 of 3,000 sampled resolved issues on
// the real instance arrive exactly this way.
func TestJiraAPIResolution_TheCategoryRouteOntoDoneIsCoveredToo(t *testing.T) {
	row := oneJiraRow(t, jiraIssueResolutionJSON("PROJ-2", "category route", "Release pending", "done",
		`{"id":"1","name":"Won't Fix"}`))

	if row.Issue.Status != model.StatusCancelled {
		t.Errorf("status = %q, want %q — this row reaches `done` only through statusCategory, which "+
			"is a route the CSV mapper does not have; the resolution rule must run after the category "+
			"fallback, not before it", row.Issue.Status, model.StatusCancelled)
	}
	if row.Issue.CompletedAt != nil {
		t.Errorf("completed_at = %v, want nil", row.Issue.CompletedAt)
	}
}

// A resolution Track cannot read as finished-or-abandoned changes NOTHING and is REPORTED with its
// word, so a human sees "Rejected" (229 issues in the sample) and decides. The 7,214-issue open
// decision lives here, and this test is what stops a future session from answering it silently.
func TestJiraAPIResolution_UnreadableResolutionIsReportedAndChangesNothing(t *testing.T) {
	for _, word := range []string{"Rejected", "Out of Date", "Duplicate", "Cannot Reproduce", "Incomplete"} {
		t.Run(word, func(t *testing.T) {
			row := oneJiraRow(t, jiraIssueResolutionJSON("PROJ-3", "unreadable", "Closed", "",
				fmt.Sprintf(`{"id":"5","name":%q}`, word)))

			if row.Issue.Status != model.StatusDone {
				t.Errorf("status = %q, want %q — Track has no reading for %q and inventing one is the "+
					"decision this merge exists to avoid making", row.Issue.Status, model.StatusDone, word)
			}
			if row.Issue.CompletedAt == nil {
				t.Errorf("completed_at = nil, want the resolutiondate — nothing about an unreadable " +
					"resolution may change the row")
			}
			got := notesWithVia(row, viaResolutionUnreadable)
			if len(got) != 1 || got[0].Value != word {
				t.Errorf("notes carrying %s = %v, want exactly one naming %q — the words and their "+
					"counts are the whole of what puts this decision in front of a human",
					viaResolutionUnreadable, got, word)
			}
		})
	}
}

// A resolution that AGREES with the status changes nothing and says nothing. Silence here is what
// keeps the report readable: on the sampled project 10,500 issues are resolved "Fixed" alone.
func TestJiraAPIResolution_AnAgreeingResolutionIsSilent(t *testing.T) {
	row := oneJiraRow(t, jiraIssueResolutionJSON("PROJ-4", "finished", "Closed", "", `{"id":"10","name":"Done"}`))

	if row.Issue.Status != model.StatusDone || row.Issue.CompletedAt == nil {
		t.Errorf("status = %q completed_at = %v, want done with a completion time",
			row.Issue.Status, row.Issue.CompletedAt)
	}
	for _, via := range []string{viaResolutionCancelled, viaResolutionUnreadable, "no-resolution-field"} {
		if got := notesWithVia(row, via); len(got) != 0 {
			t.Errorf("a resolution that agrees with the status emitted %s notes %v — a report nobody "+
				"can read is a report nobody reads", via, got)
		}
	}
}

// ⚠ THE STRUCTURAL ZERO, AND IT IS REACHABLE RATHER THAN DEFENSIVE. Measured on the shipped
// endpoint: an unknown field name in the POST body's `fields` list is answered HTTP 200 with the key
// simply absent — so a rename or a typo in jiraFields lands here and NOWHERE ELSE. Without this
// line, "Track read your resolutions" and "Track recorded every abandoned issue as delivered" produce
// byte-identical database rows AND byte-identical reports.
//
// ⚠ IT FIRES ONLY ON A ROW THAT IMPORTED AS `done`, WHICH IS A DECISION. The field can only ever
// change a done row (#82's rule, inherited), so warning about a backlog row would report a loss that
// did not happen. A project with no closed work is correctly silent.
func TestJiraAPIResolution_AnAbsentFieldIsReportedOnEveryDoneRow(t *testing.T) {
	done := oneJiraRow(t, jiraIssueResolutionJSON("PROJ-5", "no field", "Closed", "", ""))
	if got := notesWithVia(done, "no-resolution-field"); len(got) != 1 {
		t.Errorf("a response with no `resolution` key produced %v, want exactly one structural-zero "+
			"note — a fields list that stopped asking is invisible in every other instrument in this "+
			"package, because the canned servers answer any request with the same page", got)
	}

	open := oneJiraRow(t, jiraIssueResolutionJSON("PROJ-6", "no field, open", "In Progress", "", ""))
	if got := notesWithVia(open, "no-resolution-field"); len(got) != 0 {
		t.Errorf("a row that did not import as done reported %v — the resolution can only ever change "+
			"a done row, so nothing was lost on this one and saying so is noise", got)
	}
}

// `"resolution": null` is the wire's way of saying the issue is UNRESOLVED — measured, both sampled
// unresolved issues came back exactly that way. It is not a loss and must not be reported.
//
// ⚠ AND IT IS THE STATE THAT MAKES THE ABSENT-KEY CASE ABOVE HARD: a decoder that cannot tell an
// absent key from a null one reports every unresolved issue as a broken response, or reports a
// broken response as an unresolved issue. Both are wrong and only one of them is loud.
func TestJiraAPIResolution_ANullResolutionIsNotALoss(t *testing.T) {
	row := oneJiraRow(t, jiraIssueResolutionJSON("PROJ-7", "done, unresolved", "Closed", "", `null`))

	if row.Issue.Status != model.StatusDone {
		t.Errorf("status = %q, want %q — a null resolution says the issue is unresolved, not that it "+
			"was abandoned", row.Issue.Status, model.StatusDone)
	}
	for _, via := range []string{viaResolutionCancelled, viaResolutionUnreadable, "no-resolution-field"} {
		if got := notesWithVia(row, via); len(got) != 0 {
			t.Errorf("a null resolution emitted %s notes %v", via, got)
		}
	}
}

// A resolution arriving in a shape this decoder cannot read is REPORTED, never silently dropped —
// the rule the whole item runs on. The bare string is the plausible variant: Jira's own CSV export
// serialises this field as a word, so a future transport or a proxy that flattens the object lands
// here.
func TestJiraAPIResolution_AnUnexpectedShapeIsReportedNotDropped(t *testing.T) {
	row := oneJiraRow(t, jiraIssueResolutionJSON("PROJ-8", "odd shape", "Closed", "", `"Won't Fix"`))

	if got := notesWithVia(row, viaResolutionUnreadable); len(got) != 1 {
		t.Errorf("a `resolution` that is not an object produced %v, want one unreadable note — a shape "+
			"nobody reports is a shape nobody fixes", got)
	}
	if row.Issue.Status != model.StatusDone {
		t.Errorf("status = %q, want %q — this importer does not read a shape it cannot decode",
			row.Issue.Status, model.StatusDone)
	}
}

// #82's rule, inherited and not re-litigated: the field can only ever move done → cancelled. A
// resolution on a row that did not import as done is Jira's own inconsistency, and reinterpreting it
// is not this importer's business.
func TestJiraAPIResolution_ARowThatDidNotImportAsDoneIsUntouched(t *testing.T) {
	row := oneJiraRow(t, jiraIssueResolutionJSON("PROJ-9", "in flight", "In Progress", "",
		`{"id":"1","name":"Won't Fix"}`))

	if row.Issue.Status != model.StatusInProgress {
		t.Errorf("status = %q, want %q", row.Issue.Status, model.StatusInProgress)
	}
	if got := notesWithVia(row, viaResolutionCancelled); len(got) != 0 {
		t.Errorf("a resolution on a non-done row reclassified it and said %v", got)
	}
}
