#!/usr/bin/env python3
"""
W3.4 / tab-9m4x — DOES ANYTHING RED IF YOU DELETE THE *CALL*?

tab-6c1a's handed-on lead (a), asked of the remaining internal helpers in
internal/analytics/engine.go. The lead's own words:

    "a unit test on a helper is not a guard on its callers, and this repo has
     more helpers than clamps. The question that found four blind sites in one
     pass is *delete the CALL, not the function — does anything red?*
     It has now been asked of clampDays (3 sites) and GetVelocity's two inline
     bounds. It has NOT been asked of any other helper — endOfDay,
     completionsThrough's `<= through` early-out, or the allowedGroupBy map
     lookup, all in internal/analytics/engine.go, are the nearest three."

WHY THE MUTATION GOES AT THE CALL SITE AND NEVER INSIDE THE HELPER
------------------------------------------------------------------
endOfDay and clampDays each have a unit test that calls the FUNCTION.  Mutating
the function body reds that unit test and tells you nothing about whether the
product still CALLS it — which is the entire question.  Every mutation below
edits the caller, or the one term the caller supplies, and leaves the helper's
body byte-identical.  #170's census learnt the mirror-image lesson (mutate the
READ, never the constant, or fixture and lookup move together).

MEMBERSHIP IS DECIDED BY SET SUBTRACTION, NEVER BY AN EXIT CODE AND NEVER BY A
TEST'S NAME
------------------------------------------------------------------------------
Five earlier sessions in this queue shipped a harness whose error ran toward
reporting health.  This one measures C0's own failing set first and subtracts
it.  A mutation that fails to COMPILE scores VOID, not CAUGHT: a build error is
not a caught mutation.

THE CLOSURE IS MEASURED, NOT ASSUMED.  `go list` says exactly four packages
compile against internal/analytics: analytics, importer, mcp, cmd/track.  A
guard for a helper's caller can live in another package — #165's M2 was caught
by a test in internal/importer — so all four are run for every mutation.
"""

import hashlib
import os
import re
import subprocess
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
ENGINE = os.path.join(REPO, "internal", "analytics", "engine.go")
CLOSURE = ["./internal/analytics/", "./internal/importer/", "./internal/mcp/", "./cmd/track/"]

CAUGHT, NOT_CAUGHT, VOID = "CAUGHT", "NOT CAUGHT", "VOID(build)"

# (id, description, anchor, replacement, PREDICTION, why)
#
# PREDICTIONS ARE WRITTEN DOWN BEFORE THE FIRST RUN.  Where the measurement
# disagrees the prediction is corrected to the measurement and said so in the
# merge note — never the other way round.
SITES = [
    (
        "W1",
        "burndown: the endOfDay CALL around the query's `through` bound (engine.go:221)",
        "completionsThrough(ctx, e.pool, cycleID, endOfDay(start.AddDate(0, 0, days-1)))",
        "completionsThrough(ctx, e.pool, cycleID, start.AddDate(0, 0, days-1))",
        NOT_CAUGHT,
        "start_date is TIMESTAMPTZ, so the bound drops from 23:59:59 to the cycle's own "
        "start time-of-day on the LAST day. Only a fixture completing an issue late on the "
        "final day can see it, and burndown fixtures seed in whole days.",
    ),
    (
        "W2",
        "burndown: the endOfDay CALL on the walk's per-day boundary (engine.go:249)",
        "\t\teod := endOfDay(day)",
        "\t\teod := day",
        CAUGHT,
        "every day's cumulative count shifts; TestBurndown_SeriesMatchesAnIndependentOracle "
        "pins every Remaining against a hand-written oracle.",
    ),
    (
        "W3",
        "completionsThrough: the `<= through` EARLY-OUT the docstring says is not a "
        "correctness term (engine.go:73)",
        "AND completed_at <= $2",
        "AND completed_at <= $2::timestamptz + INTERVAL '100 years'",
        NOT_CAUGHT,
        "THE DOCSTRING'S OWN CLAIM, TURNED INTO A CONTROL: 'rows completed after the last "
        "day of the window are excluded by the walk anyway'. If that is true nothing can "
        "red. If something reds, the comment is false and the term is load-bearing.",
    ),
    (
        "W4",
        "distribution: the allowedGroupBy GATE lookup (engine.go:312)",
        "\tcol, ok := allowedGroupBy[groupBy]",
        "\tcol, ok := groupBy, true",
        CAUGHT,
        "MUST-STAY-GREEN. groupby_gate_realpg_test.go exists for exactly this. If it does "
        "not red, the harness is not reading the analytics package at all.",
    ),
    (
        "W5",
        "distribution: the `label` DISPATCH to distributionByLabel (engine.go:309)",
        '\tif groupBy == "label" {',
        '\tif groupBy == "label" && days < 0 {',
        CAUGHT,
        "a label request would fall through to the gate, which does not hold the key, so "
        "the whole label report becomes a 400. distribution_counting_realpg_test.go's "
        "header names a separate UNNEST path, so something should read it.",
    ),
    (
        "W6",
        "distribution: scanDistribution's PCT loop, the only arithmetic it adds "
        "(engine.go:358)",
        "\tfor i := range buckets {\n\t\tif total > 0 {\n\t\t\tbuckets[i].Pct = float64(buckets[i].Count) / float64(total)\n\t\t}\n\t}",
        "\tfor i := range buckets {\n\t\tif total < 0 {\n\t\t\tbuckets[i].Pct = float64(buckets[i].Count) / float64(total)\n\t\t}\n\t}",
        NOT_CAUGHT,
        "Pct is a derived presentation field on both distribution paths; no counting test "
        "in the package names it.",
    ),
    (
        "W7",
        "distribution: the clampDays CALL (engine.go:308) — #165's site, re-run",
        "\tdays = clampDays(days)\n\tif groupBy ==",
        "\tif groupBy ==",
        CAUGHT,
        "MUST-STAY-GREEN #2. #165 built window_clamp_wiring_realpg_test.go for this exact "
        "site (its M1). A NOT CAUGHT here would mean that merge's guard has stopped "
        "reading, which is a bigger finding than anything else in this census.",
    ),
    (
        "W8",
        "VOID CONTROL: endOfDay's walk call wrapped in an arithmetic identity",
        "\t\teod := endOfDay(day)",
        "\t\teod := endOfDay(day).Add(0)",
        NOT_CAUGHT,
        "behaviour is identical by construction. A red here would mean something is "
        "matching engine.go's TEXT rather than its answers — #160's fingerprint class.",
    ),
]


