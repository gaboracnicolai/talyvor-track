-- 0026_import_jobs_warnings.sql — W3.4: a degraded import must not report itself as a clean one.
-- ADDITIVE ONLY.
--
-- import_jobs already records imported/skipped/failed + error_summary, all of which describe rows
-- that did NOT land. There was nowhere to record a row that DID land with a field the mapper could
-- not place — so an issue whose provider status Track does not know (measured on 014b6e2: 11 of 22
-- realistic Jira statuses, 7 of 13 Linear states, "Deployed" and Linear's default "Duplicate"
-- among them) was written as `backlog` and the job finished
-- {status:'succeeded', imported:N, skipped:0, failed:0, error_summary:NULL}.
--
-- TEXT[] rather than a joined TEXT: the whole value of this field is the list, and error_summary
-- already demonstrates the cost of collapsing a list into one string (it keeps only the FIRST
-- error). The importer counts per distinct (field, value), so the array is bounded by the provider's
-- vocabulary, not by the row count — a 10,000-row import of one unknown status stores one element.
ALTER TABLE import_jobs
    ADD COLUMN IF NOT EXISTS warnings TEXT[] NOT NULL DEFAULT '{}';
