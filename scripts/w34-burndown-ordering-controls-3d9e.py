#!/usr/bin/env python3
"""Positive controls for TestBurndown_TheCompletionReadMustBeOrdered_RealPG (W3.4, tab-3d9e).

⚠ THIS GUARD PASSED ON ITS FIRST RUN AND THAT IS THE CASE TO DISTRUST. The production code is
already correct — the `ORDER BY completed_at` is there in both implementations — so a green
first run says nothing at all about whether the test can fail. Everything that makes this a
guard rather than decoration is measured below.

Every control NAMES ITS PREDICTED VERDICT BEFORE IT RUNS, mutates exactly one term, runs the
FULL suite, and restores every touched file in a `finally` with a sha256 comparison.

THREE THINGS ARE BEING MEASURED, NOT TWO:
  1. the new test reds when either implementation loses its ORDER BY          (C1, C2)
  2. the EXISTING oracle test does NOT — i.e. the gap this file closes was real. That is read
     off C1/C2's "ALSO caught by" line rather than asserted separately.
  3. the reporter that produces line 2 can actually name another test. C5 mutates a term the
     existing oracle DOES cover, and its "ALSO caught by" line must be non-empty. Without C5,
     "NOTHING ELSE" in C1/C2 is indistinguishable from a broken exclusivity reporter.

⚠ internal/importer IS RED ON THIS MACHINE BEFORE ANY MUTATION AND IT IS NOT MINE: the corpus
census tests read /tmp/w34-jira-corpus and /tmp/w34-linear-corpus-cache, both of which EXIST but
are EMPTY, so they correctly refuse to skip (the skip is keyed on the directory being ABSENT,
which is CI's case) and fail closed. Attributed from `go test -json`'s Package field — a fact
the toolchain reports, not one inferred from a test name. The merge gate remains CI, where
those directories do not exist.
"""

import hashlib
import io
import json
import os
import subprocess
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
ENGINE = os.path.join(REPO, "internal/analytics/engine.go")
CYCLE = os.path.join(REPO, "internal/cycle/store.go")
TESTF = os.path.join(REPO, "internal/analytics/burndown_ordering_realpg_test.go")
FILES = [ENGINE, CYCLE, TESTF]
DSN = os.environ.get(
    "TRACK_TEST_DATABASE_URL",
    "postgres://postgres:postgres@localhost:55439/postgres?sslmode=disable",
)

NEW_TEST = "TestBurndown_TheCompletionReadMustBeOrdered_RealPG"
OLD_TEST = "TestBurndown_SeriesMatchesAnIndependentOracle"
ENV_PKG = "github.com/talyvor/track/internal/importer"

# Anchors, each asserted unique before use. A silently non-unique anchor would mutate a term the
# control does not name and still print a tidy CAUGHT/NOT CAUGHT.
ENGINE_ORDER = "AND completed_at <= $2\n        ORDER BY completed_at`, cycleID, through)"
ENGINE_NOORDER = "AND completed_at <= $2`, cycleID, through)"
CYCLE_ORDER = "AND completed_at <= $2\n        ORDER BY completed_at`,"
CYCLE_NOORDER = "AND completed_at <= $2`,"
ENGINE_WALK = "for completed < len(completions) && !completions[completed].After(eod) {"
ENGINE_EARLY = "completed_at IS NOT NULL AND completed_at <= $2\n        ORDER BY completed_at`, cycleID, through)"
TEST_DESC = "\tfor i := len(completions) - 1; i >= 0; i-- {"
TEST_ASC = "\tfor i := 0; i < len(completions); i++ {"


def sha(p):
    return hashlib.sha256(io.open(p, "rb").read()).hexdigest()


def read(p):
    return io.open(p, encoding="utf-8").read()


def write(p, s):
    io.open(p, "w", encoding="utf-8").write(s)


def swap(path, old, new, count=1):
    src = read(path)
    n = src.count(old)
    assert n == count, f"anchor {old!r} occurs {n}x in {os.path.basename(path)}, expected {count}"
    write(path, src.replace(old, new))


def run_suite():
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
        if "/" in ev["Test"]:
            continue
        (inside if ev.get("Package") == ENV_PKG else outside).append(ev["Test"])
    return sorted(set(outside)), len(inside), p.stdout + p.stderr


def control(name, prediction, mutate, expect_caught, expect_others=None):
    print(f"\n=== {name} ===")
    print(f"  PREDICTION (stated before running): {prediction}")
    before = {p: sha(p) for p in FILES}
    originals = {p: read(p) for p in FILES}
    try:
        mutate()
        other, imp, out = run_suite()
        caught = NEW_TEST in other
        others = [t for t in other if t != NEW_TEST]
        print(f"  internal/importer (environmental, pre-existing, by -json Package): {imp}")
        print(f"  ALSO caught by (exclusivity, FULL suite): {others or 'NOTHING ELSE'}")
        ok = caught == expect_caught
        if expect_others is not None:
            ok = ok and (set(others) == set(expect_others))
            print(f"  expected ALSO: {expect_others}")
        print(f"  RESULT: {'CAUGHT' if caught else 'NOT CAUGHT'} by {NEW_TEST}"
              f"  -> prediction {'MATCHED' if ok else '*** MISMATCH ***'}")
        if not ok:
            print("  ---- suite tail ----")
            print("\n".join(out.splitlines()[-30:]))
        return ok, others
    finally:
        for p, s in originals.items():
            write(p, s)
        for p, h in before.items():
            assert sha(p) == h, f"RESTORE FAILED for {p}"
        print("  restored, sha256 verified (3 files)")


