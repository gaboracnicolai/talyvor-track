#!/usr/bin/env python3
"""w34-linear-csv-tostring-controls.py — the positive-control harness for the ECMAScript
Date.prototype.toString shape in a real Linear CSV export's date columns.

WHAT A CONTROL IS HERE: one mutation of the SHIPPED code, a PREDICTION of which test names must go
red written BEFORE the run, and a MUST-STAY-GREEN companion for every one of them. A guard that
cannot fail is not a guard, and an exit code cannot tell a caught mutation from a compile error —
so every verdict below is read from the set of failing test NAMES and from the ASSERTION MESSAGE
that fired, never from `go test`'s status alone.

⚠ THE OUTPUT PARSER IS #107's AND IS DUPLICATED RATHER THAN IMPORTED, with its reason carried
across so this file is not trusting a shape it never checked: `go test -v` prints a test's
assertion lines UNDER its `=== RUN` line and only THEN `--- FAIL:`, so a parser that begins
collecting at `--- FAIL:` captures nothing and every verdict reads "(no message)". A verdict read
from test NAMES alone cannot tell a real catch from a panic. The messages are what turned C3 below
from a guess into a measurement.

⚠ THE RESTORE IS IN A `finally` AND IS CHECKED BY sha256, because a crash between mutate and
restore leaves a mutated parser on disk and the closing check never runs.

⚠ THE TARGET IS BOTH PACKAGES, NOT JUST THE MUTATED ONE. A control whose target includes only the
mutated module's own tests scores CAUGHT by construction and says nothing about which guard spoke.

⚠⚠ C3 AND C5 ARE THE ONES WORTH READING. Every other control here is caught by two or three guards
at once, which justifies none of them individually. C3 is caught ONLY by the narrowing guard, and
C5 is caught ONLY by the second row of the job fixture — the row that exists because the corpus
carries two zone-name spellings from two disjoint owner sets.

    TRACK_TEST_DATABASE_URL=... python3 scripts/w34-linear-csv-tostring-controls.py
"""
import hashlib
import os
import re
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DATES = os.path.join(ROOT, "internal/importer/linear_csv_dates.go")
TOSTR = os.path.join(ROOT, "internal/importer/linear_csv_tostring_dates.go")

PKGS = ["./internal/importer/", "./internal/issue/"]

# The three names that carry the finding end to end, used in several predictions below.
JOB_COLUMNS = "TestJobRow_LinearCSV_ToStringDatesLandOnTheirColumns"
JOB_WARNINGS = "TestJobRow_LinearCSV_ToStringDatesAreNoLongerReportedUnparseable"
JOB_REFUSAL = "TestJobRow_LinearCSV_AnUnknownDateShapeIsStillRefusedAndReported"
UNIT_VARIANTS = "TestParseLinearCSVTime_TheTwoZoneNameSpellingsTheCorpusCarries"
UNIT_ANCHORED = "TestParseLinearCSVTime_TheStripIsAnchoredToTheOffset"
UNIT_REGRESS = "TestParseLinearCSVTime_TheShapesThatAlreadyParsedStillDo"
UNIT_STRIP = "TestStripJSDateToStringZoneName_LeavesEverythingElseByteIdentical"
UNIT_MST = "TestParseLinearCSVTime_TheMSTLayoutWouldHaveCoveredOnlyFourOfSixOwners"
UNIT_NONZERO = "TestParseLinearCSVTime_ANonZeroOffsetIsHonoured"
CENSUS = "TestRealLinearExportDateCellsParse"

