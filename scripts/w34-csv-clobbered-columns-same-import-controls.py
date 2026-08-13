#!/usr/bin/env python3
"""w34-csv-clobbered-columns-same-import-controls.py — the positive-control campaign for the fix
that stops the clobbered-column report calling THIS JOB'S OWN ROW a re-import.

THE FIX: internal/importer/source.go folds SourceRow.NotesIfUpdated in on
`overwroteExisting && len(dupNote) == 0` rather than on `overwroteExisting` alone.

WHY IT EXISTS: the two new tests in csv_clobbered_columns_same_import_job_test.go were RED before
the fix and green after, which is necessary and not sufficient — a suppression is exactly the shape
that can go too far (silence the real report) or not far enough (key on the wrong state) while both
of them stay green. Each control below breaks the product on purpose and NAMES the tests it expects
to red; a catch by a different test is printed as a WRONG PREDICTION rather than quietly counted.

The reading rules are the ones scripts/w34-csv-clobbered-columns-controls.py wrote down and this
file follows verbatim:

  · the package runs with -v and the verdict is the SET OF FAILING TEST NAMES, not an exit code;
    PASS lines are counted too, so a run whose PASS count collapses is reported rather than scored
  · a mutation that stops the package COMPILING scores ERROR, not CAUGHT
  · the anchor must be present exactly once and the bytes must actually change before any test runs
  · restore happens in a `finally` and the file's sha256 is compared to the pre-run value

    TRACK_TEST_DATABASE_URL=... python3 scripts/w34-csv-clobbered-columns-same-import-controls.py
    TRACK_TEST_DATABASE_URL=... python3 scripts/… C2          # a subset
"""
import hashlib
import os
import re
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SRC = os.path.join(ROOT, "internal/importer/source.go")
DSN = os.environ.get("TRACK_TEST_DATABASE_URL", "")

# The two tests this merge added.
FIRST = "TestJobRow_JiraCSV_AFirstImportNamingOneIssueTwiceIsNotReportedAsARe_import"
COUNT = "TestJobRow_JiraCSV_ARe_importNamingOneIssueTwiceCountsTheIssueOnce"
# The neighbours whose subject this fix touches: #121's report and #139's duplicate warning.
JIRA_NARROW = "TestJobRow_JiraCSV_ANarrowerReimportIsReported"
LINEAR_NARROW = "TestJobRow_LinearCSV_ANarrowerReimportIsReported"
FIRST_NARROW = "TestJobRow_JiraCSV_AFirstNarrowImportIsNotReported"
DUP = "TestJobRow_OneExportNamingTheSameIssueTwiceSaysSo"

RUN = "|".join([FIRST, COUNT, JIRA_NARROW, LINEAR_NARROW, FIRST_NARROW, DUP])

FOLD = "\t\tif overwroteExisting && len(dupNote) == 0 {"
DUP_APPEND = "\t\tif len(dupNote) > 0 {\n\t\t\tnotes = concatNotes(notes, dupNote)\n\t\t}"
REMEMBER = "\t\t\twritten[issueModel.Identifier] = struct{}{}"

