package importer

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// linear_date_fields_test.go — `dueDate` and `completedAt` on the Linear half. #74 did this for Jira;
// this is the same merge with the query risk removed by #76's method.
//
// ⚠ THE PROVENANCE HERE IS WEAKER THAN #74's AND IS NOT DRESSED UP AS EQUAL. #74 OBSERVED the actual
// bytes a real Jira sends (`"2027-12-31"`, and `"2026-08-06T20:06:39.000+0000"` whose offset defeats
// time.RFC3339). For Linear this environment can measure THREE things and not a fourth:
//
//	MEASURED — the fields exist and the query validates. `dueDate` and `completedAt` added to the
//	  node selection still answer HTTP 401 AUTHENTICATION_ERROR, not 400 GRAPHQL_VALIDATION_FAILED.
//	MEASURED — the DECLARED SCALAR TYPES, by unauthenticated introspection:
//	      Issue.dueDate     : TimelessDate  "The date at which the issue is due."
//	      Issue.completedAt : DateTime      "The time at which the issue was moved into completed state."
//	MEASURED — scalar coercion errors arrive PRE-AUTH too, so the accepted INPUT space is probeable:
//	      dueDate eq "talyvor-not-a-date"  ⇒ 400, `Unable to parse value ... into a valid date`
//	      dueDate eq "2026-08-09"          ⇒ 401 (accepted)
//	      dueDate eq "2026-08-09T00:00:00Z" ⇒ 401 (also accepted)
//
//	⚠ NOT MEASURED, AND THIS IS THE LIMIT THAT MATTERS: the OUTPUT serialisation. Both scalar
//	  descriptions describe what the API ACCEPTS ("Accepts shortcuts like `2021`... Also accepts ISO
//	  8601 durations"), which is INPUT COERCION. Nothing reachable from here without a tenant emits
//	  one of these values, and the probe above proves the input space is permissive enough to tell us
//	  nothing about the output.
//
// ⚠ SO THE DESIGN IS WHAT MAKES THIS SHIPPABLE, NOT THE EVIDENCE. A value NO pinned layout accepts is
// REPORTED, never nil'd — #74's rule. If Linear's output differs from both shapes pinned below, the
// first real import says so out loud in the job row instead of delivering a column of nulls that
// reads as "this tenant has no due dates". That is strictly better than today, where BOTH FIELDS ARE
// SILENTLY DISCARDED INTO COLUMNS BUILT TO HOLD THEM with `warnings: []` — this item's own
// "data loss reported as success" shape, found for the sixth time.
//
// ⚠ AND parseLinearTime IS A SEPARATE FUNCTION FROM parseJiraTime ON PURPOSE, even though their
// layout lists overlap. Sharing it would silently lend Jira's OBSERVED-BYTES provenance to a field
// nobody in this environment has ever seen serialised — the exact overclaim #75 caught when it found
// every "measured against a real Jira" note in this package was taken on v2 while the code calls v3.

// The two shapes a Linear date can plausibly arrive in, pinned by hand. TimelessDate is ISO-8601
// date-only; DateTime is ISO-8601 date-time, which Linear renders with milliseconds and a Z.
var measuredLinearDateShapes = map[string]string{
	"2026-09-01":               "TimelessDate — ISO 8601 date, no time, no offset (time.RFC3339 REFUSES this)",
	"2026-08-01T10:00:00.000Z": "DateTime — ISO 8601 with milliseconds and Z",
	"2026-08-01T10:00:00Z":     "DateTime — the same instant without milliseconds",
}

// notesToTally is the shape renderWarnings takes — the pipeline COUNTS notes rather than
// accumulating a line per row, so a test that wants the rendered sentence has to tally first.
func notesToTally(notes []FieldNote) map[FieldNote]int {
	out := map[FieldNote]int{}
	for _, n := range notes {
		out[n]++
	}
	return out
}

