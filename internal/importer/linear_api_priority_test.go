package importer

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/model"
)

// linear_api_priority_test.go — the ONE leaf of either provider's response decoder that is not a
// string, a bool or raw bytes, and the only one where disagreeing with the wire is FATAL rather
// than reported.
//
// ⚠ THE PACKAGE ALREADY WROTE THIS RULE DOWN, ONE FIELD OVER. linear.go's comment on DueDate and
// CompletedAt says both "decode as strings so an unrecognised shape can be REPORTED rather than
// becoming a decode error that fails the whole page". Every leaf in linearNode and jiraIssue obeys
// it — measured, a census of both structs finds exactly one scalar exception, `Priority int`.
//
// ⚠ AND THE SCHEMA DECLARES THAT FIELD FRACTIONAL. Measured first-hand by unauthenticated
// introspection against api.linear.app/graphql, negative-controlled first (fabricated host ⇒ no
// resolution; fabricated path on the real host ⇒ 404; an unknown field ⇒ 400
// GRAPHQL_VALIDATION_FAILED, so a 200 is not a blanket answer):
//
//	Issue.priority : Float!   "The priority of the issue. 0 = No priority, 1 = Urgent, 2 = High,
//	                           3 = Medium, 4 = Low."
//
// A GraphQL Float is a double. `2`, `2.0` and `2e0` are all legal JSON serialisations of the double
// 2, and Go's encoding/json accepts only the first into an `int`:
//
//	json: cannot unmarshal number 2.0 into Go struct field ... of type int
//
// ⚠ THE BLAST RADIUS IS THE WHOLE IMPORT, NOT THE FIELD. linearClient.fetchPage returns on any
// decode error, so ONE such value discards the entire 100-issue page — measured: the sibling nodes
// decode correctly and are thrown away with it — and linearSource then surfaces one error row and
// STOPS, abandoning every later page. The job reports `failed` with ONE failed row for an import
// that lost everything after it.
//
// ⚠ WHAT IS NOT CLAIMED, AND IT IS THE SAME LIMIT linearTimeLayouts STATES FOR ITS OWN FIELD: this
// environment cannot observe how Linear's server SERIALISES a Float, because the API authenticates
// before it executes (the shipped query answers 401, per scripts/w34-linear-schema-probe.py) — that
// still needs a real tenant, W3.4 item (3). What IS measured is that the decoder refuses values the
// DECLARED contract permits, and that refusing them costs the import rather than one field. The fix
// is therefore the package's own already-decided rule applied to the field that missed it, not a new
// policy: an unrecognised value is REPORTED, never fatal, and the integral wire shape is unchanged.
//
// ⚠ THE EXISTING FIXTURES CANNOT SEE THIS BY CONSTRUCTION — linNode formats priority with %d, so
// every canned page in this package sends the one serialisation that happens to work. That is why
// these fixtures are written by hand here rather than reusing the helper.

// linNodePrioRaw builds a Linear issue node with the priority serialised VERBATIM, so a test can
// send a shape %d cannot produce. Everything else matches linNode.
func linNodePrioRaw(id, status, rawPriority string) string {
	return fmt.Sprintf(`{"identifier":%q,"title":"T-%s","description":"d","state":{"name":%q},"priority":%s,"labels":{"nodes":[{"name":"bug"}]},"createdAt":%q,"updatedAt":%q}`,
		id, id, status, rawPriority, fixtureLinearCreated, fixtureLinearUpdated)
}

// drainRows collects every row a source yields WITHOUT failing on an error row, so a test can assert
// on what actually happened instead of aborting inside the helper. Returning the rows rather than
// calling t.Fatalf is deliberate: an assertion behind a Fatalf in a shared helper can only ever
// agree with it.
func drainRows(src IssueSource) (rows []SourceRow) {
	for {
		row, ok := src.Next()
		if !ok {
			return rows
		}
		rows = append(rows, row)
	}
}

func firstRowErr(rows []SourceRow) error {
	for _, r := range rows {
		if r.Err != nil {
			return r.Err
		}
	}
	return nil
}

