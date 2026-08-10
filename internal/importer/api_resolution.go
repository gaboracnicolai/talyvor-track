package importer

import (
	"encoding/json"

	"github.com/talyvor/track/internal/model"
)

// api_resolution.go — whether closed work was FINISHED or ABANDONED, on the JIRA API TRANSPORT.
//
// ⚠⚠ #82 SHIPPED THIS RULE FOR THE CSV TRANSPORT AND ENUMERATED WHICH PATHS IT LEFT ALONE. It names
// the Linear CSV path explicitly and says nothing about jira_api — so `applyJiraResolution` had
// exactly one call site (csv.go), `mapJiraIssues` never asked the question, and `jiraFields` never
// asked for the field. That is #84's shape one field over: the fix landing where the defect was
// found, and the sibling transport inheriting neither it nor the evidence.
//
// MEASURED 2026-08-10 ON THE SHIPPED ENDPOINT — POST /rest/api/3/search/jql against a real Jira
// CLOUD site (hibernate.atlassian.net, project HHH, anonymous; the host #84 found answers the exact
// endpoint jiraSearchPath names). Negative-controlled first, so no 200 is read as a blanket answer:
// a fabricated site name ⇒ 404, a fabricated path on the real host ⇒ 404. Re-run it with
// scripts/w34-jira-api-resolution-probe.py, which FAILS rather than reports if a control answers.
//
//	project HHH, issues                                       20,550
//	  ... resolved (statusCategory = Done) — all import as
//	      Track `done` today                                  18,267
//	  ... whose resolution Track's OWN vocabulary reads as
//	      ABANDONED ("Won't Fix" / "Won't Do")                    539, every one of them dated
//	issues whose STATUS is "Cancelled"/"Canceled" — the only
//	  cancellation signal mapJiraStatus could see                   0
//
// ⚠ THE EVIDENCE IS SEPARATE FROM #82's AND IS NOT INHERITED FROM IT. #82's numbers are a
// SERVER/DC instance read through a CSV export view; these are a CLOUD instance read through the
// POST body this client actually sends. The RULE below is shared by CALL rather than copied — two
// copies would be two vocabularies that can drift — but a helper lends only its logic, never the
// measurement it was justified by.
//
// ⚠ AND THIS TRANSPORT'S EXPOSURE IS STRICTLY LARGER THAN THE CSV TRANSPORT'S. mapJiraIssues has a
// second route onto `done` that jiraRowMapper does not have: resolveJiraStatusCategory. In a
// 3,000-issue sample of that project's resolved work, 2,862 rows reached done by status NAME
// ("Closed") and 138 reached it ONLY through statusCategory, from the status "Release pending" —
// a name Track has never heard of. The rule therefore runs AFTER the category fallback.

const (
	// Jira's API name for the field, and the name a warning line points an operator at. Distinct
	// from jira_csv_resolution.go's "Resolution" header — there is no export to go and re-make here,
	// so the sentence has to name the `fields` list the client sends.
	jiraAPIResolutionField = "resolution"
)

// viaNoResolutionField is the STRUCTURAL ZERO: the response carried no `resolution` key at all.
//
// ⚠ IT IS REACHABLE, NOT DEFENSIVE. MEASURED on the shipped endpoint: `fields` containing an
// unknown name is answered HTTP 200 with the key simply absent (`["summary","talyvorTotallyFake"]`
// ⇒ only summary), and the shipped list before this merge returned no `resolution` key at all. So a
// rename or a typo in jiraFields lands HERE and nowhere else — and without this line "Track read
// your resolutions" and "Track recorded every abandoned issue as delivered" produce byte-identical
// database rows AND byte-identical reports. #86 measured why no other instrument in this package
// can see it: every canned server here answers ANY request with the same page, so a fixture
// supplies the field whether or not the request asked for it.
//
// ⚠ AND THE PROVIDER IS NOT CONSISTENT ABOUT NULLS, WHICH IS WHY THE SENTENCE BLAMES NOTHING IT
// CANNOT SEE. In the same measured response, `resolution` came back as an explicit null on an
// unresolved issue while `duedate` was OMITTED ENTIRELY rather than sent as null. So an instance
// that omits a null `resolution` on a row that nonetheless imported as done would land here too —
// and the line stays true of that row: Track could not check whether the work was finished or
// abandoned. It reports what was not checked, never why.
const viaNoResolutionField = "no-resolution-field"

// jiraAPIResolution reads Jira's `resolution` object and returns the status the row should import
// as, plus whatever that decision has to say out loud.
//
// The three states below are the three the WIRE actually has, and telling them apart is the whole
// job of taking json.RawMessage rather than a decoded struct: a *struct is nil for an ABSENT key and
// nil for a NULL one, so a decoder that cannot separate them either reports every unresolved issue
// as a broken response or reports a broken response as an unresolved issue.
//
//	key absent          the fields list did not ask, or asked wrongly ⇒ STRUCTURAL ZERO, reported
//	key present, null   the issue is UNRESOLVED (measured: that is exactly what Jira sends) ⇒ silent
//	an object           classified by the shared rule below
//
// ⚠ IT DOES NOT HOLD #82's done-ONLY RULE ITSELF, AND THAT IS A CORRECTION A CONTROL FORCED. The
// first draft opened with `if status != model.StatusDone { return status, nil }` — which reads well
// and is entirely redundant, because applyJiraResolution enforces the same invariant for the CSV
// transport and therefore for this one. The cost was not a wasted line: with the rule held in two
// places, NO single-line mutation could red the test that pins it, so the control campaign scored
// `NOT CAUGHT` on a rule that was in fact held twice over. A guard two lines enforce is a guard
// neither line is answerable for. The classification gate now lives ONLY in the shared rule; what
// is left here is reportOnDone, which governs something else entirely — see below.
func jiraAPIResolution(raw json.RawMessage, status model.IssueStatus) (model.IssueStatus, []FieldNote) {
	if len(raw) == 0 {
		return status, reportOnDone(status, FieldNote{
			Field:  fieldResolution,
			Mapped: string(status),
			Via:    viaNoResolutionField,
		})
	}
	var res struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		// A shape this decoder cannot read is REPORTED, never dropped — the rule this whole item
		// runs on. The raw bytes go in the line because the next reader needs to see what arrived,
		// and they are bounded by #80's exemplar cap like every other value.
		return status, reportOnDone(status, FieldNote{
			Field:  fieldResolution,
			Value:  string(raw),
			Mapped: string(status),
			Via:    viaResolutionUnreadable,
		})
	}
	// `null` decodes cleanly and leaves Name empty, which is the unresolved case; the shared rule
	// already treats an empty value as "nothing to say".
	return applyJiraResolution(res.Name, status)
}

// reportOnDone withholds a line about the resolution FIELD from a row that did not import as done.
//
// ⚠ IT IS A DIFFERENT RULE FROM THE CLASSIFICATION GATE, WHICH IS WHY IT IS A DIFFERENT LINE. The
// classification gate answers "may a resolution change this row's status" (#82: only done →
// cancelled). This answers "was anything LOST when the resolution did not arrive" — and on a row
// that never imported as done the answer is no, because the field could not have changed it. Report
// it anyway and every backlog issue in a project with no closed work carries a warning about a loss
// that did not happen, which is how a report stops being read.
func reportOnDone(status model.IssueStatus, n FieldNote) []FieldNote {
	if status != model.StatusDone {
		return nil
	}
	return []FieldNote{n}
}
