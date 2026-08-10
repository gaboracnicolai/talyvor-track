#!/usr/bin/env python3
"""w34-linear-csv-issue-id-controls.py — positive controls for the linear_csv provider-key merge.

WHAT A CONTROL HAS TO DO HERE. Every guard added by this change passed on its first run once the
fix was in, which is exactly the state that has shipped three unfallible guards in this fleet. So
each mutation below names, IN ADVANCE, the test that must catch it. A mutation caught by a
DIFFERENT test than predicted is reported as a WRONG PREDICTION and kept wrong in this file — the
prediction is the falsifiable claim, not the catch.

THE LESSONS THIS HARNESS IS BUILT AROUND, each paid for in this repo or its siblings:
  · a build failure is NOT a catch — it proves the file moved, not that the product was wrong,
    so it is scored `BUILD-BROKEN` and the control is void
  · a test that never RAN is not a test that passed — the verdict reads `--- FAIL:` lines out of
    `go test -v` and prints the assertion MESSAGE, because a crash and a real catch look identical
    in a list of test names
  · every anchor is asserted UNIQUE before ANY write, and every write is verified to have CHANGED
    THE BYTES on disk — a control that silently matched nothing reads exactly like a dead guard
  · files are restored from SAVED BYTES and sha256-compared, never from git
  · NOT CAUGHT must be REACHABLE, or CAUGHT means nothing: C7 is an inverted control whose
    prediction IS "not caught", and if it ever reports CAUGHT this harness is broken

Requires TRACK_TEST_DATABASE_URL and a real Postgres. Run from the repo root.
"""
import hashlib
import os
import re
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CSV_GO = os.path.join(ROOT, "internal/importer/csv.go")
CONST_GO = os.path.join(ROOT, "internal/importer/linear_csv_issue_id.go")
SOURCE_GO = os.path.join(ROOT, "internal/importer/source.go")

PKG = "./internal/importer/"

# The line the merge adds to linearRowMapper. Anchors are matched EXACTLY and asserted unique.
MAPPER_LINE = "\t\t\tIdentifier:  ci.get(row, linearCSVIssueIDColumn),\n"
CONST_LINE = 'const linearCSVIssueIDColumn = "ID"'
ROUTE_PREDICATE = "if issueModel.Identifier != \"\" && imp.upserter != nil {"


class Edit:
    def __init__(self, path, old, new):
        self.path = path
        self.old = old
        self.new = new