CONTROLS = {
    "C1": dict(
        why="revert the fix: fold on `overwroteExisting` alone, which is the UPDATE branch and not "
            '"an issue that already existed"',
        anchor=FOLD, replacement="\t\tif overwroteExisting {",
        expect={FIRST, COUNT},
    ),
    "C2": dict(
        why="silence the duplicate-identifier note while keeping the fix — the suppression must not "
            "be able to hide the EVENT as well as the false sentence about it",
        anchor=DUP_APPEND,
        replacement="\t\tif false && len(dupNote) > 0 {\n\t\t\tnotes = concatNotes(notes, dupNote)\n\t\t}",
        # NOT {FIRST, COUNT, DUP}: this mutation leaves dupNote SET and only stops it being
        # APPENDED, so the fold is still suppressed and COUNT's "1 issue(s)" still holds. The first
        # prediction here said COUNT would red too and the run corrected it.
        expect={FIRST, DUP},
    ),
    "C3": dict(
        why="suppress NotesIfUpdated ALWAYS — the over-broad version of this fix, which must break "
            "the genuine narrower-re-import report #121 built",
        # `if false {` alone does NOT compile — overwroteExisting would be assigned and never read,
        # and a build failure proves the edit landed rather than that the product was wrong. The
        # condition therefore still READS the variable.
        anchor=FOLD, replacement="\t\tif overwroteExisting && false {",
        expect={JIRA_NARROW, LINEAR_NARROW, COUNT},
    ),
    "C4": dict(
        why="never record the identifiers this import wrote: dupNote can never be set, so the fix is "
            "INERT — proves it keys on the same-import state and not on something incidental",
        anchor=REMEMBER, replacement="\t\t\t_ = issueModel.Identifier",
        expect={FIRST, COUNT, DUP},
    ),
}


def sha(path):
    return hashlib.sha256(open(path, "rb").read()).hexdigest()


def go_test():
    """Returns (failing set, pass count, build_error?)."""
    p = subprocess.run(
        ["go", "test", "-run", RUN, "-count=1", "-v", "./internal/importer/"],
        cwd=ROOT, capture_output=True, text=True,
        env=dict(os.environ, TRACK_TEST_DATABASE_URL=DSN),
    )
    out = p.stdout + p.stderr
    if "build failed" in out or "[build failed]" in out or "cannot use" in out or "undefined:" in out:
        return set(), 0, out
    failing = set(re.findall(r"^--- FAIL: (\S+)", out, re.M))
    passing = len(re.findall(r"^--- PASS: (\S+)", out, re.M))
    return failing, passing, None


def main():
    if not DSN:
        sys.exit("TRACK_TEST_DATABASE_URL is required — every test here runs on real Postgres")
    wanted = [a for a in sys.argv[1:] if a in CONTROLS] or list(CONTROLS)

    base_fail, base_pass, err = go_test()
    if err:
        sys.exit("baseline does not build:\n" + err[-2000:])
    print(f"baseline: {len(base_fail)} failing, {base_pass} passing")
    if base_fail:
        sys.exit(f"baseline is not green ({sorted(base_fail)}) — controls would be meaningless")

    scores = {}
    for name in wanted:
        c = CONTROLS[name]
        before = sha(SRC)
        original = open(SRC, encoding="utf-8").read()
        if original.count(c["anchor"]) != 1:
            sys.exit(f"{name}: anchor appears {original.count(c['anchor'])} times, want 1")
        try:
            mutated = original.replace(c["anchor"], c["replacement"], 1)
            if mutated == original:
                sys.exit(f"{name}: the edit changed no bytes")
            open(SRC, "w", encoding="utf-8").write(mutated)
            failing, passing, err = go_test()
        finally:
            open(SRC, "w", encoding="utf-8").write(original)
            if sha(SRC) != before:
                sys.exit(f"{name}: tree NOT restored")
        if err:
            scores[name] = "ERROR (did not compile — proves the edit landed, not that the product was wrong)"
        elif failing == c["expect"]:
            scores[name] = f"CAUGHT by exactly {sorted(failing)} ({passing} still passing)"
        elif failing:
            scores[name] = (f"WRONG PREDICTION: red = {sorted(failing)}, predicted {sorted(c['expect'])}")
        else:
            scores[name] = "NOT CAUGHT — every test stayed green with the product broken"
        print(f"  {name}: {scores[name]}\n      {c['why']}")

    caught = sum(1 for v in scores.values() if v.startswith("CAUGHT"))
    print(f"\n{caught}/{len(wanted)} controls CAUGHT by exactly the predicted tests")
    sys.exit(0 if caught == len(wanted) else 1)


if __name__ == "__main__":
    main()
