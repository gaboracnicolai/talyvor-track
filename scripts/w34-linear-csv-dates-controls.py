#!/usr/bin/env python3
"""Positive controls for the two LINEAR CSV date columns (W3.4, after #88).

WHAT THIS PROVES AND WHAT IT DOES NOT. Ten of the twelve test functions were RED before the fix, on
real Postgres, with real numbers (created_at off by 4804h · completed_at NULL · median time to
resolution 0 · job row {succeeded, imported:1, warnings:[]}). That is necessary and not sufficient:
it shows the guards can fail on the ORIGINAL defect, not that they still fail on each INDIVIDUAL
half of it. Each control below removes exactly ONE half and names the test that must speak.

⚠ TWO TESTS PASSED ON THEIR FIRST RUN AND ARE THE REASON HALF THIS FILE EXISTS.
TestLinearCSV_AFullyReadableRowAddsNoWarning and TestLinearCSV_AnAbsentCompletedColumnIsNotAWarning
are must-stay-green companions BY CONSTRUCTION: before the fix nothing was reported at all, so they
could not have gone red on the defect. C8 earns the first (a layout list that accepts nothing turns
every clean row into a warning) and C9 the second (reporting an absent Completed column fills the
channel the Created line has to be read in with lines nobody can act on).

⚠ EVERY CONTROL CARRIES A MUST-STAY-GREEN COMPANION. Without one, "the target went red" is equally
consistent with a mutation that broke the build or reddened everything. BOTH RED IS `SUSPECT`,
NEVER `CAUGHT`.

⚠ THE MUST-RED OUTPUT IS READ BY ASSERTION, NOT BY EXIT CODE. A CAUGHT verdict can name a test that
never reached the assertion the control exists for — an earlier t.Fatalf is enough to skip it. This
runner prints the file:line of the first failing assertion for every must-red target, so the
campaign's own output says WHICH sentence spoke.

⚠ THE RUNNER AND ITS VERDICT LOGIC ARE #86/#87/#88's, CARRIED OVER UNCHANGED because they were paid
for by RUNNING them: an ambiguous anchor that silently matched twice, a mutation that did not
compile being scored as CAUGHT (hence the BUILD state), and a `-run` pattern matching nothing
exiting 0 (hence NOMATCH). The CONTROLS, and the assertion-name reporting, are this merge's own.

⚠ THE BASELINE GATE IS LOAD-BEARING. Without TRACK_TEST_DATABASE_URL every job control would SKIP,
`go test` would exit 0, and this script would report a clean sweep of controls that never ran.

    TRACK_TEST_DATABASE_URL=... python3 scripts/w34-linear-csv-dates-controls.py
"""
import hashlib
import os
import pathlib
import re
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent

IMP = "./internal/importer/"

DATES = "internal/importer/linear_csv_dates.go"
CSV = "internal/importer/csv.go"
JIRA_CREATED = "internal/importer/jira_csv_created.go"

CREATED_LANDS = "TestLinearCSV_CreatedLandsOnTheIssue"
NO_COL = "TestLinearCSV_MissingCreatedColumnIsReported"
EMPTY_CELL = "TestLinearCSV_EmptyCreatedValueIsReportedApartFromTheMissingColumn"
BAD_CREATED = "TestLinearCSV_UnparseableCreatedIsReportedNotSilentlyDefaulted"
COMPLETED_LANDS = "TestLinearCSV_CompletedLandsOnADoneIssue"
REFUSED = "TestLinearCSV_CompletedIsRefusedAndReportedWhenTheIssueIsNotDone"
BAD_COMPLETED = "TestLinearCSV_UnparseableCompletedIsReported"
CLEAN = "TestLinearCSV_AFullyReadableRowAddsNoWarning"
NO_COMPLETED_COL = "TestLinearCSV_AnAbsentCompletedColumnIsNotAWarning"
MAPPER_PIN = "TestLinearCSVMapper_ReadsTheDocumentedDatesAndInventsNoOthers"
JOB_DATES = "TestJobRow_LinearCSV_ImportedIssueKeepsTheDatesLinearRecorded"
JOB_CYCLE = "TestJobRow_LinearCSV_CycleTimeOfAnImportedIssueIsRealAndPositive"
JOB_WARN = "TestJobRow_LinearCSV_ADatelessExportSaysSoInTheJobROW"
JIRA_CREATED_WIRED = "TestJiraCSVCreated_Rule2_TheMeasuredBytes"

