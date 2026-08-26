#!/usr/bin/env python3
"""Positive controls for W3.9 — the scoring summary's empty-population scan.

WHAT IS BEING CONTROLLED
    summary_empty_workspace_realpg_test.go has two arms and they must be able to fail for
    DIFFERENT reasons, or the file is one assertion wearing two names:

      ..._OnAWorkspaceThatHasNeverBeenScored   the RED on main — an unscored workspace
      ..._OnAPopulatedWorkspace                the must-stay-green control — real numbers

    C2 is the arm that matters most. A "fix" that hard-codes top_issue_id to '' makes the
    empty test green and is WRONG, and the only thing that can say so is the populated arm.
    Without C2 this file would accept a fix that deletes the feature.

PREDICTED BEFORE THE RUN. Catchers are disjoint except where stated.

    C1  COALESCE moved back INSIDE the subquery      -> EMPTY red      POPULATED green
    C2  top_issue_id hard-coded to ''                -> EMPTY green    POPULATED red
    C3  workspace scope dropped from top_issue_id    -> EMPTY green    POPULATED red
    C4  COALESCE dropped from AVG(rice_score)        -> EMPTY red      POPULATED green
    C5  GetScore loses its workspace predicate       -> NEITHER of mine; the PRE-EXISTING
                                                       package tests must catch it. This is
                                                       the specificity arm: it proves the new
                                                       file is not a catch-all.
    C6  VOID — a pure Go local rename, no behaviour  -> NOTHING may catch it.

    C3 and C4 are NOT redundant with C1: C1 is the shipped defect, C4 is the SIBLING shape
    that is correct today (an aggregate's COALESCE) and would break the same test — if C4
    were NOT caught, the empty test would be reading only one of the five columns.

DISCIPLINE
    - Refuses to start on a dirty target file or on a suite that is not already green.
    - Every mutation asserts it CHANGED THE BYTES. A drifted anchor stops the run rather
      than scoring NOT-CAUGHT for a defect that was never introduced.
    - A BUILD/VET failure scores VOID, never CAUGHT. An exit code is not an assertion.
    - The classifier REFUSES when '--- FAIL:' is present but no test name parses, rather
      than defaulting to an empty set (W3.5's harness scored 0/5 that way).
    - Restored in a finally; sha256 of every touched file verified identical at the end.
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

MINE = ("TestMeasured_ScoreSummary_OnAWorkspaceThatHasNeverBeenScored",
        "TestMeasured_ScoreSummary_OnAPopulatedWorkspace")


def sha(p):
    return hashlib.sha256(open(p, "rb").read()).hexdigest()


def run(pattern=None):
    """Returns (build_ok, failed_test_names)."""
    cmd = ["go", "test", "-count=1", "-v"]
    if pattern:
        cmd += ["-run", pattern]
    cmd += ["./internal/scoring/"]
    env = dict(os.environ, TRACK_TEST_DATABASE_URL=DBURL)
    p = subprocess.run(cmd, cwd=ROOT, env=env, capture_output=True, text=True)
    out = p.stdout + p.stderr
    if "build failed" in out or "[build failed]" in out or re.search(r"^# ", out, re.M):
        return False, set()
    names = set(re.findall(r"^\s*--- FAIL: (\S+)", out, re.M))
    if "--- FAIL:" in out and not names:
        sys.exit("CLASSIFIER REFUSES: '--- FAIL:' present but no test name parsed.\n" + out[-3000:])
    return True, names


MUTATIONS = [
    ("C1  COALESCE back INSIDE the subquery", STORE,
     "COALESCE((SELECT issue_id FROM issue_scores WHERE workspace_id = $1\n                ORDER BY GREATEST(COALESCE(rice_score, 0), COALESCE(ice_score, 0)) DESC LIMIT 1), '') AS top_issue_id",
     "(SELECT COALESCE(issue_id, '') FROM issue_scores WHERE workspace_id = $1\n                ORDER BY GREATEST(COALESCE(rice_score, 0), COALESCE(ice_score, 0)) DESC LIMIT 1) AS top_issue_id",
     {MINE[0]}),

    ("C2  top_issue_id hard-coded to ''", STORE,
     "COALESCE((SELECT issue_id FROM issue_scores WHERE workspace_id = $1\n                ORDER BY GREATEST(COALESCE(rice_score, 0), COALESCE(ice_score, 0)) DESC LIMIT 1), '') AS top_issue_id",
     "'' AS top_issue_id",
     {MINE[1]}),

    ("C3  workspace scope dropped from top_issue_id", STORE,
     "COALESCE((SELECT issue_id FROM issue_scores WHERE workspace_id = $1\n                ORDER BY GREATEST",
     "COALESCE((SELECT issue_id FROM issue_scores WHERE workspace_id = ANY(ARRAY[$1, workspace_id])\n                ORDER BY GREATEST",
     {MINE[1]}),

    ("C4  COALESCE dropped from AVG(rice_score)", STORE,
     "(SELECT COALESCE(AVG(rice_score), 0) FROM issue_scores",
     "(SELECT AVG(rice_score) FROM issue_scores",
     {MINE[0]}),

    ("C5  GetScore loses its workspace predicate", STORE,
     "`SELECT `+scoreColumns+` FROM issue_scores WHERE issue_id = $1 AND workspace_id = $2`",
     "`SELECT `+scoreColumns+` FROM issue_scores WHERE issue_id = $1`",
     set()),

    ("C6  VOID — pure local rename", STORE,
     "topIssueID               string",
     "topIssueIdent            string",
     set()),
]

BEFORE = {STORE: sha(STORE)}
if subprocess.run(["git", "diff", "--quiet", "--", STORE], cwd=ROOT).returncode != 0:
    sys.exit("REFUSING: internal/scoring/store.go already has uncommitted changes.")

print("BASELINE — the whole scoring package must be green before any mutation.")
ok, failed = run()
if not ok or failed:
    sys.exit(f"REFUSING: baseline not green (build_ok={ok}, failed={sorted(failed)}).")
print("  baseline green\n")

results, orig = [], open(STORE).read()
try:
    for label, path, anchor, repl, predicted in MUTATIONS:
        src = open(path).read()
        if src.count(anchor) != 1:
            sys.exit(f"ANCHOR DRIFT on {label}: found {src.count(anchor)} occurrences, want 1.")
        mutated = src.replace(anchor, repl)
        assert mutated != src, f"{label}: mutation changed no bytes"
        open(path, "w").write(mutated)
        # C6 renames a declaration; fix its one use so the arm tests behaviour, not the compiler.
        if label.startswith("C6"):
            s2 = open(path).read().replace("&avgIce, &topIssueID)", "&avgIce, &topIssueIdent)") \
                                   .replace("strings.TrimSpace(topIssueID)", "strings.TrimSpace(topIssueIdent)")
            open(path, "w").write(s2)

        build_ok, mine_failed = run("|".join(MINE))
        if not build_ok:
            results.append((label, "VOID (build failed)", predicted, set()))
            open(path, "w").write(orig)
            continue
        _, all_failed = run()
        others = all_failed - set(MINE)
        verdict = "AS PREDICTED" if mine_failed == predicted else "*** MISPREDICTED ***"
        results.append((label, verdict, predicted, mine_failed, others))
        open(path, "w").write(orig)
finally:
    open(STORE, "w").write(orig)

print("\n" + "=" * 78)
bad = 0
for r in results:
    label, verdict = r[0], r[1]
    if verdict != "AS PREDICTED":
        bad += 1
    print(f"{label:<48} {verdict}")
    if len(r) == 5:
        print(f"{'':<48} mine caught: {sorted(r[3]) or '(none)'}")
        print(f"{'':<48} pre-existing caught: {sorted(r[4]) or '(none)'}")
print("=" * 78)

after = sha(STORE)
print(f"sha256 restored identical: {after == BEFORE[STORE]}")
if after != BEFORE[STORE]:
    sys.exit("FILE NOT RESTORED")
ok, failed = run()
print(f"suite green after restore: {ok and not failed}")
print(f"\n{len(results) - bad}/{len(results)} as predicted")
sys.exit(1 if bad else 0)
