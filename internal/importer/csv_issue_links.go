package importer

import (
	"sort"
	"strings"
)

// csv_issue_links.go — THE SIXTH LOSS CLASS, AND THE FIRST ONE THAT IS NOT ABOUT A COLUMN ON THE
// ISSUE: an issue LINK.
//
// ⚠⚠ THE FINDING, MEASURED WHOLE-POPULATION OVER BOTH REAL CORPORA BEFORE A LINE OF THIS FILE
// EXISTED. Rows whose export names another issue this one is linked to, counted by
// csv_issue_links_corpus_census_test.go over THE SHIPPED MAPPERS:
//
//	Jira    3,789 of 18,807 rows (20.1%) in 188 of 302 exports  — 2,023 of them a BLOCKS-family link
//	Linear  1,403 of  3,099 rows (45.3%) in  27 of  45 exports  — 707 of them `Blocked by`
//
// Every one of those rows imported with `succeeded imported=N` and an empty warnings list.
//
// ⚠ TWO INSTRUMENTS, TWO POPULATIONS, AND THEY RECONCILE ROW FOR ROW. The census above selects Jira
// exports on `Summary`+`Status`, the way every census in this package does. An independent
// instrument written first and in another language (~/talyvor-queue/w34-links-census-c2b7.py;
// negative-controlled before it was believed — a fabricated column answered 0, a fabricated corpus
// directory REFUSED rather than answering 0, an emptied link predicate took the link figure to 0,
// and `Summary` reproduced as a positive) selects on Jira's own `Issue id`+`Issue key` surrogate and
// reads 3,793 of 17,923 rows in 189 of 305 files. The gap is the file selection and nothing else:
// three files carry the surrogate without `Summary` (25 rows, 4 of them linked), one carries
// `Summary` without the surrogate (909 rows, 0 linked), one more is header-only. 17,923 − 25 + 909
// = 18,807 and 3,793 − 4 = 3,789, exactly.
//
// ⚠ IT IS NOT "ANOTHER UNREAD COLUMN", AND THAT IS WHY IT IS A SECOND FILE AND A SECOND SENTENCE.
// The five references in csv_unread_refs.go are columns ON the issue: they end up NULL (or, for the
// creator, stamped), and an operator can go look at the row. A link is a row in `issue_relations` —
// the table Track's RelationsSection, DependencyGraph and Kanban BlockerAlert read, and the one
// issue.Store's blockedChecker asks before it sets Issue.IsBlocked. Nothing on the imported issue is
// empty. The edge is absent, and the issue that arrived saying it was BLOCKED reads as unblocked in
// every one of those surfaces.
//
// ⚠ IT REPORTS, IT DOES NOT MAP, for the same reason the references do not. Creating the relation
// needs the target issue resolved by identifier INSIDE the workspace (a link can name an issue in
// another project that this import never carries), a policy for the unresolved case (skip / defer /
// create a stub), and a mapping from the provider's link TYPE onto the five Track models — Jira's
// corpus alone names 55 distinct link columns and Track has `blocks`, `blocked_by`, `relates_to`,
// `duplicates`, `clones`. Three product decisions. Telling the operator the link did not arrive
// needs none of them.
//
// ⚠ CSV ONLY, DELIBERATELY — the same asymmetry csv_unread_refs.go states. Neither API transport
// REQUESTS links (`jiraFields` and `linearIssuesQuery` name none), so an API import has no evidence
// for a count and would be asserting one.

// fieldIssueLinkRef names the Track object the value would have created, in the operator's Track
// vocabulary: `issue_relations` sends someone to a schema, "issue link" sends them to the issue.
const fieldIssueLinkRef = "issue link"

// viaIssueLinkNotRead is the path, and it is a SEPARATE constant from viaColumnNotRead because the
// sentence that one renders would be FALSE here. "their Track assignee is left empty" is exactly
// what happened to an unread assignee; there is no field on an issue that holds a link, so there is
// nothing for an operator to find empty. See FieldNote.render.
const viaIssueLinkNotRead = "issue-link-not-read"