# ─── the controls ───────────────────────────────────────────────────────────
# Each: (id, file, old, new, prediction, must_stay_green, note)
CONTROLS = [
    ("C1", DATES,
     "\tlinearCSVDateToStringLayout, // ECMAScript Date.prototype.toString — 746 of 2,947 real `Updated` cells\n",
     "",
     [JOB_COLUMNS, JOB_WARNINGS, UNIT_VARIANTS],
     [JOB_REFUSAL, UNIT_ANCHORED, UNIT_REGRESS, UNIT_STRIP],
     "Drop the layout from the list. The strip still runs, so the value reaches the loop as "
     "`Fri Feb 06 2026 10:01:29 GMT+0000` and nothing accepts it — this is the pre-merge "
     "behaviour with the plumbing left in place."),

    ("C2", DATES,
     "\ts = stripJSDateToStringZoneName(s)\n",
     "",
     [JOB_COLUMNS, JOB_WARNINGS, UNIT_VARIANTS],
     [JOB_REFUSAL, UNIT_ANCHORED, UNIT_REGRESS, UNIT_STRIP],
     "Drop the call. The layout is still in the list, so this is the other half of the fix — it "
     "proves neither half alone reads the corpus's cells."),

    ("C3", TOSTR,
     '`( GMT[+-]\\d{4}) \\([^)]*\\)$`',
     '`(.*) \\([^)]*\\)$`',
     [UNIT_ANCHORED, UNIT_STRIP],
     [JOB_COLUMNS, JOB_WARNINGS, JOB_REFUSAL, UNIT_VARIANTS, UNIT_REGRESS, CENSUS],
     "⚠ THE ONE THAT JUSTIFIES THE NARROWING GUARD. Un-anchor the strip from the numeric offset: "
     "it now removes ANY trailing parenthetical, so `2026-01-15 (approx)` becomes a date. The "
     "toString cells still parse — every guard written for the FINDING stays green — and only "
     "the two tests written for the PROPERTY can see it.\n"
     "     ⚠⚠ THE FIRST DRAFT WAS `( ?)\\s*\\([^)]*\\)$`, WHOSE PROSE SAID THIS AND WHOSE EDIT "
     "DID SOMETHING ELSE: the capture no longer held the OFFSET, so the strip destroyed the very "
     "bytes the layout reads and the toString cells stopped parsing. It reddened three "
     "must-stay-greens and left UNIT_ANCHORED green (`2026-01-15 (approx)` became "
     "`2026-01-15 `, with a trailing space, which still parses nowhere) — so it measured a "
     "BROKEN STRIP rather than an unanchored one, and would have read as a guard that does not "
     "work. `(.*)` keeps everything before the parenthetical, which is what the prose meant."),

    ("C4", TOSTR,
     '"Mon Jan 02 2006 15:04:05 GMT-0700"',
     '"Mon Jan 02 2006 15:04:05 GMT+0000"',
     [UNIT_NONZERO],
     [JOB_COLUMNS, JOB_WARNINGS, JOB_REFUSAL, UNIT_ANCHORED, UNIT_REGRESS, UNIT_STRIP, UNIT_VARIANTS, CENSUS],
     "Pin the offset as literal text instead of reading it. Every corpus cell is GMT+0000, so "
     "this parses all 746 of them and is INVISIBLE to the census, the job tests and the corpus "
     "unit cases. Only the non-zero-offset test can see it.\n"
     "     ⚠⚠ I PREDICTED UNIT_VARIANTS AND MY OWN NOTE SAID OTHERWISE — the note was right and "
     "the prediction was wrong, and the run is what said so. This is the control that JUSTIFIES "
     "TestParseLinearCSVTime_ANonZeroOffsetIsHonoured: that test asserts a property no byte in "
     "the corpus exercises, which normally makes a test unjustifiable, and it is the only thing "
     "in the repository that can see this mutation."),

    ("C5", TOSTR,
     '`( GMT[+-]\\d{4}) \\([^)]*\\)$`',
     '`( GMT[+-]\\d{4}) \\(GMT\\)$`',
     [JOB_COLUMNS, UNIT_VARIANTS],
     [JOB_REFUSAL, UNIT_ANCHORED, UNIT_REGRESS],
     "⚠ THE ONE THAT JUSTIFIES THE SECOND FIXTURE ROW. Pin the literal `(GMT)` spelling — the "
     "one-line fix that reads the corpus's four-owner majority and silently leaves the "
     "two-owner minority defaulting. A fixture carrying only the `(GMT)` variant would be "
     "GREEN on this, which is the whole reason LIN-2 exists."),
]


def sha(p):
    return hashlib.sha256(open(p, "rb").read()).hexdigest()


