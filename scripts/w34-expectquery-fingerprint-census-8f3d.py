#!/usr/bin/env python3
"""W3.4 / tab-8f3d — THE `ExpectQuery` TEXT-FINGERPRINT CENSUS.

Takes tab-b9d7's handed-on lead (b). A pgxmock `ExpectQuery(regex)` matches the STATEMENT
TEXT. When the regex names a SQL term, the mock reds if that term's TEXT changes — whether
or not any assertion in the test can see the term's BEHAVIOUR. #152 (workload), #153
(velocity) and #160 (the ai-cost leaderboard) each found one as a one-off, and in #160 the
fingerprint was the ONLY thing that reddened, so a session measuring coverage would have
recorded the term as covered.

⚠ THE POPULATION, MEASURED RATHER THAN INHERITED. The lead says "14 `ExpectQuery` call
sites"; `grep -c 'ExpectQuery('` on engine_test.go does return 14, but ONE of those is a
PROSE MENTION inside the file's own header comment (it quotes the regex while explaining
this very hazard). The call sites are `pool.ExpectQuery(` and there are **13**. Every other
`ExpectQuery` in the package — in aicost_ordering_realpg_test.go, velocity_counting_realpg_test.go,
distribution_counting_realpg_test.go and scope_read_test.go — is likewise a comment. A
census that counts a sentence about a call site as a call site is off by one before it
starts.

⚠⚠ THE FIRST VERSION OF THIS INSTRUMENT WAS BROKEN, AND THE DIRECTION OF THE ERROR WAS
FLATTERING. Four sites were probed by inserting an EXTRA SPACE between existing tokens and
all four came back "not pinned by text" — i.e. healthy. They are not: pgxmock (like sqlmock,
which it derives from) COLLAPSES RUNS OF WHITESPACE on both the expected and the actual
statement before matching, so a double space is normalised away and the probe never fired at
all. MEASURED, not read from the library: the same four sites red immediately when the
rewrite is a TOKEN change instead (below), which is the positive control proving the site is
reachable and the probe can fire there. A probe that cannot fire reports the same word as a
guard that found nothing.

THE INSTRUMENT. For each fingerprint, rewrite the SHIPPED statement so the matched text
changes while the SQL means EXACTLY what it meant before — an explicit `AS` on a table alias,
a redundant parenthesis around an expression, or a space INSIDE an argument list (which
survives normalisation because it is not a run). Behaviour is unchanged by construction, so:

    RED   => the test is watching that term's TEXT. A harmless refactor breaks it, and the
             red carries no information about the product.
    GREEN => the regex does not pin that term (or the term is spelled elsewhere too).

This is the (A) direction. The complementary (B) direction — change the BEHAVIOUR while
leaving the matched substring byte-identical — is what #152/#153/#160 ran per report, and
is what the real-Postgres oracle tests in this package now cover.

Usage:  python3 scripts/w34-expectquery-fingerprint-census-8f3d.py
"""

import hashlib
import os
import re
import subprocess
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
ENGINE = os.path.join(REPO, "internal", "analytics", "engine.go")
TESTFILE = os.path.join(REPO, "internal", "analytics", "engine_test.go")
DSN = os.environ.get(
    "TRACK_TEST_DATABASE_URL",
    "postgres://postgres:postgres@localhost:55438/postgres?sslmode=disable",
)


def sha(path):
    with open(path, "rb") as fh:
        return hashlib.sha256(fh.read()).hexdigest()


def run_tests():
    env = dict(os.environ, TRACK_TEST_DATABASE_URL=DSN)
    p = subprocess.run(
        ["go", "test", "-timeout", "300s", "-count=1", "./internal/analytics/"],
        cwd=REPO, env=env, capture_output=True, text=True,
    )
    return p.returncode, p.stdout + p.stderr


def failing_tests(out):
    return sorted(set(re.findall(r"^\s*--- FAIL: (\S+)", out, re.M)))


