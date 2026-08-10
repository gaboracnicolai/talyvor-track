#!/usr/bin/env python3
"""w34-linear-csv-short-row-controls.py — positive controls for the short-row merge.

WHAT A CONTROL HAS TO DO HERE. Every guard this change adds passed on its first run once the fix
was in, which is exactly the state that has shipped three unfallible guards in this fleet. So each
mutation below names, IN ADVANCE, the test that must catch it. A mutation caught by a DIFFERENT
test than predicted is reported as a WRONG PREDICTION and KEPT WRONG in this file — the prediction
is the falsifiable claim, not the catch.

⚠⚠ THIS MERGE LOOSENS A REFUSAL, WHICH MAKES THE CONTROL SET SHAPED DIFFERENTLY FROM THIS ITEM'S
OTHERS. Every earlier merge here added a read; this one stops refusing a row. The two ways that
goes wrong are the two controls to read first:

  C2  the row is imported and NOTHING is reported   — a silent loosening, strictly worse than the
                                                      refusal it replaces
  C4  the TITLE check is removed too                — the narrowing becomes a deletion, and a
                                                      genuinely mangled row lands

C4/C4b are also the controls that prove I did not gut two OTHER people's tests. Retargeting a
fixture is how a guard gets blinded: TestJobRow_GenuineFailureStaysInFailed and
TestRunner_PartialImport_Observable both used a raggedly-short row as their example of a per-row
failure, and this merge made that example stop failing. Their fixtures moved to an empty title. If
they do not turn red, they were not retargeted — they were switched off.

⚠⚠ AND THAT TOOK TWO CONTROLS, WHICH IS THE MOST TRANSFERABLE THING IN THIS FILE. C4 removes the
required-title refusal and predicted BOTH of them. Only one fired. The other RAN AND PASSED —
checked in isolation rather than inferred from its absence in a failure list — because C4 mutates
linearRowMapper and that test drives jira_csv. THIS FILE HAS TWO REQUIRED-FIELD CHECKS, ONE PER
TRANSPORT. A control that mutates one of them justifies nothing whatever about a test that
exercises the other, and "the mutation is in the same file as the guard" is not the same claim as
"the mutation is on the path the guard runs". C4b is the missing half.

THE RUNNER IS ADAPTED FROM scripts/w34-linear-csv-updated-controls.py (#100), including the two
mechanisms that file fixed rather than inherited: edits are folded PER FILE IN ORDER (so two edits
in one file cannot erase each other), and restore runs in a `finally` (so a crash between mutate and
restore cannot leave the mutation on disk). Both are kept, and this header names them so the next
copy does not silently drop one.

THE LESSONS THE REST OF IT IS BUILT AROUND, each paid for in this repo or its siblings:
  · a build failure is NOT a catch — scored BUILD-BROKEN, the control is void
  · a test that never RAN is not a test that passed — verdicts read `--- FAIL:` out of `go test -v`
    and print the assertion MESSAGE, because a crash and a real catch look identical in a name list
  · every anchor is asserted UNIQUE before ANY write, and every write is verified to have CHANGED
    THE BYTES on disk — a control that silently matched nothing reads exactly like a dead guard
  · files are restored from SAVED BYTES and sha256-compared, never from git
  · NOT CAUGHT must be REACHABLE, or CAUGHT means nothing: C6 is an inverted control whose
    prediction IS "not caught"

Requires TRACK_TEST_DATABASE_URL and a real Postgres. Run from the repo root.
"""
import hashlib
import os
import re
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SOURCE_GO = os.path.join(ROOT, "internal/importer/source.go")
CSV_GO = os.path.join(ROOT, "internal/importer/csv.go")
RUNNER_GO = os.path.join(ROOT, "internal/importer/runner.go")

PKG = "./internal/importer/"

# ── the anchors, matched EXACTLY and asserted unique before any write ────────

# The seam this merge changed, in full. Anchoring on the whole block rather than on the `if` line
# alone is deliberate: the `if len(row) < s.expectedCols` shape appears once today, and pinning the
# body with it means a future edit that moves the body makes the anchor fail loudly instead of
# applying somewhere subtly different.
NOTE_BLOCK = """	var notes []FieldNote
	if len(row) < s.expectedCols {
		notes = append(notes, FieldNote{
			Field: fieldRowWidth,
			Value: fmt.Sprintf("%d of %d columns", len(row), s.expectedCols),
			Via:   viaShortRow,
		})
	}
"""

