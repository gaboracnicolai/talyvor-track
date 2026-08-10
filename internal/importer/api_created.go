package importer

import (
	"strings"
	"time"
)

// api_created.go — the issue's OPENING TIME on the two API transports.
//
// ⚠ WHY THIS IS A SECOND IMPLEMENTATION AND NOT A CALL INTO jira_csv_created.go. That file reads a
// CSV columnIndex and reports through CSV-shaped vias ("no `Created` column in this export"). An
// operator whose Jira API import lost its opening times must not be told to go check a column in an
// export they never made. The two transports fail in DIFFERENT WAYS and must say so — the same rule
// that keeps viaStateType separate from viaCategory, and fieldCompletionTime separate from
// fieldResolutionDate. What IS shared is the field NAME (fieldCreated) and the parsers, because
// those are facts about the value rather than about the transport.
//
// MEASURED AT THE WIRE, both halves negative-controlled and both RE-RUN for this field rather than
// inherited from a sibling merge — scripts/w34-jira-api-created-probe.py and
// scripts/w34-linear-api-created-probe.py. See api_created_job_test.go's header for the numbers.

const (
	// Jira's API name for the field. Distinct from jira_csv_created.go's "Created" header: the JSON
	// key is lowercase and the sentence an operator needs points at a `fields` list, not a column.
	jiraAPICreatedField = "created"

	// Linear's, likewise.
	linearAPICreatedField = "createdAt"
)

const (
	// The Jira response carried no `created` key at all. This is the STRUCTURAL-ZERO line, and on
	// this field it is load-bearing in a way it is on no other: created_at is DEFAULTed in Postgres,
	// so without this line "Track read your opening times" and "Track recorded every one of these as
	// opened today" produce byte-identical database rows AND byte-identical reports.
	//
	// ⚠ AND IT IS NOT UNREACHABLE DEFENSIVE CODE, which is the usual reason a line like this gets
	// dropped in review. MEASURED against a real Jira Cloud: an unknown field name in the `fields`
	// list is SILENTLY IGNORED (HTTP 200, the key simply absent from the response). So a future
	// rename or a typo in jiraFields produces exactly this state and produces no error anywhere.
	viaNoCreatedField = "no-created-field"

	// Linear declares Issue.createdAt as `DateTime!` — NON_NULL, measured by unauthenticated
	// introspection. A null here therefore does NOT mean "this issue has no opening time"; it means
	// the response did not come from the schema this importer was written against. That is a
	// different sentence from Jira's absent-key case and gets its own via rather than sharing one.
	viaNullCreatedAt = "null-createdAt"
)

// jiraAPICreated maps Jira's `created` to the instant the PROVIDER opened the issue.
//
// A value the pinned layouts refuse is REPORTED, never silently defaulted — #74's rule. It carries
// more weight here than anywhere else in this package, because the silent default is not a null
// anybody can spot but a plausible-looking timestamp that makes every cycle-time number wrong.
func jiraAPICreated(raw string) (time.Time, []FieldNote) {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, []FieldNote{{Field: fieldCreated, Via: viaNoCreatedField}}
	}
	t, ok := parseJiraTime(raw)
	if !ok {
		return time.Time{}, []FieldNote{{Field: fieldCreated, Value: raw, Via: viaUnparseableDate}}
	}
	return t, nil
}

// linearAPICreated maps Linear's `createdAt`. Same two failures, different first sentence — see
// viaNullCreatedAt.
func linearAPICreated(raw string) (time.Time, []FieldNote) {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, []FieldNote{{Field: fieldCreated, Via: viaNullCreatedAt}}
	}
	t, ok := parseLinearTime(raw)
	if !ok {
		return time.Time{}, []FieldNote{{Field: fieldCreated, Value: raw, Via: viaUnparseableDate}}
	}
	return t, nil
}

// ⚠ THE ZERO VALUE IS THE SIGNAL, IN BOTH MAPPERS, AND THAT IS DELIBERATE. issue.Store reads a zero
// CreatedAt as "nobody supplied one" and takes the column DEFAULT. So a refused or absent value
// falls back to EXACTLY today's behaviour — this merge cannot make an import worse than it already
// is — and the warning line is the whole of what stops that fallback from being silent.
