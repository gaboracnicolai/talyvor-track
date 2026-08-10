#!/usr/bin/env python3
"""w34-jira-csv-status-category-controls.py — positive controls for the Jira CSV `Status Category`
read (internal/importer/jira_csv_status_category.go).

A guard that has never been watched fail is a guard nobody knows still works. Each control below
MUTATES the product, names the tests that MUST go red BEFORE the run, and is scored against the
WHOLE importer package rather than against this merge's own tests — so a mutation that reddens
something unpredicted is visible instead of absorbed. The verdict is read from the set of FAILING
TEST NAMES, never from an exit code: a control that stops the package compiling is scored ERROR, not
CAUGHT, because a build failure reddens everything and would read as the most emphatic guard here.

  C1  never read the column                  the whole finding (deliberately UNDER-specific)
  C2  reuse mapJiraStatusCategory verbatim   THE TRAP: the CSV spells the category, not the key
  C3  let the category overrule the NAME     373 corpus rows disagree, 34 of them dangerously
  C4  read the category AFTER the resolution the ordering jira.go documents as load-bearing
  C5  treat a MISSING column as a blank cell the structural-zero distinction, absent-column half
  C6  treat a BLANK cell as a value          the same distinction, blank-cell half
  C7  give "No Category" a Track status      #73's refusal, on the transport that just gained it
  C8  read the column on the LINEAR path too scope: a Jira column name means nothing to Linear
  C9  report the resolution as unresolved    the report half, apart from the value half
  C10 INVERTED — same vocabulary, map lookup behaviourally identical, must NOT be caught

⚠ THE HARNESS IS ADAPTED FROM scripts/w34-jira-csv-bom-controls.py (#103) AND ITS EVIDENCE IS NOT
INHERITED. What is reused is the mechanism — anchor-count-before-write, one in-memory copy per
control, restore in a `finally` with a sha256 check. Every prediction, every must-stay-green and
every verdict below was produced by running THIS file against THIS change.

⚠ EVERY ANCHOR IS COUNTED BEFORE ANY BYTE IS WRITTEN (#71: a substitution that matches nothing is
byte-indistinguishable from a working guard), all of a control's edits are applied to ONE in-memory
copy (#99: rebuilding each edit from the saved bytes silently keeps only the last), and the restore
runs in a `finally` with a sha256 comparison (#102: a crash between mutate and restore leaves the
mutation on disk).

    TRACK_TEST_DATABASE_URL=... python3 scripts/w34-jira-csv-status-category-controls.py
"""
import hashlib
import os
import re
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CSV = os.path.join(ROOT, "internal/importer/csv.go")
CAT = os.path.join(ROOT, "internal/importer/jira_csv_status_category.go")
FILES = {CSV, CAT}

# The three lines this merge added to jiraRowMapper, verbatim.
FIX_BLOCK = """	var statusFB statusFallback
	if !statusOK {
		status, statusFB = jiraCSVStatusCategory(ci, row, status)
	}
"""

# mapJiraCSVStatusCategory's two display-only arms — the half that reaches 1,294 of the 1,424 rows.
DISPLAY_ARMS = """	switch strings.ToLower(strings.TrimSpace(v)) {
	case "to do":
		return model.StatusTodo, true
	case "in progress":
		return model.StatusInProgress, true
	}
"""

