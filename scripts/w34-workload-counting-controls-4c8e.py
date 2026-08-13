#!/usr/bin/env python3
"""Positive controls for the GetWorkload counting rules (W3.4, tab-4c8e).

Every control mutates ONE predicate of the shipped SQL in internal/analytics/engine.go,
runs a test command, and restores the file in a `finally` with a sha256 check, so a
control that crashes cannot leave the tree edited.

Each control NAMES ITS PREDICTED CATCHER BEFORE IT RUNS. A control whose catcher is
"nothing" is a measured blindness, not a pass.

Usage:  python3 scripts/w34-workload-counting-controls-4c8e.py [--cmd "go test ./internal/analytics/"]
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
        "M1-overdue-inverted",
        "a real-Postgres assertion on the overdue count",
        "AND i.due_date < NOW()",
        "AND i.due_date > NOW()",
    ),
    (
        "M2-in-review-dropped",
        "a real-Postgres assertion on the in_progress count",
        "COUNT(*) FILTER (WHERE i.status IN ('in_progress','in_review')) AS in_progress",
        "COUNT(*) FILTER (WHERE i.status IN ('in_progress')) AS in_progress",
    ),
    (
        "M3-open-includes-cancelled",
        "a real-Postgres assertion on the open count",
        "COUNT(*) FILTER (WHERE i.status NOT IN ('done','cancelled')) AS open_issues",
        "COUNT(*) FILTER (WHERE i.status NOT IN ('done')) AS open_issues",
    ),
    (
        "M4-overdue-ignores-status",
        "a real-Postgres assertion that a DONE issue past its due date is not overdue",
        "                  AND i.due_date < NOW()\n                  AND i.status NOT IN ('done','cancelled')\n            ) AS overdue",
        "                  AND i.due_date < NOW()\n            ) AS overdue",
    ),
    (
        "M5-team-scope-dropped",
        "a real-Postgres assertion that the team filter excludes another team's issues",
        'teamSQL = " AND i.team_id = $2"',
        'teamSQL = " AND ($2::text IS NOT NULL)"',
    ),
    # ⚠ M5 IS THE SECOND SPELLING OF THIS CONTROL AND THE FIRST ONE IS KEPT AS M5-VOID BECAUSE IT
    # SCORED "NOT CAUGHT" ON AN INNOCENT PRODUCT: `i.team_id = COALESCE($2, i.team_id)` still
    # filters on the team when $2 is non-NULL, so it mutated nothing. A control that does not
    # change behaviour reports a blindness that is not there.
    (
        "M5-VOID-not-a-mutation",
        "nothing — this edit is behaviour-identical and MUST score NOT CAUGHT",
        'teamSQL = " AND i.team_id = $2"',
        'teamSQL = " AND i.team_id = COALESCE($2, i.team_id)"',
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
            if not caught:
                print(f"[{cid}] ⚠ MEASURED BLINDNESS: the mutated rule ships and every test is green")
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