CONTROLS = [
    dict(
        name="C1  revert the fix: linearRowMapper stops reading the ID column",
        why="This IS the red-first run, re-executed as a control so the finding stays falsifiable.",
        edits=[Edit(CSV_GO, MAPPER_LINE, "")],
        predict={
            "TestLinearCSVIssueID_ReachesTheModel",
            "TestJobRow_LinearCSV_TheIssueKeepsTheKeyLinearGaveIt",
            "TestJobRow_LinearCSV_ReimportingTheSameExportDoesNotDuplicate",
            "TestJobRow_LinearCSV_AReimportUpdatesTheRowItAlreadyWrote",
            "TestJobRow_LinearCSV_ARowAHumanOwnsIsRefusedNotOverwritten",
        },
        expect_caught=True,
    ),
    dict(
        name="C2  the column constant becomes the empty string",
        why="An empty spelling makes ci.get return \"\" for every row — the defect with the fix's "
            "shape still in place. The hardcoded-spelling test is the named catcher; if it were "
            "written as `linearCSVIssueIDColumn != linearCSVIssueIDColumn` it would pass here.",
        edits=[Edit(CONST_GO, CONST_LINE, 'const linearCSVIssueIDColumn = ""')],
        predict={
            "TestLinearCSVIssueID_TheMeasuredSpelling",
            "TestLinearCSVIssueID_ReachesTheModel",
            "TestJobRow_LinearCSV_TheIssueKeepsTheKeyLinearGaveIt",
            "TestJobRow_LinearCSV_ReimportingTheSameExportDoesNotDuplicate",
            "TestJobRow_LinearCSV_AReimportUpdatesTheRowItAlreadyWrote",
            "TestJobRow_LinearCSV_ARowAHumanOwnsIsRefusedNotOverwritten",
        },
        expect_caught=True,
    ),
    dict(
        name="C3  a plausible WRONG spelling: `Identifier` instead of `ID`",
        why="The failure mode of guessing a header instead of measuring it. `Identifier` is what "
            "Linear's GraphQL API calls this field, so it is the exact wrong answer a reader of "
            "linear.go would reach for.",
        edits=[Edit(CONST_GO, CONST_LINE, 'const linearCSVIssueIDColumn = "Identifier"')],
        predict={
            "TestLinearCSVIssueID_TheMeasuredSpelling",
            "TestLinearCSVIssueID_ReachesTheModel",
            "TestJobRow_LinearCSV_TheIssueKeepsTheKeyLinearGaveIt",
            "TestJobRow_LinearCSV_ReimportingTheSameExportDoesNotDuplicate",
            "TestJobRow_LinearCSV_AReimportUpdatesTheRowItAlreadyWrote",
            "TestJobRow_LinearCSV_ARowAHumanOwnsIsRefusedNotOverwritten",
        },
        expect_caught=True,
    ),
    dict(
        name="C4  substring generosity: fall back to the `UUID` column when `ID` is absent",
        why="THE CONTROL THAT JUSTIFIES THE NEIGHBOUR TESTS, AND THE ONLY ONE THAT REDS THEM ALONE. "
            "`ID` is two characters; `Project ID`, `Project Milestone ID` and `UUID` all contain it. "
            "A fallback keeps every keyed fixture green and every keyless-WITHOUT-neighbours fixture "
            "green, so only the two neighbour tests can see it.",
        edits=[Edit(
            CSV_GO,
            MAPPER_LINE,
            "\t\t\tIdentifier:  linearCSVFallbackIdentifierCONTROL(ci, row),\n",
        ), Edit(
            CONST_GO,
            CONST_LINE,
            CONST_LINE + """

func linearCSVFallbackIdentifierCONTROL(ci columnIndex, row []string) string {
\tif v := ci.get(row, linearCSVIssueIDColumn); v != "" {
\t\treturn v
\t}
\treturn ci.get(row, "UUID")
}""",
        )],
        predict={
            "TestLinearCSVIssueID_ANeighbouringIDColumnIsNotRead",
            "TestJobRow_LinearCSV_TheNeighbouringIDColumnsAreNotTheKey",
        },
        expect_caught=True,
    ),
    dict(
        name="C5  a keyless row is given a FABRICATED key",
        why="RECORDED AS JUSTIFYING NEITHER OF THE TWO KEYLESS TESTS, WHICH IS THE POINT. "
            "TestLinearCSVIssueID_AbsentColumnStillImports and "
            "TestLinearCSVIssueID_ANeighbouringIDColumnIsNotRead assert the SAME postcondition "
            "(Identifier == \"\") and the first's fixture is a STRICT SUBSET of the second's, so no "
            "mutation can red one without redding the other. This control reds both plus the job "
            "fail-safe; it is a CATCH that earns no individual test its place. C4 is what does.",
        edits=[Edit(
            CSV_GO,
            MAPPER_LINE,
            "\t\t\tIdentifier:  linearCSVFabricateIdentifierCONTROL(ci, row),\n",
        ), Edit(
            CONST_GO,
            CONST_LINE,
            CONST_LINE + """

func linearCSVFabricateIdentifierCONTROL(ci columnIndex, row []string) string {
\tif v := ci.get(row, linearCSVIssueIDColumn); v != "" {
\t\treturn v
\t}
\treturn "FABRICATED-1"
}""",
        )],
        # ⚠ THE FIRST PREDICTION IS KEPT HERE, WRONG, BECAUSE THE MISS IS THE FINDING. It was:
        #     {AbsentColumnStillImports, ANeighbouringIDColumnIsNotRead, TheJiraColumnIsNotReadHere,
        #      JobRow_AKeylessExportStillImports, JobRow_TheNeighbouringIDColumnsAreNotTheKey}
        # The two JOB tests did NOT red, and the reason was a hole in MY OWN GUARD rather than in
        # the control: JobRow_AKeylessExportStillImports asserted `identifier != ""`, and
        # "FABRICATED-1" is not empty — so the fail-safe test that exists to stop a keyless row
        # being routed into the upsert on an invented key could not see a keyless row being routed
        # into the upsert on an invented key. JobRow_TheNeighbouringIDColumnsAreNotTheKey compared
        # against an ENUMERATED list of three wrong values, which an invented fourth walks past.
        # Both now assert the Track-DERIVED identifier itself. The prediction below is the corrected
        # one; the original is above so nobody re-derives the lesson.
        predict={
            "TestLinearCSVIssueID_AbsentColumnStillImports",
            "TestLinearCSVIssueID_ANeighbouringIDColumnIsNotRead",
            "TestLinearCSVIssueID_TheJiraColumnIsNotReadHere",
            "TestJobRow_LinearCSV_AKeylessExportStillImports",
            "TestJobRow_LinearCSV_TheNeighbouringIDColumnsAreNotTheKey",
        },
        expect_caught=True,
    ),
    dict(
        name="C6  the mapper still reads the key, but source.go's ROUTING predicate is disarmed",
        why="THE CONTROL THE JOB TESTS EXIST FOR. Identifier is a routing key, not a column: with "
            "the upsert branch unreachable every SOURCE-level assertion is still green while the "
            "product duplicates. The must-stay-green half of this control is as load-bearing as the "
            "catch — if a source test reds here, it is reaching the database and is misfiled.",
        edits=[Edit(SOURCE_GO, ROUTE_PREDICATE, "if false && imp.upserter != nil {")],
        predict={
            "TestJobRow_LinearCSV_TheIssueKeepsTheKeyLinearGaveIt",
            "TestJobRow_LinearCSV_ReimportingTheSameExportDoesNotDuplicate",
            "TestJobRow_LinearCSV_AReimportUpdatesTheRowItAlreadyWrote",
            "TestJobRow_LinearCSV_ARowAHumanOwnsIsRefusedNotOverwritten",
        },
        # Other transports' job tests red here too — this predicate is shared. Only the Linear set
        # is predicted; the harness reports the rest as CONTEXT, not as a miss.
        subset_ok=True,
        must_stay_green={
            "TestLinearCSVIssueID_TheMeasuredSpelling",
            "TestLinearCSVIssueID_ReachesTheModel",
            "TestLinearCSVIssueID_AbsentColumnStillImports",
            "TestLinearCSVIssueID_ANeighbouringIDColumnIsNotRead",
            "TestLinearCSVIssueID_TheJiraColumnIsNotReadHere",
        },
        expect_caught=True,
    ),
    dict(
        name="C7  the constant is lowercased to `id`",
        why="⚠ PREDICTED **NOT CAUGHT** AND THE PREDICTION WAS WRONG — KEPT HERE AS A CATCH. The "
            "reasoning was: buildIndex lowercases every header, so `id` and `ID` select the same "
            "column and this edit is behaviourally inert. That is true of the PRODUCT and false of "
            "the GUARD. TheMeasuredSpelling is a hardcoded string equality and is case-sensitive "
            "ON PURPOSE — it pins what the 45 measured exports actually emit, and `id` is not it. "
            "So this is a real catch by a guard doing its job, and the harness's ability to report "
            "NOT CAUGHT is NOT demonstrated by it. C8 exists because of that.",
        edits=[Edit(CONST_GO, CONST_LINE, 'const linearCSVIssueIDColumn = "id"')],
        predict={"TestLinearCSVIssueID_TheMeasuredSpelling"},
        expect_caught=True,
    ),
    dict(
        name="C8  INVERTED — the mapper's read is wrapped in a redundant TrimSpace",
        why="PREDICTED NOT CAUGHT, AND THIS IS THE CONTROL THAT MAKES EVERY `CAUGHT` ABOVE MEAN "
            "SOMETHING. ci.get already returns strings.TrimSpace(...) of the cell, so this edit "
            "changes real bytes on disk and cannot change one observable value. A harness that "
            "reports CAUGHT for every mutation it is given has not measured anything; this is the "
            "row that proves NOT CAUGHT is reachable. If it ever flips to CAUGHT, some assertion "
            "in this set is reading the source rather than the behaviour.",
        edits=[Edit(
            CSV_GO,
            MAPPER_LINE,
            "\t\t\tIdentifier:  strings.TrimSpace(ci.get(row, linearCSVIssueIDColumn)),\n",
        )],
        predict=set(),
        expect_caught=False,
    ),
]


