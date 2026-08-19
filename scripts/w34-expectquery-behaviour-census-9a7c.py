#!/usr/bin/env python3
"""W3.4 / tab-9a7c — THE `ExpectQuery` **BEHAVIOUR** CENSUS (the (B) direction).

The (A) direction — `scripts/w34-expectquery-fingerprint-census-8f3d.py` — asked of each of
the 13 `pool.ExpectQuery(` call sites in `internal/analytics/engine_test.go`: does the site
red on a rewrite that changes the statement TEXT and nothing the product does? It answered
10 of 13.

This is the complementary and harder question, handed on by tab-8f3d as "(c) THE (B)
DIRECTION OF THE CENSUS IS NOT DONE": **change the BEHAVIOUR while leaving the matched
substring byte-identical, and ask whether anything reds.** A site that is green in BOTH
directions names a term no assertion in this repository can see.

⚠ THE ANCHOR CLAIM IS MEASURED, NOT ASSERTED, AND THAT IS THE WHOLE GUARD. "Byte-identical
matched substring" is easy to say and easy to get wrong: pgxmock normalises whitespace on
BOTH sides before matching (`stripQuery` = `\\s+` -> one space, then TrimSpace, in
pgxmock/v4/query.go), and a `.*` in an expectation makes the WHOLE SPAN between two literals
mutable — so an edit that looks safely outside the "regex" can be squarely inside the
matched text. For every site this script therefore:

    1. extracts the shipped SQL literal, substitutes the fmt verbs the call site supplies,
       and normalises it exactly as pgxmock does;
    2. runs the site's own expectation regex over it and records `M_before`, the matched
       BYTES;
    3. applies the mutation, re-extracts, re-normalises, re-matches, records `M_after`;
    4. **VOIDs the site unless `M_before == M_after`.** A verdict from a site whose
       expectation stopped matching is a verdict about the regex, not about the product.

Read the verdicts as:

    CAUGHT  => some test in the analytics import closure reds. The behaviour is asserted
               somewhere, by something other than the query text.
    BLIND   => nothing reds anywhere. The shipped statement can be made to answer a
               different question and every gate in this repository stays green.
    VOID    => the matched substring moved; no verdict is claimed.

⚠ SCOPE, AND IT IS AN IMPORT-GRAPH FACT RATHER THAN A CHOICE. `go list -f '{{.ImportPath}}
{{.Imports}} {{.TestImports}} {{.XTestImports}}'` names exactly four packages that can see
`internal/analytics/engine.go`: internal/analytics, internal/mcp, cmd/track (non-test) and
internal/importer (test-only — its date/resolution job tests call GetTimeToResolution against
real Postgres). No other package in this repository compiles against this file, so no other
package can red for an edit to it. Those four are what the census runs.

⚠ ENVIRONMENTAL, AND SUBTRACTED RATHER THAN IGNORED. `/tmp/w34-jira-corpus` and
`/tmp/w34-linear-corpus-cache` exist-but-empty on this machine, so 11 `internal/importer`
corpus censuses fail at C0 by design (an instrument that read nothing must not report a clean
answer). The baseline failure SET is recorded at C0 and every verdict is the DIFFERENCE
against it — a site is CAUGHT only on a red that C0 did not already have.

POSITIVE CONTROLS ON THE INSTRUMENT ITSELF, because three sessions in this queue have now
shipped a harness that reported health it never measured:

    P0  identity rewrite (anchor replaced by itself)   -> must report NO new reds
    P1  the velocity workspace scope neutralised       -> must report a red, and from a test
                                                          file that is NOT engine_test.go
    P2  an edit INSIDE a `.*` span of a site's own
        matched text                                   -> must be VOIDed by step 4

P2 is the one to read. Without it "the matched substring was byte-identical" is a sentence
this script prints about itself.

Usage:  TRACK_TEST_DATABASE_URL=... python3 scripts/w34-expectquery-behaviour-census-9a7c.py
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
    "postgres://postgres:postgres@127.0.0.1:55471/postgres?sslmode=disable",
)

# The whole import closure of engine.go, from `go list`. See the header.
PACKAGES = [
    "./internal/analytics/",
    "./internal/importer/",
    "./internal/mcp/",
    "./cmd/track/",
]


def sha(path):
    with open(path, "rb") as fh:
        return hashlib.sha256(fh.read()).hexdigest()


def strip_query(q):
    """pgxmock/v4 query.go: `\\s+` -> " ", then TrimSpace. Applied to BOTH sides."""
    return re.sub(r"\s+", " ", q).strip()


def run_tests():
    env = dict(os.environ, TRACK_TEST_DATABASE_URL=DSN)
    p = subprocess.run(
        ["go", "test", "-timeout", "600s", "-count=1"] + PACKAGES,
        cwd=REPO, env=env, capture_output=True, text=True,
    )
    return p.returncode, p.stdout + p.stderr


def failing_tests(out):
    return sorted(set(re.findall(r"^\s*--- FAIL: (\S+)", out, re.M)))


def literal_holding(source, marker):
    """The backtick raw string literal that contains `marker`, exactly once."""
    lits = [m.group(0)[1:-1] for m in re.finditer(r"`[^`]*`", source)]
    hits = [l for l in lits if marker in l]
    if len(hits) != 1:
        raise AssertionError(f"marker {marker!r} is in {len(hits)} literals, want 1")
    return hits[0]


def matched_bytes(source, site):
    """M — the bytes the site's expectation regex matches in the shipped statement."""
    sql = literal_holding(source, site["marker"])
    for verb in site["fmt"]:
        sql = sql.replace("%s", verb, 1)
    m = re.search(strip_query(site["regex"]), strip_query(sql))
    return m.group(0) if m else None


