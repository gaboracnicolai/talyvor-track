#!/usr/bin/env python3
"""Positive controls for TestAICostTrends_TheLimitedSubQueriesRankByCost_RealPG (W3.4, tab-b9d7).

`GetAICostTrends` runs five sub-queries; two carry a LIMIT, and in those two the ORDER BY
decides MEMBERSHIP rather than layout. The guard passes on pristine main because the shipped
SQL is correct, so everything that makes it a guard is here.

  P1  leaderboard serves the TEN CHEAPEST (fingerprint intact) -> [L-MEMBERSHIP] [L-PREFIX]
  P2  leaderboard DESC -> ASC (the naive spelling)             -> the new test AND the mock's
                                                                  query-TEXT fingerprint
  P3  leaderboard ORDER BY DELETED                             -> NOT CAUGHT by the new test
                                                                  (documented blindness: the index)
  P4  cost_by_label DESC -> ASC                                -> [B-ORDER] [B-MEMBERSHIP]
  P5  cost_by_label ORDER BY DELETED                           -> measured, not predicted
  P6  cost_by_TEAM DESC -> ASC (no LIMIT there)                -> must-stay-green in the new test
  P7  whitespace-only change in the label ORDER BY             -> CAUGHT BY NOTHING (void)
  P8  leaderboard LIMIT 10 -> LIMIT 5                          -> [L-LIMIT]

⚠ P1 EXISTS BECAUSE P2 LIES. A plain DESC->ASC scores CAUGHT on pristine main — by
engine_test.go's `ExpectQuery("ORDER BY ai_cost_usd DESC LIMIT 10")`, a pgxmock QUERY-TEXT
fingerprint over a mock that FEEDS the rows and cannot see an ordering. P1 wraps the real
statement in a subselect that takes the cheapest ten and leaves that substring
BYTE-IDENTICAL, so its verdict is an assertion rather than a moved bracket. This is the
same trap #152 and #153 each measured; it was still the only thing standing here.

⚠ P3 IS THE CONTROL ON THIS GUARD'S OWN LIMITS. migrations/0009_analytics.sql indexes
(workspace_id, ai_cost_usd DESC) WHERE ai_cost_usd > 0, so an unordered read of this cohort
comes back ranked anyway and the new test CANNOT see the clause removed. Predicting NOT
CAUGHT and reporting it is the difference between a documented blindness and a hidden one.

Usage:  TRACK_TEST_DATABASE_URL=... python3 scripts/w34-aicost-ordering-controls-b9d7.py
"""

import hashlib
import os
import re
import subprocess
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
TARGET = os.path.join(REPO, "internal", "analytics", "engine.go")

LEADER = """	rows, err = e.pool.Query(ctx, `
        SELECT id, identifier, title, ai_cost_usd, ai_tokens
        FROM issues
        WHERE workspace_id = $1 AND ai_cost_usd > 0
          AND updated_at > NOW() - (INTERVAL '1 day' * $2::int)
        ORDER BY ai_cost_usd DESC LIMIT 10`,"""

LEADER_CHEAPEST = """	rows, err = e.pool.Query(ctx, `
        SELECT id, identifier, title, ai_cost_usd, ai_tokens
        FROM (SELECT * FROM issues
              WHERE workspace_id = $1 AND ai_cost_usd > 0
                AND updated_at > NOW() - (INTERVAL '1 day' * $2::int)
              ORDER BY ai_cost_usd ASC LIMIT 10) issues
        ORDER BY ai_cost_usd DESC LIMIT 10`,"""

LEADER_TAIL = "        ORDER BY ai_cost_usd DESC LIMIT 10`,"
LABEL_TAIL = "        ORDER BY SUM(ai_cost_usd) DESC LIMIT 20`,"
TEAM_TAIL = "        ORDER BY SUM(i.ai_cost_usd) DESC NULLS LAST`,"

