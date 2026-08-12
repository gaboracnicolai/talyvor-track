#!/usr/bin/env python3
"""Mutation controls for roadmap_completed_divergence_realpg_test.go.

The test PASSES on an unmutated tree — it pins behaviour that is already there. That makes it
exactly the kind of test that can be green because it cannot fail. Each control below mutates ONE
predicate, or the fixture, and asserts the test turns RED for the RIGHT reason.

    TRACK_TEST_DATABASE_URL=... python3 scripts/w34-completed-divergence-controls.py
"""

import os
import re
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
TEST = "TestRoadmapAndMilestoneProgress_DisagreeOnWhetherCancelledIsCompleted_RealPG"
FAILURES = []


def run_test():
    r = subprocess.run(["go", "test", "./internal/project/", "-run", TEST, "-count=1"],
                       cwd=ROOT, capture_output=True, text=True)
    return r.returncode, r.stdout + r.stderr


def control(name, rel, before, after, want_red, expect_in_output=None):
    path = os.path.join(ROOT, rel)
    original = open(path, encoding="utf-8").read()
    if before not in original:
        raise SystemExit(f"CONTROL IS STALE: anchor not in {rel}\n  {before[:100]}")
    open(path, "w", encoding="utf-8").write(original.replace(before, after, 1))
    try:
        code, out = run_test()
    finally:
        open(path, "w", encoding="utf-8").write(original)
    red = code != 0
    ok = red == want_red
    if ok and expect_in_output and red:
        ok = expect_in_output in out
        if not ok:
            print(f"           ^ fired, but not on the expected assertion "
                  f"(looking for {expect_in_output!r})")
    print(f"  [{'PASS' if ok else 'FAIL'}] {name}: test went "
          f"{'RED' if red else 'GREEN'} (expected {'RED' if want_red else 'GREEN'})")
    if not ok:
        FAILURES.append(name)
        for line in out.splitlines():
            if "roadmap_completed" in line or "FAIL" in line:
                print("           ", line[:150])


def main():
    if not os.environ.get("TRACK_TEST_DATABASE_URL"):
        raise SystemExit("TRACK_TEST_DATABASE_URL is required — this control needs real Postgres. "
                         "Refusing to report a green from a skipped test.")

    print("C0  unmutated tree — the test must be GREEN (it pins what is there today)")
    code, _ = run_test()
    print(f"  [{'PASS' if code == 0 else 'FAIL'}] C0 baseline: {'GREEN' if code == 0 else 'RED'}")
    if code != 0:
        FAILURES.append("C0")

    print("\nC1  roadmap milestone rollup: drop 'cancelled' from the FILTER -> the two surfaces")
    print("    agree, which is what assertion (4) exists to catch.")
    control("C1 roadmap -> done only", "internal/project/roadmap.go",
            "COUNT(i.id) FILTER (WHERE i.status IN ('done','cancelled')) AS completed_count,\n"
            "                COALESCE(SUM(i.ai_cost_usd), 0)                            AS ai_cost_usd\n"
            "            FROM milestones m",
            "COUNT(i.id) FILTER (WHERE i.status = 'done') AS completed_count,\n"
            "                COALESCE(SUM(i.ai_cost_usd), 0)                            AS ai_cost_usd\n"
            "            FROM milestones m",
            want_red=True, expect_in_output="now agree at 1 completed")

    print("\nC2  milestone.GetProgress: ADD 'cancelled' -> same convergence from the other side.")
    control("C2 milestone -> done or cancelled", "internal/milestone/store.go",
            "COUNT(*) FILTER (WHERE status = 'done')",
            "COUNT(*) FILTER (WHERE status IN ('done','cancelled'))",
            want_red=True, expect_in_output="now agree at 2 completed")

    print("\nC3  cycle.GetProgress: ADD 'cancelled' -> only assertion (3) may move. If this comes")
    print("    back GREEN the cycle surface is not really being measured, only described.")
    control("C3 cycle -> done or cancelled", "internal/cycle/store.go",
            "COUNT(*) FILTER (WHERE status = 'done')                              AS completed,",
            "COUNT(*) FILTER (WHERE status IN ('done','cancelled'))               AS completed,",
            want_red=True, expect_in_output="cycle progress completed = 2")

    print("\nC4  VACUITY: delete the cancelled issue from the fixture. Every predicate then agrees")
    print("    and the test must fail — proving the divergence comes from that row and not from")
    print("    a query that happens to return different numbers for unrelated reasons.")
    control("C4 fixture without the cancelled issue",
            "internal/project/roadmap_completed_divergence_realpg_test.go",
            '\t\t{"cancelled", model.StatusCancelled},\n', "",
            want_red=True)

    print("\nC5  VACUITY: delete the todo issue. The roadmap and milestone numerators are unchanged")
    print("    (2 and 1) but the DENOMINATOR drops to 2, so the test must fail on the count guard —")
    print("    which is what stops a query that ignores status from satisfying assertion (1).")
    control("C5 fixture without the open issue",
            "internal/project/roadmap_completed_divergence_realpg_test.go",
            '\t\t{"still open", model.StatusTodo},\n', "",
            want_red=True, expect_in_output="want 3")

    print("\nC6  the tree is restored — re-run the untouched test")
    code, _ = run_test()
    print(f"  [{'PASS' if code == 0 else 'FAIL'}] C6 baseline again: {'GREEN' if code == 0 else 'RED'}")
    if code != 0:
        FAILURES.append("C6")
    dirty = subprocess.run(["git", "diff", "--stat", "internal/project/roadmap.go",
                            "internal/milestone/store.go", "internal/cycle/store.go"],
                           cwd=ROOT, capture_output=True, text=True).stdout.strip()
    print(f"  [{'PASS' if not dirty else 'FAIL'}] mutated product files restored: {dirty or '(clean)'}")
    if dirty:
        FAILURES.append("C6 restore")

    print()
    if FAILURES:
        print("FAILED CONTROLS:", ", ".join(FAILURES))
        sys.exit(1)
    print("ALL CONTROLS PASSED")


if __name__ == "__main__":
    main()