# Each site: the engine_test.go line, its expectation regex verbatim, a marker that picks the
# shipped statement out of engine.go, the fmt verbs the CALL SITE supplies, and a mutation
# that changes what the statement ANSWERS. Every mutation is checked to land outside M.
SITES = [
    dict(
        line="39", test="TestGetVelocity_ReturnsCompletionRates",
        regex=r"FROM cycles c\s+WHERE c.team_id",
        marker="FROM cycles c\n        WHERE c.team_id", fmt=[],
        old="AND status IN ('done','cancelled')), 0) AS completed",
        new="AND status IN ('done')), 0) AS completed",
        why="`completed` stops counting cancelled issues — every completion rate moves",
    ),
    dict(
        line="68", test="TestGetVelocity_IncludesAICostPerCycle",
        regex=r"FROM cycles c\s+WHERE c.team_id",
        marker="FROM cycles c\n        WHERE c.team_id", fmt=[],
        old="COALESCE((SELECT SUM(ai_cost_usd) FROM issues WHERE cycle_id = c.id), 0) AS ai_cost",
        new="COALESCE((SELECT SUM(ai_cost_usd) FROM issues WHERE cycle_id IS NOT NULL), 0) AS ai_cost",
        why="the AI-cost subquery is DE-CORRELATED — every cycle reports every cycle's spend",
    ),
    dict(
        line="94", test="TestGetDistribution_GroupsByStatus",
        regex=r"GROUP BY status",
        marker="SELECT %s::text, COUNT(*)", fmt=["status", "status"],
        old="SELECT %s::text, COUNT(*), COALESCE(SUM(ai_cost_usd), 0)",
        new="SELECT %s::text, COUNT(*), COALESCE(MAX(ai_cost_usd), 0)",
        why="each bucket's money becomes its most expensive ISSUE instead of its total "
            "(MAX, not SUM(ai_tokens): a type the scan would refuse turns a wrong NUMBER "
            "into a scan error, and a red for the wrong reason is not a caught behaviour)",
    ),
    dict(
        line="124", test="TestGetDistribution_GroupsByPriority",
        regex=r"GROUP BY priority",
        marker="SELECT %s::text, COUNT(*)", fmt=["priority", "priority"],
        old="WHERE workspace_id = $1\n          AND created_at > NOW() - (INTERVAL '1 day' * $2::int)\n        GROUP BY %s",
        new="WHERE workspace_id = $1\n          AND updated_at > NOW() - (INTERVAL '1 day' * $2::int)\n        GROUP BY %s",
        why="the window keys on updated_at, so the cohort is 'touched' rather than 'created'",
    ),
    dict(
        line="151", test="TestGetTimeToResolution_CalculatesMedianCorrectly",
        regex=r"PERCENTILE_CONT\(0\.5\).*PERCENTILE_CONT\(0\.75\).*PERCENTILE_CONT\(0\.95\)",
        marker="PERCENTILE_CONT(0.75)", fmt=[""],
        old="COALESCE(AVG(EXTRACT(EPOCH FROM completed_at - created_at)/3600), 0),",
        new="COALESCE(AVG(EXTRACT(EPOCH FROM completed_at - created_at)/60), 0),",
        why="`avg_hours` is served in MINUTES — 60x, on the field whose name is its unit",
    ),
    dict(
        line="159", test="TestGetTimeToResolution_CalculatesMedianCorrectly",
        regex=r"GROUP BY priority",
        marker="SELECT priority::text,", fmt=[""],
        old="SELECT priority::text,\n            COALESCE(PERCENTILE_CONT(0.5) WITHIN GROUP",
        new="SELECT priority::text,\n            COALESCE(PERCENTILE_CONT(0.95) WITHIN GROUP",
        why="the per-priority MEDIAN becomes a p95 — the slowest tail served as the middle",
    ),
    dict(
        line="187", test="TestGetAICostTrends_ReturnsDailyCostsAndProjection",
        regex=r"SELECT COALESCE\(SUM\(ai_cost_usd\), 0\), COUNT\(\*\)",
        marker="SELECT COALESCE(SUM(ai_cost_usd), 0), COUNT(*)", fmt=[],
        old="SELECT COALESCE(SUM(ai_cost_usd), 0), COUNT(*) FILTER (WHERE ai_cost_usd > 0)",
        new="SELECT COALESCE(SUM(ai_cost_usd), 0), COUNT(*)",
        why="avg_cost_per_issue divides by EVERY issue in the window, not the ones that cost",
    ),
    dict(
        line="191", test="TestGetAICostTrends_ReturnsDailyCostsAndProjection",
        regex=r"date_trunc\('day'",
        marker="GROUP BY day", fmt=[],
        old="SELECT date_trunc('day', updated_at) AS day,",
        new="SELECT date_trunc('day', created_at) AS day,",
        why="the daily series buckets by created_at while the window still filters updated_at",
    ),
    dict(
        line="202", test="TestGetAICostTrends_ReturnsDailyCostsAndProjection",
        regex=r"ORDER BY ai_cost_usd DESC LIMIT 10",
        marker="SELECT id, identifier, title, ai_cost_usd, ai_tokens", fmt=[],
        old="WHERE workspace_id = $1 AND ai_cost_usd > 0",
        new="WHERE workspace_id = $1 AND ai_cost_usd >= 0",
        why="zero-cost issues enter the top-cost leaderboard and pad it out to ten",
    ),
    dict(
        line="207", test="TestGetAICostTrends_ReturnsDailyCostsAndProjection",
        regex=r"JOIN teams t ON t.id = i.team_id",
        marker="JOIN teams t ON t.id = i.team_id", fmt=[],
        old="SELECT t.id, t.name, COALESCE(SUM(i.ai_cost_usd), 0)",
        new="SELECT t.id, t.name, COALESCE(MAX(i.ai_cost_usd), 0)",
        why="cost_by_team reports each team's most expensive ISSUE instead of its total",
    ),
    dict(
        line="212", test="TestGetAICostTrends_ReturnsDailyCostsAndProjection",
        regex=r"UNNEST\(labels\)",
        marker="ORDER BY SUM(ai_cost_usd) DESC LIMIT 20", fmt=[],
        old="SELECT label, COALESCE(SUM(ai_cost_usd), 0)",
        new="SELECT label, COALESCE(MAX(ai_cost_usd), 0)",
        why="cost_by_label reports each label's most expensive ISSUE instead of its total",
    ),
    dict(
        line="245", test="TestGetWorkload_ScansEachRowIntoMemberWorkload",
        regex=r"JOIN members m ON m.id = i.assignee_id",
        marker="JOIN members m ON m.id = i.assignee_id", fmt=[" AND i.team_id = $2"],
        old="COALESCE(SUM(i.ai_cost_usd), 0) AS ai_cost_usd",
        new="COALESCE(MAX(i.ai_cost_usd), 0) AS ai_cost_usd",
        why="a member's AI cost becomes their single most expensive issue, not their total",
    ),
    dict(
        line="271", test="TestExportVelocityCSV_ProducesValidCSV",
        regex=r"FROM cycles c\s+WHERE c.team_id",
        marker="FROM cycles c\n        WHERE c.team_id", fmt=[],
        old="ORDER BY c.number DESC\n        LIMIT $3",
        new="ORDER BY c.number ASC\n        LIMIT $3",
        why="the export ships the team's FIRST n cycles where it promised the LAST n",
    ),
]

