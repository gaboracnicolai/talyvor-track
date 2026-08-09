#!/usr/bin/env python3
"""w34-jira-csv-resolution-controls.py — the positive-control campaign for the Jira CSV
`Resolution` merge.

Every control follows the rules this package has paid for one at a time:

  #71  ASSERT THE ANCHOR COUNT BEFORE APPLYING. A substitution that matches nothing edits zero
       bytes and is byte-indistinguishable from a guard that works.
  #74  THE BLINDING MUST BE BEHAVIOURAL. A control that stops the package compiling reds the
       must-stay-green companion too, and "the guard caught it" becomes indistinguishable from
       "nothing built".
  #76  RESTORE FROM THE ORIGINAL BYTES, NEVER BY REVERSE SUBSTITUTION. `s.replace("", old, 1)`
       INSERTS AT POSITION 0 in Python and silently corrupts the file; every later verdict is
       then noise. sha256 is asserted after every restore.
  #76  EACH RED MUST SAY THE THING IT IS SUPPOSED TO SAY. An anchor assertion proves the edit
       applied; it says nothing about whether the replacement MEANT anything.
  #80  A **NOT CAUGHT** IS A CLAIM ABOUT THE GUARD *AND* ABOUT MY MODEL OF HOW IT FAILS. Read the
       output before believing either.
  #81  EACH CONTROL NAMES A COMPANION THAT MUST STAY GREEN, RUN SEPARATELY.

    TRACK_TEST_DATABASE_URL=... python3 scripts/w34-jira-csv-resolution-controls.py
"""

import hashlib
import os
import re
import subprocess
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
PKG = "./internal/importer/"

RESOLUTION = "internal/importer/jira_csv_resolution.go"
CSV = "internal/importer/csv.go"
RUNNER = "internal/importer/runner.go"


def sha(path):
    with open(os.path.join(REPO, path), "rb") as f:
        return hashlib.sha256(f.read()).hexdigest()


def read(path):
    with open(os.path.join(REPO, path), "r", encoding="utf-8") as f:
        return f.read()


def write(path, s):
    with open(os.path.join(REPO, path), "w", encoding="utf-8") as f:
        f.write(s)


def go_test(pattern):
    """Run one -run pattern. Returns (passed, combined output)."""
    p = subprocess.run(
        ["go", "test", PKG, "-count=1", "-run", pattern],
        cwd=REPO, capture_output=True, text=True, timeout=600,
    )
    return p.returncode == 0, p.stdout + p.stderr


