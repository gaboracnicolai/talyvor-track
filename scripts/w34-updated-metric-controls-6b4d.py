#!/usr/bin/env python3
"""Positive controls for track_issues_updated_total (W3.4, tab-6b4d).

Every guard added with the counter is a claim that a particular wrong version of the code would be
caught. This harness makes each wrong version REAL — it edits the production file, runs the tests
that are supposed to notice, and restores the file — so "the guard works" is a measurement rather
than an intention. A guard that stays green under its own mutation is reported as NOT CAUGHT rather
than quietly dropped.

Both directions are covered: mutations that REMOVE an increment (the undercount this merge fixes)
and mutations that ADD or MISPLACE one (the overcount a fix like this can introduce, which is worse
because it looks like work and a Prometheus counter cannot be decremented).

Usage:
    TRACK_TEST_DATABASE_URL=postgres://... python3 scripts/w34-updated-metric-controls-6b4d.py
"""

import os
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
STORE = os.path.join(ROOT, "internal/issue/store.go")
HANDLER = os.path.join(ROOT, "internal/issue/handler.go")

ISSUE_PKG = "./internal/issue/"
IMPORTER_PKG = "./internal/importer/"

# (id, file, old, new, test-name-regex, packages, why)
MUTATIONS = [
    (
        "M1 remove the Store.Update increment",
        STORE,
        "\treturn countUpdated(i, err)\n}",
        "\treturn i, err\n}",
        "TestStore_Update_CountsTheIssueItUpdated",
        [ISSUE_PKG],
        "the door TWELVE non-handler callers use (MCP x3, automation x8, ai/handler x1)",
    ),
    (
        "M2 remove the upsert-UPDATE increment",
        STORE,
        "\t\t_, _ = countUpdated(out, nil)\n",
        "\n",
        "TestStore_UpsertByIdentifier_CountsAnUpdateAndNotAnInsert|TestJobRow_JiraCSV_EveryIssueAReImportOverwrites",
        [ISSUE_PKG, IMPORTER_PKG],
        "every RE-import of a keyed export; the end-to-end job test must see it too",
    ),
    (
        "M3 remove the BulkUpdate increment",
        STORE,
        "\tfor _, c := range counted {\n\t\tcountUpdatedLabels(workspaceID, c.teamID, c.status)\n\t}\n",
        "",
        "TestStore_BulkUpdate_Counts|TestStore_BulkUpdate_ARow|TestStore_BulkUpdate_ADrag",
        [ISSUE_PKG],
        "the kanban board — every card drag in the product",
    ),
    (
        "M4 count per statement instead of after Commit",
        STORE,
        "\t\tupdated++\n\t\tcounted = append(counted, bulkCounted{teamID: teamID, status: status})",
        "\t\tupdated++\n\t\tcountUpdatedLabels(workspaceID, teamID, status)",
        "TestStore_BulkUpdate_ABatchThatRolledBackCountsNothing",
        [ISSUE_PKG],
        "THE control for the rollback test: without it that test is green either way",
    ),
    (
        "M5 count the two write-nothing early returns in Update",
        STORE,
        "\tif len(setClauses) == 0 {\n\t\treturn s.getInWorkspace(ctx, id, workspaceID)\n\t}",
        "\tif len(setClauses) == 0 {\n\t\treturn countUpdated(s.getInWorkspace(ctx, id, workspaceID))\n\t}",
        "TestStore_AnUpdateThatWroteNothingCountsNothing",
        [ISSUE_PKG],
        "an allowlist-dropped field map runs no statement; counting it invents edits",
    ),
    (
        "M6a delete countUpdated's whole guard",
        STORE,
        "func countUpdated(out *model.Issue, err error) (*model.Issue, error) {\n\tif err != nil || out == nil {\n\t\treturn out, err\n\t}",
        "func countUpdated(out *model.Issue, err error) (*model.Issue, error) {",
        "TestStore_AFailedUpdateCountsNothing|TestStore_AnUpdateInAnotherWorkspaceCountsNothing",
        [ISSUE_PKG],
        "a refused write must count nothing (expected: panic, which is a red)",
    ),
    (
        "M6b delete only the err!=nil half of the guard",
        STORE,
        "func countUpdated(out *model.Issue, err error) (*model.Issue, error) {\n\tif err != nil || out == nil {",
        "func countUpdated(out *model.Issue, err error) (*model.Issue, error) {\n\tif out == nil {",
        "TestStore_AFailedUpdateCountsNothing|TestStore_AnUpdateInAnotherWorkspaceCountsNothing",
        [ISSUE_PKG],
        "reported honestly: on every reachable failure scanIssue returns (nil, err), so the nil "
        "check alone may already answer — the created-metric twin measured NOT CAUGHT here",
    ),
    (
        "M7 restore the handler-side increment (double count)",
        HANDLER,
        "\t// track_issues_updated_total is incremented by the STORE (issue.countUpdated), not here.",
        "\tmetrics.IssuesUpdated.WithLabelValues(out.WorkspaceID, out.TeamID, string(out.Status)).Inc()\n"
        "\t// track_issues_updated_total is incremented by the STORE (issue.countUpdated), not here.",
        "TestMetrics_IssuesUpdatedIsIncrementedInExactlyOnePlace",
        [ISSUE_PKG],
        "the reach guard, in the ADD direction",
    ),
    (
        "M8 label the bulk row with the REQUESTED status, not the written one",
        STORE,
        "\t\tcounted = append(counted, bulkCounted{teamID: teamID, status: status})",
        "\t\tcounted = append(counted, bulkCounted{teamID: teamID, status: u.Status})",
        "TestStore_BulkUpdate_ADragWithinAColumn",
        [ISSUE_PKG],
        "a drag WITHIN a column sets no status, so the requested value is the empty string",
    ),
    (
        "M9 label every update with a fixed status",
        STORE,
        "\tcountUpdatedLabels(out.WorkspaceID, out.TeamID, string(out.Status))",
        '\tcountUpdatedLabels(out.WorkspaceID, out.TeamID, "todo")',
        "TestStore_Update_CountsTheIssueItUpdated",
        [ISSUE_PKG],
        "the Help says 'labelled by ... new status'; this files every transition under one series",
    ),
]