# Controls on the instrument. Same shape as a site plus the verdict this script must reach.
CONTROLS = [
    dict(
        name="P0  identity rewrite", expect="CLEAN",
        line="39", test="(none)", regex=r"FROM cycles c\s+WHERE c.team_id",
        marker="FROM cycles c\n        WHERE c.team_id", fmt=[],
        old="ORDER BY c.number DESC", new="ORDER BY c.number DESC",
        why="the anchor replaced by ITSELF — proves 'no new reds' is a state this can report",
    ),
    dict(
        name="P1  velocity workspace scope neutralised", expect="CAUGHT",
        line="39", test="(none)", regex=r"FROM cycles c\s+WHERE c.team_id",
        marker="FROM cycles c\n        WHERE c.team_id", fmt=[],
        old="WHERE c.team_id = $1 AND c.workspace_id = $2",
        new="WHERE c.team_id = $1 AND (c.workspace_id = $2 OR TRUE)",
        why="tenancy off, argument arity untouched — proves a red is reachable, and from a "
            "file that is not engine_test.go",
    ),
    dict(
        name="P2  edit INSIDE a `.*` span", expect="VOID",
        line="151", test="(none)",
        regex=r"PERCENTILE_CONT\(0\.5\).*PERCENTILE_CONT\(0\.75\).*PERCENTILE_CONT\(0\.95\)",
        marker="PERCENTILE_CONT(0.75)", fmt=[""],
        old="COALESCE(PERCENTILE_CONT(0.5)  WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM completed_at - created_at)/3600), 0),",
        new="COALESCE(PERCENTILE_CONT(0.5)  WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM completed_at - created_at)/60), 0),",
        why="the p50 is served in MINUTES — squarely inside the span the `.*` matches, so the "
            "anchor check must refuse to score it",
    ),
]


