#!/usr/bin/env python3
"""
W3.4 / tab-7c4d — controls for the SIX `UPDATE ... SET %s = $n` allowlists.

WHAT THIS ASKS, AND WHY IT IS THE QUESTION tab-5b91 HANDED ON.

#166 found a completely unguarded SQL-composition gate on a READ route by asking
"is this refusal asserted with an input the DATABASE would ACCEPT?".  This harness
points the same question at the WRITE side.  Six stores build an UPDATE's SET list by
interpolating CALLER-SUPPLIED MAP KEYS into SQL:

    for k, v := range updates {                       // updates = raw request JSON
        if _, ok := <allowlist>[k]; !ok { continue }  // <- the whole gate
        setClauses = append(setClauses, fmt.Sprintf("%s = $%d", k, argN))
    }

    internal/issue/store.go:1075      updatableFields
    internal/milestone/store.go:141   updatable
    internal/project/store.go:116     projectUpdatable
    internal/team/store.go:103        teamUpdatable
    internal/workspace/store.go:264   workspaceUpdatable
    internal/template/store.go:279    updatableFields

Two INDEPENDENT things can be wrong, and a control that moves both at once tells you
nothing about which one was seen (#166's C-lesson).  So they are separated:

  G* GATE-OPEN     — delete the `continue`.  Every caller key reaches SQL.
                     Answers: does anything at all watch the gate?
  M* MEMBERSHIP    — leave the gate intact, ADD a tenancy/identity column to the
                     allowlist.  Answers: does anything watch WHAT the gate admits?
                     This is the one that matters: the allowlist is the only reason
                     `PATCH {"workspace_id": "<other tenant>"}` is not a cross-tenant
                     write, and adding a key is a one-line edit.

Membership by SET SUBTRACTION over the FULL CI command
(`go test -timeout 300s -race -count=1 ./...`) against a baseline captured on the
unmutated tree, so the 13 environmental `--- FAIL:` lines in internal/importer
(empty /tmp/w34-jira-corpus + /tmp/w34-linear-corpus-cache — see W3.4 handover (f))
are subtracted rather than misread as a catch.

P1 is a POSITIVE CONTROL ON THE HARNESS ITSELF: a mutation the queue already records
as caught by three tests (#165 wired `maxWindowDays`).  If P1 scores NOT CAUGHT the
harness is broken and every NOT CAUGHT below is meaningless.
"""

import os
import pathlib
import re
import subprocess
import sys

REPO = pathlib.Path(__file__).resolve().parent.parent
TEST_CMD = ["go", "test", "-timeout", "300s", "-race", "-count=1", "./..."]

