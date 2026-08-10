package importer

// linear_csv_issue_id.go — the column a Linear CSV export uses to NAME an issue.
//
// THE COLUMN SPELLING IS MEASURED, NOT GUESSED, AND THE MEASUREMENT OVERTURNS THIRTY MERGES OF
// STOP REASON. #98 gave `jira_csv` its provider key and deliberately left this transport alone,
// writing: "Linear's export header cannot be measured from this environment (no tenant, no
// anonymous export view)". That was a true statement about THIS MACHINE'S ACCESS and a false one
// about the question. scripts/w34-linear-csv-export-probe.py reads REAL Linear CSV exports that
// other people's tenants produced and committed to public repositories:
//
//	45 files parsed as Linear exports, from 16 distinct team prefixes across unrelated tenants
//	SIX distinct header shapes (29, 30 and 34 columns — Linear's export has grown over time)
//	`ID` present in 45 of 45, at column index 0 in 45 of 45
//	3,026 data rows: 2,977 carry an `ID` matching ^PREFIX-<int>$ (AWA-27, SAN-617, KAP-5, KNO-8…)
//
// ⚠ THE PROVENANCE IS WEAKER THAN THE JIRA CONSTANT'S AND IS NOT DRESSED UP AS EQUAL — the
// overclaim #75 caught in this package is the one worth not repeating. jiraCSVIssueKeyColumn was
// read off bytes a real Jira server emitted to this machine on request. This is read off bytes
// OTHER tenants' Linear emitted to THEM, which they then committed. What makes it evidence rather
// than folklore is AGREEMENT ACROSS TENANTS THAT HAVE NEVER MET, six header shapes wide, plus the
// probe's three negative controls (a fabricated column set finds 0 files; a fabricated repository
// and a fabricated path both refuse) so that a search returning nothing cannot read as a clean
// answer.
//
// ⚠ AND THE FAIL-SAFE IS WHAT MAKES IT SAFE UNDER THE RESIDUAL UNCERTAINTY. buildIndex matches the
// FULL header case-insensitively, so an export whose header this constant does not name yields ""
// — which is EXACTLY today's behaviour, the Create branch and a Track-derived `<team>-<n>`. The
// change can only ever move a row from "duplicated on re-import" to "idempotent"; it cannot break
// an export shape nobody here has seen.
//
// ⚠ WHY IT IS A CONSTANT IN ITS OWN FILE AND NOT AN INLINE LITERAL, the same reason #98 gave:
// Identifier is the ROUTING KEY of source.go's write pipeline. A row carrying one goes through
// issue.Store.UpsertByIdentifier (INSERT-or-UPDATE on the provider key, guarded by #71's
// refuse-to-clobber-a-human predicate); a row without one goes through issue.Store.Create, which
// DERIVES `<team>-<n>` and discards whatever the caller supplied. This string decides which of two
// different INSERT statements a Linear CSV import takes.
//
// ⚠⚠ THE NEGATIVE IS A BIGGER PART OF THE MEASUREMENT HERE THAN IT WAS FOR JIRA, because this
// header is two characters long. The SAME export carries three other columns whose lowercased
// names CONTAIN "id": `Project ID`, `Project Milestone ID` and `UUID` — and the last of those is
// the issue's actual UUID, sitting in the same row, looking every bit like a stable provider key
// while being the wrong one. A mapper that matched by substring rather than by full header would
// satisfy every positive assertion in this package and key the whole backlog off a project.
// linear_csv_issue_id_test.go and linear_csv_issue_id_job_test.go hold those apart, at the mapper
// and at the database respectively.
const linearCSVIssueIDColumn = "ID"