# (site-lines-in-engine_test.go, regex as written, anchor, semantics-preserving replacement, why-preserving)
SITES = [
    (
        "39,68,271",
        r"FROM cycles c\s+WHERE c.team_id",
        "        FROM cycles c\n        WHERE c.team_id = $1 AND c.workspace_id = $2",
        "        FROM cycles AS c\n        WHERE c.team_id = $1 AND c.workspace_id = $2",
        "an explicit AS on a table alias — identical parse tree",
    ),
    (
        "94,124",
        r"GROUP BY status  /  GROUP BY priority  (one fmt template serves both)",
        "        GROUP BY %s\n        ORDER BY COUNT(*) DESC`, col, col)",
        "        GROUP BY (%s)\n        ORDER BY COUNT(*) DESC`, col, col)",
        "a redundant parenthesis around the group expression — survives whitespace normalisation",
    ),
    (
        "151",
        r"PERCENTILE_CONT\(0\.5\).*PERCENTILE_CONT\(0\.75\).*PERCENTILE_CONT\(0\.95\)",
        "COALESCE(PERCENTILE_CONT(0.5)  WITHIN GROUP",
        "COALESCE(PERCENTILE_CONT( 0.5)  WITHIN GROUP",
        "one extra space inside the argument list",
    ),
    (
        "159",
        r"GROUP BY priority  (the resolution breakdown, a different statement)",
        "        GROUP BY priority`, teamSQL),",
        "        GROUP BY (priority)`, teamSQL),",
        "a redundant parenthesis around the group expression",
    ),
    (
        "187",
        r"SELECT COALESCE\(SUM\(ai_cost_usd\), 0\), COUNT\(\*\)",
        "        SELECT COALESCE(SUM(ai_cost_usd), 0), COUNT(*) FILTER (WHERE ai_cost_usd > 0)",
        "        SELECT COALESCE(SUM(ai_cost_usd), 0), COUNT( *) FILTER (WHERE ai_cost_usd > 0)",
        "a space INSIDE COUNT's argument list — not a run, so normalisation keeps it",
    ),
    (
        "191",
        r"date_trunc\('day'",
        "        SELECT date_trunc('day', updated_at) AS day,",
        "        SELECT date_trunc( 'day', updated_at) AS day,",
        "one extra space inside the argument list",
    ),
    (
        "202",
        r"ORDER BY ai_cost_usd DESC LIMIT 10",
        "        ORDER BY ai_cost_usd DESC LIMIT 10`,",
        "        ORDER BY (ai_cost_usd) DESC LIMIT 10`,",
        "a redundant parenthesis around the sort expression",
    ),
    (
        "207",
        r"JOIN teams t ON t.id = i.team_id",
        "        JOIN teams t ON t.id = i.team_id",
        "        JOIN teams AS t ON t.id = i.team_id",
        "an explicit AS on a table alias",
    ),
    (
        "212",
        r"UNNEST\(labels\)  (the ai-cost by-label sub-query)",
        "            SELECT UNNEST(labels) AS label, ai_cost_usd\n            FROM issues\n            WHERE workspace_id = $1\n              AND updated_at > NOW()",
        "            SELECT UNNEST( labels) AS label, ai_cost_usd\n            FROM issues\n            WHERE workspace_id = $1\n              AND updated_at > NOW()",
        "one extra space inside the argument list (anchored on updated_at, which is what "
        "distinguishes this UNNEST from the distribution path's created_at one)",
    ),
    (
        "245",
        r"JOIN members m ON m.id = i.assignee_id",
        "        JOIN members m ON m.id = i.assignee_id",
        "        JOIN members AS m ON m.id = i.assignee_id",
        "an explicit AS on a table alias",
    ),
]


def main():
    pristine = sha(ENGINE)
    with open(TESTFILE, encoding="utf-8") as fh:
        tf = fh.read()
    calls = tf.count("pool.ExpectQuery(")
    grepish = tf.count("ExpectQuery(")
    print("=" * 78)
    print("POPULATION")
    print(f"  engine_test.go  'ExpectQuery('       occurrences : {grepish}")
    print(f"  engine_test.go  'pool.ExpectQuery('  CALL SITES  : {calls}")
    print(f"  difference is prose in the file's own header    : {grepish - calls}")
    print("=" * 78)

    code, out = run_tests()
    print(f"\nC0 BASELINE (no mutation): {'GREEN' if code == 0 else 'RED'}")
    if code != 0:
        print(out[-3000:])
        return 1

    with open(ENGINE, encoding="utf-8") as fh:
        original = fh.read()

    results = []
    for lines, regex, anchor, repl, why in SITES:
        n = original.count(anchor)
        print(f"\n=== engine_test.go:{lines}  {regex}")
        print(f"    preserving rewrite: {why}")
        if n != 1:
            print(f"    !! ANCHOR MATCHES {n} TIMES — refusing to mutate (it would report a "
                  f"verdict about a different statement)")
            results.append((lines, regex, f"ANCHOR x{n}", []))
            continue
        try:
            with open(ENGINE, "w", encoding="utf-8") as fh:
                fh.write(original.replace(anchor, repl))
            code, out = run_tests()
            verdict = "FINGERPRINT (reds on a harmless refactor)" if code != 0 else "not pinned by text"
            tests = failing_tests(out)
            print(f"    -> {verdict}")
            print(f"       reds: {tests if tests else '(none)'}")
            results.append((lines, regex, verdict, tests))
        finally:
            with open(ENGINE, "w", encoding="utf-8") as fh:
                fh.write(original)
            back = sha(ENGINE)
            assert back == pristine, f"engine.go NOT restored ({back} != {pristine})"

    print("\n" + "=" * 78)
    print("CENSUS")
    pinned = 0
    for lines, regex, verdict, tests in results:
        if verdict.startswith("FINGERPRINT"):
            pinned += 1
        print(f"  engine_test.go:{lines:<12s} {verdict}")
        if tests:
            print(f"      {', '.join(tests)}")
    print(f"\n  {pinned} of {len(results)} probed fingerprints red on a SEMANTICS-PRESERVING rewrite.")
    print(f"engine.go sha256 (final) = {sha(ENGINE)}  (pristine = {pristine})")
    return 0


if __name__ == "__main__":
    sys.exit(main())
