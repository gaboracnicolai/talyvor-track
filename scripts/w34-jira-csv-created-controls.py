#!/usr/bin/env python3
"""w34-jira-csv-created-controls.py — the positive-control campaign for the `Created` merge.

EVERY GUARD IN THIS MERGE PASSED ON ITS FIRST RUN. That is the reason this file exists: a test that
has never been observed failing is a claim, not a guard.

The rules this package has paid to learn, all enforced here:

  · ASSERT THE ANCHOR COUNT BEFORE THE EDIT (#71). A substitution matching nothing edits zero bytes
    and is byte-indistinguishable from a working guard.
  · REQUIRE THE RED TO SAY THE THING (#76's C1). A red for the wrong reason is not a catch — #78's
    C10 scored one for an assertion that never ran, and #80's (3) scored a real catch as a miss.
  · EVERY CONTROL NAMES A COMPANION THAT MUST STAY GREEN, RUN SEPARATELY (#74's C1). Otherwise "the
    guard caught it" and "nothing compiled" are the same output.
  · RESTORE FROM THE ORIGINAL BYTES, NEVER BY REVERSE SUBSTITUTION (#76's C7) and never by
    `git checkout`, WHICH SILENTLY NO-OPS ON AN UNTRACKED FILE (#82) — three files in this merge are
    new, so a VCS restore would leave the mutation in place with exit code 0.

    python3 scripts/w34-jira-csv-created-controls.py
"""

import hashlib
import os
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DSN = os.environ.get("TRACK_TEST_DATABASE_URL")
if not DSN:
    sys.exit("set TRACK_TEST_DATABASE_URL to a real Postgres — these controls are database-level")

CSV = "internal/importer/csv.go"
CREATED = "internal/importer/jira_csv_created.go"
STORE = "internal/issue/store.go"

# ─── the suites ────────────────────────────────────────────────────────────
UNIT = (["-run", "TestJiraCSVCreated|TestJiraRowMapper_Carries|TestLinearRowMapper_CreationTime"],
        ["./internal/importer/"])
JOB = (["-run", "TestJobRow_JiraCSV_(ImportedIssueKeepsTheDateJiraOpenedIt|CycleTimeOfAnImportedIssueIsNotNegative)"],
       ["./internal/importer/"])
JOB_COLUMN = (["-run", "TestJobRow_JiraCSV_ImportedIssueKeepsTheDateJiraOpenedIt"], ["./internal/importer/"])
JOB_CYCLE = (["-run", "TestJobRow_JiraCSV_CycleTimeOfAnImportedIssueIsNotNegative"], ["./internal/importer/"])
GATE = (["-run", "TestCreate_(PersistsCreatedAtOnAnImportedIssue|RefusesACallerSuppliedCreatedAtOnANonImportedIssue|AZeroCreatedAtTakesTheDatabaseDefault)"],
        ["./internal/issue/"])
GATE_INVERSE = (["-run", "TestCreate_RefusesACallerSuppliedCreatedAtOnANonImportedIssue"], ["./internal/issue/"])
GATE_ZERO = (["-run", "TestCreate_AZeroCreatedAtTakesTheDatabaseDefault"], ["./internal/issue/"])
# The neighbouring merges' guards. Nothing in this campaign may red them.
NEIGHBOURS = (["-run", "TestJobRow_JiraCSV_DatesLandInPostgres|TestJobRow_JiraCSV_AbandonedWorkLandsCancelledAndUndatedInPostgres"],
              ["./internal/importer/"])
NEIGHBOUR_STORE = (["-run", "TestCreate_PersistsCompletedAtOnADoneIssue"], ["./internal/issue/"])


def run(suite):
    flags, pkgs = suite
    p = subprocess.run(["go", "test", "-count=1", *flags, *pkgs],
                       cwd=ROOT, capture_output=True, text=True,
                       env={**os.environ, "TRACK_TEST_DATABASE_URL": DSN})
    return p.returncode == 0, p.stdout + p.stderr


def sha(path):
    return hashlib.sha256(open(os.path.join(ROOT, path), "rb").read()).hexdigest()


ORIGINAL = {p: open(os.path.join(ROOT, p), "rb").read() for p in (CSV, CREATED, STORE)}


def restore():
    for p, b in ORIGINAL.items():
        open(os.path.join(ROOT, p), "wb").write(b)


def apply_edits(edits):
    """edits: list of (path, old, new). Anchor count asserted BEFORE any write."""
    staged = {}
    for path, old, new in edits:
        s = staged.get(path) or open(os.path.join(ROOT, path)).read()
        n = s.count(old)
        if n != 1:
            restore()
            sys.exit(f"ANCHOR COUNT {n} for {old[:60]!r} in {path} — the control would have edited "
                     f"the wrong bytes or none at all. Stop.")
        staged[path] = s.replace(old, new, 1)
    for path, s in staged.items():
        open(os.path.join(ROOT, path), "w").write(s)