def sha(b):
    return hashlib.sha256(b).hexdigest()


def read(path):
    with open(path, "rb") as f:
        return f.read()


def run_tests():
    """Returns (build_ok, failing_test_names, message_lines)."""
    env = dict(os.environ)
    p = subprocess.run(
        ["go", "test", "-timeout", "300s", "-count=1", "-v", PKG],
        cwd=ROOT, capture_output=True, text=True, env=env)
    out = p.stdout + p.stderr
    # A build/vet failure is NOT a catch. Detect it before reading any verdict.
    if "[build failed]" in out or "cannot use" in out or "undefined:" in out or "syntax error" in out:
        return False, set(), [l for l in out.splitlines() if l.strip()][:14]
    failing = set(re.findall(r"^\s*--- FAIL: (\S+)", out, re.M))
    ran = set(re.findall(r"^=== RUN\s+(\S+)", out, re.M))
    msgs = [l.rstrip() for l in out.splitlines()
            if re.match(r"^\s{4,}\S+_test\.go:\d+:", l)]
    return True, failing, (msgs[:10] if msgs else [f"(no assertion messages; {len(ran)} tests ran)"])


def apply_control(ctrl, saved):
    """Assert EVERY anchor unique BEFORE any write, then write. Returns None or an error string."""
    plans = []
    for e in ctrl["edits"]:
        body = saved[e.path].decode()
        n = body.count(e.old)
        if n != 1:
            return f"ANCHOR NOT UNIQUE in {os.path.basename(e.path)}: {n} occurrences"
        plans.append((e.path, body.replace(e.old, e.new, 1)))
    for path, new_body in plans:
        with open(path, "w") as f:
            f.write(new_body)
        if read(path) == saved[path]:
            return f"WRITE CHANGED NOTHING in {os.path.basename(path)}"
    return None