func linNodeDated(id, stateName, stateType, due, completed string, prio int) string {
	fields := fmt.Sprintf(`"identifier":%q,"title":"T-%s","description":"d","state":{"name":%q,"type":%q},"priority":%d,"labels":{"nodes":[{"name":"bug"}]}`,
		id, id, stateName, stateType, prio)
	// null is what Linear sends for an absent date, and it must decode to "" not to a literal "null".
	if due == "" {
		fields += `,"dueDate":null`
	} else {
		fields += fmt.Sprintf(`,"dueDate":%q`, due)
	}
	if completed == "" {
		fields += `,"completedAt":null`
	} else {
		fields += fmt.Sprintf(`,"completedAt":%q`, completed)
	}
	// The opening time every real node carries — see the banner in linear_test.go. linNodeCreated
	// REPLACES this value (or nulls it) when a case is specifically about createdAt.
	fields += fmt.Sprintf(`,"createdAt":%q`, fixtureLinearCreated)
	return "{" + fields + "}"
}

// THE FINDING AS A TEST. Both columns exist in model.Issue, both are named by the importer's upsert
// (#74 had to ADD `completed_at` there — a mapper fix alone would have been inert), and today a
// Linear import drops both without a word.
func TestLinearSource_DueDateAndCompletedAtLand(t *testing.T) {
	rows := linearRowsFrom(t, linNodeDated("ENG-1", "Done", "completed", "2026-09-01", "2026-08-01T10:00:00.000Z", 1))
	got := rows[0].Issue
	if got.DueDate == nil {
		t.Fatalf("dueDate was discarded — Track has a due_date column and the provider sent one")
	}
	if want := "2026-09-01"; got.DueDate.UTC().Format("2006-01-02") != want {
		t.Errorf("dueDate = %v, want %s", got.DueDate, want)
	}
	if got.CompletedAt == nil {
		t.Fatalf("completedAt was discarded on an issue that imported as done")
	}
	if want := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC); !got.CompletedAt.Equal(want) {
		t.Errorf("completedAt = %v, want %v", got.CompletedAt, want)
	}
	if len(rows[0].Notes) != 0 {
		t.Errorf("nothing was lost, so nothing should be reported; got %+v", rows[0].Notes)
	}
}

// #74's DECISION, INHERITED AND RE-PINNED HERE RATHER THAN ASSUMED: CompletedAt means FINISHED, not
// "left the board". Track's issue.Store.Update stamps completed_at only on a transition ONTO done and
// CLEARS it on any transition away, so a non-done row carrying one is a state no Track path can
// produce; and analytics' resolution-stats query selects on `completed_at IS NOT NULL` with NO status
// predicate, so an abandoned issue carrying one counts as delivered work.
//
// ⚠ MEASURED, AND IT MAKES THE RULE FIT LINEAR BETTER THAN JIRA: Linear's own schema says completedAt
// is "the time at which the issue was moved into completed state" and gives cancellation its OWN
// field, `canceledAt`. So Linear should rarely send completedAt on a non-completed issue at all —
// which is exactly why the refusal must still be TESTED rather than assumed away. A state whose type
// this importer refuses (`triage`, `duplicate`) imports as backlog, and if such an issue ever carries
// a completedAt it must be refused and reported, not written.
func TestLinearSource_CompletedAtRefusedUnlessDone(t *testing.T) {
	rows := linearRowsFrom(t, linNodeDated("ENG-2", "Needs Review", "started", "", "2026-08-01T10:00:00.000Z", 1))
	if rows[0].Issue.CompletedAt != nil {
		t.Errorf("completedAt landed on an issue that imported as in_progress: %v", rows[0].Issue.CompletedAt)
	}
	joined := strings.Join(renderWarnings(notesToTally(rows[0].Notes)), "\n")
	if !strings.Contains(joined, "completion time only") {
		t.Errorf("a refused completion time must be REPORTED — a deliberate drop nobody is told about "+
			"is indistinguishable from the silent ones; got:\n%s", joined)
	}
}