# ─── the controls ──────────────────────────────────────────────────────────
# (id, what it does, edits, suite that must go RED, substring the red must SAY,
#  companion that must stay GREEN)
CONTROLS = [
    # ⚠ C1's FIRST FORM WAS A BUILD FAILURE, NOT A CAUGHT MUTATION, AND THE HARNESS SAID SO RATHER
    # THAN SCORING IT. Deleting the struct field left `created` declared-and-not-used, so the whole
    # package failed to compile: every test reds, companion included, and "the guard caught it"
    # becomes indistinguishable from "nothing built". #74's C1, paid again in this merge.
    ("C1", "the mapper stops putting the instant on the issue (full revert of the wiring)",
     [(CSV, "\tcreated, createdNotes := jiraCSVCreated(ci, row)", "\t_, createdNotes := jiraCSVCreated(ci, row)"),
      (CSV, "\t\t\tCreatedAt:   created,\n", "")],
     JOB, "created_at", NEIGHBOURS),

    ("C2", "THE ISOLATION CONTROL — revert ONLY the SQL half; the mapper is untouched",
     # ⚠ ALSO REWRITTEN: dropping the column from the statement left an arity mismatch the compiler
     # caught, which is the same not-a-control as C1. This form keeps the SQL shape and severs the
     # VALUE — exactly #78's C1 ("revert ONLY the SQL half") one column over.
     [(STORE, "issue.Labels, issue.SortOrder, createdAt,\n", "issue.Labels, issue.SortOrder, nil,\n"),
      # ...and keep the now-orphaned variable USED, or the mutation is a compile error again. Third
      # form of this control; the first two were build failures the harness refused to score.
      (STORE, "\t}\n\n\tconst insertSQL = ", "\t}\n\t_ = createdAt\n\n\tconst insertSQL = ")],
     GATE, "created_at column", NEIGHBOUR_STORE),

    ("C3", "BLIND THE GATE ITS OWN INVERSE WAY — drop the creator predicate, so ANY caller may "
           "choose its created_at (#82's C3: a new classifier is blind to its own inverse)",
     [(STORE, "if !issue.CreatedAt.IsZero() && issue.CreatorID == model.ImporterCreatorID {",
       "if !issue.CreatedAt.IsZero() {")],
     GATE_INVERSE, "window predicate", GATE_ZERO),

    ("C4", "blind the gate the other way — no row may ever carry a supplied created_at",
     [(STORE, "if !issue.CreatedAt.IsZero() && issue.CreatorID == model.ImporterCreatorID {",
       "if false {")],
     JOB_COLUMN, "created_at =", NEIGHBOUR_STORE),

    ("C5", "drop the IsZero half — an importer row with no supplied time lands the ZERO TIME",
     [(STORE, "if !issue.CreatedAt.IsZero() && issue.CreatorID == model.ImporterCreatorID {",
       "if issue.CreatorID == model.ImporterCreatorID {")],
     GATE_ZERO, "zero CreatedAt reached the column", GATE_INVERSE),

    ("C6", "THE HONESTY GUARANTEE — a shape no layout accepts is silently defaulted instead of "
           "reported (#77's D7, on the field where the silent fallback is a plausible timestamp)",
     [(CREATED, "\t\treturn time.Time{}, []FieldNote{{Field: fieldCreated, Value: raw, Via: viaUnparseableDate}}",
       "\t\treturn time.Time{}, nil")],
     UNIT, "unparseable-date", NEIGHBOURS),

    ("C7", "blind the STRUCTURAL-ZERO report — an export with no Created column says nothing, so "
           "'Track read your column' and 'Track invented every one of these' become identical",
     [(CREATED, "\t\treturn time.Time{}, []FieldNote{{Field: fieldCreated, Via: viaNoCreatedColumn}}",
       "\t\treturn time.Time{}, nil")],
     UNIT, "no-Created-column", NEIGHBOURS),

    ("C8", "collapse the two absences onto ONE via — the struct keeps them apart and the report "
           "does not, which is the same defect one layer down",
     [(CREATED, "\t\treturn time.Time{}, []FieldNote{{Field: fieldCreated, Via: viaNoCreatedValue}}",
       "\t\treturn time.Time{}, []FieldNote{{Field: fieldCreated, Via: viaNoCreatedColumn}}")],
     UNIT, "no-Created-value", NEIGHBOURS),

    ("C9", "pin a SECOND layout list inside jira_csv_created.go — the overclaim #75 caught, which "
           "would lend this column a measurement made for two others",
     [(CREATED, "\tt, ok := parseJiraCSVTime(raw)",
       '\tt, err := time.Parse("2/Jan/2006 3:04 PM", raw)\n\tok := err == nil')],
     UNIT, "own time layout", NEIGHBOURS),

    ("C10", "THE FLOOR UNDER RULE 1's ABSENCE ASSERTIONS (#82's C11) — remove the parse entirely; "
            "zero layouts and zero time.Parse calls, so both absence halves PASS on a dead body",
     [(CREATED, "\tt, ok := parseJiraCSVTime(raw)", "\tvar t time.Time\n\tok := raw != \"\"")],
     UNIT, "want exactly 1", NEIGHBOURS),

    ("C11", "teach linearRowMapper to read a Created column it has never been measured to have",
     # ⚠ C11's FIRST FORM MATCHED ITS ANCHOR, EDITED REAL BYTES AND CHANGED NOTHING: it computed the
     # instant and threw it away, so the guard was reported NOT CAUGHT while working perfectly. An
     # anchor assertion proves the edit APPLIED; it says nothing about whether the replacement MEANT
     # anything (#76's C6, a fifth door). The mutation now reaches model.Issue.
     [(CSV, "func linearRowMapper(ci columnIndex, row []string) (mappedIssue, error) {\n\ttitle := ci.get(row, \"Title\")",
       "func linearRowMapper(ci columnIndex, row []string) (mappedIssue, error) {\n\tlinearCreated, _ := parseJiraCSVTime(ci.get(row, \"Created\"))\n\ttitle := ci.get(row, \"Title\")"),
      (CSV, "\t\t\tLabels:      splitLabelColumns(ci.getAll(row, \"Labels\")),\n\t\t},\n\t\tnotes: collectNotes(rawStatus, status, statusOK, statusFallback{}, rawPrio, prio, prioOK),",
       "\t\t\tLabels:      splitLabelColumns(ci.getAll(row, \"Labels\")),\n\t\t\tCreatedAt:   linearCreated,\n\t\t},\n\t\tnotes: collectNotes(rawStatus, status, statusOK, statusFallback{}, rawPrio, prio, prioOK),")],
     UNIT, "no Linear export has been measured", NEIGHBOURS),

    ("C12", "ARITY-PRESERVING AND IT COMPILES — swap the COALESCE arms so the column is ALWAYS "
            "NOW(). Every source-level assertion stays green; only the database knows.",
     [(STORE, "COALESCE($19::timestamptz, NOW())", "COALESCE(NOW(), $19::timestamptz)")],
     JOB_COLUMN, "created_at =", NEIGHBOURS),

    ("C13", "THE TWO JOB TESTS ARE NOT ONE ASSERTION TWICE — shift the landed instant by 30 "
            "minutes: inside the analytics test's 24h tolerance, outside the column test's 1m one",
     [(CSV, "\t\t\tCreatedAt:   created,\n", "\t\t\tCreatedAt:   created.Add(30 * time.Minute),\n"),
      (CSV, '\t"strconv"\n', '\t"strconv"\n\t"time"\n')],
     JOB_COLUMN, "off by", JOB_CYCLE),
]