def main():
    results, exclusivity = [], {}

    print("\n=== C0 (baseline, no mutation) ===")
    other, imp, _ = run_suite()
    print(f"  internal/importer (environmental, pre-existing, by -json Package): {imp}")
    print(f"  failures outside importer: {other or 'NONE'}")
    results.append(("C0 baseline green outside importer", not other))

    # ── C1 — the defect class, in the analytics implementation. The "ALSO caught by" line is the
    # measurement that the existing oracle test cannot see this.
    ok, ex = control(
        "C1  analytics.completionsThrough loses `ORDER BY completed_at`",
        f"CAUGHT by {NEW_TEST} ([O-ANALYTICS]) and by NOTHING ELSE — in particular NOT by "
        f"{OLD_TEST}, whose fixture stamps its completions in ascending order",
        lambda: swap(ENGINE, ENGINE_ORDER, ENGINE_NOORDER),
        expect_caught=True, expect_others=[],
    )
    results.append(("C1 analytics ORDER BY", ok)); exclusivity["C1"] = ex

    # ── C2 — the same defect in the OTHER implementation. Both carry their own copy of the read
    # and the walk; a fix to one would leave the other silently wrong.
    ok, ex = control(
        "C2  cycle.Store.GetBurndown loses `ORDER BY completed_at`",
        f"CAUGHT by {NEW_TEST} ([O-CYCLE]) and by NOTHING ELSE",
        lambda: swap(CYCLE, CYCLE_ORDER, CYCLE_NOORDER),
        expect_caught=True, expect_others=[],
    )
    results.append(("C2 cycle ORDER BY", ok)); exclusivity["C2"] = ex

    # ── C3 — THE CONTROL ON THE FIXTURE'S OWN PREMISE. [O-PREMISE] claims the rows are stored out
    # of completion order; stamp them in ascending order instead and that claim becomes false. The
    # test must FAIL LOUDLY as a broken fixture rather than pass while asserting nothing, which is
    # the failure mode a "storage order" fixture has.
    ok, ex = control(
        "C3  the fixture stamps ASCENDING (its premise made false), production untouched",
        f"CAUGHT by {NEW_TEST} — at [O-PREMISE], refusing to be a guard it can no longer be",
        lambda: swap(TESTF, TEST_DESC, TEST_ASC),
        expect_caught=True, expect_others=[],
    )
    results.append(("C3 premise self-check fires", ok)); exclusivity["C3"] = ex

    # ── C4 — MUST STAY GREEN. Four CAUGHTs in a row could just mean the test reds at any edit to
    # the read. The `completed_at <= $2` bound is documented in completionsThrough as an EARLY-OUT
    # rather than a correctness term (the walk excludes later rows anyway).
    ok, ex = control(
        "C4  MUST-STAY-GREEN: the read's early-out bound neutralised, ORDER BY intact",
        f"NOT CAUGHT by {NEW_TEST} — every completion in this fixture is inside the window",
        lambda: swap(ENGINE, ENGINE_EARLY,
                     "completed_at IS NOT NULL AND ($2::timestamptz IS NOT NULL)\n"
                     "        ORDER BY completed_at`, cycleID, through)"),
        expect_caught=False,
    )
    results.append(("C4 must-stay-green", ok)); exclusivity["C4"] = ex

    # ── C5 — THE CONTROL ON THE EXCLUSIVITY REPORTER ITSELF. Every line above rests on "ALSO
    # caught by: NOTHING ELSE" being a measurement. Mutate a term the EXISTING oracle test does
    # cover — the walk's boundary comparison — and the reporter must name it. If this came back
    # empty too, the reporter would be broken and C1/C2's exclusivity claims would mean nothing.
    #
    # ⚠ MY FIRST PREDICTION HERE WAS WRONG AND THE MISMATCH IS RECORDED RATHER THAN TUNED AWAY.
    # I predicted the new test would ALSO red. It does not, and the reason is a fact about the two
    # fixtures worth writing down: `!After(eod)` and `Before(eod)` differ ONLY on an instant that
    # falls EXACTLY on the 23:59:59 boundary, and this file's completions sit two hours inside
    # end-of-day. The sibling oracle test is the one that seeds instants ON the boundary — that is
    # its subject — while this file's subject is storage ORDER. So the two are COMPLEMENTARY, and
    # neither subsumes the other. The prediction below is what was measured; the fixture was
    # deliberately NOT extended to cover the boundary too, because duplicating the sibling's
    # coverage would blur which test is answering which question.
    ok, ex = control(
        "C5  REPORTER CONTROL: the merge-walk boundary `!After(eod)` -> `Before(eod)`",
        f"NOT CAUGHT by {NEW_TEST} (its instants sit 2h inside end-of-day, so the two comparisons "
        f"agree on them) BUT the ALSO line must NAME {OLD_TEST}, whose fixture seeds the boundary "
        f"instant — this is the control on the exclusivity reporter, not on the new guard",
        lambda: swap(ENGINE, ENGINE_WALK,
                     "for completed < len(completions) && completions[completed].Before(eod) {"),
        expect_caught=False, expect_others=[OLD_TEST],
    )
    results.append(("C5 reporter names another catcher", ok)); exclusivity["C5"] = ex

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