# (id, file, anchor, replacement, must_red, must_stay_green, package, note, scope)
CONTROLS = [
    ("C1", DATES,
     "func linearCSVCreated(ci columnIndex, row []string) (time.Time, []FieldNote) {\n",
     "func linearCSVCreated(ci columnIndex, row []string) (time.Time, []FieldNote) {\n"
     "\treturn time.Time{}, nil // CONTROL\n",
     [CREATED_LANDS, NO_COL, EMPTY_CELL, BAD_CREATED, MAPPER_PIN, JOB_DATES, JOB_CYCLE, JOB_WARN],
     [COMPLETED_LANDS, REFUSED, CLEAN, NO_COMPLETED_COL], IMP,
     "THE DEFECT ITSELF, created half — the exact pre-merge behaviour. ⚠ THE VERDICT TO READ HERE "
     "IS JOB_CYCLE's MESSAGE, not its exit code: with Completed read and Created not, the median "
     "goes NEGATIVE rather than to zero. That is #83's defect reproduced on this transport, and it "
     "is what earns the Created half SEPARATELY from the Completed half.",
     None),

    ("C2", DATES,
     "func linearCSVCompleted(raw string, status model.IssueStatus) (*time.Time, []FieldNote) {\n",
     "func linearCSVCompleted(raw string, status model.IssueStatus) (*time.Time, []FieldNote) {\n"
     "\treturn nil, nil // CONTROL\n",
     [COMPLETED_LANDS, REFUSED, BAD_COMPLETED, MAPPER_PIN, JOB_DATES, JOB_CYCLE],
     [CREATED_LANDS, NO_COL, CLEAN, NO_COMPLETED_COL], IMP,
     "THE DEFECT ITSELF, completed half. JOB_CYCLE reds with median = 0 rather than negative: no "
     "row satisfies analytics' `completed_at IS NOT NULL`, so the finished history is not wrong in "
     "the report, it is ABSENT from it. The two halves hide each other, which is why both are here.",
     None),

    ("C3", DATES,
     '\tlinearCSVCreatedColumn   = "Created"',
     '\tlinearCSVCreatedColumn   = "Created At"',
     [CREATED_LANDS, EMPTY_CELL, MAPPER_PIN, JOB_DATES, JOB_CYCLE],
     [COMPLETED_LANDS, REFUSED, NO_COMPLETED_COL], IMP,
     "A PLAUSIBLE WRONG SPELLING — `Created At` is what anybody writing from memory of the GraphQL "
     "field `createdAt` reaches for. ⚠ NO_COL IS DELIBERATELY NOT A COMPANION HERE: under this "
     "mutation it stays green FOR THE WRONG REASON (the column really is not found), and EMPTY_CELL "
     "is the test that tells the two absences apart. Listing NO_COL as must-green would have "
     "recorded a pass that means nothing.",
     None),

    ("C4", DATES,
     '\tlinearCSVCompletedColumn = "Completed"',
     '\tlinearCSVCompletedColumn = "Completed At"',
     [COMPLETED_LANDS, REFUSED, BAD_COMPLETED, MAPPER_PIN, JOB_DATES, JOB_CYCLE],
     [CREATED_LANDS, NO_COL, CLEAN, NO_COMPLETED_COL], IMP,
     "The same wrong spelling on the other column. It is a separate control because the two columns "
     "have separate consumers: this one empties the resolution report, C3 corrupts the window every "
     "analytics query filters on.",
     None),

    ("C5", DATES,
     "\tif status != model.StatusDone {\n"
     "\t\treturn nil, []FieldNote{{Field: fieldCompletionTime, Value: raw, Mapped: string(status), Via: viaStatusNotDone}}\n"
     "\t}\n",
     "",
     [REFUSED], [COMPLETED_LANDS, CREATED_LANDS, CLEAN, JOB_DATES], IMP,
     "DROP THE GATE. #74's decision is inherited by this transport rather than re-litigated, and "
     "this is the mutation that makes it a decision rather than a copied line: analytics' "
     "resolution query has NO status predicate, so a CANCELLED Linear issue carrying a Completed "
     "date is counted as delivered work. mapLinearStatus reads 'Cancelled' natively, so the row "
     "this control creates is an ordinary export line.",
     None),

    ("C6", DATES,
     "\t\treturn time.Time{}, []FieldNote{{Field: fieldCreated, Value: raw, Via: viaUnparseableDate}}\n",
     "\t\treturn time.Time{}, nil // CONTROL\n",
     [BAD_CREATED], [CREATED_LANDS, NO_COL, EMPTY_CELL, CLEAN], IMP,
     "⚠ THE CONTROL THAT EARNS THE HONESTY OF A LIST NOBODY COULD MEASURE. It keeps the column "
     "READ and kills only the REPORT of a value no pinned layout accepts. linearCSVTimeLayouts is "
     "the weakest-provenance list in this package — no credential here can produce a Linear export "
     "— and the refusal being REPORTED is the entire reason shipping it is honest rather than a "
     "guess. Silently defaulted, a tenant whose serialisation differs gets a column of "
     "import-instant timestamps that reads as a working import.",
     None),

    ("C7", CSV,
     "concatNotes(createdNotes, completedNotes)...",
     "concatNotes(createdNotes, completedNotes[:0])...",
     [REFUSED, BAD_COMPLETED], [COMPLETED_LANDS, CREATED_LANDS, NO_COL, CLEAN], IMP,
     "CUT THE REPORT CHANNEL AT THE WIRING rather than at the mapper. #88's C7 found its hole here: "
     "a fix that lands the VALUE while silently swallowing the REFUSAL passes every value "
     "assertion. The must-green list is what says the values still land.\n"
     "     ⚠ THE FIRST DRAFT SCORED NOT CAUGHT AND IT WAS THE CONTROL'S FAULT, NOT THE "
     "CODE'S — dropping the argument left `completedNotes` declared and not used, a BUILD "
     "FAILURE, which the runner correctly refuses to score as a catch (#88's C9, recurring "
     "in my own harness). Emptying the slice keeps the variable used and cuts exactly the "
     "channel.",
     None),

    ("C8", DATES,
     '\t"2006-01-02", // the shape this package\'s fixtures have always carried, and what Linear\'s docs call a "created date"\n',
     "",
     [CREATED_LANDS, COMPLETED_LANDS, CLEAN, MAPPER_PIN, JOB_DATES, JOB_CYCLE],
     [NO_COL, NO_COMPLETED_COL], IMP,
     "⚠ THE CONTROL THAT EARNS A TEST THAT COULD NOT HAVE BEEN RED. Remove the only layout that "
     "accepts the documented shape and every clean row starts reporting a warning — so "
     "TestLinearCSV_AFullyReadableRowAddsNoWarning, green on the first run and therefore suspect, "
     "is a live guard rather than a vacuous one. It is also the mutation a real Linear export would "
     "cause if its serialisation differs, which is exactly the residual risk this merge ships with.",
     None),

    ("C9", DATES,
     "\tif strings.TrimSpace(raw) == \"\" {\n\t\treturn nil, nil\n\t}\n",
     "\tif strings.TrimSpace(raw) == \"\" {\n"
     "\t\treturn nil, []FieldNote{{Field: fieldCompletionTime, Via: viaUnparseableDate}} // CONTROL\n\t}\n",
     [NO_COMPLETED_COL], [CLEAN, COMPLETED_LANDS, CREATED_LANDS, JOB_DATES], IMP,
     "⚠ THE CONTROL THAT EARNS THE SECOND FIRST-RUN-GREEN TEST, AND THE ASYMMETRY IT PINS. Report "
     "an absent Completed as a loss and the warnings channel fills with lines an operator cannot "
     "act on — in the one channel the Created line has to be read in. An unread Created is a WRONG "
     "value that looks right; an absent Completed is an honest NULL. Only this control separates "
     "'reports the losses' from 'reports everything'.",
     None),

    ("C10", CSV,
     "\tcase n.Via == viaNoLinearCreatedColumn:\n",
     "\tcase n.Via == viaNoLinearCreatedColumn && false:\n",
     [NO_COL, JOB_WARN], [EMPTY_CELL, CREATED_LANDS, CLEAN, COMPLETED_LANDS], IMP,
     "BLIND THE RENDERED SENTENCE while leaving the note itself in the tally. The structural-zero "
     "line is the only thing that distinguishes 'Track read your Created column' from 'Track "
     "recorded every one of these as opened today', and JOB_WARN is the assertion that it reaches "
     "import_jobs.warnings — the channel an operator actually reads — rather than only the "
     "in-process ImportResult.",
     None),

    ("C11", JIRA_CREATED,
     'const jiraCSVCreatedColumn = "Created"',
     'const jiraCSVCreatedColumn = "Created At"',
     [JIRA_CREATED_WIRED],
     [CREATED_LANDS, NO_COL, EMPTY_CELL, JOB_DATES, JOB_WARN], IMP,
     "⚠ THE CONTROL THAT EARNS A SEPARATE CONSTANT FOR A COLUMN SPELLED THE SAME ON BOTH "
     "PROVIDERS. `Created` is `Created` on a Jira export and a Linear one, so reusing "
     "jiraCSVCreatedColumn would have compiled, passed, and read fine — and tied two facts about "
     "two different products together forever. This breaks the JIRA constant and the whole LINEAR "
     "set must stay green. If any Linear target reds here, the two transports are coupled.",
     None),

    ("C12", CSV,
     '\t\t\tLabels:      splitLabelColumns(ci.getAll(row, "Labels")),\n'
     "\t\t\tCompletedAt: completed,\n\t\t\tCreatedAt:   created,\n",
     '\t\t\tLabels:      splitLabelColumns(ci.getAll(row, "Labels")),\n'
     "\t\t\tCompletedAt: completed,\n\t\t\tCreatedAt:   created,\n"
     "\t\t\tDueDate:     completed, // CONTROL\n",
     [MAPPER_PIN],
     [CREATED_LANDS, COMPLETED_LANDS, NO_COL, CLEAN, JOB_DATES], IMP,
     "⚠ THE CONTROL THAT EARNS THE HALF OF A PREDECESSOR GUARD THIS MERGE KEPT. "
     "TestLinearCSVMapper_ReadsTheDocumentedDatesAndInventsNoOthers replaced a test that pinned "
     "the WHOLE Linear date gap as unmeasured; the surviving half is that Linear's documented "
     "export carries NO due-date column, so a DueDate on this transport is INVENTED. Nothing else "
     "in the suite would notice one appearing. The mutation is the wrong-field-in-a-struct-literal "
     "slip this item has already paid for four times, and the must-green list says both real dates "
     "still land while it happens.\n"
     "     ⚠ ITS FIRST ANCHOR MATCHED TWICE — jiraRowMapper's literal carries the same two "
     "lines — and the harness REFUSED TO RUN rather than edit the wrong mapper. The anchor now "
     "includes the Labels line, which is where the two literals differ.",
     None),
]


