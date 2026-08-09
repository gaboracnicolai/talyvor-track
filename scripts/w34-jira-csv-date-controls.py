#!/usr/bin/env python3
"""w34-jira-csv-date-controls.py — the positive-control campaign for the Jira CSV date capture.

Every control below MUTATES the shipped code, RUNS a named test, and requires it to go RED. A guard
nobody has watched fail is a guard nobody knows still works.

FOUR RULES THIS HARNESS ENFORCES, EACH PAID FOR BY AN EARLIER MERGE IN THIS ITEM:

  · #71 — ASSERT THE ANCHOR COUNT BEFORE EDITING. A substitution that matches nothing edits zero
    bytes and is byte-indistinguishable from a working guard. Every control declares how many times
    its anchor must occur and dies if the file disagrees.
  · #74 — EVERY CONTROL NAMES A TEST THAT MUST STAY GREEN. Otherwise a mutation that simply breaks
    the build reds everything and is scored as a catch.
  · #76 C1 — EVERY RED MUST SAY THE THING IT IS SUPPOSED TO SAY. A control asserts a substring of
    the failure output, so "the guard caught it" is distinguishable from "something else broke".
  · #76 C7 — RESTORE FROM THE ORIGINAL BYTES, NEVER BY REVERSE SUBSTITUTION. `s.replace("", old, 1)`
    INSERTS AT POSITION 0, which silently corrupts the file and makes every later verdict noise.
    Each file's original bytes and sha256 are captured up front and restored verbatim.

    TRACK_TEST_DATABASE_URL=... python3 scripts/w34-jira-csv-date-controls.py
"""

import hashlib
import os
import pathlib
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
CSV_GO = "internal/importer/csv.go"
DATES_GO = "internal/importer/jira_csv_dates.go"
STORE_GO = "internal/issue/store.go"

IMPORTER = "./internal/importer/"
ISSUE = "./internal/issue/"


class Control:
    def __init__(self, name, why, edits, red_test, red_pkg, red_says, green_test, green_pkg):
        self.name, self.why = name, why
        self.edits = edits              # [(path, old, new, expected_count)]
        self.red_test, self.red_pkg = red_test, red_pkg
        self.red_says = red_says        # substring the failure output MUST contain
        self.green_test, self.green_pkg = green_test, green_pkg


