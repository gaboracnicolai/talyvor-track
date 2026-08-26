#!/usr/bin/env python3
"""Positive controls for W3.5's four mock guards AND its real-Postgres characterisation test.

WHY THE LAST ONE MATTERS MOST
    Four of the five arms below break the new warnings and watch a guard go red — ordinary.
    M5 is different: it changes the KEY the notification lookup uses, which is the exact
    product change this item deliberately did NOT make. The real-Postgres test is a
    CHARACTERISATION test — it asserts today's behaviour and therefore passes on main — and a
    characterisation test that cannot detect the change it characterises is decoration. M5 is
    the only thing standing between that test and being decoration.

PREDICTED BEFORE THE RUN, and the catchers are deliberately DISJOINT so no arm can hide
behind another:

    M1  drop the `credited` capture (back to `_`)     -> divergence RED, lookup-error GREEN
    M2  drop the `lookupErr` capture (back to `_`)    -> lookup-error RED, divergence GREEN
    M3  warn whenever issue == nil (drop `credited>0`)-> the nothing-credited control RED
    M4  warn on every alert (guard removed entirely)  -> the working-case control RED
    M5  notification lookup keyed on lens_feature     -> the real-PG characterisation test RED

DISCIPLINE
    - Refuses if the target files are already modified, or if anything is not green first.
    - Every mutation asserts it CHANGED THE BYTES; a drifted anchor stops the run rather than
      scoring NOT-CAUGHT for a defect it never introduced.
    - Restored in a `finally`, sha256-verified at the end, suite re-run green after.
"""
from __future__ import annotations

import hashlib
import os
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
WEBHOOK = ROOT / "internal" / "lensintegration" / "webhook.go"

MOCK_TESTS = "TestSpendAlert_CreditedButNoIdentifierMatch_IsLoud|TestSpendAlert_IdentifierMatches_StaysQuiet|TestSpendAlert_NothingCreditedAndNoMatch_IsNotADivergence|TestSpendAlert_LookupError_IsReportedSeparately"
PG_TEST = "TestSpendAlert_MoneyFollowsLensFeature_AlertFollowsIdentifier_RealPG"

DIVERGENCE = "TestSpendAlert_CreditedButNoIdentifierMatch_IsLoud"
LOOKUP_ERR = "TestSpendAlert_LookupError_IsReportedSeparately"
QUIET = "TestSpendAlert_IdentifierMatches_StaysQuiet"
NOT_DIVERGENCE = "TestSpendAlert_NothingCreditedAndNoMatch_IsNotADivergence"


def sha(p: Path) -> str:
    return hashlib.sha256(p.read_bytes()).hexdigest()


def run(pattern: str) -> tuple[int, str]:
    r = subprocess.run(
        ["go", "test", "-count=1", "-run", pattern, "-v", "./internal/lensintegration/"],
        cwd=ROOT, capture_output=True, text=True,
    )
    return r.returncode, r.stdout + r.stderr


def failed_tests(out: str) -> set[str]:
    """Names from `--- FAIL: TestName (0.00s)`.

    ⚠ THE FIRST VERSION OF THIS TOOK `ln.split()[-1]`, which is the DURATION, so every arm
    below scored `{"(0.00s)"}` and the campaign reported 0 of 5 — the mutations had all landed
    and the classifier could not name them. It failed loudly rather than passing falsely, which
    is the only reason it was caught in one run; a parser that had defaulted to an empty set
    would have reported NOT-CAUGHT for five defects that were all caught.
    """
    out_names = set()
    for ln in out.splitlines():
        ln = ln.strip()
        if ln.startswith("--- FAIL:"):
            rest = ln[len("--- FAIL:"):].strip()
            out_names.add(rest.split()[0])
    if not out_names and "--- FAIL:" in out:
        raise SystemExit("REFUSE: output contains '--- FAIL:' but no test name could be parsed "
                         "from it — the classifier is broken, not the tree.")
    return out_names


def mutate(path: Path, old: str, new: str) -> str:
    before = path.read_text(encoding="utf-8")
    if before.count(old) != 1:
        raise SystemExit(
            f"REFUSE: anchor for a mutation of {path.name} occurs {before.count(old)}x, want 1. "
            "It has drifted; scoring now would report NOT-CAUGHT for a defect never introduced."
        )
    path.write_text(before.replace(old, new, 1), encoding="utf-8")
    return before