def restore(saved):
    bad = []
    for path, body in saved.items():
        with open(path, "wb") as f:
            f.write(body)
        if sha(read(path)) != sha(body):
            bad.append(path)
    return bad


def main():
    if not os.environ.get("TRACK_TEST_DATABASE_URL"):
        print("TRACK_TEST_DATABASE_URL unset — the job controls need real Postgres. Refusing to run.")
        return 2

    saved = {p: read(p) for p in (CSV_GO, CONST_GO, SOURCE_GO)}
    print("SAVED BYTES:")
    for p, b in saved.items():
        print(f"  {os.path.basename(p):<28} {len(b):>7} bytes  sha256 {sha(b)[:16]}")

    print("\nBASELINE (no mutation) — must be GREEN, or every verdict below is meaningless")
    ok, failing, msgs = run_tests()
    if not ok or failing:
        print(f"  BASELINE BROKEN: build_ok={ok} failing={sorted(failing)}")
        for m in msgs:
            print("   ", m)
        restore(saved)
        return 2
    print("  baseline green")

    results = []
    for ctrl in CONTROLS:
        print("\n" + "=" * 78)
        print(ctrl["name"])
        print("  WHY: " + ctrl["why"])
        print(f"  PREDICTED CATCHER(S): {sorted(ctrl['predict']) or 'NONE (inverted control)'}")

        err = apply_control(ctrl, saved)
        if err:
            print(f"  CONTROL VOID — {err}")
            results.append((ctrl["name"], "VOID"))
            restore(saved)
            continue

        ok, failing, msgs = run_tests()
        restore_bad = restore(saved)
        if restore_bad:
            print(f"  ⚠ RESTORE FAILED for {restore_bad} — STOPPING")
            return 2

        if not ok:
            print("  BUILD-BROKEN — scored as NOT a catch. The edit proved the file moved, not "
                  "that the product was wrong.")
            for m in msgs:
                print("   ", m)
            results.append((ctrl["name"], "VOID (build)"))
            continue

        mine = {f for f in failing if "LinearCSVIssueID" in f or "JobRow_LinearCSV" in f}
        others = sorted(failing - mine)
        print(f"  OBSERVED FAIL (this change's guards): {sorted(mine) or 'NONE'}")
        if others:
            print(f"  CONTEXT — other tests also red: {others[:6]}{' …' if len(others) > 6 else ''}")
        for m in msgs[:4]:
            print("   ", m)

        if not ctrl["expect_caught"]:
            verdict = "NOT CAUGHT (as predicted)" if not failing else f"UNEXPECTED CATCH {sorted(failing)}"
        elif not mine:
            verdict = "NOT CAUGHT — THE GUARD IS BLIND"
        elif ctrl.get("subset_ok"):
            missing = ctrl["predict"] - mine
            verdict = "CAUGHT as predicted" if not missing else f"WRONG PREDICTION — missed {sorted(missing)}"
        elif mine == ctrl["predict"]:
            verdict = "CAUGHT as predicted"
        else:
            verdict = (f"WRONG PREDICTION — extra {sorted(mine - ctrl['predict'])}, "
                       f"missing {sorted(ctrl['predict'] - mine)}")

        if ctrl.get("must_stay_green"):
            leaked = ctrl["must_stay_green"] & failing
            print(f"  MUST-STAY-GREEN: {'held' if not leaked else 'VIOLATED by ' + str(sorted(leaked))}")
            if leaked:
                verdict += f" | MUST-STAY-GREEN VIOLATED {sorted(leaked)}"

        print(f"  VERDICT: {verdict}")
        results.append((ctrl["name"], verdict))

    print("\n" + "=" * 78)
    print("SUMMARY")
    for name, verdict in results:
        print(f"  {name.split()[0]:<4} {verdict}")

    print("\nFINAL TREE CHECK (restored from saved bytes, sha256):")
    clean = True
    for p, b in saved.items():
        now = sha(read(p))
        same = now == sha(b)
        clean = clean and same
        print(f"  {os.path.basename(p):<28} {'IDENTICAL' if same else 'DRIFTED'}  {now[:16]}")
    return 0 if clean else 2


if __name__ == "__main__":
    sys.exit(main())