# C1: put the pre-merge refusal back, exactly as it read.
NOTE_BLOCK_REVERTED = """	if len(row) < s.expectedCols {
		return SourceRow{RowNum: s.rowNum, Err: fmt.Errorf("row %d: expected %d columns, got %d", s.rowNum, s.expectedCols, len(row))}, true
	}
	var notes []FieldNote
"""

# C2: import the row and say nothing — the silent loosening.
NOTE_BLOCK_SILENT = """	var notes []FieldNote
"""

# C3: report EVERY row as narrow, not only the narrow ones.
NOTE_IF_LINE = "	if len(row) < s.expectedCols {\n"
NOTE_IF_ALWAYS = "	if true {\n"

# C5: the widths in the reported line stop being the row's own.
NOTE_VALUE = '			Value: fmt.Sprintf("%d of %d columns", len(row), s.expectedCols),\n'
NOTE_VALUE_WRONG = '			Value: fmt.Sprintf("%d of %d columns", s.expectedCols, s.expectedCols),\n'

# C4: the mapper's required-field check, which is what the refusal was NARROWED to.
TITLE_CHECK = """	title := ci.get(row, "Title")
	if title == "" {
		return mappedIssue{}, errEmptyTitle
	}
"""
TITLE_CHECK_REMOVED = """	title := ci.get(row, "Title")
	if title == "" {
		title = "(untitled)"
	}
"""

# ⚠ THE JIRA MAPPER HAS ITS OWN COPY, AND C4 NOT REACHING IT IS A MEASURED WRONG PREDICTION —
# see C4b. jiraRowMapper reads `Summary`, falls back to `Title`, and only then refuses; the block
# below is the refusal at the end of that chain, which is a DIFFERENT `if title == ""` from
# linearRowMapper's. Two transports, two required-field checks, and a control that mutates one says
# nothing whatever about a test that drives the other.
JIRA_TITLE_CHECK = """	if title == "" {
		return mappedIssue{}, errEmptyTitle
	}
	rawStatus, rawPrio := ci.get(row, "Status"), ci.get(row, "Priority")
	status, statusOK := mapJiraStatus(rawStatus)
"""
JIRA_TITLE_CHECK_REMOVED = """	if title == "" {
		title = "(untitled)"
	}
	rawStatus, rawPrio := ci.get(row, "Status"), ci.get(row, "Priority")
	status, statusOK := mapJiraStatus(rawStatus)
"""

# C7: the note reaches ImportResult but never the job row.
FINISH_CALL = "out.Imported, out.Refused, out.Skipped, summary, out.Warnings)"
FINISH_CALL_DROPPED = "out.Imported, out.Refused, out.Skipped, summary, nil)"

# C6 (inverted): behaviourally inert — a pre-sized empty slice instead of a nil one.
NOTE_DECL = "	var notes []FieldNote\n	if len(row) < s.expectedCols {\n"
NOTE_DECL_INERT = "	notes := make([]FieldNote, 0, 1)\n	if len(row) < s.expectedCols {\n"

# ── the guards, by name ─────────────────────────────────────────────────────
G_FIXTURE = "TestLinearCSV_ShortRowFixtureIsActuallyShort"
G_IMPORTS = "TestLinearCSV_ARowMissingOnlyItsTrailingFieldStillImports"
G_FIELDS = "TestLinearCSV_TheShortRowKeepsEveryColumnTheImporterReads"
G_WARN = "TestLinearCSV_AShortRowIsReportedRatherThanSilent"
G_WIDE = "TestLinearCSV_TheWideShapeIsUnchanged"
G_FLOOR = "TestLinearCSV_ARowTruncatedPastTheTitleIsStillRefused"
G_DATE = "TestLinearCSV_AShortRowStillRefusesAnUnpinnedDateShape"
G_JOB_FIXTURE = "TestJobRow_LinearCSV_ShortRowFixturePremise"
G_JOB_IMPORTS = "TestJobRow_LinearCSV_AnExportThatOmitsItsTrailingFieldImports"
G_JOB_DATES = "TestJobRow_LinearCSV_TheShortRowKeepsTheDatesLinearSupplied"
G_JOB_WARN = "TestJobRow_LinearCSV_TheShortRowIsReportedInTheJobWarnings"
# Pre-existing tests this merge TOUCHED. They are listed here because a control's job is to prove
# they still fire, not to prove the new ones do.
G_CSVTEST = "TestImporter_ShortRowImportsAndIsReported"
G_GENUINE = "TestJobRow_GenuineFailureStaysInFailed"
G_PARTIAL = "TestRunner_PartialImport_Observable"
G_EMPTYTITLE = "TestImporter_RowsWithEmptyTitleAreSkipped"
# A pre-existing guard this merge did NOT touch, and the independent catcher for C3.
G_NOWARN = "TestLinearCSV_AFullyReadableRowAddsNoWarning"


