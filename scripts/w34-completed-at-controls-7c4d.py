#!/usr/bin/env python3
"""
W3.4 / tab-7c4d — controls for `a completion time is recorded only on a row that is done`.

THE DEFECT, measured at 1c0323a through the real /v1 chain BEFORE any fix was written:
`PATCH /v1/workspaces/{ws}/issues/{id}` with body {"completed_at":"2020-01-02T03:04:05Z"}
answered 200 and Postgres stored that value on a BACKLOG issue.

Every control is a MUTATION applied to the FIXED tree, scored over the FULL CI command
(`go test -timeout 300s -race -count=1 ./...`), membership by SET SUBTRACTION against a
baseline captured on the fixed tree — so the 13 environmental `--- FAIL:` lines in
internal/importer (empty corpus dirs; W3.4 handover (f)) are subtracted, not misread.

  C1  the defect itself, both hunks reverted        predict CAUGHT by the new file
  C2  C1 with the new file DELETED                  predict NOT CAUGHT  <- the blindness on main
  C3  C1 with the new file's FIRST subtest removed  predict CAUGHT (the census subtest alone)
  C4  C1 with the new file's CENSUS subtest removed predict CAUGHT (the first subtest alone)
  C5  the LAZY fix: completed_at dropped from the
      write path entirely, server stamp included    predict CAUGHT (subtest 2)
  C6  the caller's value survives a done transition predict CAUGHT (subtest 3)

C3 and C4 exist because a file with four subtests can catch a defect with one of them and be
inert in the other three; scoring the file as a unit would hide that. C5 and C6 exist because
two plausible wrong fixes pass the defect control: deleting the column from the write path,
and admitting the caller's value alongside a real transition.
"""

import os
import pathlib
import re
import subprocess
import sys

REPO = pathlib.Path(__file__).resolve().parent.parent
TEST_CMD = ["go", "test", "-timeout", "300s", "-race", "-count=1", "./..."]
STORE = "internal/issue/store.go"
GUARD = "internal/issue/update_completed_at_route_test.go"

FIXED_GATE = '\t\tif _, ok := updatableFields[k]; !ok && !(k == "completed_at" && serverStamped) {'
BROKEN_GATE = '\t\tif _, ok := updatableFields[k]; !ok && k != "completed_at" {'
STAMP = '\t\t\t\tupdates["completed_at"] = time.Now().UTC()'

FIRST_SUBTEST_ANCHOR = '\tt.Run("a body carrying only completed_at leaves a backlog row with none", func(t *testing.T) {'
CENSUS_ANCHOR = '\tt.Run("no row in this workspace holds a completion time without being done", func(t *testing.T) {'

FAIL_RE = re.compile(r"^\s*--- FAIL: (\S+)", re.M)


def run_suite():
    p = subprocess.run(TEST_CMD, cwd=REPO, capture_output=True, text=True, timeout=1800)
    return set(FAIL_RE.findall(p.stdout + p.stderr)), p.stdout + p.stderr


def cut_block(src, anchor):
    """Delete the t.Run(...) block that starts at `anchor`, up to its closing `\\t})`."""
    i = src.index(anchor)
    end = src.index("\n\t})\n", i) + len("\n\t})\n")
    return src[:i] + src[end:]


def revert_fix(store_src):
    return store_src.replace(FIXED_GATE, BROKEN_GATE, 1)


def apply_control(cid):
    """Mutate the tree for cid. Returns nothing; caller restores with git checkout."""
    store = REPO / STORE
    guard = REPO / GUARD
    if cid in ("C1", "C2", "C3", "C4"):
        store.write_text(revert_fix(store.read_text()))
    if cid == "C2":
        guard.unlink()
    if cid == "C3":
        guard.write_text(cut_block(guard.read_text(), FIRST_SUBTEST_ANCHOR))
    if cid == "C4":
        guard.write_text(cut_block(guard.read_text(), CENSUS_ANCHOR))
    if cid == "C5":
        # completed_at never reaches the SET list at all — not even the server's own stamp.
        s = store.read_text().replace(
            FIXED_GATE, '\t\tif _, ok := updatableFields[k]; !ok {', 1
        )
        store.write_text(s)
    if cid == "C6":
        # A done transition no longer overwrites a caller-supplied completed_at.
        s = store.read_text().replace(
            STAMP,
            '\t\t\t\tif _, supplied := updates["completed_at"]; !supplied {\n'
            '\t\t\t\t\tupdates["completed_at"] = time.Now().UTC()\n'
            "\t\t\t\t}",
            1,
        )
        store.write_text(s)


CONTROLS = [
    ("C1", "the defect itself (gate reverted)", "CAUGHT"),
    ("C2", "C1 with the guard file DELETED", "NOT CAUGHT"),
    ("C3", "C1 with the guard's FIRST subtest removed", "CAUGHT"),
    ("C4", "C1 with the guard's CENSUS subtest removed", "CAUGHT"),
    ("C5", "the lazy fix: completed_at never written at all", "CAUGHT"),
    ("C6", "a done transition keeps the caller's completed_at", "CAUGHT"),
]


def main():
    only = set(sys.argv[1:])
    if not os.environ.get("TRACK_TEST_DATABASE_URL"):
        sys.exit("TRACK_TEST_DATABASE_URL unset — real-PG tests would FAIL, not skip")
    dirty = subprocess.run(
        ["git", "status", "--porcelain", "--untracked-files=no"],
        cwd=REPO, capture_output=True, text=True,
    ).stdout.strip()
    if dirty:
        sys.exit(f"tracked files modified; refusing to mutate:\n{dirty}")

    print("=== BASELINE (fixed tree, full CI command) ===", flush=True)
    baseline, _ = run_suite()
    print(f"baseline --- FAIL: lines = {len(baseline)}", flush=True)

    results = []
    for cid, note, prediction in CONTROLS:
        if only and cid not in only:
            continue
        print(f"\n### {cid} — predicted {prediction} — {note}", flush=True)
        try:
            apply_control(cid)
            fails, out = run_suite()
            new = fails - baseline
            verdict = "CAUGHT" if new else "NOT CAUGHT"
            if "[build failed]" in out or "build constraints" in out:
                verdict = "BUILD BROKE (control void)"
            if any("panic:" in line for line in out.splitlines()):
                verdict += " ⚠ PANIC IN RUN — check it is not a crash-red"
            print(f"    verdict: {verdict}  (predicted {prediction})")
            for tst in sorted(new):
                print(f"    + {tst}")
            gone = baseline - fails
            if gone:
                print(f"    ⚠ baseline failures that VANISHED: {sorted(gone)}")
            results.append((cid, prediction, verdict, ", ".join(sorted(new)) or "-", note))
        finally:
            subprocess.run(["git", "checkout", "--", STORE, GUARD], cwd=REPO, check=True)

    print("\n\n================ SUMMARY ================")
    ok = True
    for cid, prediction, verdict, tests, note in results:
        match = "MATCH" if verdict == prediction else "*** MISMATCH ***"
        if verdict != prediction:
            ok = False
        print(f"{cid:<4} predicted {prediction:<11} got {verdict:<12} {match}  {note}")
        if tests != "-":
            print(f"       caught by: {tests}")
    print(f"\nall predictions matched: {ok}")
    left = subprocess.run(
        ["git", "status", "--porcelain", "--untracked-files=no"],
        cwd=REPO, capture_output=True, text=True,
    ).stdout.strip()
    print(f"tree after harness: {'CLEAN' if not left else left}")


if __name__ == "__main__":
    main()
