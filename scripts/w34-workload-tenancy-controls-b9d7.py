#!/usr/bin/env python3
"""Positive controls for TestAnalytics_Workload_WorkspaceScoped (W3.4, tab-b9d7).

The guard this file controls PASSED ON ITS FIRST RUN, because the shipped SQL is already
correct: `analytics.GetWorkload` really does carry `WHERE i.workspace_id = $1`. Everything
that makes the new test a guard rather than a decoration is therefore in these controls —
one term of the shipped statement mutated at a time, each run over the WHOLE analytics
package, each restored in a `finally` with sha256 verified against the pristine file.

Each control names its predicted verdict BEFORE it runs. The runner reports every test
that failed, not only the predicted one, so "caught by nothing else" is measured rather
than assumed — a control that scores CAUGHT by a test I did not predict is a wrong
prediction and is printed as one.

  C1  the workspace scope neutralised          -> CAUGHT by [W-DEPUTY] + [W-TENANCY]
  C2  the workspace scope broken the other way -> CAUGHT by the POSITIVE halves
  C3  ` AND i.team_id` -> ` OR i.team_id`      -> CAUGHT by [W-DEPUTY] alone (in this file)
  C4  the team scope neutralised               -> NOT caught by the new test (must-stay-green)
  C5  `$1 = i.workspace_id` (identical)        -> CAUGHT BY NOTHING (void control)
  C6  scope only on the team-named path        -> CAUGHT by [W-TENANCY] alone (in this file)
  C7  ` AND i.team_id <> $2`                   -> CAUGHT by [W-OWN-TEAM] alone (in this file)
  C8  the no-team branch answers nothing       -> CAUGHT by [W-OWN] alone (in this file)

C6/C7/C8 exist because the FIRST run of C1 and C2 came back with a prediction miss each:
the test fataled on its first failing check, so `[W-TENANCY]` and `[W-OWN-TEAM]` were never
evaluated. Those checks are Errorf now, and these three are the mutations that reach each
one on its own — an assertion no control can reach is justified by nothing.

⚠ THE PREDICTIONS ABOVE ARE LEFT AS THEY WERE WRITTEN, INCLUDING THE TWO THAT WERE WRONG.
C1 and C6 both ALSO red [W-OWN] — a leak into the unscoped call breaks the "exactly one
row" count as well as the canary — so their real catcher lists are longer than predicted.
Under-listing is the direction that makes a catch-all look precise, so it is recorded here
rather than corrected in place. The consequence is stated in scope_read_test.go's header:
[W-TENANCY] has NO mutation that reds it alone, and the file says so.

C1's spelling is #153's: `= ANY(ARRAY[$1, i.workspace_id])` leaves the substring a pgxmock
ExpectQuery regex matches alone, so a red can only be an assertion rather than a moved
bracket. C5 exists for the same reason from the other side: a semantically identical
rewrite must red nothing, or C1's "not caught" would be confounded by text matching.

Usage:  TRACK_TEST_DATABASE_URL=... python3 scripts/w34-workload-tenancy-controls-b9d7.py
"""

import hashlib
import os
import re
import subprocess
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
TARGET = os.path.join(REPO, "internal", "analytics", "engine.go")

SCOPE = "        WHERE i.workspace_id = $1%s"
TEAM = '\t\tteamSQL = " AND i.team_id = $2"'

CONTROLS = [
    (
        "C1",
        "the workspace scope neutralised — every tenant's workload, to any caller",
        SCOPE,
        "        WHERE i.workspace_id = ANY(ARRAY[$1, i.workspace_id])%s",
        "CAUGHT",
        ["W-DEPUTY", "W-TENANCY"],
    ),
    (
        "C2",
        "the workspace scope broken the other way — the caller's OWN rows vanish",
        SCOPE,
        "        WHERE i.workspace_id = ($1 || '-nope')%s",
        "CAUGHT",
        ["W-OWN", "W-OWN-TEAM"],
    ),
    (
        "C3",
        "` AND i.team_id` -> ` OR i.team_id` — the caller-supplied team widens past the workspace",
        TEAM,
        '\t\tteamSQL = " OR i.team_id = $2"',
        "CAUGHT",
        ["W-DEPUTY"],
    ),
    (
        "C4",
        "the team scope neutralised — must stay green in the NEW test, red in the counting test",
        TEAM,
        '\t\tteamSQL = " AND i.team_id = ANY(ARRAY[$2, i.team_id])"',
        "CAUGHT elsewhere / NOT CAUGHT here",
        [],
    ),
    (
        "C5",
        "`$1 = i.workspace_id` — semantically identical, must red NOTHING",
        SCOPE,
        "        WHERE $1 = i.workspace_id%s",
        "NOT CAUGHT",
        [],
    ),
    (
        "C7",
        "` AND i.team_id = $2` -> ` <> $2` — the team-named path returns everyone ELSE",
        TEAM,
        '\t\tteamSQL = " AND i.team_id <> $2"',
        "CAUGHT",
        ["W-OWN-TEAM"],
    ),
    (
        "C8",
        "the no-team branch answers nothing — the unscoped call's positive half alone",
        '\tteamSQL := ""',
        '\tteamSQL := " AND FALSE"',
        "CAUGHT",
        ["W-OWN"],
    ),
]

