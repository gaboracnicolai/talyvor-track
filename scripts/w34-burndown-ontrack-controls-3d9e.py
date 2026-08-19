#!/usr/bin/env python3
"""Positive controls for TestBurndown_OnTrackAndProjection_AreComputedNotDefaulted (W3.4, tab-3d9e).

Every control NAMES ITS PREDICTED VERDICT BEFORE IT RUNS, mutates exactly one term, runs the
FULL suite, and restores the file in a `finally` with a sha256 comparison — a control that
leaves the tree mutated has poisoned every measurement after it.

WHY THE FULL SUITE AND NOT ./internal/analytics ALONE: "only the new guard catches this" is a
claim about the WHOLE repository. It is not a safe one to assume here in particular — the term
that carries the burndown's TOTAL is already caught, from another report's test, by
`TestGetVelocity_TheSQLsOwnCountingRules_RealPG`'s [V-BURNDOWN] section, which is in the same
package but about a different report. Coverage in this repo does arrive from unrelated places,
so exclusivity is measured rather than reasoned.

⚠ internal/importer IS RED ON THIS MACHINE BEFORE ANY MUTATION AND IT IS NOT MINE. Its corpus
census tests read /tmp/w34-jira-corpus and /tmp/w34-linear-corpus-cache; both directories EXIST
but are EMPTY, so the tests correctly refuse to skip (the skip is keyed on the directory being
ABSENT, which is CI's case) and fail closed rather than reporting a clean answer from an
instrument that read nothing. Measured on CLEAN main 6b31a75 with the tree stashed: 13 failing
test entries, all in that one package. This harness counts failures OUTSIDE internal/importer,
attributed from `go test -json`'s Package field — a fact the toolchain reports, not one inferred
from a test name or a failure string. The merge gate remains CI, where those directories do not
exist.
"""

import hashlib
import io
import json
import os
import subprocess
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
ENGINE = os.path.join(REPO, "internal/analytics/engine.go")
DSN = os.environ.get(
    "TRACK_TEST_DATABASE_URL",
    "postgres://postgres:postgres@localhost:55439/postgres?sslmode=disable",
)

NEW_TEST = "TestBurndown_OnTrackAndProjection_AreComputedNotDefaulted"
ENV_PKG = "github.com/talyvor/track/internal/importer"

# ── The four terms under control, each verified unique in the file before use. ─────────────
SEED = "\treport.IsOnTrack = true\n"                       # the fix
LOOP = "\t\t\treport.IsOnTrack = remaining <= ideal"       # the per-day verdict
PICK = "\t\tif !day.After(now) {"                          # which day the verdict is read from
PROJ = "\tif daysElapsed > 0.5 && currentRemaining > 0 {"  # the projection gate
EARLY = "completed_at IS NOT NULL AND completed_at <= $2"  # the read's early-out (not a verdict)


def sha(path):
    return hashlib.sha256(io.open(path, "rb").read()).hexdigest()


def read(path):
    return io.open(path, encoding="utf-8").read()


def write(path, s):
    io.open(path, "w", encoding="utf-8").write(s)


def swap(old, new, count=1):
    """Replace `old` with `new` in engine.go, asserting the anchor is unique first.

    An anchor that has silently become non-unique would mutate a term the control does not
    name, and the result would still look like a clean CAUGHT/NOT CAUGHT.
    """
    src = read(ENGINE)
    n = src.count(old)
    assert n == count, f"anchor {old!r} occurs {n}x, expected {count}"
    write(ENGINE, src.replace(old, new))


def run_suite():
    """Full suite. Returns (failing tests outside the environmental package, count inside, raw)."""
    env = dict(os.environ, TRACK_TEST_DATABASE_URL=DSN)
    p = subprocess.run(
        ["go", "test", "-json", "-timeout", "300s", "-race", "-count=1", "./..."],
        cwd=REPO, env=env, capture_output=True, text=True, timeout=2400,
    )
    inside, outside = [], []
    for line in p.stdout.splitlines():
        try:
            ev = json.loads(line)
        except ValueError:
            continue
        if ev.get("Action") != "fail" or not ev.get("Test"):
            continue
        if "/" in ev["Test"]:  # subtest; its parent already counts
            continue
        (inside if ev.get("Package") == ENV_PKG else outside).append(ev["Test"])
    return sorted(set(outside)), len(inside), p.stdout + p.stderr


def control(name, prediction, mutate, expect_caught):
    print(f"\n=== {name} ===")
    print(f"  PREDICTION (stated before running): {prediction}")
    before = sha(ENGINE)
    original = read(ENGINE)
    try:
        mutate()
        other, imp, out = run_suite()
        caught = NEW_TEST in other
        others = [t for t in other if t != NEW_TEST]
        print(f"  internal/importer (environmental, pre-existing, by -json Package): {imp}")
        print(f"  ALSO caught by (exclusivity, measured over the FULL suite): {others or 'NOTHING ELSE'}")
        ok = caught == expect_caught
        print(f"  RESULT: {'CAUGHT' if caught else 'NOT CAUGHT'} by {NEW_TEST}"
              f"  -> prediction {'MATCHED' if ok else '*** MISMATCH ***'}")
        if not ok:
            print("  ---- suite tail ----")
            print("\n".join(out.splitlines()[-30:]))
        return ok, others
    finally:
        write(ENGINE, original)
        assert sha(ENGINE) == before, "RESTORE FAILED for engine.go"
        print("  restored, sha256 verified")


