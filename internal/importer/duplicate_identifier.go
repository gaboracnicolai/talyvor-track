package importer

// duplicate_identifier.go — ONE EXPORT NAMING THE SAME ISSUE ON MORE THAN ONE ROW.
//
// ⚠⚠ MEASURED WHOLE-POPULATION over the cached real corpus with the shipped column index, and then
// END TO END through the async runner on real Postgres, before a line of this file existed
// (duplicate_identifier_corpus_census_test.go and duplicate_identifier_job_test.go):
//
//	JIRA    3 of 305 exports carrying `Issue key`   ·  71 of 17,923 rows land on a key an EARLIER
//	                                                   row of the SAME file already wrote
//	LINEAR  3 of  45 exports carrying `ID`          ·  96 of  3,099 rows do the same
//
// The largest is Atlassian's own SourceTree-Windows export (900 rows, 68 keys on two rows each):
// through the runner it reported `partial imported=893 skipped=0 failed=7` — the 7 are titleless
// rows, unrelated and unchanged — and left 825 issues, with 44 warnings, none of them about this.
// A Linear export left 118 issues after reporting `succeeded imported=185 skipped=0 failed=0` with
// 8 warnings, none of them about this either.
//
// ⚠ WHAT IS WRONG IS THE REPORT, NOT THE WRITE. `imported` counts ROWS WRITTEN and a workspace
// holds ISSUES; the upsert collapsing two rows that name one issue is the conflict arm working. The
// gap between the two numbers is carried by no counter (`skipped`/`failed` are both 0), no status
// (`succeeded`) and, until this file, no sentence — so an operator reconciling 185 against 118 has
// nothing in the job row to reconcile it WITH.
//
// ⚠ AND THE SURVIVING ROW IS NEITHER EXPORT ROW. UpsertByIdentifier's conflict arm CLOBBERS title,
// description and labels and PRESERVES status, priority, due_date, completed_at and created_at, so
// the row left behind carries the LATER row's title on the EARLIER row's workflow state — a
// combination that appears on no row of the export. Pinned by
// TestJobRow_TheSurvivingRowIsNeitherExportRow, so the day the conflict arm's SET list changes, the
// sentence below stops being true and a test says so.
//
// ⚠ IT REPORTS, IT DOES NOT REFUSE, and the precedent is two files old rather than invented here:
// csvSource.Next's short-row branch REPLACED a refusal (the refusal was the defect) and its
// wide-row twin reports a row it cannot repair. WHICH of two rows naming one issue should win —
// first, last, or refuse the pair — is a product decision with three defensible answers, and it is
// written up in the queue rather than made silently in a row loop. No row changes where it lands
// and no count changes: imports are byte-for-byte identical to what they were, plus a warning.
//
// ⚠ THE DETECTION IS IN run(), NOT IN csvSource, AND THAT IS DELIBERATE. Identifier is the routing
// key of the shared write pipeline, so the collision belongs to the pipeline: putting it in the CSV
// source would leave `linear_api` and `jira_api` — the two transports that page a provider and can
// legitimately see one issue twice across a page boundary — with no report at all.
//
// ⚠ WHAT IT COSTS, said rather than left to be discovered: one map entry per DISTINCT identifier
// this import writes, held for the length of the run. That is bounded by the source — 64 MiB for a
// CSV job (jobMaxUploadBytes) and the provider's own result set for an *_api one — and it is the
// only way to answer "have I written this key already"; a second pass over the source is not
// available, because an IssueSource is a one-shot stream by construction.

const (
	// fieldDuplicateIdentifier names the SUBJECT of the note. It is the noun that renders in
	// renderWarnings' over-the-bound line ("+ N further distinct identifier finding(s) …"), so it
	// is the operator's word for the thing, not a column name: neither provider spells this column
	// the same way (`Issue key` for Jira, `ID` for Linear) and the *_api transports have no column
	// at all.
	fieldDuplicateIdentifier = "identifier"

	// viaDuplicateInSameImport is the path: this import ALREADY wrote a row under this identifier.
	//
	// ⚠ IT IS NOT THE SAME EVENT AS overwroteExisting, and the two must not share a via. That flag
	// is true of a row that overwrote an issue ANY earlier import (or this one) put there, and the
	// notes hung off it — viaNoDescriptionColumn, viaNoLabelsColumn — say "already in Track", which
	// is a true sentence about a re-import and a MISLEADING one here: the issue was put in Track by
	// this same job, seconds earlier, by a row of the same file. That imprecision is REPORTED in
	// the queue and deliberately not repaired here; those sentences are pinned by tests of their
	// own and re-wording them is a different merge from measuring this.
	viaDuplicateInSameImport = "duplicate-identifier-in-same-import"
)