# (id, file, old, new, prediction, note)
CONTROLS = [
    (
        "P1",
        "internal/analytics/engine.go",
        "maxWindowDays     = 365",
        "maxWindowDays     = 3650",
        "CAUGHT",
        "HARNESS POSITIVE CONTROL — queue records this as caught by three tests",
    ),
    # ---- GATE-OPEN -------------------------------------------------------
    (
        "G1",
        "internal/issue/store.go",
        # ⚠ RE-CUT 2026-08-29 (W6.55). The anchor this arm shipped with was
        # `!ok && k != "completed_at"` — the PRE-FIX text of a line that 4eb06a2,
        # THE COMMIT THAT ADDED THIS SCRIPT, had already rewritten. The arm scored
        # HARNESS ERROR on every run for 74 commits and never once executed
        # (W6.50, 77f70ac; count at that commit = 0). Verified red-first here:
        # `ANCHOR NOT UNIQUE (0x)` with P1 CAUGHT by three tests in the same run,
        # so the harness was sane and only this arm was dead.
        # ⚠⚠ THE RE-CUT PRESERVES WHAT THE ARM PROVES AND THAT IS THE WHOLE
        # CONSTRAINT: deleting today's block still means every caller-supplied key
        # reaches the SET list, which is GATE-OPEN as defined above. Re-pointing an
        # anchor at code that asks a DIFFERENT question is a control passing for a
        # new reason, which is the failure this repository keeps paying for.
        '\t\tif _, ok := updatableFields[k]; !ok && (k != "completed_at" || !serverStamped) {\n\t\t\tcontinue\n\t\t}\n',
        # ⚠⚠⚠ THE REPLACEMENT IS NOT "" LIKE ITS FIVE SIBLINGS, AND THAT IS MEASURED
        # RATHER THAN STYLISTIC. Deleting this block outright orphans `serverStamped`
        # (`store.go:1139: declared and not used`) and the arm scores BUILD BROKE
        # (control void) — a compile error is not a caught mutation, and a VOID arm is
        # indistinguishable at a glance from a NOT CAUGHT one. This keeps the variable
        # referenced and still removes the gate, so every caller-supplied key reaches
        # the SET list, which is GATE-OPEN exactly as this file defines it above.
        "\t\t_ = serverStamped // GATE OPEN: allowlist check removed\n",
        # MEASURED 2026-08-29 (W6.55), THE FIRST TIME THIS ARM HAS EVER RUN. Was "?".
        "CAUGHT",
        "issue: gate open — every caller key reaches SET. CAUGHT by "
        "TestStore_AnUpdateThatWroteNothingCountsNothing and "
        "TestUpdateRoute_ACompletionTimeIsRecordedOnlyOnARowThatIsDone_RealPG (2 subtests). "
        "⚠ BOTH CATCH IT INCIDENTALLY: neither is about the allowlist, and the second is the "
        "test 4eb06a2 shipped alongside the very fix that killed this arm's anchor. The gate "
        "is defended, and by accident — see the M* arms for the question that matters.",
    ),
    (
        "G2",
        "internal/milestone/store.go",
        "\t\tif _, ok := updatable[k]; !ok {\n\t\t\tcontinue\n\t\t}\n",
        "",
        "?",
        "milestone: gate open",
    ),
    (
        "G3",
        "internal/project/store.go",
        "\t\tif _, ok := projectUpdatable[k]; !ok {\n\t\t\tcontinue\n\t\t}\n",
        "",
        "?",
        "project: gate open",
    ),
    (
        "G4",
        "internal/team/store.go",
        "\t\tif _, ok := teamUpdatable[k]; !ok {\n\t\t\tcontinue\n\t\t}\n",
        "",
        "?",
        "team: gate open",
    ),
    (
        "G5",
        "internal/workspace/store.go",
        "\t\tif _, ok := workspaceUpdatable[k]; !ok {\n\t\t\tcontinue\n\t\t}\n",
        "",
        "?",
        "workspace: gate open",
    ),
    (
        "G6",
        "internal/template/store.go",
        "\t\tif _, ok := updatableFields[k]; !ok {\n\t\t\tcontinue\n\t\t}\n",
        "",
        "?",
        "template: gate open",
    ),
    # ---- MEMBERSHIP ------------------------------------------------------
    (
        "M1",
        "internal/issue/store.go",
        'var updatableFields = map[string]struct{}{\n\t"title":       {},',
        'var updatableFields = map[string]struct{}{\n\t"workspace_id": {},\n\t"title":       {},',
        "?",
        "issue: allowlist admits workspace_id — a PATCH could move an issue between tenants",
    ),
    (
        "M2",
        "internal/milestone/store.go",
        'var updatable = map[string]struct{}{\n\t"name": {},',
        'var updatable = map[string]struct{}{\n\t"workspace_id": {},\n\t"name": {},',
        "?",
        "milestone: allowlist admits workspace_id",
    ),
    (
        "M3",
        "internal/project/store.go",
        'var projectUpdatable = map[string]struct{}{\n\t"name": {},',
        'var projectUpdatable = map[string]struct{}{\n\t"workspace_id": {},\n\t"name": {},',
        "?",
        "project: allowlist admits workspace_id",
    ),
    (
        "M4",
        "internal/team/store.go",
        'var teamUpdatable = map[string]struct{}{\n\t"name": {},',
        'var teamUpdatable = map[string]struct{}{\n\t"workspace_id": {},\n\t"name": {},',
        "?",
        "team: allowlist admits workspace_id",
    ),
    (
        "M5",
        "internal/workspace/store.go",
        'var workspaceUpdatable = map[string]struct{}{\n\t"name": {},',
        'var workspaceUpdatable = map[string]struct{}{\n\t"id": {},\n\t"name": {},',
        "?",
        "workspace: allowlist admits its own primary key",
    ),
    (
        "M6",
        "internal/template/store.go",
        'var updatableFields = map[string]struct{}{\n\t"name":             {},',
        'var updatableFields = map[string]struct{}{\n\t"workspace_id":     {},\n\t"name":             {},',
        "?",
        "template: allowlist admits workspace_id",
    ),
]

