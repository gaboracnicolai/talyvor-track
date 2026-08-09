#!/usr/bin/env python3
"""w34-warning-bound-controls.py — the positive-control campaign behind the ImportResult.Warnings bound.

Same contract as scripts/w34-jira-csv-labels-controls.py: anchor count asserted BEFORE the edit,
each red required to SAY the thing it is supposed to say, each with a companion that must stay
GREEN, edits staged CUMULATIVELY, the file restored sha256-identical FROM THE ORIGINAL BYTES.

    TRACK_TEST_DATABASE_URL=postgres://... python3 scripts/w34-warning-bound-controls.py

⚠ C9 IS THE ONE WORTH READING, AND ITS FIRST VERSION WAS WRONG IN MY FAVOUR. It RAISES the bound to
1000 rather than breaking anything. I wrote that every unit test reads `maxWarningExemplars` and so
would compare the constant to itself (#75's C6), leaving only the hardcoded job-level literal to
notice. MEASURED: the companion went RED too. Several unit cases feed ABSOLUTE counts (500, 3000)
against a SYMBOLIC bound, so raising the bound past 500 breaks that relationship and they catch it
after all. The guard is stronger than I claimed; the claim is corrected here rather than left
standing because it flattered the design.

⚠ TWO CONTROLS (C2, C6) FIRST REPORTED **NOT CAUGHT** FOR A GUARD THAT WAS WORKING, AND THE REASON IS
worth keeping: the `must_say` string named a message the test never reached, because it failed
EARLIER on the line-count assertion. #78's C10 through the other door — there, a catch was scored for
an assertion that never ran; here, a real catch was scored as a miss. "Require the red to say the
thing" is the right rule and it makes the HARNESS a thing that can be wrong: a NOT CAUGHT is a claim
about the guard AND about my prediction of how it fails, and only reading the output tells them apart.
"""

import hashlib
import os
import pathlib
import subprocess
import sys

REPO = pathlib.Path(__file__).resolve().parent.parent
TARGET = REPO / "internal/importer/source.go"

GROUP_KEY = "\t\tk := kind{n.Field, n.Mapped, n.Via, n.ViaValue, n.ViaResolved}"
GROUP_KEY_GLOBAL = "\t\tk := kind{}"

SORT_EXEMPLARS = "\t\tsort.Slice(notes, func(i, j int) bool { return notes[i].Value < notes[j].Value })"

BOUND_APPLY = """		shown := notes
		if len(notes) > maxWarningExemplars {
			shown = notes[:maxWarningExemplars]
		}"""
BOUND_NONE = """		shown := notes
		if false {
			shown = notes[:maxWarningExemplars]
		}"""
BOUND_DROPS_EXEMPLARS = """		shown := notes[:0]
		if len(notes) > maxWarningExemplars {
			shown = notes[:0]
		}"""

SUMMARY_BLOCK = """		if len(notes) > maxWarningExemplars {
			restValues, restIssues := 0, 0
			for _, n := range notes[maxWarningExemplars:] {
				restValues++
				restIssues += degraded[n]
			}
			out = append(out, fmt.Sprintf(
				"%s%d further distinct %s value(s) across %d issue(s) not listed individually (%d shown above)",
				warningSummaryPrefix, restValues, notes[0].Field, restIssues, len(shown)))
		}"""
SUMMARY_GONE = """		if false {
			restValues, restIssues := 0, 0
			for _, n := range notes[maxWarningExemplars:] {
				restValues++
				restIssues += degraded[n]
			}
			out = append(out, fmt.Sprintf(
				"%s%d further distinct %s value(s) across %d issue(s) not listed individually (%d shown above)",
				warningSummaryPrefix, restValues, notes[0].Field, restIssues, len(shown)))
		}"""
SUMMARY_SWAPPED = """		if len(notes) > maxWarningExemplars {
			restValues, restIssues := 0, 0
			for _, n := range notes[maxWarningExemplars:] {
				restValues++
				restIssues += degraded[n]
			}
			out = append(out, fmt.Sprintf(
				"%s%d further distinct %s value(s) across %d issue(s) not listed individually (%d shown above)",
				warningSummaryPrefix, restIssues, notes[0].Field, restValues, len(shown)))
		}"""

# ⚠ THE OBVIOUS OFF-BY-ONE IS A NO-OP AND WAS MEASURED AS ONE BEFORE IT WAS REPLACED. Flipping
# `len(notes) > maxWarningExemplars` to `>=` on the SHOWN clamp re-slices notes[:10] to itself at
# exactly ten, and the summary is governed by its own condition — real bytes edited, zero behaviour
# changed, the control GREEN and the guard perfectly fine. The load-bearing boundary is the SUMMARY
# condition, so that is what this mutates.
SUMMARY_OFF_BY_ONE = SUMMARY_BLOCK.replace(
    "if len(notes) > maxWarningExemplars {", "if len(notes) >= maxWarningExemplars {", 1)

CONST_10 = "const maxWarningExemplars = 10"