// (1) THE DEFECT. A priority serialised as `2.0` — the same double every other node carries as `2` —
// must import as High, and must not take its page down with it.
func TestLinearSource_AFractionallySerialisedPriorityImportsAndDoesNotDiscardThePage(t *testing.T) {
	srv := httptest.NewServer(cannedPages([]string{
		linPage(false, "",
			linNodePrioRaw("ENG-1", "Todo", "1"),
			linNodePrioRaw("ENG-2", "Todo", "2.0"),
			linNodePrioRaw("ENG-3", "Todo", "3"),
		),
	}, linPage(false, "")))
	defer srv.Close()

	src := newLinearSource("api-key", "TEAM-UUID", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep
	rows := drainRows(src)

	if err := firstRowErr(rows); err != nil {
		t.Fatalf("a legal Float serialisation took the whole page down: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("yielded %d issue(s), want 3 — the siblings of the fractional row were discarded with it", len(rows))
	}
	if got := rows[1].Issue.Priority; got != model.PriorityHigh {
		t.Errorf("priority 2.0 mapped to %q, want %q", got, model.PriorityHigh)
	}
	if got := rows[1].Issue.Identifier; got != "ENG-2" {
		t.Errorf("row 2 identifier = %q, want ENG-2", got)
	}
}

// (2) THE OTHER LEGAL SPELLING OF THE SAME DOUBLE. `2e0` is what a serialiser that prints doubles in
// exponent form emits; it is the same value and must map the same way. Separate from (1) because a
// decoder could be fixed for one shape and not the other.
func TestLinearSource_AnExponentSerialisedPriorityIsReadAsTheSameValue(t *testing.T) {
	srv := httptest.NewServer(cannedPages([]string{
		linPage(false, "", linNodePrioRaw("ENG-9", "Todo", "2e0")),
	}, linPage(false, "")))
	defer srv.Close()

	src := newLinearSource("api-key", "TEAM-UUID", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep
	rows := drainRows(src)

	if err := firstRowErr(rows); err != nil {
		t.Fatalf("exponent-form priority took the page down: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("yielded %d issue(s), want 1", len(rows))
	}
	if got := rows[0].Issue.Priority; got != model.PriorityHigh {
		t.Errorf("priority 2e0 mapped to %q, want %q", got, model.PriorityHigh)
	}
}

// (3) A VALUE OFF LINEAR'S SCALE IS REPORTED, NOT FATAL AND NOT SILENT — and the warning carries the
// provider's own bytes. `strconv.Itoa` could never have rendered "7.5"; it would have said "0".
func TestLinearSource_APriorityOffLinearsScaleIsReportedVerbatim(t *testing.T) {
	srv := httptest.NewServer(cannedPages([]string{
		linPage(false, "", linNodePrioRaw("ENG-7", "Todo", "7.5")),
	}, linPage(false, "")))
	defer srv.Close()

	src := newLinearSource("api-key", "TEAM-UUID", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep
	rows := drainRows(src)

	if err := firstRowErr(rows); err != nil {
		t.Fatalf("an off-scale priority took the page down: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("yielded %d issue(s), want 1", len(rows))
	}
	if got := rows[0].Issue.Priority; got != model.PriorityNone {
		t.Errorf("off-scale priority mapped to %q, want %q", got, model.PriorityNone)
	}
	var found *FieldNote
	for i, n := range rows[0].Notes {
		if n.Field == "priority" {
			found = &rows[0].Notes[i]
		}
	}
	if found == nil {
		t.Fatalf("an off-scale priority produced no note; notes = %+v", rows[0].Notes)
	}
	if found.Value != "7.5" {
		t.Errorf("note carries priority %q, want the provider's own bytes %q", found.Value, "7.5")
	}
}

// (3b) A NON-INTEGRAL VALUE MUST NOT BE TRUNCATED INTO A REAL PRIORITY. 2.5 is not "High" — Linear's
// own field description enumerates 0..4 and nothing between — so widening the decoder must not
// quietly start inventing a meaning the int decode never had the chance to invent. This is the case
// (3) cannot make: 7.5 truncates to 7, which is off the scale either way, so a decoder that rounded
// would still satisfy it.
func TestLinearSource_ANonIntegralPriorityIsNotTruncatedIntoTheScale(t *testing.T) {
	srv := httptest.NewServer(cannedPages([]string{
		linPage(false, "", linNodePrioRaw("ENG-25", "Todo", "2.5")),
	}, linPage(false, "")))
	defer srv.Close()

	src := newLinearSource("api-key", "TEAM-UUID", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep
	rows := drainRows(src)

	if err := firstRowErr(rows); err != nil {
		t.Fatalf("a non-integral priority took the page down: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("yielded %d issue(s), want 1", len(rows))
	}
	if got := rows[0].Issue.Priority; got != model.PriorityNone {
		t.Errorf("priority 2.5 mapped to %q — truncating it into Linear's scale invents a meaning; want %q reported",
			got, model.PriorityNone)
	}
	var reported bool
	for _, n := range rows[0].Notes {
		if n.Field == "priority" && n.Value == "2.5" {
			reported = true
		}
	}
	if !reported {
		t.Errorf("priority 2.5 produced no verbatim note; notes = %+v", rows[0].Notes)
	}
}

// (4) THE BLAST RADIUS, ASSERTED AS ITS OWN CASE. One such value on page ONE abandons every LATER
// page — the loss is not the field and is not even the page, it is the rest of the import. This is
// the assertion that distinguishes "one issue degraded" from "the job stopped".
func TestLinearSource_AFractionalPriorityOnPageOneDoesNotAbandonPageTwo(t *testing.T) {
	srv := httptest.NewServer(cannedPages([]string{
		linPage(true, "c1", linNodePrioRaw("ENG-1", "Todo", "2.0")),
		linPage(false, "", linNodePrioRaw("ENG-2", "Todo", "1")),
	}, linPage(false, "")))
	defer srv.Close()

	src := newLinearSource("api-key", "TEAM-UUID", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep
	rows := drainRows(src)

	if err := firstRowErr(rows); err != nil {
		t.Fatalf("page one's decode error ended the import: %v", err)
	}
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.Issue.Identifier)
	}
	if strings.Join(ids, ",") != "ENG-1,ENG-2" {
		t.Fatalf("identifiers = %v, want [ENG-1 ENG-2] — page two was never fetched", ids)
	}
}

// (5) THE FLOOR, AND IT MUST STAY GREEN IN BOTH DIRECTIONS. Every priority on Linear's documented
// scale, in the integral serialisation every existing fixture uses, maps exactly as it did before.
// A fix that widened the decoder and moved a mapping would be caught here and nowhere else in this
// file — every case above sends a shape the old code could not read at all.
func TestLinearSource_TheIntegralPriorityScaleIsUnchanged(t *testing.T) {
	want := []model.IssuePriority{
		model.PriorityNone,   // 0
		model.PriorityUrgent, // 1
		model.PriorityHigh,   // 2
		model.PriorityMedium, // 3
		model.PriorityLow,    // 4
	}
	var nodes []string
	for i := range want {
		nodes = append(nodes, linNodePrioRaw(fmt.Sprintf("ENG-%d", i), "Todo", fmt.Sprintf("%d", i)))
	}
	srv := httptest.NewServer(cannedPages([]string{linPage(false, "", nodes...)}, linPage(false, "")))
	defer srv.Close()

	src := newLinearSource("api-key", "TEAM-UUID", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep
	rows := drainRows(src)

	if err := firstRowErr(rows); err != nil {
		t.Fatalf("unexpected source error on the integral scale: %v", err)
	}
	if len(rows) != len(want) {
		t.Fatalf("yielded %d issue(s), want %d", len(rows), len(want))
	}
	for i, w := range want {
		if got := rows[i].Issue.Priority; got != w {
			t.Errorf("priority %d mapped to %q, want %q", i, got, w)
		}
		if len(rows[i].Notes) != 0 {
			t.Errorf("priority %d produced notes %+v, want none — every value here is on Linear's scale", i, rows[i].Notes)
		}
	}
}
