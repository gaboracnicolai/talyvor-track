package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/talyvor/track/internal/model"
)

// date_fields_test.go — W3.4's fifth merge: the two date fields Jira sends on a response Track
// already asks for, and which Track has a column for and drops.
//
// MEASURED FIRST, against a real Jira (jira.atlassian.com, anonymous REST v2), negative-controlled
// before the 200 was believed — a fabricated host resolved to nothing and a fabricated path on the
// SAME host answered 404, so the 200 is not a blanket answer:
//
//	GET /rest/api/2/search?jql=duedate is not EMPTY …&fields=duedate,resolutiondate,status
//	  JRACLOUD-94211  duedate "2027-12-31"  resolutiondate null
//	  JRACLOUD-83493  duedate "2027-12-31"  resolutiondate "2025-08-06T04:34:04.000+0000"
//	  JRASERVER-73766 duedate null          resolutiondate "2026-08-06T20:06:39.000+0000"
//	3,318 issues on that instance carry a duedate.
//
// ⚠ THE TRAP, MEASURED AND NOT GUESSED: NEITHER SHAPE IS RFC3339. `duedate` is a bare date with no
// time and no offset; `resolutiondate` carries the offset as `+0000`, not `+00:00`. time.Parse with
// time.RFC3339 REFUSES BOTH — proven by running it. An implementation that reached for the obvious
// constant would have parsed nothing, written nil into both columns, and reported {imported:N,
// warnings:[]} — the same "data loss reported as success" shape as #71/#72/#73, one field over —
// while every fabricated RFC3339 fixture in this package passed. That is why the layouts below are
// pinned BY HAND from the measured strings, and why an unparseable date is REPORTED, never nil'd.

// The exact strings the real instance returned. Pinned by hand: this is the vocabulary rule (#72's
// rule 2), and it is the one a parser rewrite cannot move without going red.
const (
	realJiraDueDate        = "2027-12-31"
	realJiraResolutionDate = "2026-08-06T20:06:39.000+0000"
)

// jiraIssueWithDatesJSON is jiraIssueJSON plus the two date fields, shaped exactly like the measured
// response. An empty string omits the key entirely — a provider that sends none — while "null" sends
// the key with a null value, which is what Jira does for an unset field and is a different wire fact.
func jiraIssueWithDatesJSON(key, summary, status, due, resolution string) string {
	field := func(name, v string) string {
		if v == "" {
			return ""
		}
		if v == "null" {
			return fmt.Sprintf(`,%q:null`, name)
		}
		return fmt.Sprintf(`,%q:%q`, name, v)
	}
	// ⚠ `resolution` IS PAIRED WITH `resolutiondate`, BECAUSE THE PROVIDER PAIRS THEM. Measured on
	// the shipped endpoint: on the sampled instance `statusCategory = Done` and `resolution IS NOT
	// EMPTY` return the SAME 18,267 issues, and an unresolved issue comes back `"resolution": null`
	// with the key present. So a dated row carries a resolution object and an undated row carries
	// null — a fixture with a resolutiondate and no resolution is a shape Jira does not send.
	// The word is "Done" rather than the instance's more common "Fixed" so that it AGREES with the
	// status this builder is given; a disagreeing word would inject an unrelated finding into a test
	// about dates. See api_resolution.go.
	res := `,"resolution":null`
	if resolution != "" && resolution != "null" {
		res = `,"resolution":{"id":"10","name":"Done"}`
	}
	return fmt.Sprintf(`{"key":%q,"fields":{"summary":%q,"description":null,"status":{"name":%q},"priority":{"name":"Medium"},"labels":[],"created":%q,"updated":%q%s%s%s}}`,
		key, summary, status, fixtureJiraCreated, fixtureJiraUpdated, field("duedate", due), field("resolutiondate", resolution), res)
}

// ── 1. THE FIELDS LAND ────────────────────────────────────────────────────────────────────────────