# red: (test regex, substring the failure output MUST contain) · green: must still pass
CONTROLS = [
    ("C1  the bound removed entirely — the shipped pre-merge behaviour",
     [(BOUND_APPLY, BOUND_NONE, 1), (SUMMARY_BLOCK, SUMMARY_GONE, 1)],
     ("TestWarnings_AreBoundedRegardlessOfHowManyDistinctValuesArrive", "want exactly 10 exemplars + 1 summary"),
     "TestWarnings_OneRepeatedValueIsStillOneLineWithItsCount"),

    ("C2  the exemplars bounded but the summary dropped — the report goes quiet about the rest",
     [(SUMMARY_BLOCK, SUMMARY_GONE, 1)],
     ("TestWarnings_AreBoundedRegardlessOfHowManyDistinctValuesArrive", "produced 10 warning lines"),
     "TestWarnings_AtOrUnderTheBoundNothingChanges"),

    ("C3  one GLOBAL bound instead of one per note kind — a noisy column swallows the report",
     [(GROUP_KEY, GROUP_KEY_GLOBAL, 1)],
     ("TestWarnings_ANoisyKindDoesNotCrowdOutAnother", "was crowded out"),
     "TestWarnings_AreBoundedRegardlessOfHowManyDistinctValuesArrive"),

    ("C4  the exemplars chosen by map iteration order — two runs of one import stop matching",
     [(SORT_EXEMPLARS, "", 1)],
     ("TestWarnings_TheSameImportRendersIdenticallyEveryTime", "map iteration order is reaching the report"),
     "TestWarnings_OneRepeatedValueIsStillOneLineWithItsCount"),

    ("C5  the summary's two numbers swapped — invisible to any fixture where they are equal",
     [(SUMMARY_BLOCK, SUMMARY_SWAPPED, 1)],
     ("TestWarnings_TheSummaryDistinguishesValuesFromIssues", "summary should name"),
     "TestWarnings_AreBoundedRegardlessOfHowManyDistinctValuesArrive"),

    ("C6  off by one on the SUMMARY condition — a summary appears at exactly the bound, on a report"
     " that needed none",
     [(SUMMARY_BLOCK, SUMMARY_OFF_BY_ONE, 1)],
     ("TestWarnings_AtOrUnderTheBoundNothingChanges", "no summary may appear at or under the bound"),
     "TestWarnings_OneRepeatedValueIsStillOneLineWithItsCount"),

    ("C7  the bound drops the exemplars entirely and keeps only the summary",
     [(BOUND_APPLY, BOUND_DROPS_EXEMPLARS, 1)],
     ("TestWarnings_AtOrUnderTheBoundNothingChanges", "want exactly 1"),
     "TestWarnings_NoDegradedRowsIsEmptyNotNil"),

    ("C8  the bound set to zero — every finding becomes a summary and no value is ever shown",
     [(CONST_10, "const maxWarningExemplars = 0", 1)],
     ("TestWarnings_AtOrUnderTheBoundNothingChanges", "summary line appeared"),
     "TestWarnings_NoDegradedRowsIsEmptyNotNil"),

    ("C9  the bound RAISED to 1000 — the hardcoded job-level literal catches it (and so, MEASURED"
     " against my own claim, do the unit cases that feed absolute counts)",
     [(CONST_10, "const maxWarningExemplars = 1000", 1)],
     ("TestJobRow_WarningsAreBoundedInPostgres", "want at most 11"),
     "TestWarnings_OneRepeatedValueIsStillOneLineWithItsCount"),

    ("C10 the bound removed, measured AT THE DATABASE through the async runner",
     [(BOUND_APPLY, BOUND_NONE, 1), (SUMMARY_BLOCK, SUMMARY_GONE, 1)],
     ("TestJobRow_WarningsAreBoundedInPostgres", "warnings TEXT[] holds"),
     "TestJobRow_ARepeatedValueStillReportsItsFullCount"),
]


def run(pattern):
    r = subprocess.run(["go", "test", "-count=1", "-timeout", "300s", "./internal/importer/", "-run", pattern],
                       cwd=REPO, capture_output=True, text=True, env=os.environ)
    return r.returncode, r.stdout + r.stderr


def main():
    if not os.environ.get("TRACK_TEST_DATABASE_URL"):
        print("TRACK_TEST_DATABASE_URL is unset — C9/C10 drive real Postgres and would red for the "
              "wrong reason. Set it and re-run.")
        return 2

    original = TARGET.read_bytes()
    original_sha = hashlib.sha256(original).hexdigest()
    print(f"source.go sha256 {original_sha[:16]}  ({len(original)} bytes)\n")

    code, out = run("TestWarnings_|TestJobRow_")
    if code != 0:
        print("the suite is not green before the campaign — nothing below would mean anything")
        print(out[-2000:])
        return 1
    print("baseline: green\n")

    caught = 0
    for name, edits, (red_test, must_say), green_test in CONTROLS:
        text = original.decode()
        ok = True
        for old, new, want_n in edits:              # CUMULATIVE
            n = text.count(old)
            if n != want_n:
                print(f"{name}\n     ⚠ ANCHOR COUNT {n}, EXPECTED {want_n} — NOT RUN")
                ok = False
                break
            text = text.replace(old, new, want_n)
        if not ok:
            continue

        TARGET.write_bytes(text.encode())
        try:
            red_code, red_out = run(red_test)
            green_code, _ = run(green_test)
        finally:
            TARGET.write_bytes(original)
            if hashlib.sha256(TARGET.read_bytes()).hexdigest() != original_sha:
                print("⚠ RESTORE FAILED — stopping before the next control mutates a corrupt file")
                return 1

        said = must_say in red_out
        verdict = "CAUGHT" if (red_code != 0 and said and green_code == 0) else "NOT CAUGHT"
        caught += verdict == "CAUGHT"
        print(f"{name}\n     {verdict}: {red_test} {'RED' if red_code else 'GREEN'}"
              f" · says-the-thing {'yes' if said else 'NO'}"
              f" · companion {'green' if green_code == 0 else 'RED'}")
        if verdict == "NOT CAUGHT":
            print("     " + "\n     ".join(l for l in red_out.splitlines()
                                            if "want" in l or "FAIL" in l)[:700])

    print(f"\n{caught}/{len(CONTROLS)} caught · source.go restored sha256-identical "
          f"({hashlib.sha256(TARGET.read_bytes()).hexdigest()[:16]})")
    return 0 if caught == len(CONTROLS) else 1


if __name__ == "__main__":
    sys.exit(main())
