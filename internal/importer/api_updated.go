package importer

import (
	"strings"
	"time"
)

// api_updated.go — the issue's LAST-TOUCHED instant on the two API transports.
//
// ⚠ WHY THIS IS A SECOND IMPLEMENTATION AND NOT A CALL INTO jira_csv_updated.go, unchanged from
// api_created.go's argument: that file reads a CSV columnIndex and reports through CSV-shaped vias
// ("no `Updated` column in this export"). An operator whose Jira API import lost its last-touched
// instants must not be told to go check a column in an export they never made. What IS shared is
// the field NAME (fieldUpdated) and the parsers, because those are facts about the value rather
// than about the transport.
//
// ⚠ NO NEW WIRE PROBE WAS RUN FOR THIS FIELD AND THAT IS STATED RATHER THAN HIDDEN. #85 scoped this
// merge and recorded why neither transport needs one: Jira's `updated` rides the SAME `fields` list
// #74 measured costs no query change, and #84 already measured Linear's `updatedAt` as `DateTime!`
// in its unauthenticated introspection call. What is NOT inherited is the assertion that the
// shipped request actually asks — see TestJiraRequest_AsksForTheUpdatedField.

const (
	// Jira's API name for the field. Distinct from jira_csv_updated.go's "Updated" header.
	jiraAPIUpdatedField = "updated"

	// Linear's, likewise.
	linearAPIUpdatedField = "updatedAt"
)

const (
	// The Jira response carried no `updated` key at all. STRUCTURAL-ZERO, and load-bearing for the
	// same reason viaNoCreatedField is: updated_at is DEFAULTed in Postgres, so without this line
	// "Track read your last-touched instants" and "Track recorded every one of these as touched at
	// the moment of import" produce byte-identical database rows AND byte-identical reports.
	//
	// ⚠ NOT UNREACHABLE DEFENSIVE CODE. MEASURED against a real Jira Cloud in #84: an unknown field
	// name in the `fields` list is SILENTLY IGNORED (HTTP 200, key simply absent). A future rename
	// or a typo in jiraFields produces exactly this state and produces no error anywhere.
	viaNoUpdatedField = "no-updated-field"

	// Linear declares Issue.updatedAt as `DateTime!` — NON_NULL. A null here therefore does NOT mean
	// "this issue has never been touched"; it means the response did not come from the schema this
	// importer was written against. A different sentence from Jira's absent-key case, so a different
	// via rather than a shared one.
	viaNullUpdatedAt = "null-updatedAt"
)

// jiraAPIUpdated maps Jira's `updated` to the instant the PROVIDER last touched the issue.
//
// A value the pinned layouts refuse is REPORTED, never silently defaulted — #74's rule. The silent
// default here is not a null anybody can spot: it is a plausible-looking timestamp that reorders
// the product's main screen and relabels every row "updated just now".
func jiraAPIUpdated(raw string) (time.Time, []FieldNote) {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, []FieldNote{{Field: fieldUpdated, Via: viaNoUpdatedField}}
	}
	t, ok := parseJiraTime(raw)
	if !ok {
		return time.Time{}, []FieldNote{{Field: fieldUpdated, Value: raw, Via: viaUnparseableDate}}
	}
	return t, nil
}

// linearAPIUpdated maps Linear's `updatedAt`. Same two failures, different first sentence — see
// viaNullUpdatedAt.
func linearAPIUpdated(raw string) (time.Time, []FieldNote) {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, []FieldNote{{Field: fieldUpdated, Via: viaNullUpdatedAt}}
	}
	t, ok := parseLinearTime(raw)
	if !ok {
		return time.Time{}, []FieldNote{{Field: fieldUpdated, Value: raw, Via: viaUnparseableDate}}
	}
	return t, nil
}

// ⚠ THE ZERO VALUE IS THE SIGNAL IN BOTH MAPPERS, as in api_created.go. issue.Store reads a zero
// UpdatedAt as "nobody supplied one" and takes the column DEFAULT, so a refused or absent value
// falls back to EXACTLY today's behaviour — this merge cannot make an import worse than it already
// is — and the warning line is the whole of what stops that fallback from being silent.
