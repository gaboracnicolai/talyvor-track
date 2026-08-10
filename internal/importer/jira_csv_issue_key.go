package importer

// jira_csv_issue_key.go — the column a Jira CSV export uses to NAME an issue.
//
// THE COLUMN SPELLING IS MEASURED, NOT GUESSED. scripts/w34-jira-csv-export-probe.py downloads a
// real export from jira.atlassian.com (`jira.issueviews:searchrequest-csv-all-fields`, no
// credential) and its header carries 279 columns with `Issue key` among them, holding values like
// `JRASERVER-64802`. That probe has printed this column since #78 — it is in the script's own
// output line — and six merges read past it.
//
// ⚠ WHY IT IS A CONSTANT IN ITS OWN FILE AND NOT AN INLINE LITERAL. Identifier is the ROUTING KEY
// of source.go's write pipeline: a row carrying one goes through issue.Store.UpsertByIdentifier
// (INSERT-or-UPDATE on the provider key, guarded by #71's refuse-to-clobber-a-human predicate) and
// a row without one goes through issue.Store.Create, which DERIVES `<team>-<n>` and overwrites
// whatever the caller supplied. So this string decides which of two different INSERT statements a
// CSV import takes, and it deserves a name and a stated provenance rather than a literal in a
// mapper.
//
// ⚠ THE NEGATIVE IS PART OF THE MEASUREMENT. The same export carries `Issue id` — the numeric
// surrogate (e.g. 1284563) — immediately beside this column, and `Parent key` further along. Both
// contain "key" or "id"; neither is the issue's name. buildIndex matches the FULL header
// case-insensitively rather than by substring, and jira_csv_issue_key_test.go holds that apart.
const jiraCSVIssueKeyColumn = "Issue key"