def probe(original, pristine, item, baseline, run):
    """Apply one mutation, score it, restore. Returns (verdict, new_reds, note)."""
    n = original.count(item["old"])
    if n != 1:
        return "VOID", [], f"mutation anchor occurs {n}x in engine.go, want 1"
    before = matched_bytes(original, item)
    if before is None:
        return "VOID", [], "the expectation regex does not match the SHIPPED statement"
    mutated = original.replace(item["old"], item["new"])
    after = matched_bytes(mutated, item)
    if after != before:
        return "VOID", [], (
            "matched substring MOVED — this mutation is inside the expectation's own match\n"
            f"           before: {before[:110]}\n"
            f"           after : {(after or '(no match)')[:110]}"
        )
    if not run:
        return "ANCHOR-OK", [], "anchor held; not executed"
    try:
        with open(ENGINE, "w", encoding="utf-8") as fh:
            fh.write(mutated)
        _, out = run_tests()
        reds = [t for t in failing_tests(out) if t not in baseline]
    finally:
        with open(ENGINE, "w", encoding="utf-8") as fh:
            fh.write(original)
        back = sha(ENGINE)
        assert back == pristine, f"engine.go NOT restored ({back} != {pristine})"
    return ("CAUGHT" if reds else "BLIND"), reds, ""