# C6 is the only control that cannot be spelled as one substring swap: separating
# `[W-TENANCY]` from `[W-DEPUTY]` requires the workspace scope to survive on the
# team-named path and NOT on the unscoped one, and the shipped code builds one predicate
# for both. It is two coordinated edits of the same statement and is applied as a pair.
C6 = (
    "C6",
    "the scope survives only when a team is named — the unscoped call loses it",
    [
        (
            "\targs := []any{workspaceID}\n\tteamSQL := \"\"\n\tif teamID != \"\" {\n\t\targs = append(args, teamID)\n\t\tteamSQL = \" AND i.team_id = $2\"\n\t}",
            "\targs := []any{workspaceID}\n\tteamSQL := \"\"\n\twsPred := \"i.workspace_id = ANY(ARRAY[$1, i.workspace_id])\"\n\tif teamID != \"\" {\n\t\targs = append(args, teamID)\n\t\tteamSQL = \" AND i.team_id = $2\"\n\t\twsPred = \"i.workspace_id = $1\"\n\t}",
        ),
        (SCOPE, "        WHERE %s%s"),
        (
            '        ORDER BY open_issues DESC`, teamSQL),',
            '        ORDER BY open_issues DESC`, wsPred, teamSQL),',
        ),
    ],
    "CAUGHT",
    ["W-TENANCY"],
)

FAIL_RE = re.compile(r"^\s*--- FAIL: (\S+)", re.M)
TAG_RE = re.compile(r"\[([A-Z][A-Z0-9-]+)\]")


def sha256(path):
    with open(path, "rb") as fh:
        return hashlib.sha256(fh.read()).hexdigest()


def run_package():
    proc = subprocess.run(
        ["go", "test", "-timeout", "300s", "-race", "-count=1", "-v", "./internal/analytics/"],
        cwd=REPO,
        capture_output=True,
        text=True,
    )
    out = proc.stdout + proc.stderr
    failed = sorted(set(FAIL_RE.findall(out)))
    # Tags only ever reach stdout from a t.Errorf/t.Fatalf message, so the set of tags in the
    # output IS the set of assertions that fired.
    tags = sorted(set(TAG_RE.findall(out)))
    return proc.returncode, failed, tags, out


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
    single = [(n, d, [(o, w)], p, tg) for n, d, o, w, p, tg in CONTROLS]
    plan = single[:5] + [(C6[0], C6[1], C6[2], C6[3], C6[4])] + single[5:]
    for name, desc, edits, predicted, want_tags in plan:
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
            verdict = "CAUGHT" if code != 0 else "NOT CAUGHT"
            print(f"    ACTUAL:    {verdict}")
            print(f"    failing tests: {failed if failed else '(none)'}")
            print(f"    tags in output: {tags if tags else '(none)'}")
            for t in want_tags:
                if f"[{t}]" not in out:
                    print(f"    ⚠ PREDICTION MISS: expected tag [{t}] absent from the failure output")
            results.append((name, verdict, failed, tags))
        finally:
            with open(TARGET, "w") as fh:
                fh.write(original)
            after = sha256(TARGET)
            assert after == pristine, f"{name}: RESTORE FAILED ({after} != {pristine})"
            print(f"    restored, sha256 {after} ok")

    print("\n=== SUMMARY ===")
    for name, verdict, failed, tags in results:
        print(f"{name:>3}  {verdict:<10}  {','.join(failed) if failed else '-'}")
    print(f"\nfinal sha256 {sha256(TARGET)} (pristine {pristine})")


if __name__ == "__main__":
    main()