# ── the twelve ────────────────────────────────────────────────────────────────────────
CONTROLS = [
    Control(
        "C1  isolation: revert ONLY the SQL half",
        "#74's C9 one write path over. Create is the SECOND copy of the seam whose first copy #74 "
        "fixed. Reverting only the INSERT must red the Postgres-level tests while EVERY source-level "
        "assertion stays green — that is what proves the job test is not the unit test twice.",
        # ⚠ THE ANCHOR IS THE `insertSQL` DECLARATION, NOT THE COLUMN TAIL. That tail occurs TWICE in
        # store.go — Create's INSERT and #74's UPSERT — and the count assertion is the only reason
        # this control did not silently mutate the wrong statement on its first run (#76's C8).
        [(STORE_GO,
          "\tconst insertSQL = `INSERT INTO issues\n"
          "        (workspace_id, team_id, project_id, number, identifier,\n"
          "         title, description, status, priority,\n"
          "         assignee_id, creator_id, cycle_id, parent_id,\n"
          "         due_date, completed_at, lens_feature, labels, sort_order)\n"
          "    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)",
          "\t_ = completedAt // the control's sink: the mutation must be BEHAVIOURAL, never a build error\n"
          "\tconst insertSQL = `INSERT INTO issues\n"
          "        (workspace_id, team_id, project_id, number, identifier,\n"
          "         title, description, status, priority,\n"
          "         assignee_id, creator_id, cycle_id, parent_id,\n"
          "         due_date, lens_feature, labels, sort_order)\n"
          "    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)", 1),
         (STORE_GO,
          "\t\tissue.DueDate, completedAt, issue.LensFeature, issue.Labels, issue.SortOrder,\n\t))\n}",
          "\t\tissue.DueDate, issue.LensFeature, issue.Labels, issue.SortOrder,\n\t))\n}", 1)],
        "TestJobRow_JiraCSV_DatesLandInPostgres", IMPORTER, "completed_at IS NULL in Postgres",
        "TestJiraCSVMapper_CapturesResolvedOnADoneRow", IMPORTER),

    Control(
        "C2  the mapper stops reading Due Date",
        "The other half. Regressing the column read must red both the mapper and the end-to-end test.",
        [(CSV_GO, "\tdue, dueNotes := jiraCSVDueDate(ci.get(row, jiraCSVDueDateColumn))",
                  "\tdue, dueNotes := jiraCSVDueDate(\"\")", 1)],
        "TestJiraCSVMapper_CapturesTheDueDateColumn", IMPORTER, "DueDate is nil",
        "TestJiraCSVMapper_CapturesResolvedOnADoneRow", IMPORTER),

    Control(
        "C3  Create stops gating on done",
        "CONTROLS A TEST THAT PASSED ON ITS FIRST RUN. Before this merge nothing was ever written, so "
        "'a non-done row has no completion time' was true for free. Drop the gate and it must red, or "
        "the assertion is decoration and the API body can put a completion time on backlog work.",
        [(STORE_GO, "\tif issue.Status != model.StatusDone {\n\t\tcompletedAt = nil\n\t}",
                    "\tif false {\n\t\tcompletedAt = nil\n\t}", 1)],
        "TestCreate_RefusesACompletionTimeOnANonDoneIssue", ISSUE, "want NULL",
        "TestCreate_PersistsCompletedAtOnADoneIssue", ISSUE),

    Control(
        "C4  the Due Date column literal is misspelled",
        "#75's C6, one transport over. The mapper's authority over which column it reads IS the "
        "string; before this merge neither spelling appeared in any test and both could have been "
        "anything.",
        [(DATES_GO, '\tjiraCSVDueDateColumn  = "Due Date"', '\tjiraCSVDueDateColumn  = "DueDate"', 1)],
        "TestJiraCSVColumns_TheMeasuredSpellingsAreWhatTheMapperReads", IMPORTER,
        "did not both reach the model",
        "TestJiraCSVMapper_CapturesResolvedOnADoneRow", IMPORTER),

    Control(
        "C5  the Resolved column takes the API's field name",
        "The PLAUSIBLE wrong move, not a random one: `resolutiondate` is what the jira_api transport "
        "asks for, and reaching for it here is exactly the mistake the two-provenance split exists to "
        "prevent.",
        [(DATES_GO, '\tjiraCSVResolvedColumn = "Resolved"', '\tjiraCSVResolvedColumn = "resolutiondate"', 1)],
        "TestJiraCSVMapper_CapturesResolvedOnADoneRow", IMPORTER, "CompletedAt is nil",
        "TestJiraCSVMapper_CapturesTheDueDateColumn", IMPORTER),

    Control(
        "C6  the mapper reuses the API's date parser",
        "THE CONTROL BEHIND THE WHOLE FILE SPLIT. parseJiraTime carries #74's observed-bytes "
        "provenance for a DIFFERENT serialisation. Reusing it must red, or 'these two helpers are "
        "separate on purpose' is a comment with nothing behind it.",
        [(DATES_GO, "\tfor _, layout := range jiraCSVTimeLayouts {\n\t\tif t, err := time.Parse(layout, s); err == nil {",
                    "\tfor _, layout := range jiraTimeLayouts {\n\t\tif t, err := time.Parse(layout, s); err == nil {", 1)],
        "TestJiraCSVTime_ParsesEveryMeasuredShape", IMPORTER, "REFUSED",
        "TestJiraCSVMapper_AbsentDatesAreSilentAndNil", IMPORTER),

    Control(
        "C7  the refusal becomes a silent drop",
        "THE HONESTY GUARANTEE (#77's D7). A hand-pinned layout list is only defensible because a "
        "shape it does not accept is REPORTED. Turn that back into a nil and the merge is no better "
        "than the silence it replaced.",
        [(DATES_GO, "\t\treturn nil, []FieldNote{{Field: fieldDueDate, Value: raw, Via: viaUnparseableDate}}",
                    "\t\treturn nil, nil", 1)],
        "TestJiraCSVMapper_ReportsADateShapeItCannotParse", IMPORTER, 'notes for "due date" = 0',
        "TestJiraCSVMapper_CapturesTheDueDateColumn", IMPORTER),

    Control(
        "C8  a cancelled issue keeps its resolution date",
        "#74's decision, inherited. Jira resolves 'Won't Do' and 'Cannot Reproduce' — both observed on "
        "the real instance — so this is the common case, not an edge one, and analytics counts every "
        "non-null completed_at as delivered work with no status predicate.",
        [(DATES_GO, "\tif status != model.StatusDone {\n\t\treturn nil, []FieldNote{{Field: fieldResolutionDate",
                    "\tif false {\n\t\treturn nil, []FieldNote{{Field: fieldResolutionDate", 1)],
        "TestJiraCSVMapper_RefusesAndReportsResolvedOnANonDoneRow", IMPORTER, "on a cancelled row",
        "TestJiraCSVMapper_CapturesResolvedOnADoneRow", IMPORTER),

    Control(
        "C9  an unmeasured layout joins the list",
        "CONTROLS A TEST THAT PASSED ON ITS FIRST RUN. The list's honesty rests on every entry having "
        "bytes behind it; a plausible-looking Jira format with no measurement is exactly how the list "
        "would grow.",
        [(DATES_GO, '\t"2/Jan/2006 3:04 PM", // every value measured above',
                    '\t"2/Jan/06 3:04 PM",\n\t"2/Jan/2006 3:04 PM", // every value measured above', 1)],
        "TestJiraCSVTime_TheLayoutListIsExactlyWhatWasMeasured", IMPORTER, "every entry needs measured bytes",
        "TestJiraCSVTime_ParsesEveryMeasuredShape", IMPORTER),

    Control(
        "C10 the mapper reads a NEIGHBOURING date column",
        "The negative half. Without it the column tests pass for a mapper that grabs any column whose "
        "name contains 'date' — and a real export has 'Custom field (Target Release Date)' sitting "
        "right there.",
        [(CSV_GO, "\tdue, dueNotes := jiraCSVDueDate(ci.get(row, jiraCSVDueDateColumn))",
                  '\tdue, dueNotes := jiraCSVDueDate(ci.get(row, "Custom field (Target Release Date)"))', 1)],
        "TestJiraCSVColumns_ANeighbouringDateColumnIsNotRead", IMPORTER,
        "read out of a column that is not",
        "TestJiraCSVMapper_CapturesResolvedOnADoneRow", IMPORTER),

    Control(
        "C11 the Linear CSV mapper quietly grows the same fields",
        "CONTROLS A TEST THAT PASSED ON ITS FIRST RUN, and it is the one that keeps a STATED GAP from "
        "closing by guess. Linear's CSV export is unreachable from here, so its spellings and date "
        "shape are unmeasured; teaching the mapper anyway must red.",
        [(CSV_GO, "\t\t\tLabels:      splitLabels(ci.get(row, \"Labels\")),\n\t\t},\n\t\tnotes: collectNotes(rawStatus, status, statusOK, statusFallback{}, rawPrio, prio, prioOK),",
                  "\t\t\tLabels:      splitLabels(ci.get(row, \"Labels\")),\n\t\t\tDueDate:     mustGuessLinearDate(ci.get(row, \"Due Date\")),\n\t\t},\n\t\tnotes: collectNotes(rawStatus, status, statusOK, statusFallback{}, rawPrio, prio, prioOK),", 1),
         (CSV_GO, "func linearRowMapper(ci columnIndex, row []string) (mappedIssue, error) {",
                  "func mustGuessLinearDate(s string) *time.Time {\n\tif t, err := time.Parse(\"2006-01-02\", s); err == nil {\n\t\treturn &t\n\t}\n\treturn nil\n}\n\nfunc linearRowMapper(ci columnIndex, row []string) (mappedIssue, error) {", 1),
         (CSV_GO, "import (\n\t\"context\"", "import (\n\t\"context\"\n\t\"time\"", 1)],
        "TestLinearCSVMapper_StillReadsNoDates_AKnownUnmeasuredGap", IMPORTER,
        "linearRowMapper now reads dates",
        "TestJiraCSVMapper_CapturesTheDueDateColumn", IMPORTER),

    Control(
        "C12 an absent column is stamped with now()",
        "CONTROLS TWO TESTS THAT PASSED ON THEIR FIRST RUN. Every 'the date landed' assertion above is "
        "satisfied by a mapper that invents one; only the silence assertions distinguish them.",
        [(DATES_GO, "func jiraCSVDueDate(raw string) (*time.Time, []FieldNote) {\n\tif strings.TrimSpace(raw) == \"\" {\n\t\treturn nil, nil\n\t}",
                    "func jiraCSVDueDate(raw string) (*time.Time, []FieldNote) {\n\tif strings.TrimSpace(raw) == \"\" {\n\t\tnow := time.Now().UTC()\n\t\treturn &now, nil\n\t}", 1)],
        "TestJobRow_JiraCSV_NoDateColumnsIsCleanAndSilent", IMPORTER, "want both NULL",
        "TestJiraCSVMapper_CapturesTheDueDateColumn", IMPORTER),
]


