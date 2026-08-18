#!/usr/bin/env python3
"""Positive controls for TestAnalytics_AICosts_WorkspaceScoped (W3.4, tab-7b2c).

GetAICostTrends runs FIVE independently-scoped queries. The claim this harness has to settle
is not "the test catches a leak" but the stronger one: FIVE ASSERTIONS ARE FIVE GUARDS RATHER
THAN ONE GUARD WRITTEN FIVE TIMES. So each control neutralises exactly ONE scope predicate and
the harness records WHICH assertion fired — the leak assertions are t.Errorf, not t.Fatalf, so
every one that can fire does, and "only its own fired" is measured rather than inferred from a
red test.

⚠ internal/importer is red on this machine before any mutation and it is NOT mine: the two
/tmp corpus directories exist but are EMPTY, so those censuses correctly refuse to skip (the
skip is keyed on the directory being ABSENT — CI's case) and fail closed. 11 failures,
verified identical on clean main. Attribution here is by `go test -json`'s Package field, never
by test name or message: guessing it from the name reported 10 of 11 and from the failure text
5 of 11, and both turn an environmental failure into a real one.
"""

import hashlib
import io
import json
import os
import re
import subprocess
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
ENGINE = os.path.join(REPO, "internal/analytics/engine.go")
DSN = os.environ.get(
    "TRACK_TEST_DATABASE_URL",
    "postgres://postgres:postgres@localhost:55432/postgres?sslmode=disable",
)
NEW_TEST = "TestAnalytics_AICosts_WorkspaceScoped"
ENV_PKG = "github.com/talyvor/track/internal/importer"

# Each of the five sub-queries, anchored on a fragment unique to it — asserted unique at
# startup, not assumed.
#
# ⚠ THE OBVIOUS ANCHOR FOR by_label WAS WRONG AND WOULD HAVE MUTATED A DIFFERENT REPORT.
# `GROUP BY label` occurs TWICE in engine.go: the AI-cost breakdown and the DISTRIBUTION
# report's by-label query, which unnests the same column three sub-queries earlier. Python's
# str.index takes the FIRST, so the control would have neutralised distribution's scope, seen
# this test stay green, and reported "the by-label assertion is not a guard" — a false verdict
# about my own test produced by an instrument that hit the wrong target. The two differ by
# window (created_at vs updated_at) and ordering; ORDER BY SUM(ai_cost_usd) is unique.
QUERIES = {
    "totals":       (").Scan(&total, &count)",              "WHERE workspace_id = $1"),
    "daily":        ("GROUP BY day",                        "WHERE workspace_id = $1"),
    "top_issues":   ("ORDER BY ai_cost_usd DESC LIMIT 10",  "WHERE workspace_id = $1 AND ai_cost_usd > 0"),
    "by_team":      ("GROUP BY t.id, t.name",               "WHERE i.workspace_id = $1"),
    "by_label":     ("ORDER BY SUM(ai_cost_usd) DESC LIMIT 20", "WHERE workspace_id = $1"),
}
# The assertion label each mutation is PREDICTED to trip, and no other.
PREDICTED = {
    "totals": "totals", "daily": "daily series", "top_issues": "top-cost leaderboard",
    "by_team": "cost by team", "by_label": "cost by label",
}

# ⚠ MY FIRST PREDICTION WAS WRONG FOR FOUR OF THE FIVE AND THE RUN IS WHAT CORRECTED IT — the
# original is recorded here rather than quietly rewritten. I predicted each mutation would trip
# ONLY its own leak assertion. Measured, C1/C3/C4/C5 trip that assertion AND the positive half:
# the positive half asserts EXACT counts (total == 55, one leaderboard row, one team, one
# label), so with the scope neutralised the SECOND request — the one that seeds wsA's own issue
# and expects to see only it — sees wsB's row too and its equality fails. That is the test
# behaving correctly; the prediction had simply missed that an exact-count positive assertion
# is also a leak detector. C2 is the exception and it says the mechanism is understood rather
# than rationalised: both issues are seeded with updated_at NOW(), so they fall on the SAME day
# and `len(DailyCosts)` is 1 either way — the one positive assertion a leak cannot move.
#
# THE CLAIM THE HARNESS ACTUALLY SETTLES IS UNCHANGED: among the five LEAK assertions, exactly
# the predicted one fires for each mutation. That exclusivity is what makes them five guards.
ALSO_TRIPS_POSITIVE = {"totals", "top_issues", "by_team", "by_label"}


def sha(p):
    return hashlib.sha256(io.open(p, "rb").read()).hexdigest()


def read(p):
    return io.open(p, encoding="utf-8").read()


def write(p, s):
    io.open(p, "w", encoding="utf-8").write(s)


def neutralise(src, which):
    anchor, scope = QUERIES[which]
    idx = src.index(anchor)
    start = src.rindex(scope, 0, idx)
    repl = scope.replace("WHERE ", "WHERE (", 1) + " OR TRUE)"
    # `WHERE (i.workspace_id = $1 AND ai_cost_usd > 0 OR TRUE)` would also disable the cost
    # filter, so keep the neutralisation to the workspace term alone.
    if scope == "WHERE workspace_id = $1 AND ai_cost_usd > 0":
        repl = "WHERE (workspace_id = $1 OR TRUE) AND ai_cost_usd > 0"
    return src[:start] + repl + src[start + len(scope):]