def run_tests():
    r = subprocess.run(["go", "test", "-count=1", "-v", "-timeout", "300s"] + PKGS,
                       cwd=ROOT, capture_output=True, text=True, env=dict(os.environ))
    failing, buffered, name = {}, [], None
    for line in r.stdout.splitlines():
        m = re.match(r"^=== RUN\s+(\S+)", line)
        if m:
            name, buffered = m.group(1), []
            continue
        m = re.match(r"^\s*--- FAIL: (\S+)", line)
        if m:
            failing[m.group(1)] = list(buffered) or [
                "(red, no assertion line printed — a panic or a t.Fatal with no message)"]
            buffered = []
            continue
        if re.match(r"^\s*--- (PASS|SKIP)", line):
            buffered = []
            continue
        if name and line.strip():
            buffered.append(line.strip())
    build_error = "[build failed]" in r.stdout or r.stderr.strip().startswith("#")
    return failing, build_error, r


def main():
    if not os.environ.get("TRACK_TEST_DATABASE_URL"):
        print("TRACK_TEST_DATABASE_URL is unset — every real-Postgres guard would FAIL rather "
              "than run, and every control would score CAUGHT for the wrong reason.")
        return 2

    # ⚠ THE CENSUS MUST NOT BE SKIPPING. It is one of the predicted catchers for C1/C2, and a
    # SKIP is indistinguishable from a pass in the failing-name set this harness reads.
    extract = "/tmp/w34-linear-csv-date-cells.txt"
    census_live = os.path.exists(extract) and os.path.getsize(extract) > 0
    print(f"== corpus extract at {extract}: "
          f"{'present — the census runs' if census_live else 'ABSENT — the census SKIPS and is not a catcher here'}")

    print("== BASELINE: the whole target must be green before any mutation ==")
    failing, build_error, r = run_tests()
    if build_error or failing:
        print(f"  NOT GREEN — {sorted(failing)} build_error={build_error}")
        print(r.stdout[-3000:])
        return 2
    print("  green\n")

    only = [a for a in sys.argv[1:] if a.startswith("C")]
    results = []
    for cid, path, old, new, predict, green, note in CONTROLS:
        if only and cid not in only:
            continue
        # C1 and C2 also red the census when the extract is present. Predicting it when it would
        # SKIP would score every one of them NOT AS PREDICTED for a reason that is not the code's.
        predict = list(predict) + ([CENSUS] if census_live and cid in ("C1", "C2", "C5") else [])
        src = open(path).read()
        before = sha(path)
        if src.count(old) != 1:
            results.append((cid, "ERROR", f"anchor matched {src.count(old)} times, want exactly 1", []))
            print(f"{cid}  ERROR  anchor matched {src.count(old)} times")
            continue
        try:
            open(path, "w").write(src.replace(old, new))
            if sha(path) == before:
                results.append((cid, "ERROR", "the edit changed no bytes", []))
                print(f"{cid}  ERROR  the edit changed no bytes")
                continue
            failing, build_error, r = run_tests()
            if build_error:
                verdict, detail = "ERROR", "the mutation stopped the package compiling"
                results.append((cid, verdict, detail, []))
                print(f"{cid}  {verdict}  {detail}")
                continue
            red = set(failing)
            missed = [t for t in predict if t not in red]
            broke = [t for t in green if t in red]
            if missed or broke:
                verdict = "NOT AS PREDICTED"
                detail = f"predicted-but-green={missed} must-stay-green-that-red={broke}"
            else:
                verdict = "CAUGHT"
                detail = f"{len(red)} red"
            results.append((cid, verdict, detail, sorted(red)))
            print(f"{cid}  {verdict}  {detail}")
            for t in predict:
                msg = failing.get(t, ["(green — the prediction was wrong)"])
                print(f"      {t}\n        {msg[0][:190]}")
            extra = sorted(red - set(predict))
            if extra:
                print(f"      also red: {extra}")
        finally:
            open(path, "w").write(src)
            assert sha(path) == before, f"{cid}: RESTORE FAILED for {path}"

    print("\n== SUMMARY ==")
    for cid, verdict, detail, _ in results:
        print(f"  {cid}  {verdict}  {detail}")
    return 1 if [c for c in results if c[1] != "CAUGHT"] else 0


if __name__ == "__main__":
    sys.exit(main())