# A companion edit some mutations need to stay COMPILABLE. Without it M7 would red on a missing
# import, and a build failure is not evidence about the guard it is supposed to exercise — the whole
# point of M7 is to watch the reach guard notice a second increment site, which it cannot do if the
# package never builds. Keyed by mutation id prefix.
COMPANION = {
    "M7": (
        HANDLER,
        '\t"github.com/talyvor/track/internal/httpx"\n\t"github.com/talyvor/track/internal/model"',
        '\t"github.com/talyvor/track/internal/httpx"\n\t"github.com/talyvor/track/internal/metrics"\n'
        '\t"github.com/talyvor/track/internal/model"',
    ),
}


def run(pattern, pkgs):
    cmd = ["go", "test", "-count=1", "-run", pattern] + pkgs
    p = subprocess.run(cmd, cwd=ROOT, capture_output=True, text=True)
    return p.returncode == 0, (p.stdout + p.stderr)


def main():
    if not os.environ.get("TRACK_TEST_DATABASE_URL"):
        print("TRACK_TEST_DATABASE_URL is unset — these controls need real Postgres. Refusing to "
              "report a green that only means 'nothing ran'.")
        return 2

    # Baseline: with the code as shipped, every named test must PASS. A control harness whose
    # baseline is red cannot tell a mutation from the state it started in.
    print("=== baseline (unmutated) ===")
    allpat = "|".join(m[4] for m in MUTATIONS)
    ok, out = run(allpat, [ISSUE_PKG, IMPORTER_PKG])
    if not ok:
        print("BASELINE RED — the harness is not measuring what it claims:\n" + out[-4000:])
        return 1
    print("baseline green\n")

    caught, missed = 0, 0
    for name, path, old, new, pattern, pkgs, why in MUTATIONS:
        src = open(path, encoding="utf-8").read()
        if src.count(old) != 1:
            print(f"[SKIP] {name}: anchor matched {src.count(old)} times, want 1 — the mutation was "
                  f"NOT applied and nothing below is a measurement of it")
            missed += 1
            continue
        comp = COMPANION.get(name.split()[0])
        comp_src = None
        if comp:
            comp_path, comp_old, _ = comp
            comp_src = open(comp_path, encoding="utf-8").read()
            if comp_src.count(comp_old) != 1:
                print(f"[SKIP] {name}: companion anchor matched {comp_src.count(comp_old)} times, "
                      f"want 1 — the mutation would red on a build failure rather than on its guard")
                missed += 1
                continue
        try:
            open(path, "w", encoding="utf-8").write(src.replace(old, new, 1))
            if comp:
                comp_path, comp_old, comp_new = comp
                # Re-read: path and comp_path may be the same file.
                cur = open(comp_path, encoding="utf-8").read()
                open(comp_path, "w", encoding="utf-8").write(cur.replace(comp_old, comp_new, 1))
            ok, out = run(pattern, pkgs)
        finally:
            open(path, "w", encoding="utf-8").write(src)
            if comp_src is not None:
                open(comp[0], "w", encoding="utf-8").write(comp_src)
        if ok:
            print(f"[NOT CAUGHT] {name}\n              {why}")
            missed += 1
        else:
            first = next((ln.strip() for ln in out.splitlines()
                          if ln.strip().startswith("---") or "FAIL" in ln or "cannot" in ln
                          or "undefined" in ln or "panic" in ln), "(red)")
            print(f"[CAUGHT]     {name}\n              {first[:150]}")
            caught += 1

    print(f"\n{caught} caught, {missed} not caught, of {len(MUTATIONS)}")
    # A NOT CAUGHT is a finding to report, not a failure of the harness — the exit code stays 0 so
    # the numbers above are read rather than a stack trace.
    return 0


if __name__ == "__main__":
    sys.exit(main())
