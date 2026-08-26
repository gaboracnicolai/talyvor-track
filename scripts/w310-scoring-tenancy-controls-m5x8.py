#!/usr/bin/env python3
"""Positive controls for W3.10 — the scoring store's workspace scopes.

WHY THIS HARNESS IS THE WHOLE ITEM
    tenancy_scope_realpg_test.go PASSES ON MAIN. The predicates it guards are correct today,
    so the file cannot prove its own worth by going red — only these arms can. If they ever
    stop firing, that file is decoration and should be deleted rather than trusted.

EVERY MUTATION IS ARITY-PRESERVING ON PURPOSE.
    W3.9's C5 taught this. Deleting `AND workspace_id = $2` and leaving the argument is NOT a
    realistic defect: Postgres refuses the surplus bind parameter outright ("Expected 1
    parameters but got 2"), so it could never ship. Deleting the argument TOO is caught, but
    only by pgxmock's argument-COUNT check — an arity assertion, not a tenancy one. Rewriting
    the predicate to `($2 = $2)` keeps the arity, runs fine on real Postgres, and removes the
    scope. That is the shape the existing coverage could not see, so that is the shape used
    here. An arm that could not ship does not measure a guard.

PREDICTED BEFORE THE RUN.

    T1  GetScore scope inert                  -> "GetScore does not disclose it" ONLY
    T2  DeleteScore scope inert               -> "DeleteScore leaves the row where it is" ONLY
    T3  GetPrioritizedBacklog scope inert     -> "GetPrioritizedBacklog never lists it" ONLY
    T4  summary total_scored scope inert      -> my summary subtest AND W3.9's populated test.
                                                 Recorded as caught by BOTH rather than claimed,
                                                 because a shared catch justifies neither alone.
    T5  SetScore loses AssertRefInWorkspace   -> NONE of my Go tests. SEMGREP must catch it.
                                                 This arm checks the EXISTING tenancy-lock is
                                                 still live, and proves this file is specific.
    T6  IssueScores GAINS a workspace scope   -> the CHARACTERISATION test. Without this arm a
                                                 test that asserts today's behaviour cannot be
                                                 shown to detect the change it characterises.
    T7  VOID — comment text only              -> nothing may catch it.

DISCIPLINE
    - Refuses on a dirty target file or a suite that is not already green.
    - Every mutation asserts it CHANGED THE BYTES; a drifted anchor stops the run rather than
      scoring NOT-CAUGHT for a defect never introduced.
    - A build/vet failure scores VOID, never CAUGHT.
    - The classifier REFUSES when '--- FAIL:' is present but no name parses.
    - Restored in a finally; sha256 verified identical at the end.
"""
import hashlib
import os
import re
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
STORE = os.path.join(ROOT, "internal/scoring/store.go")
DBURL = os.environ.get("TRACK_TEST_DATABASE_URL")
if not DBURL:
    sys.exit("TRACK_TEST_DATABASE_URL is required — these are real-Postgres controls.")

MINE = "TestMeasured_ScoringStore_RefusesAnotherWorkspacesIssue|TestMeasured_ScoringStore_AllowsItsOwnWorkspace|TestCharacterised_IssueScores_ReadsByBareIDWithNoWorkspaceScope"
MINE_PREFIXES = ("TestMeasured_ScoringStore_", "TestCharacterised_IssueScores_")


def sha(p):
    return hashlib.sha256(open(p, "rb").read()).hexdigest()


def gotest(pattern=None):
    cmd = ["go", "test", "-count=1", "-v"]
    if pattern:
        cmd += ["-run", pattern]
    cmd += ["./internal/scoring/"]
    p = subprocess.run(cmd, cwd=ROOT, env=dict(os.environ, TRACK_TEST_DATABASE_URL=DBURL),
                       capture_output=True, text=True)
    out = p.stdout + p.stderr
    if "[build failed]" in out or re.search(r"^# ", out, re.M):
        return False, set()
    names = set(re.findall(r"^\s*--- FAIL: (\S+)", out, re.M))
    if "--- FAIL:" in out and not names:
        sys.exit("CLASSIFIER REFUSES: '--- FAIL:' present but no test name parsed.\n" + out[-2500:])
    return True, names


def semgrep_findings():
    p = subprocess.run(["semgrep", "scan", "--config", ".semgrep/", "--error", "--metrics=off",
                        "internal/", "cmd/"], cwd=ROOT, capture_output=True, text=True)
    m = re.search(r"Ran \d+ rules on \d+ files: (\d+) findings", p.stdout + p.stderr)
    return int(m.group(1)) if m else -1


