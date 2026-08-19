#!/usr/bin/env python3
"""Positive controls for GetTimeToResolution's SQL arithmetic (W3.4, tab-8d3f).

Every control mutates ONE term of the shipped SQL in internal/analytics/engine.go, runs a
test command over the WHOLE import closure, and restores the file in a `finally` with a
sha256 check, so a control that crashes cannot leave the tree edited.

⚠ WHY THIS HARNESS DOES NOT READ THE EXIT CODE. `./internal/importer/` ALREADY FAILS on a
pristine tree on any machine where /tmp/w34-jira-corpus and /tmp/w34-linear-corpus-cache
exist but are empty (11 `--- FAIL:` lines, correct fail-closed behaviour, LOCAL ONLY — CI
does not have the dirs and is green). A harness that scored `rc != 0` as CAUGHT would score
EVERY mutation as caught here and report a suite that sees nothing as a suite that sees
everything. So membership is decided by SET SUBTRACTION against the C0 baseline's own
measured FAIL set — never by an exit code, and never by a test's NAME.

Each control NAMES ITS PREDICTED CATCHER BEFORE IT RUNS. A control whose catcher is
"nothing" is a measured blindness, not a pass. Two controls exist to keep the harness
honest rather than to measure the product:

  POS-*  MUST be caught. If it is not, the harness cannot see a red and every NOT CAUGHT
         below it is worthless.
  VOID-* MUST NOT be caught. It is behaviour-identical, so a CAUGHT verdict means the
         suite is keyed on the SQL's text rather than on its answers.

Usage:  python3 scripts/w34-resolution-arithmetic-controls-8d3f.py \
            [--cmd "go test -count=1 ./internal/analytics/ ./internal/importer/"]
"""

import argparse
import hashlib
import shlex
import subprocess
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
ENGINE = REPO / "internal/analytics/engine.go"

GLOBAL_P50 = (
    "COALESCE(PERCENTILE_CONT(0.5)  WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM "
    "completed_at - created_at)/3600), 0)"
)
PRIO_P50 = (
    "COALESCE(PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM "
    "completed_at - created_at)/3600), 0)"
)
GLOBAL_WHERE_TAIL = (
    "          AND completed_at IS NOT NULL\n"
    "          AND created_at > NOW() - (INTERVAL '1 day' * $2::int)%s`, teamSQL),\n"
    "\t\targs...)"
)
PRIO_WINDOW_TAIL = (
    "          AND created_at > NOW() - (INTERVAL '1 day' * $2::int)%s\n"
    "        GROUP BY priority"
)

# (id, predicted catcher, old, new)
CONTROLS = [
    (
        "M1-avg-served-in-minutes",
        "nothing — engine_test.go:151 FEEDS avg and asserts Median/SampleSize/P95/ByPriority, "
        "never AvgHours; the importer callers assert dates, not the report's arithmetic",
        "COALESCE(AVG(EXTRACT(EPOCH FROM completed_at - created_at)/3600), 0)",
        "COALESCE(AVG(EXTRACT(EPOCH FROM completed_at - created_at)/60), 0)",
    ),
    (
        "M2-priority-median-is-p95",
        "nothing — engine_test.go:159 matches on `GROUP BY priority` and supplies the "
        "per-priority number itself, so the percentile it asks Postgres for is unasserted",
        PRIO_P50,
        PRIO_P50.replace("PERCENTILE_CONT(0.5)", "PERCENTILE_CONT(0.95)"),
    ),
    (
        "M3-global-p50-in-minutes",
        "THE ANCHOR TRAP: engine_test.go:151's `PERCENTILE_CONT\\(0\\.5\\).*PERCENTILE_CONT"
        "\\(0\\.75\\)` puts this /3600 INSIDE a `.*`, so the regex still matches and the "
        "value is fed — predicted nothing",
        GLOBAL_P50,
        GLOBAL_P50.replace("created_at)/3600", "created_at)/60"),
    ),
    (
        "M4-p95-column-serves-p75",
        "engine_test.go:151's SHAPE regex (it names the literal 0.95) — a text catch, NOT a "
        "value assertion; predicted CAUGHT by the mock and by nothing that ran real SQL",
        "COALESCE(PERCENTILE_CONT(0.95) WITHIN",
        "COALESCE(PERCENTILE_CONT(0.75) WITHIN",
    ),
    (
        "M5-priority-team-scope-dropped",
        "nothing — no test calls GetTimeToResolution with a non-empty teamID except "
        "index_claims_realpg_test.go, which reads pg_stat_user_indexes and never a number",
        PRIO_WINDOW_TAIL,
        PRIO_WINDOW_TAIL.replace("$2::int)%s", "$2::int)%.0s"),
    ),
    (
        "M6-samplesize-counts-rows-the-percentiles-skipped",
        "nothing — and this one falsifies a COMMENT. engine.go:431-433 claims `COUNT(*) rides "
        "the SAME WHERE clause as the aggregates ... no row it counts is a row the percentiles "
        "skipped`. Dropping the predicate from the global query alone leaves the percentiles "
        "UNCHANGED (EXTRACT over a NULL completed_at is NULL and PERCENTILE_CONT/AVG ignore "
        "NULLs), so only COUNT(*) moves — and no real-Postgres test reads SampleSize.",
        GLOBAL_WHERE_TAIL,
        GLOBAL_WHERE_TAIL.replace("          AND completed_at IS NOT NULL\n", ""),
    ),
    (
        "M7-p75-column-serves-p50",
        "engine_test.go:151's SHAPE regex (it names the literal 0.75) — predicted a text catch "
        "and no value assertion anywhere: P75Hours appears in ZERO assertions in the repo",
        "COALESCE(PERCENTILE_CONT(0.75) WITHIN",
        "COALESCE(PERCENTILE_CONT(0.5) WITHIN",
    ),
    (
        "POS-global-median-is-p95",
        "MUST BE CAUGHT — internal/importer/api_resolution_job_test.go:181 asserts "
        "stats.MedianHours to 0.01h against a hand-computed 9 days. If this is NOT caught the "
        "harness cannot see a red and every NOT CAUGHT above is void.",
        GLOBAL_P50,
        GLOBAL_P50.replace("PERCENTILE_CONT(0.5) ", "PERCENTILE_CONT(0.95)"),
    ),
    (
        "VOID-not-a-mutation",
        "nothing — `+ 0` is arithmetically identity, so a CAUGHT verdict would mean some "
        "test is keyed on the SQL's TEXT and not on its answers",
        "COALESCE(AVG(EXTRACT(EPOCH FROM completed_at - created_at)/3600), 0)",
        "COALESCE(AVG(EXTRACT(EPOCH FROM completed_at - created_at)/3600 + 0), 0)",
    ),
]