// A Jira issue that is done and carries both dates must arrive with both columns populated. Today
// both are nil and the caller is told the import was clean.
func TestJiraSource_RecordsDueDateAndCompletedAt(t *testing.T) {
	rows := jiraRowsFrom(t, jiraIssueWithDatesJSON("PROJ-1", "Shipped", "Done", realJiraDueDate, realJiraResolutionDate))
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	got := rows[0].Issue

	if got.DueDate == nil {
		t.Fatalf("DueDate is nil — the provider sent %q and Track has a column for it", realJiraDueDate)
	}
	if want := "2027-12-31T00:00:00Z"; got.DueDate.UTC().Format(time.RFC3339) != want {
		t.Errorf("DueDate = %s, want %s", got.DueDate.UTC().Format(time.RFC3339), want)
	}
	if got.CompletedAt == nil {
		t.Fatalf("CompletedAt is nil — the provider sent %q on a DONE issue", realJiraResolutionDate)
	}
	if want := "2026-08-06T20:06:39Z"; got.CompletedAt.UTC().Format(time.RFC3339) != want {
		t.Errorf("CompletedAt = %s, want %s", got.CompletedAt.UTC().Format(time.RFC3339), want)
	}
}

// An issue carrying neither key must be untouched — absent is not zero. This is the fail-safe half:
// a Jira that sends no dates imports exactly as it does today.
func TestJiraSource_AbsentDatesStayNil(t *testing.T) {
	rows := jiraRowsFrom(t, jiraIssueWithDatesJSON("PROJ-2", "No dates", "To Do", "", ""))
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if rows[0].Issue.DueDate != nil {
		t.Errorf("DueDate = %v, want nil when the provider sends no duedate", rows[0].Issue.DueDate)
	}
	if rows[0].Issue.CompletedAt != nil {
		t.Errorf("CompletedAt = %v, want nil when the provider sends no resolutiondate", rows[0].Issue.CompletedAt)
	}
	if len(rows[0].Notes) != 0 {
		t.Errorf("notes = %v, want none — an absent date is not a loss to report", rows[0].Notes)
	}
	// An explicit JSON null is the same fact on the wire, and must behave identically.
	nullRows := jiraRowsFrom(t, jiraIssueWithDatesJSON("PROJ-3", "Null dates", "To Do", "null", "null"))
	if nullRows[0].Issue.DueDate != nil || nullRows[0].Issue.CompletedAt != nil {
		t.Errorf("explicit nulls must behave as absent, got due=%v completed=%v",
			nullRows[0].Issue.DueDate, nullRows[0].Issue.CompletedAt)
	}
}

// ── 2. THE DECISION, PINNED ───────────────────────────────────────────────────────────────────────

// THE DECISION THIS MERGE HAD TO STATE, AND IT IS DERIVED FROM SHIPPED MECHANICS, NOT PREFERENCE:
// Jira resolves "Won't Do" too, so a CANCELLED issue carries a resolutiondate. Track's OWN rule —
// issue.Store.Update — stamps completed_at when a status transition lands on "done" and CLEARS it on
// any transition away. So an imported row carrying completed_at on a non-done issue is a state no
// Track path can produce, and the first status edit through the API would erase it silently.
// It is also not free: analytics' resolution-stats query filters on `completed_at IS NOT NULL` with
// NO status predicate, so a cancelled issue with a completion time is counted as delivered work in
// cycle time and throughput.
// THEREFORE: completed_at is recorded only when the issue imports as "done" — and when a resolution
// date arrives that is NOT recorded, the import SAYS SO. A deliberate drop that is reported is a
// decision; the same drop unreported is #71's "data loss reported as success".
func TestJiraSource_ResolutionDateNotRecordedUnlessDone(t *testing.T) {
	rows := jiraRowsFrom(t, jiraIssueWithDatesJSON("PROJ-4", "Abandoned", "Won't Do", "", realJiraResolutionDate))
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if got := rows[0].Issue.Status; got != model.StatusCancelled {
		t.Fatalf("status = %q, want %q — the rest of this test is about that status", got, model.StatusCancelled)
	}
	if rows[0].Issue.CompletedAt != nil {
		t.Errorf("CompletedAt = %v on a CANCELLED issue; Track stamps a completion time only on \"done\"",
			rows[0].Issue.CompletedAt)
	}
	if !anyNoteMentions(rows[0].Notes, realJiraResolutionDate, "cancelled") {
		t.Errorf("the dropped resolution date was not reported; notes = %#v", rows[0].Notes)
	}
}

// ── 3. THE WIRE ───────────────────────────────────────────────────────────────────────────────────

// Narrowing jiraFields would take both fields away silently: every fixture in this package supplies
// its own JSON, so nothing else in the suite would notice. Same assertion #73 added for `status`.
func TestJiraRequest_AsksForTheDateFields(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issues":[],"isLast":true}`))
	}))
	defer srv.Close()

	src := newJiraSource(context.Background(), "e:t", "PROJ", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep
	src.Next()

	var sent struct {
		Fields []string `json:"fields"`
	}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("decode outgoing body %q: %v", body, err)
	}
	for _, want := range []string{"duedate", "resolutiondate", "status"} {
		if !containsString(sent.Fields, want) {
			t.Errorf("outgoing fields %v does not ask for %q — the field would never arrive", sent.Fields, want)
		}
	}
}