def main():
    results, exclusivity = [], {}

    # ── C0 — baseline. A guard green here and green everywhere is not a guard.
    print("\n=== C0 (baseline, no mutation, fix in place) ===")
    other, imp, _ = run_suite()
    print(f"  internal/importer (environmental, pre-existing, by -json Package): {imp}")
    print(f"  failures outside importer: {other or 'NONE'}")
    results.append(("C0 baseline green outside importer", not other))

    # ── C1 — THE DEFECT ITSELF. Remove the seed and the not-yet-started case falls back to Go's
    # zero value, which is exactly what main shipped.
    ok, ex = control(
        "C1  the fix reverted: `report.IsOnTrack = true` seed removed",
        f"CAUGHT by {NEW_TEST} — [B-FUTURE], [B-EMPTY] and [B-CONTINUITY]; this is the defect",
        lambda: swap(SEED, ""),
        expect_caught=True,
    )
    results.append(("C1 the defect", ok)); exclusivity["C1"] = ex

    # ── C2 — the per-day verdict neutralised the LAZY way. The seed alone must not be able to
    # satisfy this test, or the fix would have replaced a computation with a constant.
    ok, ex = control(
        "C2  per-day verdict -> constant true (seed left in place)",
        f"CAUGHT by {NEW_TEST} — [B-BEHIND] and [B-ORACLE]; a live cycle that IS behind",
        lambda: swap(LOOP, "\t\t\treport.IsOnTrack = true"),
        expect_caught=True,
    )
    results.append(("C2 verdict -> true", ok)); exclusivity["C2"] = ex

    # ── C3 — and the other direction, so the assertion is not one-sided.
    ok, ex = control(
        "C3  per-day verdict -> constant false",
        f"CAUGHT by {NEW_TEST} — [B-AHEAD] and [B-ORACLE]; a live cycle at/below the line",
        lambda: swap(LOOP, "\t\t\treport.IsOnTrack = false"),
        expect_caught=True,
    )
    results.append(("C3 verdict -> false", ok)); exclusivity["C3"] = ex

    # ── C4 — WHICH DAY the verdict is read from. `if true` still produces a plausible boolean:
    # the verdict of the LAST day of the window rather than of the current one. Nothing before
    # this file distinguished those two.
    ok, ex = control(
        "C4  current-day selection `!day.After(now)` -> `true` (verdict of the LAST day)",
        f"CAUGHT by {NEW_TEST} — [B-AHEAD]/[B-ORACLE]: the ahead cycle's final ideal is 0",
        lambda: swap(PICK, "\t\tif true {"),
        expect_caught=True,
    )
    results.append(("C4 current-day selection", ok)); exclusivity["C4"] = ex

    # ── C5 — ProjectedEnd is a pointer with `omitempty`: a block that never runs is invisible on
    # the wire, which is the shape a missing field takes when nobody asserts it.
    ok, ex = control(
        "C5  ProjectedEnd block never runs",
        f"CAUGHT by {NEW_TEST} — [B-PROJ-SET]",
        lambda: swap(PROJ, "\tif false && daysElapsed > 0.5 && currentRemaining > 0 {"),
        expect_caught=True,
    )
    results.append(("C5 projection absent", ok)); exclusivity["C5"] = ex

    # ── C6 — and invented where there is nothing left to finish.
    ok, ex = control(
        "C6  ProjectedEnd gate loses `currentRemaining > 0`",
        f"CAUGHT by {NEW_TEST} — [B-PROJ-DONE]: a completed cycle gains a projection",
        lambda: swap(PROJ, "\tif daysElapsed > 0.5 {"),
        expect_caught=True,
    )
    results.append(("C6 projection invented", ok)); exclusivity["C6"] = ex

    # ── C7 — MUST STAY GREEN. Without a control in this direction, "CAUGHT" six times over could
    # just mean the test reds at any edit to GetBurndown. The read's `completed_at <= $2` bound is
    # documented in completionsThrough as an EARLY-OUT rather than a correctness term (rows past
    # the last day are excluded by the walk anyway), and it is outside this test's subject.
    ok, ex = control(
        "C7  MUST-STAY-GREEN: the read's early-out bound neutralised",
        f"NOT CAUGHT by {NEW_TEST} — it is an early-out; the walk excludes those rows regardless",
        lambda: swap(EARLY, "completed_at IS NOT NULL AND ($2::timestamptz IS NOT NULL)", count=1),
        expect_caught=False,
    )
    results.append(("C7 must-stay-green", ok)); exclusivity["C7"] = ex

    print("\n" + "=" * 78)
    for name, ok in results:
        print(f"  {'PASS' if ok else '*** FAIL ***'}  {name}")
    print("\nEXCLUSIVITY — tests OTHER than the new guard that redded, per control:")
    for k, v in exclusivity.items():
        print(f"  {k}: {v or 'NOTHING ELSE IN THE REPOSITORY'}")
    bad = [n for n, ok in results if not ok]
    print("\n" + ("ALL PREDICTIONS MATCHED" if not bad else f"MISMATCHES: {bad}"))
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