class Edit:
    def __init__(self, path, old, new):
        self.path, self.old, self.new = path, old, new


CONTROLS = [
    dict(
        name="C1  REVERT THE FIX — the whole-header width refusal comes back",
        why="The red-first run, re-run as a control. ⚠ G_FLOOR IS A PREDICTED CATCHER HERE AND THAT "
            "IS THE POINT OF ITS MESSAGE ASSERTION: with the refusal back, the truncated-past-title "
            "row is still skipped (so a count-only floor would stay green and prove nothing), but "
            "the error reads 'expected 30 columns, got 2' instead of naming the title. A floor that "
            "cannot tell those apart is a floor that cannot see this control.",
        edits=[Edit(SOURCE_GO, NOTE_BLOCK, NOTE_BLOCK_REVERTED)],
        predict={G_IMPORTS, G_FIELDS, G_WARN, G_DATE, G_FLOOR,
                 G_JOB_IMPORTS, G_JOB_DATES, G_JOB_WARN, G_CSVTEST},
        must_stay_green={G_FIXTURE, G_JOB_FIXTURE, G_WIDE, G_NOWARN},
        expect_caught=True,
    ),
    dict(
        name="C2  THE SILENT LOOSENING — the row imports and nothing is reported",
        why="THE MOST DANGEROUS OUTCOME OF THIS MERGE, AND THE ONE THE REPORTING HALF EXISTS FOR. "
            "A row truncated past a column the mapper DOES read would land with that column silently "
            "empty, which is indistinguishable from a column the provider left blank — the "
            "structural-zero class this package reports everywhere else. Importing without "
            "reporting is strictly worse than the refusal it replaces, so if this comes back NOT "
            "CAUGHT the merge should not land.",
        edits=[Edit(SOURCE_GO, NOTE_BLOCK, NOTE_BLOCK_SILENT)],
        predict={G_WARN, G_DATE, G_JOB_WARN, G_CSVTEST},
        must_stay_green={G_IMPORTS, G_FIELDS, G_WIDE, G_FLOOR, G_JOB_IMPORTS, G_JOB_DATES,
                         G_FIXTURE, G_JOB_FIXTURE, G_NOWARN},
        expect_caught=True,
    ),
    dict(
        name="C3  THE NOTE FIRES ON EVERY ROW, not only the narrow ones",
        why="THE CONTROL THAT JUSTIFIES G_WIDE'S NEGATIVE ASSERTION EXISTING. Warning about every "
            "row would satisfy every positive assertion in this change set — the short rows still "
            "import, still carry a note, still report exactly one line — while telling an operator "
            "their perfectly ordinary export is truncated. Only an assertion of ABSENCE on the wide "
            "shape can see it. ⚠ G_NOWARN is a SECOND, INDEPENDENT catcher I did not write: #89's "
            "'a fully readable row adds no warning'. Naming it in advance is the check on my own "
            "prediction — if the only thing that reds is my own test, the negative assertion is "
            "load-bearing; if only G_NOWARN reds, mine is decoration. "
            "\u26a0 OBSERVED: BOTH predicted catchers fired, so the negative assertion IS "
            "load-bearing — and TEN MORE fired that I did not predict (every 'a clean import "
            "produces no warnings' test in the package, on both transports). That is an "
            "UNDER-PREDICTION and it is kept: warning on every row is a far broader breakage than "
            "the two names I wrote down, and a prediction that names 2 of 12 catchers is a "
            "prediction I had not thought through. It does not change the verdict; it changes how "
            "much this control's CAUGHT tells you about MY test specifically, which is why "
            "G_WIDE's absence assertion is still worth having.",
        edits=[Edit(SOURCE_GO, NOTE_IF_LINE, NOTE_IF_ALWAYS)],
        predict={G_WIDE, G_NOWARN},
        must_stay_green={G_IMPORTS, G_FIELDS, G_WARN, G_FLOOR, G_JOB_IMPORTS, G_JOB_WARN,
                         G_FIXTURE, G_JOB_FIXTURE},
        expect_caught=True,
    ),
    dict(
        name="C4  THE NARROWING BECOMES A DELETION — the required-title check is removed",
        why="TWO JOBS IN ONE MUTATION, AND THE SECOND MATTERS MORE. (a) It is the control for "
            "G_FLOOR: this merge claims to NARROW the refusal to the mapper's own required field, "
            "and a narrowing whose remaining half nobody has watched fail is a deletion with better "
            "prose. (b) ⚠⚠ IT IS THE CONTROL FOR TWO TESTS THIS MERGE RETARGETED. G_GENUINE and "
            "G_PARTIAL both used a raggedly-short row as their example of a per-row failure; this "
            "merge made that example stop failing, so their fixtures moved to an empty title. If "
            "they do not red here, they were blinded rather than retargeted — and that is exactly "
            "the failure mode a merge that edits somebody else's test is most likely to ship. "
            "\u26a0\u26a0 THE PREDICTION BELOW WAS WRONG ON ONE NAME AND IS KEPT WRONG. G_GENUINE was "
            "predicted to fire here and did NOT — it RAN and PASSED, checked in isolation rather "
            "than read off an absence from a failure list. The reason is not that the test is "
            "blind: it drives jira_csv, and this mutation removes linearRowMapper's required-field "
            "check. THERE ARE TWO REQUIRED-FIELD CHECKS IN THIS FILE, one per transport, and a "
            "control that mutates one justifies nothing about a test that exercises the other. "
            "C4b below is the same mutation on the Jira copy, and it is what actually justifies "
            "G_GENUINE. ⚠ TestImporter_NonMemberUploadIsNotRead also fired unpredicted: with no "
            "title required, its large payload imports 19,420 rows instead of 1, which is a "
            "fixture-scale effect rather than a statement about this merge.",
        edits=[Edit(CSV_GO, TITLE_CHECK, TITLE_CHECK_REMOVED)],
        predict={G_FLOOR, G_PARTIAL, G_EMPTYTITLE},
        must_stay_green={G_IMPORTS, G_FIELDS, G_WARN, G_WIDE, G_JOB_IMPORTS, G_JOB_WARN,
                         G_FIXTURE, G_JOB_FIXTURE},
        expect_caught=True,
    ),
    dict(
        name="C4b THE SAME DELETION ON THE JIRA MAPPER — the control C4's wrong prediction demanded",
        why="C4 predicted G_GENUINE and G_GENUINE did not fire, because C4 mutates linearRowMapper "
            "and G_GENUINE drives jira_csv. That is a fact about MY PREDICTION, not about the test — "
            "but until this control existed, 'G_GENUINE still fires after its fixture was "
            "retargeted' was an unwatched claim, and retargeting somebody else's fixture is exactly "
            "how a guard gets switched off. This is the mutation that reaches it: the Jira "
            "transport's own required-field refusal, removed. If G_GENUINE does not red HERE, the "
            "retarget in refused_rows_job_test.go blinded it and the merge must not land.",
        edits=[Edit(CSV_GO, JIRA_TITLE_CHECK, JIRA_TITLE_CHECK_REMOVED)],
        predict={G_GENUINE},
        must_stay_green={G_IMPORTS, G_FIELDS, G_WARN, G_WIDE, G_FLOOR, G_DATE,
                         G_JOB_IMPORTS, G_JOB_WARN, G_FIXTURE, G_JOB_FIXTURE},
        expect_caught=True,
    ),
    dict(
        name="C5  THE REPORTED WIDTHS STOP BEING THE ROW'S OWN",
        why="The line still appears, still once, still on the right rows — and its numbers are "
            "always 'N of N'. Those two numbers are the only thing separating a harmless truncation "
            "(29 of 30, a trailing field nothing reads) from a harmful one (2 of 9, most of the "
            "export), so a warning that carries the wrong ones is a warning that cannot be acted "
            "on. Every assertion that merely looks for the SENTENCE stays green here, which is why "
            "one test asserts the digits.",
        edits=[Edit(SOURCE_GO, NOTE_VALUE, NOTE_VALUE_WRONG)],
        predict={G_CSVTEST},
        must_stay_green={G_IMPORTS, G_FIELDS, G_WARN, G_WIDE, G_FLOOR, G_DATE,
                         G_JOB_IMPORTS, G_JOB_WARN, G_NOWARN},
        expect_caught=True,
    ),
    dict(
        name="C7  THE WARNING REACHES ImportResult AND NEVER THE JOB ROW",
        why="THE CONTROL THE JOB-LEVEL WARNING TEST EXISTS FOR. Every source-level assertion in this "
            "change stays true — the note is built, tallied, rendered and returned — while the "
            "operator, who reads the job row and not an in-process struct, is told nothing. A change "
            "set held only by source-level tests would be green here and the product would report "
            "nothing at all. \u26a0 OBSERVED: the predicted catcher fired and so did TWENTY-ONE "
            "others — this mutation drops the warnings of EVERY job in the package, not only the "
            "short-row one, so it is a blunt instrument for the claim it is making. Kept as an "
            "UNDER-PREDICTION rather than narrowed after the fact: the honest reading is that C7 "
            "proves the ImportResult\u2192job-row seam is guarded AT ALL, and the reason to trust "
            "G_JOB_WARN specifically is C2, where it fired alongside its source-level twin on a "
            "mutation confined to this merge's own seam.",
        edits=[Edit(RUNNER_GO, FINISH_CALL, FINISH_CALL_DROPPED)],
        predict={G_JOB_WARN},
        must_stay_green={G_WARN, G_IMPORTS, G_FIELDS, G_WIDE, G_FLOOR, G_DATE, G_CSVTEST,
                         G_JOB_IMPORTS, G_JOB_DATES},
        expect_caught=True,
    ),
    dict(
        name="C6  INVERTED — a pre-sized empty note slice instead of a nil one",
        why="PREDICTED NOT CAUGHT, AND THIS IS THE ROW THAT MAKES EVERY `CAUGHT` ABOVE MEAN "
            "SOMETHING. `var notes []FieldNote` and `make([]FieldNote, 0, 1)` differ on disk and "
            "cannot differ in any observable value: concatNotes appends both into a fresh slice and "
            "the pipeline tallies notes into a map. A harness that reports CAUGHT for every mutation "
            "it is given has measured nothing. ⚠ It is a no-op AT THE SEAM rather than a spelling "
            "change somewhere else, because 'behaviourally inert' is a claim about the PRODUCT that "
            "does not transfer to a guard whose job is to pin bytes (#99's C7 lesson).",
        edits=[Edit(SOURCE_GO, NOTE_DECL, NOTE_DECL_INERT)],
        predict=set(),
        expect_caught=False,
    ),
]


