#!/usr/bin/env python3
"""w34-analytics-scope-wiring-controls-2q7v.py — is every workspace-scope predicate in
internal/analytics/engine.go actually PINNED by a test, or only present?

THE QUESTION. engine.go carries TWELVE `workspace_id = $N` predicates across seven reports.
`scope_read_test.go` is the cross-workspace leak sweep and it drives FIVE of the seven HTTP
read endpoints (Velocity, Burndown, Resolution, AICosts, Workload) — `Distribution` and
`Export` are in no leak test at all. A comment in engine_test.go:87 asserts that the
distribution scope is nevertheless covered ("**the workspace scope neutralised** ... Those
rules are asserted against real Postgres in distribution_counting_realpg_test.go"). This
script asks that as a MEASUREMENT instead of reading it: neutralise ONE predicate at a time
and see whether ANY test in the repository turns red.

THE NEUTRALISATION IS `x = $N` -> `(x = $N OR TRUE)`, chosen for three properties:
  · the parameter stays REFERENCED, so pgx still binds N args and no arg-count mismatch can
    masquerade as a caught defect;
  · it is a pure widening — the cohort grows to every workspace, which is exactly the leak;
  · it does NOT disturb any `ExpectQuery` regex in engine_test.go. Each of the ten
    fingerprints (`GROUP BY status`, `JOIN teams t ON t.id = i.team_id`, `UNNEST(labels)`,
    `PERCENTILE_CONT(0.5).*`, `ORDER BY ai_cost_usd DESC LIMIT 10`, ...) names text that this
    edit leaves byte-identical. That matters: a CAUGHT verdict here is a COHORT assertion
    firing, never a query-text mismatch. engine_test.go:32 records that two earlier spellings
    of a scope control were discarded for exactly that confusion.

SCORING IS SET SUBTRACTION, NEVER AN EXIT CODE. C0 measures the failing set on a pristine
tree; a mutation is CAUGHT only if it adds a failure C0 did not already have. An exit code
would score every mutation CAUGHT on any machine where some unrelated package is already red
(tab-6c1a measured exactly that trap in ./internal/importer/), and a test's NAME proves
nothing about what it reached.

TWO CONTROLS ON THE INSTRUMENT ITSELF:
  · VOID — an arithmetically identity edit (`= $1` -> `= $1 AND TRUE`). MUST be NOT CAUGHT.
    If the harness reports it caught, the harness is reporting the edit, not the defect.
  · BUILD — a mutation that fails to compile is recorded as BROKEN, never as CAUGHT. A
    compile error reds every package and is the cheapest way to fake total coverage.
"""

import hashlib
import io
import re
import subprocess
import sys

REPO = "/Users/ng/talyvor-track"
ENGINE = REPO + "/internal/analytics/engine.go"
DSN = "postgres://postgres:postgres@localhost:55442/postgres?sslmode=disable"

# (id, 1-based line, exact old text, new text, report it scopes, PRE-MERGE prediction)
# PRE-MERGE is the state MEASURED before this session's fix, so a later run on a healthy tree
# does not print "prediction wrong" for every site that was since guarded (tab-6c1a's trap).
MUTATIONS = [
    ("S1-velocity", 133,
     "WHERE c.team_id = $1 AND c.workspace_id = $2",
     "WHERE c.team_id = $1 AND (c.workspace_id = $2 OR TRUE)", "GetVelocity", "CAUGHT"),
    ("S2-burndown", 188,
     "FROM cycles c WHERE c.id = $1 AND c.workspace_id = $2`,",
     "FROM cycles c WHERE c.id = $1 AND (c.workspace_id = $2 OR TRUE)`,", "GetBurndown", "CAUGHT"),
    ("S3-distribution-main", 318,
     "WHERE workspace_id = $1",
     "WHERE (workspace_id = $1 OR TRUE)", "GetDistribution status/priority", "?"),
    ("S4-distribution-label", 331,
     "WHERE workspace_id = $1",
     "WHERE (workspace_id = $1 OR TRUE)", "distributionByLabel", "?"),
    ("S5-resolution-global", 427,
     "WHERE workspace_id = $1",
     "WHERE (workspace_id = $1 OR TRUE)", "GetTimeToResolution global", "CAUGHT"),
    ("S6-resolution-priority", 443,
     "WHERE workspace_id = $1",
     "WHERE (workspace_id = $1 OR TRUE)", "GetTimeToResolution by-priority", "?"),
    ("S7-aicost-totals", 543,
     "WHERE workspace_id = $1",
     "WHERE (workspace_id = $1 OR TRUE)", "GetAICostTrends totals", "CAUGHT"),
    ("S8-aicost-daily", 565,
     "WHERE workspace_id = $1",
     "WHERE (workspace_id = $1 OR TRUE)", "GetAICostTrends daily series", "?"),
    ("S9-aicost-top", 608,
     "WHERE workspace_id = $1 AND ai_cost_usd > 0",
     "WHERE (workspace_id = $1 OR TRUE) AND ai_cost_usd > 0", "GetAICostTrends leaderboard", "CAUGHT"),
    ("S10-aicost-team", 631,
     "WHERE i.workspace_id = $1",
     "WHERE (i.workspace_id = $1 OR TRUE)", "GetAICostTrends by team", "CAUGHT"),
    ("S11-aicost-label", 656,
     "WHERE workspace_id = $1",
     "WHERE (workspace_id = $1 OR TRUE)", "GetAICostTrends by label", "CAUGHT"),
    ("S12-workload", 715,
     "WHERE i.workspace_id = $1%s",
     "WHERE (i.workspace_id = $1 OR TRUE)%s", "GetWorkload", "CAUGHT"),
    # ---- instrument controls ----
    ("VOID-identity", 318,
     "WHERE workspace_id = $1",
     "WHERE workspace_id = $1 AND TRUE", "GetDistribution (identity edit)", "NOT CAUGHT"),
]