def main():
    pristine = sha(ENGINE)
    with open(ENGINE, encoding="utf-8") as fh:
        original = fh.read()
    with open(TESTFILE, encoding="utf-8") as fh:
        tf = fh.read()

    print("=" * 78)
    print("POPULATION — recounted here rather than inherited from the (A) census")
    print(f"  engine_test.go  'ExpectQuery('       occurrences : {tf.count('ExpectQuery(')}")
    print(f"  engine_test.go  'pool.ExpectQuery('  CALL SITES  : {tf.count('pool.ExpectQuery(')}")
    print(f"  sites probed here                                : {len(SITES)}")
    print(f"  packages that can see engine.go                  : {' '.join(PACKAGES)}")
    print("=" * 78)

    code, out = run_tests()
    baseline = failing_tests(out)
    print(f"\nC0 BASELINE: exit={code}  pre-existing failures={len(baseline)}")
    for t in baseline:
        print(f"     {t}")
    print("   (empty-but-present corpus dirs; see the header. Every verdict below is the "
          "DIFFERENCE against this set.)")

    print("\n" + "=" * 78)
    print("CONTROLS ON THE INSTRUMENT")
    print("=" * 78)
    ctl_ok = True
    for c in CONTROLS:
        verdict, reds, note = probe(original, pristine, c, baseline, run=True)
        if c["expect"] == "CLEAN":
            got_ok = verdict == "BLIND" and not reds
            shown = "CLEAN" if got_ok else verdict
        else:
            got_ok = verdict == c["expect"]
            shown = verdict
        ctl_ok = ctl_ok and got_ok
        print(f"\n  {c['name']}")
        print(f"    {c['why']}")
        print(f"    predicted {c['expect']:<7s} got {shown:<7s} {'OK' if got_ok else '!! CONTROL FAILED'}")
        if note:
            print(f"           {note}")
        if reds:
            print(f"    new reds: {', '.join(reds)}")
    if not ctl_ok:
        print("\n!! AN INSTRUMENT CONTROL FAILED — no census verdict below is worth reading.")
        return 1

    print("\n" + "=" * 78)
    print("CENSUS — behaviour changed, matched substring byte-identical")
    print("=" * 78)
    results = []
    for s in SITES:
        verdict, reds, note = probe(original, pristine, s, baseline, run=True)
        print(f"\n=== engine_test.go:{s['line']}  {s['regex']}")
        print(f"    {s['test']}")
        print(f"    mutation: {s['why']}")
        print(f"    -> {verdict}")
        if note:
            print(f"       {note}")
        if reds:
            for t in reds:
                print(f"       red: {t}")
        results.append((s, verdict, reds))

    print("\n" + "=" * 78)
    print("SUMMARY")
    blind = [s for s, v, _ in results if v == "BLIND"]
    for s, v, reds in results:
        tail = f"  <- {', '.join(reds)}" if reds else ""
        print(f"  engine_test.go:{s['line']:<5s} {v:<7s}{tail}")
    print(f"\n  {len(blind)} of {len(results)} sites are BLIND: the statement can be made to "
          f"answer a different\n  question and nothing in this repository reds.")
    for s in blind:
        print(f"    · engine_test.go:{s['line']}  {s['why']}")
    print(f"\nengine.go sha256 (final) = {sha(ENGINE)}  (pristine = {pristine})")
    return 0


if __name__ == "__main__":
    sys.exit(main())
