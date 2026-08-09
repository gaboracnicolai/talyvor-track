#!/usr/bin/env python3
"""w34-refused-rows-controls.py — positive controls for the REFUSED/FAILED split.

Run:  TRACK_TEST_DATABASE_URL=... python3 scripts/w34-refused-rows-controls.py

Each control MUTATES a shipped file, runs the tests, and requires:

  1. AN ANCHOR COUNT ASSERTED BEFORE THE EDIT. #71's lesson, paid for twice in this repo: a
     substitution that matches nothing edits zero bytes and is byte-indistinguishable from a
     guard that works. If the count is not exactly what is expected, the control ABORTS.
  2. THE RED MUST SAY THE THING IT IS SUPPOSED TO SAY. #76's C1 / #78's C10: a test that reds for
     an unrelated reason (or reds on an earlier t.Fatalf and never reaches the assertion the
     control exists for) scores a catch for an assertion that never ran.
  3. A COMPANION THAT MUST STAY GREEN, run SEPARATELY. #74's C1: a control that reds everything —
     because nothing compiled — cannot tell "the guard caught it" from "the build broke".
  4. RESTORE FROM THE ORIGINAL BYTES, verified by sha256. #76's C7: reverse substitution for a
     DELETION control is str.replace("", old, 1), which Python INSERTS AT POSITION 0 and silently
     corrupts the file, after which every later verdict is noise.

⚠ AND AN ANCHOR ASSERTION PROVES THE EDIT APPLIED, NEVER THAT THE REPLACEMENT MEANT ANYTHING
(#76's C6, #80's (1) — the same lesson through four doors now). Two of the controls below exist
specifically because the obvious mutation is a no-op; they are marked NO-OP-HAZARD.
"""

import hashlib
import pathlib
import re
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
SOURCE = ROOT / "internal/importer/source.go"
RUNNER = ROOT / "internal/importer/runner.go"
STORE = ROOT / "internal/issue/store.go"

REFUSED_MIXED = "TestJobRow_RefusedRowIsCountedAsRefusedNotFailed"
REFUSED_ALL = "TestJobRow_AllRowsRefused"
GENUINE = "TestJobRow_GenuineFailureStaysInFailed"
COLLISION = "TestImport_DoesNotClobberANativeIssueSharingTheProviderKey"
CLASSIFY = "TestRun_UpsertErrorClassification"
REIMPORT = "TestImport_ReImportStillUpdatesItsOwnRow"
PARTIAL = "TestRunner_PartialImport_Observable"


def sha(p):
    return hashlib.sha256(p.read_bytes()).hexdigest()


def run_tests(names):
    """Run the named tests. Returns (passed, combined output)."""
    proc = subprocess.run(
        ["go", "test", "-count=1", "-run", "^(" + "|".join(names) + ")$", "./internal/importer/"],
        cwd=ROOT, capture_output=True, text=True,
    )
    return proc.returncode == 0, proc.stdout + proc.stderr