def sha(b):
    return hashlib.sha256(b).hexdigest()


def read(path):
    with open(path, "rb") as f:
        return f.read()


def run_tests():
    """Returns (build_ok, failing_test_names, message_lines)."""
    p = subprocess.run(
        ["go", "test", "-timeout", "300s", "-count=1", "-v", PKG],
        cwd=ROOT, capture_output=True, text=True, env=dict(os.environ))
    out = p.stdout + p.stderr
    if ("[build failed]" in out or "cannot use" in out or "undefined:" in out
            or "syntax error" in out or "declared and not used" in out):
        return False, set(), [l for l in out.splitlines() if l.strip()][:14]
    failing = set(re.findall(r"^\s*--- FAIL: (\S+)", out, re.M))
    ran = set(re.findall(r"^=== RUN\s+(\S+)", out, re.M))
    msgs = [l.rstrip() for l in out.splitlines()
            if re.match(r"^\s{4,}\S+_test\.go:\d+:", l)]
    failing = {f.split("/")[0] for f in failing}
    return True, failing, (msgs[:12] if msgs else [f"(no assertion messages; {len(ran)} tests ran)"])


def apply_control(ctrl, saved):
    """Assert EVERY anchor unique BEFORE any write, then write. Edits folded per file, in order."""
    bodies = {p: b.decode() for p, b in saved.items()}
    for e in ctrl["edits"]:
        n = bodies[e.path].count(e.old)
        if n != 1:
            return f"ANCHOR NOT UNIQUE in {os.path.basename(e.path)}: {n} occurrences"
        bodies[e.path] = bodies[e.path].replace(e.old, e.new, 1)
    for path in dict.fromkeys(e.path for e in ctrl["edits"]):
        with open(path, "w") as f:
            f.write(bodies[path])
        if read(path) == saved[path]:
            return f"WRITE CHANGED NOTHING in {os.path.basename(path)}"
    return None


