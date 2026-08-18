#!/usr/bin/env python3
"""Positive controls for the GetVelocity counting rules (W3.4, tab-8f5c).

The sibling of scripts/w34-workload-counting-controls-4c8e.py, pointed at Report 1.
Every control mutates ONE term of the shipped SQL in internal/analytics/engine.go, runs
a test command, and restores the file in a `finally` with a sha256 check, so a control
that crashes cannot leave the tree edited.

Each control NAMES ITS PREDICTED CATCHER BEFORE IT RUNS. A control whose catcher is
"nothing" is a measured blindness, not a pass. V0 and V9 are the VOID controls: they are
behaviour-identical edits and MUST score NOT CAUGHT, otherwise every other NOT CAUGHT
in the table is unreadable (a harness that catches nothing looks the same as a suite
that is blind to everything).

Usage:  python3 scripts/w34-velocity-counting-controls-8f5c.py [--cmd "go test ./internal/analytics/"]
"""

import argparse
import hashlib
import shlex
import subprocess
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
ENGINE = REPO / "internal/analytics/engine.go"


def sha(p: Path) -> str:
    return hashlib.sha256(p.read_bytes()).hexdigest()


# (id, predicted catcher, old, new)
CONTROLS = [
    (
        "V0-VOID-alias-renamed",
        "nothing — pgx scans positionally, so the alias is behaviour-identical: MUST be NOT CAUGHT",
        "), 0) AS total,",
        "), 0) AS total_count,",
    ),
    (
        "V1-completed-drops-cancelled",
        "a real-Postgres assertion on what `completed` counts",
        "AND status IN ('done','cancelled')), 0) AS completed",
        "AND status IN ('done')), 0) AS completed",
    ),
    (
        "V2-completed-takes-in-review",
        "a real-Postgres assertion on what `completed` counts",
        "AND status IN ('done','cancelled')), 0) AS completed",
        "AND status IN ('done','cancelled','in_review')), 0) AS completed",
    ),
    (
        "V3-oldest-cycles-first",
        "a real-Postgres assertion that the report is the LAST N cycles",
        "ORDER BY c.number DESC",
        "ORDER BY c.number ASC",
    ),
    (
        "V4-limit-ignores-argument",
        "a real-Postgres assertion that `cycles` bounds the row count",
        "LIMIT $3`, teamID, workspaceID, cycles)",
        "LIMIT GREATEST($3::int, 50)`, teamID, workspaceID, cycles)",
    ),
    (
        "V5-total-not-scoped-to-the-cycle",
        "a real-Postgres assertion that `total` counts THIS cycle's issues",
        "COALESCE((SELECT COUNT(*) FROM issues WHERE cycle_id = c.id), 0) AS total",
        "COALESCE((SELECT COUNT(*) FROM issues WHERE cycle_id IS NOT NULL), 0) AS total",
    ),
    (
        "V6-aicost-not-scoped-to-the-cycle",
        "a real-Postgres assertion that `ai_cost` sums THIS cycle's issues",
        "COALESCE((SELECT SUM(ai_cost_usd) FROM issues WHERE cycle_id = c.id), 0) AS ai_cost",
        "COALESCE((SELECT SUM(ai_cost_usd) FROM issues WHERE cycle_id IS NOT NULL), 0) AS ai_cost",
    ),
    # ⚠ V7 IS THE **THIRD** SPELLING OF THIS CONTROL AND THE TWO DEAD ONES ARE THE POINT.
    # The three pgxmock tests in engine_test.go set ExpectQuery(`FROM cycles c\s+WHERE c.team_id`)
    # — a QUERY-TEXT fingerprint. Spelling 1 (`WHERE ($1::text IS NOT NULL) AND …`) and spelling 2
    # (`WHERE (c.team_id = $1 OR $1::text IS NOT NULL) AND …`, kept as V7-VOID below) BOTH scored
    # CAUGHT by all three of them, and neither catch was an assertion: the mock redded because the
    # SQL string stopped matching the regex — spelling 2 only because of the opening bracket. A
    # mock that no longer recognises the statement fails whatever the statement computes, so that
    # verdict is the same for a mutation that fixes a bug as for one that ships it. This spelling
    # leaves `WHERE c.team_id` byte-identical and neutralises the filter with ANY(ARRAY[…]), so a
    # CAUGHT here is a claim about rows rather than about characters.
    (
        "V7-team-scope-neutralised",
        "a real-Postgres assertion that another team's cycles are excluded",
        "WHERE c.team_id = $1 AND c.workspace_id = $2",
        "WHERE c.team_id = ANY(ARRAY[$1, c.team_id]) AND c.workspace_id = $2",
    ),
    (
        "V7-VOID-mock-text-fingerprint",
        "the pgxmock ExpectQuery regex ONLY — a CAUGHT here says the SQL text changed, not that "
        "a count did, and it is kept so that reading is written down rather than re-derived",
        "WHERE c.team_id = $1 AND c.workspace_id = $2",
        "WHERE (c.team_id = $1 OR $1::text IS NOT NULL) AND c.workspace_id = $2",
    ),
    (
        "V8-workspace-scope-neutralised",
        "TestAnalytics_Velocity_WorkspaceScoped — the must-stay-green companion that keeps "
        "NOT CAUGHT from being a property of the harness",
        "WHERE c.team_id = $1 AND c.workspace_id = $2",
        "WHERE c.team_id = $1 AND ($2::text IS NOT NULL)",
    ),
    (
        "V9-VOID-operands-swapped",
        "nothing — `c.id = cycle_id` is the same predicate written the other way round, and the "
        "mock's regex does not reach this line: MUST be NOT CAUGHT",
        "COALESCE((SELECT SUM(ai_cost_usd) FROM issues WHERE cycle_id = c.id), 0) AS ai_cost",
        "COALESCE((SELECT SUM(ai_cost_usd) FROM issues WHERE c.id = cycle_id), 0) AS ai_cost",
    ),
]


