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
// ⚠⚠ THE POPULATION EVERY FIGURE BELOW WAS COUNTED OVER IS A CACHE, AND A CACHE ENTRY IS NOT AN
// EXPORT. scripts/w34-jira-csv-corpus-probe.py keys each fetch on `sha256(repo \t path)` (:105), so
// the SAME export committed to two repositories — a fork, a vendored copy, a re-upload — lands as
// two entries, and every census that walks the directory counts them as two independent instances.
// The probe's own header says what makes second-hand bytes into evidence is "agreement — or
// disagreement — ACROSS INSTANCES THAT HAVE NEVER MET"; two copies of one file have not met anybody.
//
// MEASURED by content sha256 (jira_csv_date_corpus_census_test.go now counts both populations in one
// walk, and distinctByContent is the rule): the cache holds 346 files / 317 distinct byte-contents;
// the 301 that pass the genuine-export predicate are 285 distinct exports, and the 16 surplus copies
// carry 2,188 of the 17,898 data rows (12.2%). The Linear cache is the same shape: 46 files / 41
// distinct, 45 carrying `ID` / 40 distinct, 280 of 3,164 rows.
//
// EVERY FIGURE BELOW IS THEREFORE GIVEN BOTH WAYS — as-counted first, because that is what the
// merges that produced them measured and the record should stay legible, then over distinct exports,
// which is the population the argument is actually about.
//
// ─── the census ────────────────────────────────────────────────
//
// THE SHIPPED parseJiraCSVTime applied to every non-empty cell of the four date columns
// jiraRowMapper reads, over the 301 corpus files whose header carries all of Summary + Issue key +
// Status — 285 of them distinct (that predicate is what excludes the ~39 non-Jira CSVs in the
// cache — ML datasets with headers like `title_Xi` and `deepseek_fewshot_sp` — so the denominator is
// exports, not files; what it does NOT exclude is a second copy of an export it accepts):
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
// ⚠ THE PER-COLUMN TABLE IS THE HISTORICAL RECORD OF WHAT THE ONE-LAYOUT LIST DID AND IS NOT
// RESTATED PER COLUMN: I re-measured the totals, not the four splits, and a half-corrected table is
// worse than one labelled. Its denominators carry the same inflation — over distinct exports the
// same one-layout list accepts 1,654 of 36,128 cells and refuses 34,474.
//
// After the six added layouts: 31,090 of the 37,255 recovered (83.5%); 34,358 of all 40,523 cells
// (84.8%) now parse. OVER DISTINCT EXPORTS: 29,647 of 34,474 recovered (86.0%); 31,301 of all
// 36,128 cells (86.6%). ⚠ THE ACCEPTANCE RATE IS HIGHER, NOT LOWER, ONCE THE COPIES COME OUT — the
// duplication was not flattering the parser, and that is exactly why nobody would have gone looking
// for it from the headline number.
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
// 6,165 cells across 37 shapes remain — 4,827 over distinct exports. They are not a long tail of
// rare serialisations — they are almost entirely ONE class, and it is a class no parser can resolve
// from the bytes.
//
// ⚠⚠ THIS LIST IS RANKED BY THE DEDUPLICATED COUNT AND THE RANKING IS NOT THE ONE THE CACHE GAVE,
// which is why it is restated rather than annotated. Three of the seven shapes the as-counted table
// named were one export counted three times; the second-largest was really the fourth, and TWO
// shapes that the as-counted top seven never mentioned belong in it. A table that feeds a decision
// about which serialisations are worth handling was partly a table of which files had been cached
// twice.
//
//	distinct  as-counted
//	   1,806       1,806  12-12-2024 14:42     dd-mm-yyyy or mm-dd-yyyy — the example is literally undecidable
//	     692         692  7/9/2026 10:00       undecidable
//	     638         638  7/15/2026 20:53      m/d here
//	     276         828  22/08/2024           22 > 12 here, but the FORMAT is not thereby determined
//	     265         265  7/9/2026 9:50
//	     252         252  7/14/2026 3:02       — not in the as-counted top seven
//	     151         151  10/15/2020 21:06     m/d here — not in the as-counted top seven
//	     129         387  5/28/25 17:00        m/d here
//	      93         279  7/02/2024
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
//	distinct  as-counted
//	      90         180  2025-11-15T04:00:00.000+0000   collides with jira.go's API layout list
//	      19          19  02/September/24 9:47 AM        full month name; one owner, below the noise floor
//
// The first is the interesting one. jira_csv_dates_test.go asserts IN BOTH DIRECTIONS that the CSV
// and API layout lists stay DISJOINT — "two provenances, two lists, and neither pretends to be the
// other" — and that invariant is the reason this merge does not add it. ⚠ BUT THE INVARIANT'S OWN
// PREMISE ("a CSV export of the same instance emits neither shape") IS MEASURED FALSE: cells of
// exactly that shape sit in real CSV exports. Whether the disjointness still earns its keep is a
// separate question from this finding, so it is written down here rather than settled quietly
// inside a date fix.
//
// ⚠⚠ AND THE 180 WAS THE DUPLICATION, WHICH CHANGES WHICH QUESTION THIS IS. The two cache entries
// are byte-identical copies of ONE export, so the population behind the shape is 90 cells from ONE
// owner, not 180 from two — the same order as the full-month shape the line beneath it dismisses as
// "one owner, below the noise floor", and the two were being weighed on different scales. Whoever
// re-argues the disjointness should re-argue it on 90 cells from one owner.
//
// ─── what the copies do to the rest of the package, MEASURED and NOT changed here ───
//
// Switching every corpus census to the deduplicated walk was run as an experiment and reverted, so
// this is a measurement rather than a prediction: SEVEN OF THE EIGHT corpus census files in this
// package then FAIL, because their pinned floors sit above the population that remains.
//
//	custom fields · dropped objects · issue links (jira)   286 files / 16,619 rows  vs floors 290-300 / 17,000-18,000
//	duplicate identifiers (jira)                           289 files / 15,735 rows  vs 300 / 17,000
//	duplicate identifiers (linear)                          40 files /  2,819 rows  vs  44 /  3,000
//	unread refs (linear)                                    40 files /  2,819 rows  vs  45 /  3,000
//	wide rows (linear)                                      40 genuine / 2,884 rows vs  45 /  3,000
//	this census                                            285 exports              vs 300
//
// The eighth (done-without-completion) passes: its floors are on counts small enough to survive.
// ⚠ SO THOSE FLOORS ARE GREEN TODAY PARTLY BECAUSE COPIES ARE COUNTED — they are floors under a
// population that includes them. Moving fifteen calibrated constants at once is a recalibration of
// every census in the package and it is NOT taken inside a date-census change: it is measured,
// written here and in the queue, and left as one decision with one diff.