CONTROLS = [
    ("P1", "the leaderboard serves the TEN CHEAPEST issues (fingerprint substring intact)",
     [(LEADER, LEADER_CHEAPEST)], "CAUGHT", ["L-MEMBERSHIP", "L-PREFIX"]),
    ("P2", "the leaderboard ranks ASC — the naive spelling, which the MOCK's text fingerprint sees",
     [(LEADER_TAIL, "        ORDER BY ai_cost_usd ASC LIMIT 10`,")], "CAUGHT", ["L-ORDER"]),
    ("P3", "the leaderboard's ORDER BY DELETED — the index already ranks it, so this file is blind",
     [(LEADER_TAIL, "        LIMIT 10`,")], "NOT CAUGHT by the new test", []),
    ("P4", "cost_by_label ranks ASC — the CHEAPEST twenty labels",
     [(LABEL_TAIL, "        ORDER BY SUM(ai_cost_usd) ASC LIMIT 20`,")], "CAUGHT", ["B-ORDER"]),
    ("P5", "cost_by_label's ORDER BY DELETED — measured rather than predicted",
     [(LABEL_TAIL, "        LIMIT 20`,")], "MEASURED", []),
    ("P6", "cost_by_TEAM ranks ASC — no LIMIT there, so must stay green in the new test",
     [(TEAM_TAIL, "        ORDER BY SUM(i.ai_cost_usd) ASC NULLS LAST`,")],
     "NOT CAUGHT by the new test", []),
    ("P7", "whitespace only in the label ORDER BY — must red NOTHING",
     [(LABEL_TAIL, "        ORDER BY SUM(ai_cost_usd)  DESC LIMIT 20`,")], "NOT CAUGHT", []),
    ("P8", "the leaderboard LIMIT 10 -> LIMIT 5",
     [(LEADER_TAIL, "        ORDER BY ai_cost_usd DESC LIMIT 5`,")], "CAUGHT", ["L-LIMIT"]),
]

FAIL_RE = re.compile(r"^\s*--- FAIL: (\S+)", re.M)
TAG_RE = re.compile(r"\[([A-Z][A-Z0-9-]+)\]")
NEW_TEST = "TestAICostTrends_TheLimitedSubQueriesRankByCost_RealPG"


def sha256(path):
    with open(path, "rb") as fh:
        return hashlib.sha256(fh.read()).hexdigest()


def run_package():
    proc = subprocess.run(
        ["go", "test", "-timeout", "300s", "-race", "-count=1", "-v", "./internal/analytics/"],
        cwd=REPO, capture_output=True, text=True,
    )
    out = proc.stdout + proc.stderr
    return proc.returncode, sorted(set(FAIL_RE.findall(out))), sorted(set(TAG_RE.findall(out))), out


def main():
    if not os.environ.get("TRACK_TEST_DATABASE_URL"):
        sys.exit("TRACK_TEST_DATABASE_URL must be set — these controls need real Postgres")

    pristine = sha256(TARGET)
    with open(TARGET) as fh:
        original = fh.read()
    print(f"pristine {os.path.relpath(TARGET, REPO)} sha256 {pristine}")

    print("\n=== BASELINE (no mutation) — must be GREEN ===")
    code, failed, _, out = run_package()
    if code != 0:
        print(out[-4000:])
        sys.exit(f"baseline is not green: {failed}")
    print("baseline: PASS")

    results = []
    for name, desc, edits, predicted, want_tags in CONTROLS:
        print(f"\n=== {name}: {desc}")
        print(f"    PREDICTED: {predicted} {want_tags if want_tags else ''}")
        try:
            mutated = original
            for old, new in edits:
                if mutated.count(old) != 1:
                    sys.exit(f"{name}: anchor matched {mutated.count(old)} times, want 1")
                mutated = mutated.replace(old, new)
            with open(TARGET, "w") as fh:
                fh.write(mutated)
            code, failed, tags, out = run_package()
            caught_here = NEW_TEST in failed
            others = [f for f in failed if f != NEW_TEST]
            print(f"    ACTUAL:    new test {'CAUGHT' if caught_here else 'NOT CAUGHT'}"
                  f" · ALSO caught by: {others if others else '(nothing)'}")
            print(f"    tags: {tags if tags else '(none)'}")
            for t in want_tags:
                if f"[{t}]" not in out:
                    print(f"    ⚠ PREDICTION MISS: expected tag [{t}] absent from the failure output")
            results.append((name, caught_here, others, tags))
        finally:
            with open(TARGET, "w") as fh:
                fh.write(original)
            after = sha256(TARGET)
            assert after == pristine, f"{name}: RESTORE FAILED ({after} != {pristine})"
            print(f"    restored, sha256 {after} ok")

    print("\n=== SUMMARY ===")
    for name, here, others, tags in results:
        print(f"{name:>3}  new test {'CAUGHT    ' if here else 'NOT CAUGHT'}  also: "
              f"{','.join(others) if others else '-'}")
    print(f"\nfinal sha256 {sha256(TARGET)} (pristine {pristine})")


if __name__ == "__main__":
    main()