def run(cmd: str) -> tuple[int, str]:
    # shlex.split, never shell=True: the command is an operator flag, and a harness that
    # goes through a shell can be made to mean something other than what it prints.
    p = subprocess.run(shlex.split(cmd), cwd=REPO, capture_output=True, text=True)
    return p.returncode, (p.stdout + p.stderr)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--cmd", default="go test -count=1 ./internal/analytics/")
    ap.add_argument("--only", default="")
    args = ap.parse_args()

    before = sha(ENGINE)
    print(f"engine.go sha256 before: {before}")
    print(f"cmd: {args.cmd}")
    rc, out = run(args.cmd)
    print(f"\n[C0] no mutation -> exit {rc} (must be 0)")
    if rc != 0:
        print(out[-4000:])
        return 1

    results = []
    for cid, predicted, old, new in CONTROLS:
        if args.only and args.only != cid:
            continue
        src = ENGINE.read_text()
        if src.count(old) != 1:
            print(f"[{cid}] SKIPPED — anchor found {src.count(old)} times, expected 1")
            results.append((cid, "VOID(anchor)", predicted))
            continue
        print(f"\n[{cid}] predicted catcher: {predicted}")
        try:
            ENGINE.write_text(src.replace(old, new, 1))
            rc, out = run(args.cmd)
            caught = rc != 0
            names = sorted({
                ln.split("--- FAIL: ")[1].split(" ")[0]
                for ln in out.splitlines() if ln.strip().startswith("--- FAIL: ")
            })
            print(f"[{cid}] exit {rc} -> {'CAUGHT' if caught else 'NOT CAUGHT'}"
                  + (f" by {names}" if names else ""))
            # A VOID control is a behaviour-identical edit, so NOT CAUGHT is its REQUIRED result and
            # calling that a blindness would be the harness lying about its own controls — which is
            # the same failure it exists to find.
            if not caught and "VOID" in cid:
                print(f"[{cid}] ✓ as required: a behaviour-identical edit changed no verdict")
            elif not caught:
                print(f"[{cid}] ⚠ MEASURED BLINDNESS: the mutated rule ships and every test is green")
            if caught and "VOID" in cid and cid != "V7-VOID-mock-text-fingerprint":
                print(f"[{cid}] ⚠⚠ A VOID CONTROL SCORED CAUGHT — the edit is not behaviour-identical "
                      f"after all, so every NOT CAUGHT in this table is suspect until it is re-cut")
            elif not names:
                print(f"[{cid}] ⚠ non-zero exit with NO named FAIL — read the output before "
                      f"calling this a catch (a build error is not a caught mutation)")
                print(out[-2000:])
            results.append((cid, "CAUGHT " + ",".join(names) if caught else "NOT CAUGHT", predicted))
        finally:
            ENGINE.write_text(src)
            assert sha(ENGINE) == before, f"{cid}: restore FAILED — tree is dirty"

    print("\n=== SUMMARY ===")
    for cid, verdict, predicted in results:
        print(f"{cid}: {verdict}   (predicted: {predicted})")
    print(f"engine.go sha256 after: {sha(ENGINE)} (== before: {sha(ENGINE) == before})")
    return 0


if __name__ == "__main__":
    sys.exit(main())