def sha(p: Path) -> str:
    return hashlib.sha256(p.read_bytes()).hexdigest()


def run(cmd: str) -> tuple[int, str]:
    # shlex.split, never shell=True: the command is an operator flag, and a harness that
    # goes through a shell can be made to mean something other than what it prints.
    p = subprocess.run(shlex.split(cmd), cwd=REPO, capture_output=True, text=True)
    return p.returncode, (p.stdout + p.stderr)


def fails(out: str) -> set[str]:
    """The set of FAILING TEST NAMES, read from the runner's own output.

    Subtests are kept whole (`Parent/child`) so a mutation that reds one subtest of a
    parent that was already red on the baseline is still visible.
    """
    return {
        ln.strip().split("--- FAIL: ", 1)[1].split(" ")[0]
        for ln in out.splitlines()
        if "--- FAIL: " in ln
    }


def build_fails(out: str) -> set[str]:
    """Compile/vet breakage — a mutation that does not build measures nothing."""
    markers = ("[build failed]", "cannot use", "undefined:", "syntax error", "declared and not used")
    return {m for m in markers if m in out}


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--cmd", default="go test -count=1 ./internal/analytics/ ./internal/importer/")
    ap.add_argument("--only", default="")
    args = ap.parse_args()

    before = sha(ENGINE)
    print(f"engine.go sha256 before: {before}")
    print(f"cmd: {args.cmd}\n")

    rc0, out0 = run(args.cmd)
    base = fails(out0)
    bb = build_fails(out0)
    print(f"[C0] no mutation -> exit {rc0}, {len(base)} baseline FAIL name(s)")
    for n in sorted(base):
        print(f"        baseline-red: {n}")
    if bb:
        print(f"[C0] ABORT — pristine tree does not build: {bb}")
        return 1

    results = []
    for cid, predicted, old, new in CONTROLS:
        if args.only and args.only != cid:
            continue
        src = ENGINE.read_text()
        n = src.count(old)
        if n != 1:
            print(f"\n[{cid}] VOID — anchor found {n} times, expected 1. NOT a measurement.")
            results.append((cid, f"VOID(anchor x{n})", predicted))
            continue
        print(f"\n[{cid}] predicted catcher: {predicted}")
        try:
            ENGINE.write_text(src.replace(old, new, 1))
            rc, out = run(args.cmd)
            bf = build_fails(out)
            new_reds = fails(out) - base
            healed = base - fails(out)
            if bf:
                verdict = f"VOID(build {sorted(bf)})"
                print(f"[{cid}] VOID — the mutated tree does not build; nothing was measured")
            elif new_reds:
                verdict = "CAUGHT by " + ",".join(sorted(new_reds))
                print(f"[{cid}] exit {rc} -> CAUGHT by {sorted(new_reds)}")
            else:
                verdict = "NOT CAUGHT"
                print(f"[{cid}] exit {rc} -> NOT CAUGHT")
                print(f"[{cid}] ⚠ MEASURED BLINDNESS: the mutated rule ships and the closure is "
                      f"exactly as green as it was pristine")
            if healed:
                print(f"[{cid}] ⚠ note — baseline reds that WENT GREEN under mutation: {sorted(healed)}")
            results.append((cid, verdict, predicted))
        finally:
            ENGINE.write_text(src)
            assert sha(ENGINE) == before, f"{cid}: restore FAILED — tree is dirty"

    print("\n=== SUMMARY ===")
    for cid, verdict, predicted in results:
        print(f"{cid}: {verdict}")
    after = sha(ENGINE)
    print(f"\nengine.go sha256 after: {after} (== before: {after == before})")

    # The harness grades ITSELF before its verdicts are worth reading.
    verdicts = dict((cid, v) for cid, v, _ in results)
    ok = True
    if "POS-global-median-is-p95" in verdicts and not verdicts["POS-global-median-is-p95"].startswith("CAUGHT"):
        print("⚠⚠ HARNESS FAILURE: the positive control was NOT caught. Every NOT CAUGHT above is void.")
        ok = False
    if "VOID-not-a-mutation" in verdicts and verdicts["VOID-not-a-mutation"] != "NOT CAUGHT":
        print("⚠⚠ HARNESS FAILURE: a behaviour-identical edit was CAUGHT — some test reads the SQL's text.")
        ok = False
    print("harness self-check: " + ("PASS" if ok else "FAIL"))
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