// ── 4. THE FORMATS, PINNED BY HAND FROM THE MEASUREMENT ───────────────────────────────────────────

// The vocabulary rule. RFC3339 is asserted to be INSUFFICIENT here on purpose: this test is the
// record that the obvious implementation is the broken one, and it goes red if someone "simplifies"
// the layout list back to the constant.
func TestJiraDateParse_TheMeasuredWireFormats(t *testing.T) {
	// Driven through the REAL source rather than a parser helper: the question is what lands in the
	// column, and a helper that parses correctly into a mapper that never calls it is the structural
	// zero this whole item keeps finding.
	for _, tc := range []struct {
		in   string
		want string
	}{
		{realJiraDueDate, "2027-12-31T00:00:00Z"},                // duedate: date only, no offset
		{"2026-08-06", "2026-08-06T00:00:00Z"},                   // the same shape, a second value
		{"2027-12-31T00:00:00.000+0000", "2027-12-31T00:00:00Z"}, // the datetime shape, offset +0000
		{"2027-12-31T00:00:00.000Z", "2027-12-31T00:00:00Z"},     // RFC3339 must keep working
		{"2027-12-31T02:00:00.000+0200", "2027-12-31T00:00:00Z"}, // a non-UTC offset in the measured shape
	} {
		rows := jiraRowsFrom(t, jiraIssueWithDatesJSON("PROJ-D", "Dated", "To Do", tc.in, ""))
		if len(rows) != 1 {
			t.Fatalf("want 1 row, got %d", len(rows))
		}
		if rows[0].Issue.DueDate == nil {
			t.Errorf("duedate %q refused — a real Jira sends this shape", tc.in)
			continue
		}
		if got := rows[0].Issue.DueDate.UTC().Format(time.RFC3339); got != tc.want {
			t.Errorf("duedate %q landed as %s, want %s", tc.in, got, tc.want)
		}
	}

	// The resolution path parses the same shapes — asserted separately because it is a separate
	// call site, and one of the two being wired is exactly how half a fix ships.
	rows := jiraRowsFrom(t, jiraIssueWithDatesJSON("PROJ-R", "Done", "Done", "", "2025-08-06T04:34:04.000+0000"))
	if rows[0].Issue.CompletedAt == nil {
		t.Fatalf("resolutiondate refused on the done path")
	}
	if got, want := rows[0].Issue.CompletedAt.UTC().Format(time.RFC3339), "2025-08-06T04:34:04Z"; got != want {
		t.Errorf("resolutiondate landed as %s, want %s", got, want)
	}

	// time.RFC3339 alone parses NEITHER of the two shapes this importer actually receives. Measured,
	// not assumed — if this ever stops being true the layout list can shrink, and this says so.
	for _, s := range []string{realJiraDueDate, realJiraResolutionDate} {
		if _, err := time.Parse(time.RFC3339, s); err == nil {
			t.Errorf("time.RFC3339 now parses %q — the hand-pinned layouts may be reducible", s)
		}
	}
}

// A date in a shape no layout accepts must be REPORTED, not silently nil'd. This is what makes the
// hand-pinned layout list safe: a tenant whose serialisation differs from all three says so on its
// first import instead of importing a column of nulls that reads as "no due dates in Jira".
func TestJiraSource_UnparseableDateIsReportedNotDropped(t *testing.T) {
	rows := jiraRowsFrom(t, jiraIssueWithDatesJSON("PROJ-5", "Odd date", "To Do", "31/12/2027", ""))
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if rows[0].Issue.DueDate != nil {
		t.Errorf("DueDate = %v, want nil for an unparseable value", rows[0].Issue.DueDate)
	}
	if !anyNoteMentions(rows[0].Notes, "31/12/2027") {
		t.Errorf("an unparseable due date was dropped without a word; notes = %#v", rows[0].Notes)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────────────────────────

// anyNoteMentions reports whether some note RENDERS a line containing every fragment. It asserts on
// the rendered sentence, not on struct internals, because the sentence is what a real import shows.
func anyNoteMentions(notes []FieldNote, fragments ...string) bool {
	for _, n := range notes {
		line := n.render(1)
		all := true
		for _, f := range fragments {
			if !strings.Contains(line, f) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

func containsString(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}