# Every control: (id, why it exists, file, anchor, replacement, expected anchor count,
#                 the guard that must go RED, a substring that red MUST contain,
#                 the companion that must STAY GREEN)
CONTROLS = [
    ("C1", "full revert — the mapper stops consulting the Resolution column at all",
     CSV,
     "\tstatus, resolutionNotes := applyJiraCSVResolution(ci.get(row, jiraCSVResolutionColumn), status)\n",
     "\tvar resolutionNotes []FieldNote\n\t_ = applyJiraCSVResolution\n",
     1,
     "TestJiraCSVResolution_AbandonedWorkDoesNotImportAsDone",
     'want "cancelled"',
     "TestJiraCSVResolution_AbsentColumnChangesNothing"),

    ("C2", "the override is blinded but keeps reporting — the row still imports as done while the "
           "warning claims it did not, which is the worst of both",
     RESOLUTION,
     "\t\treturn model.StatusCancelled, []FieldNote{{",
     "\t\treturn status, []FieldNote{{",
     1,
     "TestJiraCSVResolution_AbandonedWorkDoesNotImportAsDone",
     'want "cancelled"',
     "TestJiraCSVResolution_AnAgreeingResolutionIsSilent"),

    ("C3", "THE CLASSIFIER BLINDED THE OTHER WAY — every resolution scores as cancellation. A new "
           "classifier is blind to its own inverse: every fixture that exercises the ACTED-ON side "
           "keeps passing, and only the agreeing/unreadable cases catch it",
     RESOLUTION,
     "\tmeaning, _ := mapJiraStatus(raw)\n",
     "\tmeaning := model.StatusCancelled\n",
     1,
     "TestJiraCSVResolution_AnAgreeingResolutionIsSilent|TestJiraCSVResolution_UnclassifiableResolutionIsReportedAndChangesNothing",
     "want",
     "TestJiraCSVResolution_AbandonedWorkDoesNotImportAsDone"),

    ("C4", "the scope limit dropped — a Resolution now reinterprets a row that did not import as done",
     RESOLUTION,
     '\tif raw == "" || status != model.StatusDone {',
     '\tif raw == "" {',
     1,
     "TestJiraCSVResolution_OnlyEverActsOnADoneRow",
     'want "in_progress"',
     "TestJiraCSVResolution_AbandonedWorkDoesNotImportAsDone"),

    ("C5", "the override stops reporting itself — a shipped mapping silently overturned is exactly "
           "the class of change this package refuses to make quietly",
     RESOLUTION,
     "\t\treturn model.StatusCancelled, []FieldNote{{\n\t\t\tField:  fieldResolution,",
     "\t\treturn model.StatusCancelled, []FieldNote{{\n\t\t\tField:  \"\",",
     1,
     "TestJiraCSVResolution_TheOverrideIsReported",
     "no warning names the override",
     "TestJiraCSVResolution_AbandonedWorkDoesNotImportAsDone"),

    # ⚠ C6's FIRST FORM WAS A BUILD FAILURE, NOT A CAUGHT MUTATION, and the harness said so rather
    # than scoring it. Returning `[]FieldNote{}[:0]` and leaving the literal below it as dead code
    # produced `missing return` — every test in the package reds, including the companion, and
    # "the guard caught it" is indistinguishable from "nothing compiled" (#74's C1). The blinding is
    # now behavioural: the refusal reports NOTHING and the package builds exactly as before.
    ("C6", "the REFUSAL stops reporting itself — 'Duplicate' on 4,938 issues goes back to being "
           "silent, which is the defect this merge exists to end",
     RESOLUTION,
     "\tdefault:\n\t\treturn status, []FieldNote{{\n\t\t\tField:  fieldResolution,\n"
     "\t\t\tValue:  raw,\n\t\t\tMapped: string(status),\n\t\t\tVia:    viaResolutionUnreadable,\n\t\t}}\n\t}",
     "\tdefault:\n\t\treturn status, nil\n\t}",
     1,
     "TestJiraCSVResolution_UnclassifiableResolutionIsReportedAndChangesNothing",
     "no warning names the unclassifiable resolution",
     "TestJiraCSVResolution_AbandonedWorkDoesNotImportAsDone"),

    ("C7", "the measured column spelling changed — nothing else in the suite reads it, so a typo "
           "here would import every row exactly as it did before the merge",
     RESOLUTION,
     'const jiraCSVResolutionColumn = "Resolution"',
     'const jiraCSVResolutionColumn = "Resolutions"',
     1,
     "TestJiraCSVResolution_AbandonedWorkDoesNotImportAsDone",
     'want "cancelled"',
     "TestJiraCSVResolution_AbsentColumnChangesNothing"),

    ("C8", "THE ORDERING SEAM — the rule runs AFTER the date mapping, so jiraCSVResolved still sees "
           "`done` and stamps a completion time on work that imported as cancelled. Arity-preserving, "
           "compiles, and every STATUS assertion stays green. #74/#78's seam, a third time",
     CSV,
     "\tstatus, resolutionNotes := applyJiraCSVResolution(ci.get(row, jiraCSVResolutionColumn), status)\n"
     "\t// The two date columns a real export carries and this mapper read for six merges as if they\n"
     "\t// were not there. Both are measured — column spelling and serialisation — in\n"
     "\t// jira_csv_dates.go; both REPORT a value they cannot place rather than nil'ing it.\n"
     "\tdue, dueNotes := jiraCSVDueDate(ci.get(row, jiraCSVDueDateColumn))\n"
     "\tcompleted, completedNotes := jiraCSVResolved(ci.get(row, jiraCSVResolvedColumn), status)\n",
     "\tdue, dueNotes := jiraCSVDueDate(ci.get(row, jiraCSVDueDateColumn))\n"
     "\tcompleted, completedNotes := jiraCSVResolved(ci.get(row, jiraCSVResolvedColumn), status)\n"
     "\tstatus, resolutionNotes := applyJiraCSVResolution(ci.get(row, jiraCSVResolutionColumn), status)\n",
     1,
     "TestJiraCSVResolution_AbandonedWorkCarriesNoCompletionTime",
     "want nil",
     "TestJiraCSVResolution_AbandonedWorkDoesNotImportAsDone"),

    ("C9", "the rule grows a word list of its own — the 'invents no vocabulary' claim in the file "
           "header becomes false while EVERY behavioural test stays green. Only rule 1 reds",
     RESOLUTION,
     "\tmeaning, _ := mapJiraStatus(raw)\n\tswitch meaning {",
     '\tmeaning, _ := mapJiraStatus(raw)\n\tswitch strings.ToLower(raw) {\n\tcase "duplicate":\n'
     "\t\treturn model.StatusCancelled, nil\n\t}\n\tswitch meaning {",
     1,
     "TestSourceDerived_TheResolutionRuleOwnsNoVocabulary",
     "carries its own case literals",
     "TestJiraCSVResolution_AbandonedWorkDoesNotImportAsDone"),

    ("C10", "mapJiraStatus loses `won't fix` — the SOURCE-DERIVED rule reads a different function and "
            "cannot see it, and 6,498 issues silently go back to importing as delivered work. This is "
            "the deletion #72 proved a parse-the-source rule is blind to; rule 2 is what reds",
     CSV,
     '\tcase "cancelled", "canceled", "won\'t do", "won\'t fix":',
     '\tcase "cancelled", "canceled", "won\'t do":',
     1,
     "TestPinned_TheMeasuredResolutionVocabularyStillClassifiesAsShipped",
     'resolution "Won\'t Fix"',
     "TestSourceDerived_TheResolutionRuleOwnsNoVocabulary"),

    ("C11", "RULE 1's LITERAL COUNT IS AN ABSENCE ASSERTION AND AN ABSENCE IS GREEN ON A DELETED BODY. "
            "The switch is removed entirely: zero case literals, so the count half PASSES. Only the "
            "`mapJiraStatus(raw)` floor beside it — and the behavioural tests — catch this",
     RESOLUTION,
     "\tmeaning, _ := mapJiraStatus(raw)\n\tswitch meaning {",
     "\tif false {\n\t\t_, _ = mapJiraStatus(raw)\n\t}\n\tmeaning := status\n\tswitch meaning {",
     1,
     "TestJiraCSVResolution_AbandonedWorkDoesNotImportAsDone",
     'want "cancelled"',
     "TestSourceDerived_TheResolutionRuleOwnsNoVocabulary"),

    # ⚠ C12 IS THE ISOLATION CONTROL — #74's C9 and #76's C11, one report-channel over. Without it
    # the job-level file is unproven as anything more than the unit test run twice. The mapper stays
    # perfectly correct and the RUNNER stops forwarding the warnings to the job row's TEXT[], so
    # every source-level assertion about the report is green while the channel a real import is
    # actually read through carries nothing.
    ("C12", "the runner stops forwarding warnings to the job row — the report is correct in memory "
            "and empty in Postgres, which is where an operator reads it",
     RUNNER,
     "terminalStatus(out), out.Imported, out.Refused, out.Skipped, summary, out.Warnings)",
     "terminalStatus(out), out.Imported, out.Refused, out.Skipped, summary, nil)",
     1,
     "TestJobRow_JiraCSV_AbandonedWorkLandsCancelledAndUndatedInPostgres",
     "job warnings do not carry both",
     "TestJiraCSVResolution_TheOverrideIsReported"),
]