// THE HALF THE EVIDENCE CANNOT COVER, MADE SAFE BY DESIGN. The output serialisation of both scalars
// is unmeasurable from this environment (see the header), so a shape neither pinned layout accepts is
// the case that decides whether this merge is honest. It must be REPORTED, never nil'd: a tenant
// whose serialisation differs learns it on its first import instead of receiving a column of nulls.
func TestLinearSource_UnparseableDateIsReportedNotNilled(t *testing.T) {
	rows := linearRowsFrom(t, linNodeDated("ENG-3", "Done", "completed", "next tuesday", "", 1))
	if rows[0].Issue.DueDate != nil {
		t.Errorf("an unparseable date must not be invented: %v", rows[0].Issue.DueDate)
	}
	joined := strings.Join(renderWarnings(notesToTally(rows[0].Notes)), "\n")
	if !strings.Contains(joined, "not a date shape this importer recognises") {
		t.Errorf("an arrived-and-refused date must be reported; got:\n%s", joined)
	}
}

// An ABSENT date is not a loss and must not be reported — otherwise every issue without a due date
// produces a warning and the channel that reports real losses becomes noise nobody reads.
func TestLinearSource_AbsentDatesAreNotReported(t *testing.T) {
	rows := linearRowsFrom(t, linNodeDated("ENG-4", "Todo", "unstarted", "", "", 1))
	if len(rows[0].Notes) != 0 {
		t.Errorf("nothing arrived and nothing was lost; got %+v", rows[0].Notes)
	}
	if rows[0].Issue.DueDate != nil || rows[0].Issue.CompletedAt != nil {
		t.Errorf("absent dates must stay nil; got due=%v completed=%v", rows[0].Issue.DueDate, rows[0].Issue.CompletedAt)
	}
}

// Every shape the measurement says can arrive must be accepted, checked through the SOURCE so a
// layout list that is right while nothing consults it still fails.
func TestLinearDateShapes_AllPinnedShapesAreAccepted(t *testing.T) {
	for shape, why := range measuredLinearDateShapes {
		rows := linearRowsFrom(t, linNodeDated("ENG-9", "Done", "completed", shape, "", 1))
		if rows[0].Issue.DueDate == nil {
			t.Errorf("%s\n  shape %q was refused, but it is pinned as one this API can send", why, shape)
		}
	}
	// ⚠ AND THE ONE THAT MUST STILL BE REFUSED, so "accept everything" cannot satisfy the loop above.
	rows := linearRowsFrom(t, linNodeDated("ENG-9", "Done", "completed", "01/09/2026", "", 1))
	if rows[0].Issue.DueDate != nil {
		t.Errorf("an ambiguous D/M/Y shape must be refused and reported, not guessed: %v", rows[0].Issue.DueDate)
	}
}

// THE WIRE. Both fields pinned in the outgoing document, for #74's reason: narrowing the selection
// would silently take a field away and every fixture in this package supplies its own JSON, so
// nothing else in the suite would notice. The literals are hardcoded — #75's C6.
func TestLinearRequest_AsksForTheDateFields(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		gotBody = string(b)
		writeRaw(w, linPage(false, ""))
	}))
	defer srv.Close()

	src := newLinearSource("k", "TEAM", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep
	src.Next()

	for _, want := range []string{"dueDate", "completedAt"} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("the outgoing query must ask for %s; body was:\n  %s", want, gotBody)
		}
	}
	// ⚠ AND THE QUERY MUST STILL BE ONE LINEAR WOULD ACCEPT. This is the local half of #76's method:
	// scripts/w34-linear-schema-probe.py answers it against the live schema, and cannot run in CI.
	// What CI can hold is that the selection set did not grow something structurally broken.
	if strings.Count(gotBody, "{") != strings.Count(gotBody, "}") {
		t.Errorf("the outgoing query document is unbalanced; body was:\n  %s", gotBody)
	}
}