def main():
    before = {p: sha(p) for p in ORIGINAL}
    ok, out = run(UNIT)
    if not ok:
        sys.exit("the suite is not green before the campaign starts:\n" + out[-2000:])
    print("baseline green\n")

    caught, results = 0, []
    for cid, what, edits, red_suite, must_say, green_suite in CONTROLS:
        apply_edits(edits)
        red_ok, red_out = run(red_suite)
        green_ok, green_out = run(green_suite)
        restore()
        for p in ORIGINAL:
            if sha(p) != before[p]:
                sys.exit(f"{cid}: {p} did NOT restore byte-identically. Every verdict after this "
                         f"point would be noise. Stop.")
        said = must_say in red_out
        verdict = "CAUGHT" if (not red_ok and said) else "NOT CAUGHT"
        if not red_ok and not said:
            verdict = "RED FOR THE WRONG REASON"
        if not green_ok:
            verdict += " ⚠ COMPANION ALSO RED"
        if verdict == "CAUGHT":
            caught += 1
        results.append((cid, verdict, what))
        print(f"{cid:4s} {verdict:34s} {what[:80]}")
        if verdict != "CAUGHT":
            print("     ── red output ──")
            print("     " + "\n     ".join(red_out.strip().splitlines()[-14:]))
            if not green_ok:
                print("     ── companion output ──")
                print("     " + "\n     ".join(green_out.strip().splitlines()[-10:]))

    print(f"\n{caught}/{len(CONTROLS)} caught")
    for p in ORIGINAL:
        print(f"  {p} sha256 {sha(p)[:16]} (restored)")
    return 0 if caught == len(CONTROLS) else 1


if __name__ == "__main__":
    sys.exit(main())