def main():
    if not os.environ.get("TRACK_TEST_DATABASE_URL"):
        print("TRACK_TEST_DATABASE_URL is unset — the job-level companion cannot run. Set it.")
        return 2

    originals = {p: read(p) for p in {c[2] for c in CONTROLS}}
    shas = {p: sha(p) for p in originals}

    # ── THE BASELINE. Every guard must be GREEN before any of them is asked to go red;
    #    a control campaign on an already-red suite scores nothing.
    ok, out = go_test("TestJiraCSVResolution|TestSourceDerived_TheResolutionRule|TestPinned_TheMeasured")
    if not ok:
        print("BASELINE IS RED — nothing below would be readable.\n" + out[-3000:])
        return 1
    print("baseline: every resolution guard green\n")

    caught, verdicts = 0, []
    for cid, why, path, anchor, repl, want_n, red_pattern, must_say, green_pattern in CONTROLS:
        src = originals[path]
        n = src.count(anchor)
        if n != want_n:
            verdicts.append((cid, "HARNESS ERROR",
                             f"anchor matched {n}x, expected {want_n} — the control would have edited "
                             f"the wrong bytes or none at all (#71)"))
            print(f"{cid}  HARNESS ERROR  anchor count {n} != {want_n}")
            continue

        write(path, src.replace(anchor, repl, 1))
        try:
            red_ok, red_out = go_test(red_pattern)
            says = must_say in red_out
            green_ok, green_out = go_test(green_pattern)
        finally:
            write(path, src)  # FROM THE ORIGINAL BYTES — never a reverse substitution (#76 C7)
            assert sha(path) == shas[path], f"{cid}: {path} did not restore byte-identically"

        if red_ok:
            verdict, detail = "NOT CAUGHT", "the guard stayed green"
        elif not says:
            verdict, detail = "RED, WRONG REASON", f"the red never said {must_say!r} — see #78's C10"
        elif not green_ok:
            verdict, detail = "RED, WHOLESALE", "the must-stay-green companion reded too"
        else:
            verdict, detail = "CAUGHT", f"{red_pattern.split('|')[0]} red, companion green"
            caught += 1
        verdicts.append((cid, verdict, detail))
        print(f"{cid}  {verdict:18s} {detail}\n      {why}")

    for p in originals:
        assert sha(p) == shas[p], f"{p} not restored"
    print(f"\nevery mutated file restored sha256-identical")
    print(f"{caught}/{len(CONTROLS)} caught")
    for cid, v, d in verdicts:
        if v != "CAUGHT":
            print(f"  ⚠ {cid}: {v} — {d}")
    return 0 if caught == len(CONTROLS) else 1


if __name__ == "__main__":
    sys.exit(main())