# (name, pattern-to-run, old, new, tests that MUST fail, tests that MUST pass)
MUTATIONS = [
    ("M1 drop the `credited` capture", MOCK_TESTS,
     "\tcredited, err := h.issues.RecordSpendEvent(",
     "\tcredited := 0\n\t_, err := h.issues.RecordSpendEvent(",
     {DIVERGENCE}, {LOOKUP_ERR, QUIET, NOT_DIVERGENCE}),

    ("M2 drop the `lookupErr` capture", MOCK_TESTS,
     "\tissue, lookupErr := h.issues.GetByIdentifier(ctx, p.Feature, p.WorkspaceID)",
     "\tvar lookupErr error\n\tissue, _ := h.issues.GetByIdentifier(ctx, p.Feature, p.WorkspaceID)",
     {LOOKUP_ERR}, {DIVERGENCE, QUIET, NOT_DIVERGENCE}),

    ("M3 warn whenever issue == nil", MOCK_TESTS,
     "\tcase issue == nil && credited > 0:",
     "\tcase issue == nil:",
     {NOT_DIVERGENCE}, {DIVERGENCE, LOOKUP_ERR, QUIET}),

    ("M4 warn on every alert", MOCK_TESTS,
     "\tcase issue == nil && credited > 0:",
     "\tcase true:",
     {QUIET, NOT_DIVERGENCE}, {DIVERGENCE, LOOKUP_ERR}),

    ("M5 notification lookup keyed on lens_feature", PG_TEST,
     "\tissue, lookupErr := h.issues.GetByIdentifier(ctx, p.Feature, p.WorkspaceID)",
     "\tissue, lookupErr := h.issues.GetByIdentifier(ctx, \"ENG-1\", p.WorkspaceID)",
     {PG_TEST}, set()),
]


def main() -> int:
    if not os.environ.get("TRACK_TEST_DATABASE_URL"):
        print("REFUSE: TRACK_TEST_DATABASE_URL is unset. M5 needs real Postgres, and a run that "
              "silently skipped it would report 4 of 5 as though it had scored 5.")
        return 3
    dirty = subprocess.run(["git", "status", "--porcelain", "--", str(WEBHOOK)],
                           cwd=ROOT, capture_output=True, text=True).stdout.strip()
    if dirty:
        print("REFUSE: internal/lensintegration/webhook.go is already modified:\n" + dirty)
        return 3

    baseline = sha(WEBHOOK)
    rc, out = run(f"{MOCK_TESTS}|{PG_TEST}")
    if rc != 0:
        print("REFUSE: the suite is not green on the untouched tree — nothing below would mean "
              f"anything.\n{out[-2000:]}")
        return 3
    print("clean tree: all five W3.5 tests GREEN\n")

    failures = 0
    for name, pattern, old, new, must_fail, must_pass in MUTATIONS:
        original = None
        try:
            original = mutate(WEBHOOK, old, new)
            _, out = run(pattern)
            got = failed_tests(out)
            ok = got == must_fail
            failures += 0 if ok else 1
            short = ", ".join(sorted(t.replace("TestSpendAlert_", "") for t in got)) or "(none)"
            print(f"  [{'ok ' if ok else 'BAD'}] {name:<44} -> RED: {short}")
            if not ok:
                want = ", ".join(sorted(t.replace("TestSpendAlert_", "") for t in must_fail))
                print(f"      expected exactly: {want}")
            # …and the arms that must NOT move
            leaked = got & must_pass
            if leaked:
                failures += 1
                print(f"      BAD: also broke {sorted(leaked)} — the arms are not disjoint")
        finally:
            if original is not None:
                WEBHOOK.write_text(original, encoding="utf-8")

    if sha(WEBHOOK) != baseline:
        print("\nBAD: webhook.go was NOT restored (sha256 differs)")
        failures += 1
    rc, _ = run(f"{MOCK_TESTS}|{PG_TEST}")
    if rc != 0:
        print("\nBAD: the suite is not green again after restore")
        failures += 1

    print(f"\n{len(MUTATIONS) - failures} of {len(MUTATIONS)} controls behaved as predicted; "
          "webhook.go restored and sha256-verified.")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