CONTROLS = [
    dict(
        name="C1  never read the column",
        edits=[(CSV, "	if !statusOK {\n		status, statusFB = jiraCSVStatusCategory(ci, row, status)",
                     "	if false && !statusOK {\n		status, statusFB = jiraCSVStatusCategory(ci, row, status)")],
        predict={"TestJiraCSVStatusCategory_ResolvesAnUnknownStatusFromTheColumn",
                 "TestJiraCSVStatusCategory_ADoneCategoryAlsoLandsTheResolvedDate",
                 "TestJiraCSVStatusCategory_TheResolutionIsReported",
                 "TestJiraCSVStatusCategory_TheResolutionStillOverturnsACategoryResolvedDone",
                 "TestJiraCSVStatusCategory_TheDisplayNameIsNotTheAPIKey",
                 "TestJiraCSVStatusCategory_NoCategoryIsRefusedNotInvented",
                 "TestJiraCSVStatusCategory_APresentColumnWithABlankCellSaysSo",
                 "TestJobRow_JiraCSV_StatusCategoryResolvesTheStatusInPostgres",
                 "TestJobRow_JiraCSV_StatusCategoryDoneLandsTheCompletionTime",
                 "TestJobRow_JiraCSV_StatusCategoryIsNamedInTheJobWarnings"},
        note="deliberately UNDER-specific: it reddens ten guards at once, so its CAUGHT says less "
             "about any one of them than the verdict suggests. C2..C9 are the narrow ones. It is "
             "also the RED-FIRST evidence for the one test written after the first red-first run "
             "(NoCategoryIsRefusedNotInvented — see C7). ⚠ THE THREE MUST-STAY-GREENS ARE THE "
             "POINT OF LISTING THIS AT ALL: ARecognisedNameIsNotOverruledByTheCategory, "
             "AnExportWithoutTheColumnIsUnchanged and TheLinearCSVPathDoesNotReadIt describe "
             "behaviour that predates this merge, and a control that reverts the merge must not "
             "touch them.",
    ),
    dict(
        name="C2  reuse mapJiraStatusCategory verbatim",
        edits=[(CAT, DISPLAY_ARMS, "")],
        predict={"TestJiraCSVStatusCategory_ResolvesAnUnknownStatusFromTheColumn",
                 "TestJiraCSVStatusCategory_TheDisplayNameIsNotTheAPIKey",
                 "TestJiraCSVStatusCategory_TheResolutionIsReported",
                 "TestJobRow_JiraCSV_StatusCategoryResolvesTheStatusInPostgres"},
        expect_msg='"Triaged in a custom workflow": status = "backlog", want "todo"',
        note="THE FIX SOMEBODY WOULD ACTUALLY WRITE, and the reason this merge is not one line. "
             "Passing the CSV column to the API's key vocabulary resolves 130 of the corpus's "
             "1,424 rows and misses 1,294. ⚠ IT MUST LEAVE THE DONE ASSERTIONS GREEN — "
             "ADoneCategoryAlsoLandsTheResolvedDate and TheResolutionStillOverturns... both pass "
             "under it, because `Done` collides with its own key. A control that reddened those "
             "too would not distinguish the display-name half from the read itself. "
             "⚠ MY FIRST PREDICTION WAS THREE TESTS AND THE RUN SAID FOUR: TheResolutionIsReported "
             "also reds, because the `New`/`To Do` row it asserts on stops resolving and its "
             "warning line changes with it. The prediction was corrected to the measured set "
             "rather than the run to the prediction; the same assertion is ALSO earned "
             "independently by C9, which changes only the sentence and leaves every status right.",
    ),
    dict(
        name="C3  let the category overrule the NAME",
        edits=[(CSV, "	if !statusOK {\n		status, statusFB = jiraCSVStatusCategory(ci, row, status)",
                     "	if true {\n		status, statusFB = jiraCSVStatusCategory(ci, row, status)")],
        predict={"TestJiraCSVStatusCategory_ARecognisedNameIsNotOverruledByTheCategory",
                 "TestJobRow_JiraCSV_StatusCategoryResolvesTheStatusInPostgres",
                 "TestJobRow_JiraCSV_StatusCategoryDoneLandsTheCompletionTime"},
        expect_msg='want "cancelled" — a name Track recognises is never overruled',
        note="the dangerous direction, measured: 373 corpus rows carry a recognised name whose "
             "category disagrees, and `Won't Do`/`Canceled` (34 rows) are filed under category "
             "`Done`. Category-first imports abandoned work as delivered AND gives it a "
             "completion time, which is why the job-row completion test is in the predicted set.",
    ),
    dict(
        name="C4  read the category AFTER the resolution",
        edits=[(CSV, FIX_BLOCK, ""),
               (CSV,
                "	status, resolutionNotes := applyJiraResolution(ci.get(row, jiraCSVResolutionColumn), status)\n",
                "	status, resolutionNotes := applyJiraResolution(ci.get(row, jiraCSVResolutionColumn), status)\n" + FIX_BLOCK)],
        predict={"TestJiraCSVStatusCategory_TheResolutionStillOverturnsACategoryResolvedDone"},
        expect_msg='status = "done", want "cancelled"',
        note="jira.go documents this ordering as load-bearing and the CSV path now inherits it. "
             "Reversed, a row that reaches `done` only through the category is never seen by the "
             "Resolution, so abandoned work imports as delivered AND carries a completion time. "
             "⚠ TWO EDITS IN ONE FILE, staged into ONE buffer — #99's lesson: rebuilding the "
             "second edit from the saved bytes would silently discard the first.",
    ),
    dict(
        name="C5  treat a MISSING column as a blank cell",
        edits=[(CAT, "	if len(ci[strings.ToLower(jiraCSVStatusCategoryColumn)]) == 0 {\n		return unresolved, statusFallback{}\n	}\n", "")],
        predict={"TestJiraCSVStatusCategory_AnExportWithoutTheColumnIsUnchanged",
                 "TestCSVWarningsAreUnchangedByThisMerge"},
        note="the structural-zero distinction, absent-column half. ⚠ TWO GUARDS CATCH IT AND THAT "
             "IS WORTH SAYING: TestCSVWarningsAreUnchangedByThisMerge is #73's, predates this "
             "merge, and its comment carried the false premise this whole change corrects — so "
             "this control justifies MY guard only in the sense that both fire. C6 is the half "
             "only my guard can see.",
    ),
    dict(
        name="C6  treat a BLANK cell as a value",
        edits=[(CAT, '	raw := ci.get(row, jiraCSVStatusCategoryColumn)\n	if raw == "" {\n		return unresolved, statusFallback{via: viaNoCategory}\n	}\n',
                     '	raw := ci.get(row, jiraCSVStatusCategoryColumn)\n')],
        predict={"TestJiraCSVStatusCategory_APresentColumnWithABlankCellSaysSo"},
        expect_msg="no statusCategory present",
        note="the same distinction from the other side, and the mutation ONLY this merge's guard "
             "can see: the column is there, the cell is empty, and the operator must be told "
             "which — otherwise 'this code never ran' and 'your export had nothing to read' are "
             "the same sentence.",
    ),
    dict(
        name="C7  give \"No Category\" a Track status",
        edits=[(CAT, '	case "in progress":\n		return model.StatusInProgress, true\n',
                     '	case "in progress":\n		return model.StatusInProgress, true\n	case "no category":\n		return model.StatusTodo, true\n')],
        predict={"TestJiraCSVStatusCategory_NoCategoryIsRefusedNotInvented"},
        note="⚠ THIS CONTROL WROTE ITS OWN GUARD, AND THE NOT-CAUGHT IS MEASURED RATHER THAN "
             "REMEMBERED. Designing this control is what showed nothing in the suite pinned #73's "
             "refusal on this transport — the corpus contains zero `No Category` rows, so a "
             "session could have invented a meaning for Jira's own \"I do not know\" with every "
             "test green. The claim was then CHECKED rather than asserted: with "
             "TestJiraCSVStatusCategory_NoCategoryIsRefusedNotInvented renamed out of the suite "
             "and this exact mutation applied, all 288 remaining test functions pass. That test "
             "exists because of this line, and C1 is its red-first evidence.",
    ),
    dict(
        name="C8  read the column on the LINEAR path too",
        edits=[(CSV,
                "	rawStatus, rawPrio := ci.get(row, \"Status\"), ci.get(row, \"Priority\")\n	status, statusOK := mapLinearStatus(rawStatus)",
                "	rawStatus, rawPrio := ci.get(row, \"Status\"), ci.get(row, \"Priority\")\n	status, statusOK := mapLinearStatus(rawStatus)\n	if !statusOK {\n		status, _ = jiraCSVStatusCategory(ci, row, status)\n	}")],
        predict={"TestJiraCSVStatusCategory_TheLinearCSVPathDoesNotReadIt"},
        note="scope. Linear's canonical field is state.type and its export has no Status Category "
             "column at all; wiring a Jira column name into the Linear mapper reads a value that "
             "provider never wrote. The guard is one test and this is the only thing that earns "
             "it — it is green before and after the merge, so nothing else could.",
    ),
    dict(
        name="C9  report the resolution as unresolved",
        edits=[(CAT, "	return mapped, statusFallback{via: viaCategory, value: raw, resolved: true}",
                     "	return mapped, statusFallback{via: viaCategory, value: raw}")],
        predict={"TestJiraCSVStatusCategory_TheResolutionIsReported",
                 "TestJobRow_JiraCSV_StatusCategoryIsNamedInTheJobWarnings"},
        note="the REPORT half, mutated apart from the VALUE half: the statuses still land "
             "correctly (ResolvesAnUnknownStatusFromTheColumn stays green) and only the sentence "
             "changes. Without this, the two warning assertions would be justified by nothing but "
             "C1's blanket revert.",
    ),
    dict(
        name="C10 INVERTED — same vocabulary, expressed as a map",
        edits=[(CAT, DISPLAY_ARMS,
                "	if s, ok := map[string]model.IssueStatus{\n"
                "		\"to do\":       model.StatusTodo,\n"
                "		\"in progress\": model.StatusInProgress,\n"
                "	}[strings.ToLower(strings.TrimSpace(v))]; ok {\n"
                "		return s, true\n"
                "	}\n")],
        predict=set(),
        inverted=True,
        note="a different implementation with identical behaviour on every value the corpus "
             "contains. Predicted NOT CAUGHT and kept: it says out loud that these guards pin the "
             "BEHAVIOUR and not the shape of the switch, so the next person may re-implement this "
             "function without fear. It is NOT evidence that the guards work — C2..C9 are.",
    ),
]