MUTATIONS = [
    ("T1  GetScore scope inert",
     "`SELECT `+scoreColumns+` FROM issue_scores WHERE issue_id = $1 AND workspace_id = $2`",
     "`SELECT `+scoreColumns+` FROM issue_scores WHERE issue_id = $1 AND ($2 = $2)`",
     {"TestMeasured_ScoringStore_RefusesAnotherWorkspacesIssue/GetScore_does_not_disclose_it"}, None),

    ("T2  DeleteScore scope inert",
     "`DELETE FROM issue_scores WHERE issue_id = $1 AND workspace_id = $2`",
     "`DELETE FROM issue_scores WHERE issue_id = $1 AND ($2 = $2)`",
     {"TestMeasured_ScoringStore_RefusesAnotherWorkspacesIssue/DeleteScore_leaves_the_row_where_it_is"}, None),

    ("T3  GetPrioritizedBacklog scope inert",
     "WHERE i.workspace_id = $1 AND i.status NOT IN ('cancelled')",
     "WHERE ($1 = $1) AND i.status NOT IN ('cancelled')",
     {"TestMeasured_ScoringStore_RefusesAnotherWorkspacesIssue/GetPrioritizedBacklog_never_lists_it"}, None),

    ("T4  summary total_scored scope inert",
     "(SELECT COUNT(*) FROM issue_scores WHERE workspace_id = $1) AS total_scored",
     "(SELECT COUNT(*) FROM issue_scores WHERE ($1 = $1)) AS total_scored",
     {"TestMeasured_ScoringStore_RefusesAnotherWorkspacesIssue/GetScoreSummary_counts_only_its_own"}, None),

    ("T5  SetScore loses AssertRefInWorkspace",
     '\tif err := tenancy.AssertRefInWorkspace(ctx, s.pool, "issues", issueID, workspaceID); err != nil {\n\t\treturn nil, err\n\t}\n',
     '\t_ = tenancy.AssertRefInWorkspace\n',
     set(), "semgrep"),

    ("T6  IssueScores GAINS a workspace scope",
     "`SELECT rice_score, ice_score FROM issue_scores WHERE issue_id = $1`",
     "`SELECT rice_score, ice_score FROM issue_scores WHERE issue_id = $1 AND workspace_id = 'nope'`",
     {"TestCharacterised_IssueScores_ReadsByBareIDWithNoWorkspaceScope"}, None),

    ("T7  VOID — comment text only",
     "// SEC-5: scoped to the caller's workspace — a foreign issue's score is never disclosed.",
     "// SEC-5: scoped to the caller's workspace (comment reworded by the VOID control arm).",
     set(), None),
]

if subprocess.run(["git", "diff", "--quiet", "--", STORE], cwd=ROOT).returncode != 0:
    sys.exit("REFUSING: internal/scoring/store.go already has uncommitted changes.")
BEFORE = sha(STORE)

print("BASELINE — scoring package green and semgrep clean before any mutation.")
ok, failed = gotest()
base_sg = semgrep_findings()
if not ok or failed or base_sg != 0:
    sys.exit(f"REFUSING: baseline (build_ok={ok}, failed={sorted(failed)}, semgrep={base_sg}).")
print("  baseline green, semgrep 0 findings\n")

orig = open(STORE).read()
results = []
try:
    for label, anchor, repl, predicted, tool in MUTATIONS:
        src = open(STORE).read()
        if src.count(anchor) != 1:
            sys.exit(f"ANCHOR DRIFT on {label}: {src.count(anchor)} occurrences, want 1.")
        open(STORE, "w").write(src.replace(anchor, repl))
        assert open(STORE).read() != orig, f"{label}: changed no bytes"

        build_ok, mine_failed_all = gotest(MINE)
        if not build_ok:
            results.append((label, "VOID (build failed)", predicted, set(), set(), None))
            open(STORE, "w").write(orig)
            continue
        mine = {n for n in mine_failed_all if n.startswith(MINE_PREFIXES)}
        # A parent test reports FAIL when a subtest does; keep only the most specific names.
        mine = {n for n in mine if not any(o != n and o.startswith(n + "/") for o in mine)}
        _, all_failed = gotest()
        others = {n for n in all_failed if not n.startswith(MINE_PREFIXES)}
        sg = semgrep_findings() if tool == "semgrep" else None

        ok_pred = (mine == predicted) and (sg is None or sg > 0)
        results.append((label, "AS PREDICTED" if ok_pred else "*** MISPREDICTED ***",
                        predicted, mine, others, sg))
        open(STORE, "w").write(orig)
finally:
    open(STORE, "w").write(orig)

print("\n" + "=" * 80)
bad = 0
for label, verdict, predicted, mine, others, sg in results:
    if verdict != "AS PREDICTED":
        bad += 1
    print(f"{label:<42} {verdict}")
    print(f"{'':<42} mine:        {sorted(mine) or '(none)'}")
    print(f"{'':<42} pre-existing: {sorted(others) or '(none)'}")
    if sg is not None:
        print(f"{'':<42} semgrep findings: {sg}")
print("=" * 80)

after = sha(STORE)
print(f"sha256 restored identical: {after == BEFORE}")
if after != BEFORE:
    sys.exit("FILE NOT RESTORED")
ok, failed = gotest()
print(f"suite green after restore: {ok and not failed}")
print(f"semgrep after restore: {semgrep_findings()} findings")
print(f"\n{len(results) - bad}/{len(results)} as predicted")
sys.exit(1 if bad else 0)
