#!/usr/bin/env python3
"""Positive controls for internal/importer/linear_query_schema_test.go.

A guard that has never been watched fail is a guard nobody knows works. Each control below EDITS a
real file, runs the guard, asserts it goes RED (or, for C0, that the baseline is GREEN), and
restores the file byte-for-byte — verified by sha256, not by an exit code.

⚠ EVERY SUBSTITUTION ASSERTS ITS ANCHOR COUNT FIRST. This package has already paid for a control
that silently matched nothing and "passed": a no-op edit is byte-indistinguishable from a guard
that works (see the #71 note in W3.4). `sub()` fails loudly when the anchor is not found exactly
once.

Run:  python3 scripts/w34-linear-query-schema-controls.py
"""

import hashlib
import pathlib
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1]
LINEAR_GO = ROOT / "internal/importer/linear.go"
SNAPSHOT = ROOT / "internal/importer/testdata/linear_schema_snapshot.json"
TEST_GO = ROOT / "internal/importer/linear_query_schema_test.go"
NULLTEAM_GO = ROOT / "internal/importer/linear_null_team_test.go"

RUN = [
    "go", "test", "-count=1", "-run",
    "TestLinearIssuesQuery_EveryFieldExistsInLinearsPinnedSchema|TestLinearSchema_QueryTeamIsNonNull",
    "./internal/importer/",
]


def sha(p):
    return hashlib.sha256(p.read_bytes()).hexdigest()


def run_guard():
    proc = subprocess.run(RUN, cwd=ROOT, capture_output=True, text=True)
    return proc.returncode, (proc.stdout + proc.stderr)


def sub(path, old, new, expect=1):
    text = path.read_text()
    found = text.count(old)
    if found != expect:
        sys.exit(f"CONTROL DID NOT APPLY: anchor {old!r} appears {found} times in {path.name}, expected {expect}. "
                 f"A control that edits zero bytes proves nothing.")
    path.write_text(text.replace(old, new, 1))


def control(name, path, old, new, expect_red=True):
    before = sha(path)
    original = path.read_text()
    sub(path, old, new)
    assert sha(path) != before, "control edited zero bytes"
    code, out = run_guard()
    path.write_text(original)
    if sha(path) != before:
        sys.exit(f"{name}: FAILED TO RESTORE {path.name} — sha differs after revert")
    red = code != 0
    verdict = "RED" if red else "GREEN"
    ok = red if expect_red else not red
    first = ""
    for line in out.splitlines():
        s = line.strip()
        if s.startswith(("--- FAIL", "linear_query_schema_test.go:")):
            first = s[:160]
            break
    print(f"  {'PASS' if ok else '**FAILED**'}  {name}: guard went {verdict} (want {'RED' if expect_red else 'GREEN'})"
          + (f"\n            {first}" if first and red else ""))
    return ok


def main():
    print("C0 baseline: the guard must be GREEN on an untouched tree")
    code, out = run_guard()
    if code != 0:
        print(out[-3000:])
        sys.exit("BASELINE IS RED — fix that before trusting any control below")
    for line in out.splitlines():
        if "validated" in line:
            print("  " + line.strip()[:200])
    print("  PASS  C0")

    results = []
    print("\nControls — each must turn the guard RED:")

    # C1 — THE EXACT MUTATION W3.4 SAYS NOTHING IN CI CAN CATCH.
    results.append(control(
        "C1 misspelled field in the shipped query (`state { type }` -> `state { typ }`)",
        LINEAR_GO, "state { name type }", "state { name typ }"))

    # C2 — an argument Linear does not declare.
    results.append(control(
        "C2 unknown argument (issues(first: 100, after: $after, orderByX: 1))",
        LINEAR_GO, "issues(first: 100, after: $after)", "issues(first: 100, after: $after, orderByX: 1)"))

    # C3 — an object field asked for as a leaf.
    results.append(control(
        "C3 object field with no selection set (`state { name type }` -> `state`)",
        LINEAR_GO, "state { name type }", "state"))

    # C4 — a variable whose declared type no longer matches the argument type.
    results.append(control(
        "C4 variable type mismatch ($teamId: String! -> $teamId: ID!)",
        LINEAR_GO, "query($teamId: String!, $after: String)", "query($teamId: ID!, $after: String)"))

    # C5 — THE SNAPSHOT MUST BE LOAD-BEARING. Remove a field from the pinned schema and the guard
    # must notice; if it stays green, the check is reading the document against itself.
    results.append(control(
        "C5 snapshot is load-bearing (drop Issue.dueDate from the pinned schema)",
        SNAPSHOT, '"dueDate": {\n          "args": {},\n          "deprecated": false,\n          "type": "TimelessDate"\n        },',
        ""))

    # C6 — ANTI-VACUITY. Empty the document body: the path floor must fire rather than pass on zero.
    results.append(control(
        "C6 anti-vacuity (floor fires when the walk visits nothing)",
        TEST_GO, "walk(\"Query\", sel)", "_ = sel"))

    # C7 — the nullability pin must be a real assertion, not a restatement.
    results.append(control(
        "C7 Query.team nullability pin (Team! -> Team in the pinned schema)",
        SNAPSHOT, '"team": {\n          "args": {\n            "id": "String!"\n          },\n          "deprecated": false,\n          "type": "Team!"\n        }',
        '"team": {\n          "args": {\n            "id": "String!"\n          },\n          "deprecated": false,\n          "type": "Team"\n        }'))

    print("\nNegative control — an edit the guard must NOT punish:")
    # C8 — prose elsewhere must not move the guard. If it does, the guard is reading the wrong thing.
    results.append(control(
        "C8 editing an unrelated comment leaves the guard GREEN",
        NULLTEAM_GO, "// linear_null_team_test.go —", "// linear_null_team_test.go (comment touched by control C8) —",
        expect_red=False))

    print()
    if all(results):
        print(f"ALL {len(results)} CONTROLS PASS — the guard fails for each class it claims to cover, "
              "and stays green for one it does not.")
        return 0
    print("SOME CONTROLS FAILED — the guard does not do what this file says it does.")
    return 1


if __name__ == "__main__":
    sys.exit(main())