def run_test(pkg, name):
    """Returns (passed, combined output)."""
    env = dict(os.environ)
    p = subprocess.run(
        ["go", "test", "-count=1", "-timeout", "300s", "-run", f"^{name}$", pkg],
        cwd=ROOT, capture_output=True, text=True, env=env)
    return p.returncode == 0, p.stdout + p.stderr


def main():
    if not os.environ.get("TRACK_TEST_DATABASE_URL"):
        print("TRACK_TEST_DATABASE_URL is unset — the Postgres-level controls cannot run.")
        return 2

    # Capture the ORIGINAL bytes of every file any control touches, up front.
    touched = sorted({e[0] for c in CONTROLS for e in c.edits})
    originals = {p: (ROOT / p).read_bytes() for p in touched}
    shas = {p: hashlib.sha256(b).hexdigest() for p, b in originals.items()}
    print("originals captured:")
    for p in touched:
        print(f"  {shas[p][:16]}  {p}")

    def restore():
        for p, b in originals.items():
            (ROOT / p).write_bytes(b)
        for p in touched:
            got = hashlib.sha256((ROOT / p).read_bytes()).hexdigest()
            assert got == shas[p], f"RESTORE FAILED for {p}: {got} != {shas[p]}"

    # Baseline: everything a control will assert on must be green before any of them runs.
    print("\nbaseline")
    for pkg in (IMPORTER, ISSUE):
        ok, out = run_test(pkg, ".*")
        print(f"  {'GREEN' if ok else 'RED  '}  {pkg}")
        if not ok:
            print(out[-3000:])
            return 1

    caught, failures = 0, []
    for c in CONTROLS:
        print(f"\n{'='*90}\n{c.name}\n  {c.why}")
        try:
            # ── ANCHOR ASSERTIONS FIRST, ALL OF THEM, BEFORE ANY WRITE ──────────────
            # A control that applies half of itself reports a working guard as blind. Every edit is
            # recomputed against the text as it will be at the moment it is applied.
            staged = {}
            for path, old, new, want in c.edits:
                cur = staged.get(path, originals[path].decode())
                got = cur.count(old)
                if got != want:
                    raise AssertionError(
                        f"anchor count {got}, want {want}, for {path}:\n    {old[:90]!r}")
                if want:
                    cur = cur.replace(old, new, 1)
                staged[path] = cur

            for path, text in staged.items():
                (ROOT / path).write_text(text)

            red_ok, red_out = run_test(c.red_pkg, c.red_test)
            says = c.red_says in red_out
            green_ok, green_out = run_test(c.green_pkg, c.green_test)

            verdict = (not red_ok) and says and green_ok
            print(f"  {c.red_test}: {'RED (caught)' if not red_ok else 'GREEN — NOT CAUGHT'}")
            print(f"  says {c.red_says!r}: {'yes' if says else 'NO — it reddened for another reason'}")
            print(f"  must-stay-green {c.green_test}: {'GREEN' if green_ok else 'RED — the mutation broke everything'}")
            if verdict:
                caught += 1
                print("  => CAUGHT")
            else:
                failures.append(c.name)
                print("  => NOT A CLEAN CATCH")
                if red_ok:
                    print(red_out[-1500:])
                if not green_ok:
                    print(green_out[-1500:])
        finally:
            restore()

    print(f"\n{'='*90}\n{caught}/{len(CONTROLS)} caught cleanly; every file restored sha256-identical.")
    for f in failures:
        print(f"  NOT A CLEAN CATCH: {f}")
    return 0 if caught == len(CONTROLS) else 1


if __name__ == "__main__":
    sys.exit(main())