def sha(path):
    return hashlib.sha256((ROOT / path).read_bytes()).hexdigest()


ASSERTION = re.compile(r"^\s+(\w+_test\.go:\d+):", re.M)


def run(targets, pkg):
    """Return (passed, output). passed is None for BUILD failure or a pattern that matched nothing."""
    cmd = ["go", "test", "-timeout", "300s", "-count=1"]
    if targets:
        cmd += ["-run", "^(" + "|".join(targets) + ")$"]
    cmd.append(pkg)
    p = subprocess.run(cmd, cwd=ROOT, capture_output=True, text=True)
    out = p.stdout + p.stderr
    # ⚠ A BUILD FAILURE IS NOT A CAUGHT MUTATION and must never be scored as one.
    if "build failed" in out or "cannot use" in out or "undefined:" in out or "declared and not used" in out:
        return None, out
    # ⚠ NO TESTS MATCHED IS NOT A PASS. `go test -run` exits 0 when the pattern matches nothing.
    if targets and "no tests to run" in out:
        return None, out
    return p.returncode == 0, out


def first_assertion(out):
    """The file:line of the first failing assertion — so a CAUGHT verdict names the sentence that
    spoke, rather than merely reporting that the test exited non-zero."""
    m = ASSERTION.search(out)
    return m.group(1) if m else "no assertion line"