CONTROLS = [
    # (name, file, old, new, expected_anchor_count, target_tests, must_say, companion_tests, note)
    (
        "C1 full revert: a refusal is counted as a failure again",
        SOURCE,
        "\t\t\t\tif errors.Is(err, model.ErrIdentifierNotImportOwned) {\n\t\t\t\t\tout.Refused++\n\t\t\t\t} else {\n\t\t\t\t\tout.Skipped++\n\t\t\t\t}",
        "\t\t\t\tout.Skipped++",
        1,
        [REFUSED_MIXED, REFUSED_ALL],
        "want 1 — a protective refusal is exactly what this column is for",
        [PARTIAL, GENUINE],
        "the shipped pre-merge behaviour, put back",
    ),
    (
        "C2 blind the OTHER way: every upsert error becomes a refusal",
        SOURCE,
        "if errors.Is(err, model.ErrIdentifierNotImportOwned) {",
        "if true || errors.Is(err, model.ErrIdentifierNotImportOwned) {",
        1,
        [CLASSIFY],
        "a transport failure is not a policy refusal",
        [REFUSED_MIXED, REFUSED_ALL, COLLISION],
        "a transport/tenancy failure laundered into the refusal count — the direction "
        "that would make `failed` under-report instead of over-report",
    ),
    (
        "C3 runner stops writing the refusal count (the structural zero returns)",
        RUNNER,
        "out.Imported, out.Refused, out.Skipped, summary",
        "out.Imported, 0, out.Skipped, summary",
        1,
        [REFUSED_MIXED, REFUSED_ALL],
        "want 1 — a protective refusal is exactly what this column is for",
        [COLLISION, PARTIAL],
        "the unit level still classifies correctly; only the JOB ROW goes back to zero. "
        "This is #74's C9 / #78's C1 shape: a fix that stopped at ImportResult would be inert here",
    ),
    (
        "C4 terminalStatus stops counting refusals (the half I deliberately did NOT change)",
        RUNNER,
        "unlanded := out.Skipped + out.Refused",
        "unlanded := out.Skipped",
        1,
        [REFUSED_ALL],
        "must stay as loud as it is today",
        [GENUINE, PARTIAL],
        "an all-refused import would start reporting `succeeded` — this item's own "
        "'reported as success' shape, which is exactly what the merge refuses to introduce",
    ),
    (
        "C5 the summary calls a refusal a failure again",
        RUNNER,
        '\t\t\t"%d row(s) refused: an issue with that identifier already exists and was not created by an import",',
        '\t\t\t"%d row(s) failed",',
        1,
        [REFUSED_MIXED],
        "error_summary calls a refusal a failure",
        [GENUINE, PARTIAL],
        "the counters would be right and the SENTENCE still wrong",
    ),
    (
        "C6 THE IDENTITY TRAP: issue re-declares the sentinel instead of aliasing model's",
        STORE,
        "var ErrIdentifierNotImportOwned = model.ErrIdentifierNotImportOwned",
        'var ErrIdentifierNotImportOwned = errors.New("identifier not owned by an import")',
        1,
        [REFUSED_MIXED, REFUSED_ALL, COLLISION],
        "want 1 — a protective refusal is exactly what this column is for",
        [PARTIAL],
        "BYTE-IDENTICAL ERROR TEXT, different identity. errors.Is compares identity, so every "
        "refusal silently scores as a failure again and the message a human reads is unchanged. "
        "This is the failure mode that cannot be caught by reading",
    ),
    (
        "C7 swap the two counters at the Finish call (arity-preserving, compiles)",
        RUNNER,
        "out.Imported, out.Refused, out.Skipped, summary",
        "out.Imported, out.Skipped, out.Refused, summary",
        1,
        [REFUSED_MIXED, GENUINE],
        "want 1 — a protective refusal is exactly what this column is for",
        [COLLISION, REIMPORT],
        "#77's D9 shape: the columns hold each other's values while every source-level "
        "assertion stays green",
    ),
    (
        "C8 NO-OP-HAZARD: the failure-only summary wording is changed",
        RUNNER,
        'parts = append(parts, fmt.Sprintf("%d row(s) failed", out.Skipped))',
        'parts = append(parts, fmt.Sprintf("%d rows failed", out.Skipped))',
        1,
        [GENUINE],
        "a genuine failure must still say so",
        [REFUSED_MIXED, REFUSED_ALL],
        "pins that the failure-only sentence is BYTE-IDENTICAL to the pre-merge one, which is "
        "what keeps this merge from re-litigating wording #72/#74 pinned by test",
    ),
]


def main():
    originals = {p: p.read_bytes() for p in (SOURCE, RUNNER, STORE)}
    shas = {p: sha(p) for p in originals}

    print("BASELINE — everything must be green before any mutation is believed")
    ok, out = run_tests([REFUSED_MIXED, REFUSED_ALL, GENUINE, COLLISION, REIMPORT, PARTIAL])
    if not ok:
        print(out)
        print("ABORT: baseline is not green; no control below would mean anything.")
        return 1
    print("  baseline green\n")

    caught = 0
    for (name, path, old, new, want_count, targets, must_say, companions, note) in CONTROLS:
        print(f"── {name}")
        print(f"   why: {note}")
        text = path.read_text()
        got = text.count(old)
        if got != want_count:
            print(f"   ABORT: anchor matched {got}×, expected {want_count}. "
                  f"NOT RUN — this is a wrong edit, not a caught mutation.")
            path.write_bytes(originals[path])
            return 1
        print(f"   anchor asserted: {got}× (expected {want_count})")

        path.write_text(text.replace(old, new, 1))
        try:
            passed, output = run_tests(targets)
            said = must_say in output
            comp_passed, comp_out = run_tests(companions)
        finally:
            path.write_bytes(originals[path])          # restore from ORIGINAL BYTES, never reverse-substitution
            assert sha(path) == shas[path], f"RESTORE FAILED for {path}"

        if passed:
            print(f"   ⚠ NOT CAUGHT — {', '.join(targets)} stayed GREEN under this mutation")
        elif not said:
            print(f"   ⚠ RED BUT FOR THE WRONG REASON — expected the output to say {must_say!r}")
            print("      (a red that never reached the assertion the control exists for is not a catch)")
            print("   " + "\n   ".join(l for l in output.splitlines() if "FAIL" in l or "want" in l)[:900])
        elif not comp_passed:
            print(f"   ⚠ RED BUT WHOLESALE — companion {', '.join(companions)} also failed; "
                  f"cannot tell a catch from a broken build")
            print("   " + "\n   ".join(l for l in comp_out.splitlines() if "FAIL" in l)[:600])
        else:
            caught += 1
            print(f"   ✓ CAUGHT — red, said the thing, and {', '.join(companions)} stayed green")
        print()

    for p in originals:
        assert sha(p) == shas[p], f"{p} not restored"
    print(f"RESULT: {caught}/{len(CONTROLS)} caught. All files restored sha256-identical.")
    return 0 if caught == len(CONTROLS) else 1


if __name__ == "__main__":
    sys.exit(main())