def sha(p):
    with open(p, "rb") as f:
        return hashlib.sha256(f.read()).hexdigest()


def run_tests():
    """-v, because `go test` prints no PASS lines without it and an absence is not a green.
    The WHOLE package runs: an unpredicted red anywhere in it is the thing worth seeing."""
    r = subprocess.run(["go", "test", "-count=1", "-v", "./internal/importer/"],
                       cwd=ROOT, capture_output=True, text=True)
    out = r.stdout + r.stderr
    if "build failed" in out or "[build failed]" in out:
        return None, None, out  # ERROR, not CAUGHT
    failed = {f.split("/")[0] for f in re.findall(r"^\s*--- FAIL: (\S+)", out, re.M)}
    passed = {f.split("/")[0] for f in re.findall(r"^\s*--- PASS: (\S+)", out, re.M)}
    return failed, passed, out


def main():
    if not os.environ.get("TRACK_TEST_DATABASE_URL"):
        print("TRACK_TEST_DATABASE_URL is unset — the job-row controls would FAIL for the wrong "
              "reason and every verdict below would be a fact about the harness. Refusing to run.")
        return 2

    print("== BASELINE: the whole importer package must be GREEN before any mutation ==")
    failed, passed, out = run_tests()
    if failed is None:
        print("the package does not build; nothing below is readable")
        print(out[-2000:])
        return 2
    if failed:
        print(f"  BASELINE RED: {sorted(failed)} — nothing below is readable")
        return 2
    total = len(passed)
    print(f"  {total} test functions green\n")

    originals = {p: open(p, "rb").read() for p in FILES}
    hashes = {p: sha(p) for p in originals}
    score = []

    for c in CONTROLS:
        print(f"== {c['name']} ==")
        staged, ok = {}, True
        for path, old, new in c["edits"]:
            cur = staged.get(path, originals[path].decode("utf-8"))
            n = cur.count(old)
            if n != 1:
                print(f"  ANCHOR FAILED in {os.path.basename(path)}: {n} occurrences of {old[:60]!r}")
                ok = False
                break
            staged[path] = cur.replace(old, new, 1)
        if not ok:
            score.append((c["name"], "VOID (anchor)"))
            continue

        try:
            for path, body in staged.items():
                with open(path, "w") as f:
                    f.write(body)
            failed, passed, out = run_tests()
        finally:
            for path, body in originals.items():
                with open(path, "wb") as f:
                    f.write(body)
            for path in originals:
                assert sha(path) == hashes[path], f"RESTORE FAILED for {path}"

        if failed is None:
            print("  ERROR — the control stopped the package COMPILING. Not a catch.")
            print(out[-800:])
            score.append((c["name"], "ERROR (build)"))
            continue

        pred, inverted = c["predict"], c.get("inverted", False)
        extra, missing = failed - pred, pred - failed
        msg_ok = True
        if c.get("expect_msg"):
            msg_ok = c["expect_msg"] in out
            print(f"  assertion message contains {c['expect_msg']!r}: {msg_ok}")

        if inverted:
            verdict = "NOT CAUGHT (as predicted)" if not failed else f"CAUGHT — UNPREDICTED {sorted(failed)}"
        elif not failed:
            verdict = "NOT CAUGHT — no guard can see this mutation"
        elif missing:
            verdict = f"CAUGHT BY THE WRONG TEST — predicted {sorted(pred)}, missing {sorted(missing)}"
        elif not msg_ok:
            verdict = "CAUGHT BY THE PREDICTED TEST THROUGH THE WRONG ASSERTION"
        elif extra:
            verdict = f"CAUGHT as predicted, PLUS unpredicted {sorted(extra)}"
        else:
            verdict = f"CAUGHT, exactly as predicted ({len(failed)})"
        print(f"  red: {sorted(failed) if failed else 'none'}")
        print(f"  still green: {len(passed)} of {total}")
        print(f"  => {verdict}")
        print(f"  note: {c['note']}\n")
        score.append((c["name"], verdict))

    print("\n== SUMMARY ==")
    for n, v in score:
        print(f"  {n:46s} {v}")
    for p in originals:
        assert sha(p) == hashes[p]
    print("\nboth product files restored sha256-identical")

    print("\n== POST-RESTORE: the whole package must be green again ==")
    failed, passed, _ = run_tests()
    print(f"  red: {sorted(failed) if failed else 'none'} · green: {len(passed)}")
    return 0 if not failed else 2


if __name__ == "__main__":
    sys.exit(main())