def main():
    if not os.environ.get("TRACK_TEST_DATABASE_URL"):
        print("REFUSING TO RUN: TRACK_TEST_DATABASE_URL is unset. Every real-Postgres control "
              "would SKIP, go test would exit 0, and this script would report a clean sweep of "
              "controls that never ran.", file=sys.stderr)
        return 3

    files = sorted({c[1] for c in CONTROLS})
    before = {f: sha(f) for f in files}

    print("BASELINE — the suite must be green before any mutation means anything")
    ok, out = run([], IMP)
    if not ok:
        print("  BASELINE RED — stopping. A control campaign on a red tree proves nothing.")
        print(out[-3000:])
        return 2
    print("  baseline green\n")

    verdicts = {}
    for cid, path, anchor, repl, must_red, must_green, pkg, note, scope in CONTROLS:
        p = ROOT / path
        src = p.read_text()
        head, body = "", src
        if scope:
            i = src.find(scope)
            if i < 0:
                verdicts[cid] = f"SCOPE MARKER {scope!r} NOT FOUND — NOT RUN"
                print(f"{cid}  scope marker not found in {path} — not run")
                continue
            head, body = src[:i], src[i:]
        n = body.count(anchor)
        if n != 1:
            verdicts[cid] = f"ANCHOR {n} != 1 — NOT RUN"
            print(f"{cid}  ANCHOR COUNT {n} != 1 in {path} — not run")
            continue
        p.write_text(head + body.replace(anchor, repl, 1))
        # ⚠ THE BYTES MUST HAVE CHANGED ON DISK. #83 lost a control whose edit never applied and
        # read the resulting green as a dead guard.
        if sha(path) == before[path]:
            p.write_text(src)
            verdicts[cid] = "EDIT DID NOT CHANGE THE FILE — NOT RUN"
            print(f"{cid}  edit left {path} byte-identical — not run")
            continue
        try:
            red_ok, red_detail = True, []
            for t in must_red:
                passed, o = run([t], pkg)
                if passed is None:
                    red_detail.append(f"{t}=BUILD/NOMATCH")
                    red_ok = False
                elif passed:
                    red_detail.append(f"{t}=STILL GREEN")
                    red_ok = False
                else:
                    red_detail.append(f"{t}=red@{first_assertion(o)}")

            green_ok, green_detail = True, []
            for t in must_green:
                passed, _ = run([t], pkg)
                if passed is None:
                    green_detail.append(f"{t}=BUILD/NOMATCH")
                    green_ok = False
                elif passed:
                    green_detail.append(f"{t}=green")
                else:
                    green_detail.append(f"{t}=WENT RED")
                    green_ok = False
        finally:
            p.write_text(src)

        restored = sha(path) == before[path]

        if not must_red and not must_green:
            v = "MEASURED-ONLY"
        elif not must_red:
            v = "STAYED GREEN (as specified)" if green_ok else "COMPANION WENT RED"
        elif red_ok and green_ok:
            v = "CAUGHT"
        elif red_ok and not green_ok:
            v = "SUSPECT — companion also red; a broken build reads like a caught mutation"
        else:
            v = "NOT CAUGHT"
        if not restored:
            v += "  ⚠ TREE NOT RESTORED"
        verdicts[cid] = v
        print(f"{cid}  {v}\n     {note}")
        if red_detail:
            print(f"     must-red   : {'; '.join(red_detail)}")
        if green_detail:
            print(f"     must-green : {'; '.join(green_detail)}")
        print(f"     restored   : {restored}")

    print("\nSUMMARY")
    for cid, v in verdicts.items():
        print(f"  {cid}: {v}")

    bad = [c for c, v in verdicts.items()
           if "NOT RESTORED" in v or v.startswith("NOT CAUGHT") or v.startswith("SUSPECT")
           or v.startswith("ANCHOR") or v.startswith("EDIT DID NOT") or v == "COMPANION WENT RED"]
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
