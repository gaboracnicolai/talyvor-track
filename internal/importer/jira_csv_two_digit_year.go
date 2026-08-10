package importer

// jira_csv_two_digit_year.go — the census behind jiraCSVTimeLayouts' six added entries, and the
// remainder this merge deliberately DOES NOT guess at.
//
// ⚠ THE FINDING IS NOT "A LAYOUT WAS MISSING", IT IS "THE LIMIT THAT NAMED IT WAS NEVER MEASURED".
// jira_csv_dates.go has said since #74 that the CSV date format is a per-instance preference and
// that "another tenant's export may not be this shape at all". Four merges quoted that sentence as
// a residual risk. Nobody applied the parser to a second tenant — and the corpus that would have
// answered it was already on disk, cached for this package's OWN status-category and priority
// censuses (jira_csv_corpus_census_test.go). The evidence was there; the question was not.
//
// ─── the census ────────────────────────────────────────────────
//
// THE SHIPPED parseJiraCSVTime applied to every non-empty cell of the four date columns
// jiraRowMapper reads, over the 301 corpus files whose header carries all of Summary + Issue key +
// Status (that predicate is what excludes the ~39 non-Jira CSVs in the cache — ML datasets with
// headers like `title_Xi` and `deepseek_fewshot_sp` — so the denominator is exports, not files):
//
//	                 accepted   refused    %refused   files with column   files losing EVERY cell
//	Created             1,634    15,418       90.4%                 299                       296
//	Updated             1,634    13,738       89.4%                 293                       290
//	Due Date                0     2,910      100.0%                 119                       119
//	Resolved                0     5,189      100.0%                 178                       178
//	TOTAL               3,268    37,255       91.9%
//
// 298 of 301 exports carry at least one refused cell. The two commonest refused shapes are
// `24/Jun/26 5:39 PM` (15,915 cells) and `05/Jun/26 11:20 PM` (10,998) — both the two-digit-year
// rendering of the shape that was already pinned.
//
// After the six added layouts: 31,090 of the 37,255 recovered (83.5%); 34,358 of all 40,523 cells
// (84.8%) now parse.
//
// ⚠ THE UNIT IS A CELL, NOT AN ISSUE AND NOT A FILE. One issue contributes up to four cells and the
// counts above are not de-duplicated by value, because the question is how much of a real import
// lands — not how many distinct instants exist. The per-file columns are given beside them so the
// two readings cannot be confused.
//
// ─── why the two losses are worse than either alone ────────────
//
// A refused Created falls to `issues.created_at DEFAULT NOW()` — a PLAUSIBLE instant, not a null,
// so nothing downstream looks broken. A refused Resolved leaves completed_at NULL, and
// analytics.GetTimeToResolution filters `completed_at IS NOT NULL`. Separately each is loud:
// a wrong created_at makes cycle time negative, a missing completed_at removes one row. TOGETHER
// the issue drops out of the report entirely, and an EMPTY resolution report reads as "no resolved
// work in this window" rather than as a parse failure. jira_csv_two_digit_year_job_test.go drives
// exactly that on real Postgres and asserts the median is the true 2,400 hours.
//
// ─── what is still refused, and why it is NOT guessed ──────────
//
// 6,165 cells across 37 shapes remain. They are not a long tail of rare serialisations — they are
// almost entirely ONE class, and it is a class no parser can resolve from the bytes:
//
//	 1,806  12-12-2024 14:42     dd-mm-yyyy or mm-dd-yyyy — the example is literally undecidable
//	   828  22/08/2024           22 > 12 here, but the FORMAT is not thereby determined
//	   692  7/9/2026 10:00       undecidable
//	   638  7/15/2026 20:53      m/d here
//	   387  5/28/25 17:00        m/d here
//	   279  7/02/2024
//	   265  7/9/2026 9:50
//
// ⚠ DAY/MONTH ORDER IS A TENANT SETTING AND GUESSING IT SILENTLY CORRUPTS DATES. Picking d/m or
// m/d globally would move roughly half of these cells to a wrong-but-plausible instant, which is
// strictly worse than the refusal they get today: a refused cell is REPORTED in the warnings
// channel, a mis-parsed one is not reported anywhere and cannot be distinguished afterwards. This
// needs a decision, not a patch — the shapes below are the input to it:
//
//	· a per-import date-order hint (the operator knows their own instance's setting), or
//	· per-FILE inference: a file containing any cell whose first component exceeds 12 has its order
//	  determined for the whole file. Not done here because it is a real design with its own failure
//	  mode (a file where no cell disambiguates), and because it belongs to whoever owns the import
//	  UI — which, per this item's own earlier measurement, does not exist yet.
//
// Two further shapes are unambiguous but were still left out, on purpose:
//
//	   180  2025-11-15T04:00:00.000+0000   collides with jira.go's API layout list
//	    19  02/September/24 9:47 AM        full month name; one owner, below the noise floor
//
// The first is the interesting one. jira_csv_dates_test.go asserts IN BOTH DIRECTIONS that the CSV
// and API layout lists stay DISJOINT — "two provenances, two lists, and neither pretends to be the
// other" — and that invariant is the reason this merge does not add it. ⚠ BUT THE INVARIANT'S OWN
// PREMISE ("a CSV export of the same instance emits neither shape") IS NOW MEASURED FALSE: 180
// cells of exactly that shape sit in real CSV exports. Whether the disjointness still earns its
// keep is a separate question from this finding, so it is written down here rather than settled
// quietly inside a date fix.