def sha256(path):
    with open(path, "rb") as fh:
        return hashlib.sha256(fh.read()).hexdigest()


def run_closure():
    """Return (fail_set, build_failed, raw)."""
    env = dict(os.environ)
    env.setdefault(
        "TRACK_TEST_DATABASE_URL",
        "postgres://postgres:postgres@localhost:55934/postgres?sslmode=disable",
    )
    proc = subprocess.run(
        ["go", "test", "-timeout", "600s", "-race", "-count=1"] + CLOSURE,
        cwd=REPO, env=env, capture_output=True, text=True,
    )
    raw = proc.stdout + proc.stderr
    fails = set(re.findall(r"^\s*--- FAIL: (\S+)", raw, re.M))
    build_failed = ("[build failed]" in raw) or ("build failed" in raw)
    return fails, build_failed, raw


def main():
    pristine = open(ENGINE).read()
    pristine_sha = sha256(ENGINE)
    print(f"engine.go sha256 {pristine_sha}")

    print("\nC0 — the baseline failing set, MEASURED rather than assumed")
    f0, b0, raw0 = run_closure()
    if b0:
        print("C0 DOES NOT BUILD — nothing below can mean anything")
        print(raw0[-3000:])
        return 2
    print(f"C0 failing set: {sorted(f0) if f0 else 'EMPTY'} ({len(f0)})")

    rows, wrong = [], []
    try:
        for sid, desc, anchor, repl, predicted, why in SITES:
            n = pristine.count(anchor)
            if n != 1:
                print(f"\n{sid}: ANCHOR MATCHES {n} TIMES, NOT 1 — refusing to mutate")
                rows.append((sid, desc, predicted, f"ANCHOR x{n}", set()))
                wrong.append(sid)
                continue

            open(ENGINE, "w").write(pristine.replace(anchor, repl))
            fails, build_failed, raw = run_closure()
            new = fails - f0
            verdict = VOID if build_failed else (CAUGHT if new else NOT_CAUGHT)

            print(f"\n{sid} — {desc}")
            print(f"    predicted {predicted}   measured {verdict}")
            if new:
                for t in sorted(new):
                    print(f"      red: {t}")
            if verdict != predicted:
                wrong.append(sid)
            rows.append((sid, desc, predicted, verdict, new))
    finally:
        open(ENGINE, "w").write(pristine)
        after = sha256(ENGINE)
        print(f"\nrestored engine.go sha256 {after} "
              f"({'MATCHES pristine' if after == pristine_sha else 'DOES NOT MATCH — STOP'})")
        if after != pristine_sha:
            return 3

    print("\n" + "=" * 78)
    for sid, desc, predicted, verdict, new in rows:
        mark = "  " if verdict == predicted else "<-"
        print(f"{mark} {sid:3} {verdict:12} (predicted {predicted:12}) {desc[:70]}")
    print("=" * 78)
    if wrong:
        print(f"PREDICTIONS WRONG: {', '.join(wrong)} — recorded, not tuned away")
    else:
        print("every prediction held")
    return 0


if __name__ == "__main__":
    sys.exit(main())