def sha256(path):
    return hashlib.sha256(io.open(path, "rb").read()).hexdigest()


def run_suite():
    """Return (failed_set, build_broken). failed_set holds FAIL'd packages and --- FAIL names."""
    p = subprocess.run(
        ["go", "test", "-count=1", "./..."], cwd=REPO, capture_output=True, text=True,
        env={**__import__("os").environ, "TRACK_TEST_DATABASE_URL": DSN},
    )
    out = p.stdout + p.stderr
    build_broken = ("[build failed]" in out) or bool(re.search(r"^# github\.com/", out, re.M))
    failed = set()
    for m in re.finditer(r"^FAIL\s+(\S+)", out, re.M):
        failed.add("PKG " + m.group(1))
    for m in re.finditer(r"^\s*--- FAIL: (\S+)", out, re.M):
        failed.add("TEST " + m.group(1))
    return failed, build_broken


def apply_mutation(line_no, old, new):
    lines = io.open(ENGINE, encoding="utf-8").read().split("\n")
    idx = line_no - 1
    if old not in lines[idx]:
        raise SystemExit(f"ANCHOR LOST at line {line_no}: expected {old!r} in {lines[idx]!r}")
    lines[idx] = lines[idx].replace(old, new, 1)
    io.open(ENGINE, "w", encoding="utf-8").write("\n".join(lines))


def main():
    pristine = io.open(ENGINE, encoding="utf-8").read()
    pristine_sha = sha256(ENGINE)
    print(f"engine.go pristine sha256 = {pristine_sha}")

    print("\nC0: pristine tree, full repo ...")
    c0, c0_broken = run_suite()
    if c0_broken:
        raise SystemExit("C0 DOES NOT BUILD — fix the tree before measuring anything")
    print(f"C0 failing set: {sorted(c0) if c0 else 'EMPTY (clean tree)'}")

    results = []
    try:
        for mid, line_no, old, new, report, predicted in MUTATIONS:
            apply_mutation(line_no, old, new)
            failed, broken = run_suite()
            io.open(ENGINE, "w", encoding="utf-8").write(pristine)
            if sha256(ENGINE) != pristine_sha:
                raise SystemExit(f"RESTORE FAILED after {mid}")

            added = failed - c0
            if broken:
                verdict = "BROKEN(build)"
            elif added:
                verdict = "CAUGHT"
            else:
                verdict = "NOT CAUGHT"
            results.append((mid, report, predicted, verdict, sorted(added)))
            catchers = ", ".join(a for a in sorted(added) if a.startswith("TEST "))[:150] or "-"
            print(f"  {mid:<24} {verdict:<14} pre-merge:{predicted:<11} by: {catchers}")
    finally:
        io.open(ENGINE, "w", encoding="utf-8").write(pristine)
        final = sha256(ENGINE)
        print(f"\nrestored engine.go sha256 = {final} "
              f"({'OK' if final == pristine_sha else 'MISMATCH!!'})")
        if final != pristine_sha:
            sys.exit(2)

    print("\n" + "=" * 78)
    void = [r for r in results if r[0].startswith("VOID")]
    unpinned = [r for r in results if not r[0].startswith("VOID") and r[3] == "NOT CAUGHT"]
    broken = [r for r in results if r[3].startswith("BROKEN")]
    for mid, report, predicted, verdict, added in results:
        print(f"{mid:<24} {report:<34} {verdict}")
    print("=" * 78)

    bad = False
    for mid, report, predicted, verdict, added in void:
        if verdict != "NOT CAUGHT":
            print(f"INSTRUMENT INVALID: {mid} was scored {verdict}; an identity edit must be "
                  f"NOT CAUGHT. Added: {added}")
            bad = True
    if broken:
        print(f"BROKEN MUTATIONS (not scored): {[b[0] for b in broken]}")
        bad = True
    if unpinned:
        print(f"UNPINNED SCOPE PREDICATES ({len(unpinned)}): "
              f"{[(u[0], u[1]) for u in unpinned]}")
        print("Each is a workspace-scope predicate that can be neutralised with the ENTIRE "
              "repository test suite green.")
        bad = True
    else:
        print("Every workspace-scope predicate in engine.go is pinned by at least one test.")
    sys.exit(1 if bad else 0)


if __name__ == "__main__":
    main()