def run_suite():
    env = dict(os.environ, TRACK_TEST_DATABASE_URL=DSN)
    p = subprocess.run(
        ["go", "test", "-json", "-timeout", "120s", "-race", "-count=1", "./..."],
        cwd=REPO, env=env, capture_output=True, text=True, timeout=1800,
    )
    inside, outside, mine = [], [], []
    for line in p.stdout.splitlines():
        try:
            ev = json.loads(line)
        except ValueError:
            continue
        if ev.get("Test") == NEW_TEST and ev.get("Action") == "output":
            m = re.search(r"CROSS-WS LEAK \(([^)]+)\)", ev.get("Output", ""))
            if m:
                mine.append(m.group(1))
            elif "own " in ev.get("Output", "") and "should" in ev.get("Output", ""):
                mine.append("POSITIVE-HALF")
        if ev.get("Action") != "fail" or not ev.get("Test") or "/" in ev["Test"]:
            continue
        (inside if ev.get("Package") == ENV_PKG else outside).append(ev["Test"])
    return outside, len(inside), sorted(set(mine))


def control(name, prediction, mutate, expect_labels):
    print(f"\n=== {name} ===")
    print(f"  PREDICTION (before running): {prediction}")
    before, original = sha(ENGINE), read(ENGINE)
    try:
        mutate()
        outside, env_n, labels = run_suite()
        caught = NEW_TEST in outside
        others = [t for t in outside if t != NEW_TEST]
        print(f"  internal/importer (environmental): {env_n}")
        print(f"  assertions that fired: {labels or 'NONE'}")
        print(f"  other tests failing:   {others or 'NONE'}")
        ok = (labels == expect_labels)
        print(f"  RESULT: {'CAUGHT' if caught else 'NOT CAUGHT'} — "
              f"prediction {'MATCHED' if ok else '*** MISMATCH ***'} (wanted {expect_labels})")
        return ok
    finally:
        write(ENGINE, original)
        assert sha(ENGINE) == before, "RESTORE FAILED"
        print("  restored, sha256 verified")


def main():
    # Assert every anchor is unique BEFORE mutating anything. An anchor that matches twice
    # silently retargets the control (see the by_label note above).
    src = read(ENGINE)
    for q, (anchor, _) in QUERIES.items():
        n = src.count(anchor)
        if n != 1:
            sys.exit(f"anchor for {q!r} matches {n} times, want exactly 1: {anchor!r}")
    print(f"all {len(QUERIES)} sub-query anchors verified unique")

    results = []
    print("\n=== C0 (baseline, no mutation) ===")
    outside, env_n, labels = run_suite()
    print(f"  internal/importer (environmental): {env_n}")
    print(f"  failures outside importer: {outside or 'NONE'}")
    results.append(("C0 baseline", not outside and not labels))

    for i, q in enumerate(QUERIES, start=1):
        want = sorted([PREDICTED[q]] + (["POSITIVE-HALF"] if q in ALSO_TRIPS_POSITIVE else []))
        ok = control(
            f"C{i}  '{q}' sub-query scope neutralised ALONE",
            f"CAUGHT by the '{PREDICTED[q]}' leak assertion and NO OTHER leak assertion"
            + (" (+ the exact-count positive half — see ALSO_TRIPS_POSITIVE)"
               if q in ALSO_TRIPS_POSITIVE else " (positive half unmoved: same-day series)"),
            (lambda q=q: write(ENGINE, neutralise(read(ENGINE), q))),
            want,
        )
        results.append((f"C{i} {q} scope is its own guard", ok))

    # C6 — the other direction. Every leak assertion above is satisfied by an EMPTY report.
    ok = control(
        "C6  totals scope broken the other way (AND FALSE) — own spend vanishes",
        "CAUGHT by the POSITIVE half only; no leak assertion fires",
        lambda: write(ENGINE, read(ENGINE).replace(
            "WHERE workspace_id = $1\n          AND updated_at > NOW() - (INTERVAL '1 day' * $2::int)`,\n\t\tworkspaceID, days,\n\t).Scan(&total, &count)",
            "WHERE workspace_id = $1 AND FALSE\n          AND updated_at > NOW() - (INTERVAL '1 day' * $2::int)`,\n\t\tworkspaceID, days,\n\t).Scan(&total, &count)")),
        ["POSITIVE-HALF"],
    )
    results.append(("C6 positive half", ok))

    # C7 — must-stay-green companion: a real change that is a COHORT question, not a SCOPE one.
    ok = control(
        "C7  MUST-STAY-GREEN: leaderboard LIMIT 10 -> LIMIT 3",
        "NOT CAUGHT — one seeded issue per workspace, so the limit cannot bind",
        lambda: write(ENGINE, read(ENGINE).replace(
            "ORDER BY ai_cost_usd DESC LIMIT 10", "ORDER BY ai_cost_usd DESC LIMIT 3")),
        [],
    )
    results.append(("C7 must-stay-green (limit)", ok))

    print("\n================ SUMMARY ================")
    for n, ok in results:
        print(f"  {'PASS' if ok else 'FAIL'}  {n}")
    bad = [n for n, ok in results if not ok]
    print("=========================================")
    if bad:
        print(f"MISMATCHES: {bad}")
        sys.exit(1)
    print("all predictions matched")


if __name__ == "__main__":
    main()
