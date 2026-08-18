#!/usr/bin/env python3
"""Positive controls for the GetDistribution counting rules (W3.4, tab-8f5c).

Third in the series after scripts/w34-workload-counting-controls-4c8e.py (#152) and
scripts/w34-velocity-counting-controls-8f5c.py (#153), pointed at Report 3.

Every control mutates ONE term of the shipped code in internal/analytics/engine.go, runs
a test command, and restores the file in a `finally` with a sha256 check, so a control
that crashes cannot leave the tree edited. Each NAMES ITS PREDICTED CATCHER BEFORE IT
RUNS; a control whose catcher is "nothing" is a measured blindness, not a pass.

⚠ THE VOID CONTROLS ARE NOT DECORATION. A behaviour-identical edit MUST score NOT
CAUGHT, otherwise every other NOT CAUGHT in the table is unreadable. #153 needed three
spellings of one control because the first two redded the pgxmock tests by changing the
SQL TEXT their ExpectQuery regex matches rather than by changing any number — so a
mutation here is only trusted if it leaves the matched substring alone.

Usage:  python3 scripts/w34-distribution-counting-controls-8f5c.py [--cmd "go test ./internal/analytics/"]
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
        "D0-VOID-order-by-ordinal",
        "nothing — `ORDER BY 2 DESC` is the same ordering by the same column: MUST be NOT CAUGHT",
        "        GROUP BY %s\n        ORDER BY COUNT(*) DESC`, col, col)",
        "        GROUP BY %s\n        ORDER BY 2 DESC`, col, col)",
    ),
    (
        "D1-window-keys-on-updated-at",
        "a real-Postgres assertion that the window is on created_at",
        "        WHERE workspace_id = $1\n          AND created_at > NOW() - (INTERVAL '1 day' * $2::int)\n        GROUP BY %s",
        "        WHERE workspace_id = $1\n          AND updated_at > NOW() - (INTERVAL '1 day' * $2::int)\n        GROUP BY %s",
    ),
    (
        "D2-assignee-loses-its-COALESCE",
        "a real-Postgres assertion that UNASSIGNED issues get a named bucket — which is what "
        "every imported issue is",
        '"assignee": "COALESCE(assignee_id, \'unassigned\')",',
        '"assignee": "assignee_id",',
    ),
    (
        "D3-workspace-scope-neutralised",
        "a real-Postgres assertion that another workspace's issues are excluded",
        "        FROM issues\n        WHERE workspace_id = $1\n          AND created_at",
        "        FROM issues\n        WHERE workspace_id = ANY(ARRAY[$1, workspace_id])\n          AND created_at",
    ),
    (
        "D4-label-path-loses-its-window",
        "a real-Postgres assertion that the label breakdown honours the same window as the rest",
        "            FROM issues\n            WHERE workspace_id = $1\n              AND created_at > NOW() - (INTERVAL '1 day' * $2::int)\n        ) t",
        "            FROM issues\n            WHERE workspace_id = $1\n              AND ($2::int IS NOT NULL)\n        ) t",
    ),
    (
        "D5-pct-denominator-is-the-bucket-count",
        "a real-Postgres assertion on pct — the share of the COHORT, not of the bucket list",
        "buckets[i].Pct = float64(buckets[i].Count) / float64(total)",
        "buckets[i].Pct = float64(buckets[i].Count) / float64(len(buckets))",
    ),
    (
        "D6-cost-column-swapped",
        "a real-Postgres assertion that the money column is ai_cost_usd",
        "sql := fmt.Sprintf(`SELECT %s::text, COUNT(*), COALESCE(SUM(ai_cost_usd), 0)",
        "sql := fmt.Sprintf(`SELECT %s::text, COUNT(*), COALESCE(SUM(ai_tokens), 0)",
    ),
    (
        "D7-clamp-ignores-the-ceiling",
        "TestClampDays_BoundsRespected — the must-stay-green companion that keeps NOT CAUGHT from "
        "being a property of the harness",
        "\tif days > maxWindowDays {\n\t\treturn maxWindowDays\n\t}",
        "\tif days > maxWindowDays {\n\t\treturn days\n\t}",
    ),
    (
        "D8-VOID-priority-cast-doubled",
        "nothing — the SELECT already appends ::text, so `priority::text::text` is the same "
        "column: MUST be NOT CAUGHT",
        '"priority": "priority::text",',
        '"priority": "priority::text::text",',
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
            if not caught and "VOID" in cid:
                print(f"[{cid}] ✓ as required: a behaviour-identical edit changed no verdict")
            elif not caught:
                print(f"[{cid}] ⚠ MEASURED BLINDNESS: the mutated rule ships and every test is green")
            if caught and "VOID" in cid:
                print(f"[{cid}] ⚠⚠ A VOID CONTROL SCORED CAUGHT — the edit is not behaviour-identical "
                      f"after all, so every NOT CAUGHT in this table is suspect until it is re-cut")
            if caught and not names:
                print(f"[{cid}] ⚠ non-zero exit with NO named FAIL — a build error is not a caught "
                      f"mutation; read the output")
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