FAIL_RE = re.compile(r"^\s*--- FAIL: (\S+)", re.M)


def run_suite():
    p = subprocess.run(
        TEST_CMD, cwd=REPO, capture_output=True, text=True, timeout=1800
    )
    return set(FAIL_RE.findall(p.stdout + p.stderr)), p.stdout + p.stderr


def clean_tree():
    p = subprocess.run(
        ["git", "status", "--porcelain", "--untracked-files=no"],
        cwd=REPO,
        capture_output=True,
        text=True,
    )
    return p.stdout.strip()


def main():
    only = set(sys.argv[1:])
    if not os.environ.get("TRACK_TEST_DATABASE_URL"):
        sys.exit("TRACK_TEST_DATABASE_URL is unset — the real-PG tests would FAIL, not skip")

    dirty = clean_tree()
    if dirty:
        sys.exit(f"tracked files are modified; refusing to mutate:\n{dirty}")

    print("=== BASELINE (unmutated tree, full CI command) ===", flush=True)
    baseline, _ = run_suite()
    print(f"baseline --- FAIL: lines = {len(baseline)}", flush=True)
    for t in sorted(baseline):
        print(f"    {t}")

    results = []
    for cid, relpath, old, new, prediction, note in CONTROLS:
        if only and cid not in only:
            continue
        f = REPO / relpath
        src = f.read_text()
        if src.count(old) != 1:
            results.append((cid, "HARNESS ERROR", f"anchor occurs {src.count(old)}x", note))
            print(f"\n### {cid}: ANCHOR NOT UNIQUE ({src.count(old)}x) in {relpath}", flush=True)
            continue
        f.write_text(src.replace(old, new, 1))
        try:
            print(f"\n### {cid} ({relpath}) — predicted {prediction} — {note}", flush=True)
            fails, out = run_suite()
            build_broke = "build failed" in out or "cannot use" in out
            new_fails = fails - baseline
            vanished = baseline - fails
            verdict = "CAUGHT" if new_fails else "NOT CAUGHT"
            if build_broke:
                verdict = "BUILD BROKE (control void)"
            print(f"    verdict: {verdict}")
            for t in sorted(new_fails):
                print(f"    + {t}")
            if vanished:
                print(f"    ⚠ baseline failures that VANISHED: {sorted(vanished)}")
            results.append((cid, verdict, ", ".join(sorted(new_fails)) or "-", note))
        finally:
            subprocess.run(["git", "checkout", "--", relpath], cwd=REPO, check=True)

    print("\n\n================ SUMMARY ================")
    for cid, verdict, tests, note in results:
        print(f"{cid:<4} {verdict:<26} {note}")
        if tests != "-":
            print(f"       caught by: {tests}")
    left = clean_tree()
    print(f"\ntree after harness: {'CLEAN' if not left else left}")


if __name__ == "__main__":
    main()