// jiraIssueLinkPrefixes — Jira spells the column `Outward issue link (<Link Type>)` /
// `Inward issue link (<Link Type>)`, one occurrence per link, and the link TYPE is configured per
// instance.
//
// ⚠⚠ A PREFIX RULE RATHER THAN A LIST OF LITERALS IS NOT A SHORTCUT — IT IS THE ONLY RULE THAT CAN
// BE COMPLETE. The 302 genuine cached exports carry 55 DISTINCT link-column spellings (`Blocks`,
// `Cloners`, `Issue split`, `Relates`, `Gantt End to Start`, `Test case`, …), and the next Jira
// instance names its own — Jira's own UI lets an administrator add a link type by name. A list would
// report the types that happened to be sampled and go silent on the rest, which is the defect #117
// fixed for the parent link one grain over; shipping the list
// shape here would have re-created it on purpose. TestJiraIssueLinkSpellings_MatchesAnUnseenLinkType
// is that argument as a guard.
var jiraIssueLinkPrefixes = []string{"outward issue link (", "inward issue link ("}

// linearIssueLinkColumns — Linear's link columns are fixed names, and they are the reason the two
// providers do not share a rule. All three appear in the 34-column published header shape and NONE
// in the 30-column one, which is why 14 of the 45 real exports carry no link column at all and can
// lose nothing (31 carry all three; 27 of those populate one on at least one row).
//
// ⚠ WHAT IS NOT HERE IS MEASURED, NOT OVERLOOKED. `Blocking`, `Blocks`, `Duplicates`, `Relates to`
// and `Clones` — the other half of every pair Linear's UI shows — appear in ZERO real export
// headers, so an entry for them would report nothing and
// TestIssueLinkColumns_AppearInARealExportHeader refuses one.
var linearIssueLinkColumns = []string{"Blocked by", "Related to", "Duplicate of"}

// jiraIssueLinkSpellings returns the link columns THIS export's header carries, lowercased and
// sorted. Sorted because it is derived from columnIndex, whose iteration order is a map's: an
// unsorted result would make two imports of one file emit their notes in different orders, and the
// warnings a job row carries are read by diffing them.
//
// ⚠ IT IS DISCOVERED FROM THE HEADER RATHER THAN PASSED IN BECAUSE A rowMapper NEVER SEES THE
// HEADER — it receives columnIndex and a row (see csv.go). columnIndex is therefore the only place
// a spelling can come from, and columnIndex lowercases every key it holds.
func jiraIssueLinkSpellings(ci columnIndex) []string {
	var out []string
	for column := range ci {
		for _, prefix := range jiraIssueLinkPrefixes {
			if strings.HasPrefix(column, prefix) {
				out = append(out, column)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// issueLinkNotes reports ONE note per link column spelling this row populates.
//
// ⚠ ONE NOTE PER SPELLING, NOT ONE PER ROW, AND THE DIFFERENCE IS INFORMATION THE OPERATOR NEEDS.
// Two spellings of the PARENT are one dropped reference and get one note (csv_unread_refs.go);
// `Outward issue link (Blocks)` and `Inward issue link (Relates)` on one row are TWO different
// links to two different issues. The bound still holds by construction: renderWarnings groups on
// (field, via) and shows at most maxWarningExemplars distinct values with a summary line for the
// rest, so a header carrying 29 link columns cannot flood the report.
//
// ⚠⚠ THE GATE IS ANY OCCURRENCE, WHICH IS WHY THIS DOES NOT USE ci.get. Jira emits ONE COLUMN PER
// LINK under the same name — up to 29 occurrences of one spelling in a single real header — and
// ci.get names the FIRST (csv.go:422), so a row whose first cell is empty and whose second holds
// the link would produce nothing. On today's corpus the two rules agree on every row (measured: the
// first-occurrence count equals the any-occurrence count for every link column and for every
// unread-reference column), so this choice buys 0 rows now and is pinned by
// TestIssueLinks_ARepeatedColumnIsReportedFromAnyOccurrence rather than by a corpus figure.
//
// The VALUE is the column name, lowercased as columnIndex holds it — never the cell. The cell holds
// another issue's KEY, and putting it here would both unbound the report (#80) and copy issue keys
// into a job row every member of the workspace can read.
func issueLinkNotes(ci columnIndex, row []string, spellings []string) []FieldNote {
	var out []FieldNote
	for _, column := range spellings {
		if len(ci.getAll(row, column)) == 0 {
			continue
		}
		out = append(out, FieldNote{
			Field: fieldIssueLinkRef,
			Value: strings.ToLower(strings.TrimSpace(column)),
			Via:   viaIssueLinkNotRead,
		})
	}
	return out
}