def restore(saved):
    bad = []
    for path, body in saved.items():
        with open(path, "wb") as f:
            f.write(body)
        if sha(read(path)) != sha(body):
            bad.append(path)
    return bad


def main():
    if not os.environ.get("TRACK_TEST_DATABASE_URL"):
        print("TRACK_TEST_DATABASE_URL unset — the job controls need real Postgres. Refusing to run.")
        return 2

    saved = {p: read(p) for p in (SOURCE_GO, CSV_GO, RUNNER_GO)}
    print("SAVED BYTES:")
    for p, b in saved.items():
        print(f"  {os.path.basename(p):<28} {len(b):>7} bytes  sha256 {sha(b)[:16]}")

    print("\nBASELINE (no mutation) — must be GREEN, or every verdict below is meaningless")
    ok, failing, msgs = run_tests()
    if not ok or failing:
        print(f"  BASELINE BROKEN: build_ok={ok} failing={sorted(failing)}")
        for m in msgs:
            print("   ", m)
        restore(saved)
        return 2
    print("  baseline green")

    results = []
    try:
        for ctrl in CONTROLS:
            print("\n" + "=" * 78)
            print(ctrl["name"])
            print("  WHY: " + ctrl["why"])
            print(f"  PREDICTED CATCHER(S): {sorted(ctrl['predict']) or 'NONE (inverted control)'}")

            err = apply_control(ctrl, saved)
            if err:
                print(f"  CONTROL VOID — {err}")
                results.append((ctrl["name"], "VOID"))
                restore(saved)
                continue

            ok, failing, msgs = run_tests()
            restore(saved)

            if not ok:
                print("  BUILD-BROKEN — a compile error is not a caught mutation; control is void")
                for m in msgs:
                    print("   ", m)
                results.append((ctrl["name"], "BUILD-BROKEN"))
                continue

            predicted = ctrl["predict"]
            caught = bool(failing)
            verdict = "CAUGHT" if caught else "NOT CAUGHT"
            expected = "CAUGHT" if ctrl["expect_caught"] else "NOT CAUGHT"
            print(f"  OBSERVED FAIL: {sorted(failing) or 'none'}")
            for m in msgs:
                print("   ", m)

            notes = []
            if verdict != expected:
                notes.append(f"⚠ EXPECTED {expected}, OBSERVED {verdict}")
            missed = predicted - failing
            extra = failing - predicted
            if missed:
                notes.append(f"⚠ PREDICTED BUT DID NOT FIRE: {sorted(missed)}")
            if extra:
                notes.append(f"⚠ FIRED BUT NOT PREDICTED: {sorted(extra)}")
            broke = ctrl.get("must_stay_green", set()) & failing
            if broke:
                notes.append(f"⚠⚠ MUST-STAY-GREEN WENT RED: {sorted(broke)}")

            status = "AS PREDICTED" if not notes else "; ".join(notes)
            print(f"  VERDICT: {verdict} — {status}")
            results.append((ctrl["name"], status if notes else f"{verdict} as predicted"))
    finally:
        bad = restore(saved)
        print("\nRESTORE:", "byte-identical" if not bad else f"⚠⚠ FAILED for {bad}")

    print("\n" + "=" * 78)
    print("SUMMARY")
    for name, status in results:
        print(f"  {status:<30} {name}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
