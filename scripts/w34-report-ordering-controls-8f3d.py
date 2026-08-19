#!/usr/bin/env python3
"""Positive-control harness for W3.4 / tab-8f3d — THE TWO PRESENTATION `ORDER BY`s.

Takes tab-b9d7's handed-on lead (a): `GetWorkload`'s `ORDER BY open_issues DESC` and
`GetDistribution`'s `ORDER BY COUNT(*) DESC` (both paths) are unasserted, and the one
assertion that LOOKS like it covers distribution — `[D-ORDER]`, which reads
`byStatus[0]` — sits over a fixture whose INSERTION order is already count-descending.

Every control:
  * names its PREDICTED verdict BEFORE the run,
  * mutates ONE term of the shipped statement,
  * asserts the anchor matches EXACTLY ONCE before mutating (an anchor that matches
    twice would mutate a different report and report a false verdict — the trap #156
    recorded),
  * restores in a `finally` and verifies the sha256 back to pristine,
  * runs over the FULL analytics package so "caught by nothing else" is MEASURED.

Usage:  python3 scripts/w34-report-ordering-controls-8f3d.py [--baseline-only]
"""

import hashlib
import os
import re
import subprocess
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
ENGINE = os.path.join(REPO, "internal", "analytics", "engine.go")
GUARD = os.path.join(REPO, "internal", "analytics", "report_ordering_realpg_test.go")
DSN = os.environ.get(
    "TRACK_TEST_DATABASE_URL",
    "postgres://postgres:postgres@localhost:55438/postgres?sslmode=disable",
)
PKG = os.environ.get("W34_PKG", "./internal/analytics/")


def sha(path):
    with open(path, "rb") as fh:
        return hashlib.sha256(fh.read()).hexdigest()


def run_tests():
    env = dict(os.environ, TRACK_TEST_DATABASE_URL=DSN)
    p = subprocess.run(
        ["go", "test", "-timeout", "300s", "-race", "-count=1", PKG],
        cwd=REPO, env=env, capture_output=True, text=True,
    )
    return p.returncode, p.stdout + p.stderr


TAGS = re.compile(r"\[([A-Z0-9-]+)\]")

# Go prints a t.Logf line and a t.Errorf line in the SAME shape (`file.go:NN: text`), and a failing
# test's LOGS are printed too — so a naive scan for [TAGS] scores a log line that names a tag as an
# assertion that fired. MEASURED: the first run of this harness reported [R-PREMISE-DIST],
# [R-PREMISE-LABEL] and [R-PREMISE-WORKLOAD] as caught by all five controls when not one of them had
# fired. Test logs that are NOT failures carry a `note:` prefix; this skips them. The rule is
# generic (a prefix, not a sentence) and control O8 is the positive control on THIS function: it
# breaks the fixture's own premise and requires the premise tag to be reported.
NOTE_LINE = re.compile(r"^\s*\S+\.go:\d+:\s*note:", re.M)


def failing_tags(out):
    """Which bracketed assertion tags fired, in first-seen order — logs excluded."""
    seen, order = set(), []
    for line in out.splitlines():
        if NOTE_LINE.match(line):
            continue
        for tag in TAGS.findall(line):
            if tag not in seen:
                seen.add(tag)
                order.append(tag)
    return order


def failing_tests(out):
    return sorted(set(re.findall(r"^\s*--- FAIL: (\S+)", out, re.M)))


# (id, target-file, prediction, anchor, replacement, note)
CONTROLS = [
    (
        "O1",
        ENGINE,
        "CAUGHT",
        "        GROUP BY %s\n        ORDER BY COUNT(*) DESC`, col, col)",
        "        GROUP BY %s\n        ORDER BY COUNT(*) ASC`, col, col)",
        "distribution (status/priority/assignee/team) ranked the WRONG WAY. The fingerprint "
        "regexes on this path are `GROUP BY status` / `GROUP BY priority`, which this leaves "
        "byte-identical, so a red can only be an assertion.",
    ),
    (
        "O2",
        ENGINE,
        "CAUGHT",
        "        GROUP BY label\n        ORDER BY COUNT(*) DESC`,",
        "        GROUP BY label\n        ORDER BY COUNT(*) ASC`,",
        "the UNNEST label path carries its OWN ORDER BY — a separate statement, so a separate "
        "control and a separate assertion.",
    ),
    (
        "O3",
        ENGINE,
        "CAUGHT",
        "        ORDER BY open_issues DESC`, teamSQL),",
        "        ORDER BY open_issues ASC`, teamSQL),",
        "workload ranked the WRONG WAY — the busiest member last. Fingerprint is "
        "`JOIN members m ON m.id = i.assignee_id`, left byte-identical.",
    ),
    (
        "O4",
        ENGINE,
        "CAUGHT",
        "        GROUP BY %s\n        ORDER BY COUNT(*) DESC`, col, col)",
        "        GROUP BY %s`, col, col)",
        "distribution's clause DELETED rather than reversed — the shape a refactor produces. "
        "Whether this is visible is a fact about the PLAN, not the product; measured, not assumed.",
    ),
    (
        "O5",
        ENGINE,
        "CAUGHT",
        "        ORDER BY open_issues DESC`, teamSQL),",
        "`, teamSQL),",
        "workload's clause DELETED. Same question as O4 for the other report.",
    ),
    (
        "O6",
        ENGINE,
        "NOT CAUGHT (void)",
        "        ORDER BY open_issues DESC`, teamSQL),",
        "        ORDER BY open_issues DESC, m.id`, teamSQL),",
        "VOID CONTROL — a tie-break appended. Semantics unchanged on any fixture with distinct "
        "open_issues counts; if this reds, the guard is pinning statement text rather than order.",
    ),
    (
        "O7",
        ENGINE,
        "CAUGHT-ELSEWHERE",
        "COUNT(*) FILTER (WHERE i.status NOT IN ('done','cancelled')) AS open_issues",
        "COUNT(*) FILTER (WHERE i.status NOT IN ('done')) AS open_issues",
        "MUST-STAY-GREEN companion for the ordering assertions: a COUNTING term of the same "
        "statement. It must be caught by the pre-existing counting test and NOT be the thing "
        "that makes an ordering assertion red — otherwise the ordering guard is a catch-all.",
    ),
    (
        "O8",
        GUARD,
        "CAUGHT ([R-PREMISE-*])",
        "var ordCohorts = []ordCohort{\n\t{status: \"backlog\", label: \"ord_one\", memberID: \"ord-m-a\", count: 1},\n"
        "\t{status: \"todo\", label: \"ord_two\", memberID: \"ord-m-b\", count: 2},\n"
        "\t{status: \"in_review\", label: \"ord_three\", memberID: \"ord-m-c\", count: 3},\n"
        "\t{status: \"in_progress\", label: \"ord_four\", memberID: \"ord-m-d\", count: 4},\n}",
        "var ordCohorts = []ordCohort{\n\t{status: \"backlog\", label: \"ord_one\", memberID: \"ord-m-a\", count: 4},\n"
        "\t{status: \"todo\", label: \"ord_two\", memberID: \"ord-m-b\", count: 3},\n"
        "\t{status: \"in_review\", label: \"ord_three\", memberID: \"ord-m-c\", count: 2},\n"
        "\t{status: \"in_progress\", label: \"ord_four\", memberID: \"ord-m-d\", count: 1},\n}",
        "THE CONTROL ON THE GUARD ITSELF, and on this harness's reporter. It seeds the cohorts "
        "count-DESCENDING — the pre-existing fixture's shape — with the PRODUCT UNTOUCHED. The "
        "[R-PREMISE-*] checks MUST red as a broken fixture; if they stay green the premise check is "
        "inert and every ordering assertion in that file is vacuous with no way to tell. It is also "
        "the positive control on failing_tags(): the premise tags must be REPORTED here and must "
        "NOT be reported by O1-O5, where they only ever appeared as logs.",
    ),
    (
        "O9",
        GUARD,
        "CAUGHT ([R-PREMISE-DIST])",
        "var ordCohorts = []ordCohort{\n\t{status: \"backlog\", label: \"ord_one\", memberID: \"ord-m-a\", count: 1},\n"
        "\t{status: \"todo\", label: \"ord_two\", memberID: \"ord-m-b\", count: 2},\n"
        "\t{status: \"in_review\", label: \"ord_three\", memberID: \"ord-m-c\", count: 3},\n"
        "\t{status: \"in_progress\", label: \"ord_four\", memberID: \"ord-m-d\", count: 4},\n}",
        "var ordCohorts = []ordCohort{\n\t{status: \"backlog\", label: \"ord_one\", memberID: \"ord-m-a\", count: 4},\n"
        "\t{status: \"todo\", label: \"ord_two\", memberID: \"ord-m-b\", count: 1},\n"
        "\t{status: \"in_review\", label: \"ord_three\", memberID: \"ord-m-c\", count: 2},\n"
        "\t{status: \"in_progress\", label: \"ord_four\", memberID: \"ord-m-d\", count: 3},\n}",
        "POSITIVE CONTROL ON THE DISTRIBUTION PREMISE, product untouched. That statement comes back "
        "ALPHABETICAL by group key (backlog, in_progress, in_review, todo) — measured, not read — so "
        "this assigns 4/3/2/1 along THAT order. [R-PREMISE-DIST] must red as a broken fixture; if it "
        "stays green the dist probe is inert and [R-DIST-ORDER] proves nothing.",
    ),
    (
        "O10",
        GUARD,
        "CAUGHT ([R-PREMISE-LABEL])",
        "var ordCohorts = []ordCohort{\n\t{status: \"backlog\", label: \"ord_one\", memberID: \"ord-m-a\", count: 1},\n"
        "\t{status: \"todo\", label: \"ord_two\", memberID: \"ord-m-b\", count: 2},\n"
        "\t{status: \"in_review\", label: \"ord_three\", memberID: \"ord-m-c\", count: 3},\n"
        "\t{status: \"in_progress\", label: \"ord_four\", memberID: \"ord-m-d\", count: 4},\n}",
        "var ordCohorts = []ordCohort{\n\t{status: \"backlog\", label: \"ord_one\", memberID: \"ord-m-a\", count: 1},\n"
        "\t{status: \"todo\", label: \"ord_two\", memberID: \"ord-m-b\", count: 3},\n"
        "\t{status: \"in_review\", label: \"ord_three\", memberID: \"ord-m-c\", count: 4},\n"
        "\t{status: \"in_progress\", label: \"ord_four\", memberID: \"ord-m-d\", count: 2},\n}",
        "POSITIVE CONTROL ON THE LABEL PREMISE, product untouched. The UNNEST statement comes back in "
        "a hash permutation (ord_three, ord_two, ord_four, ord_one), so this assigns 4/3/2/1 along "
        "THAT order. It must red [R-PREMISE-LABEL] and must NOT red [R-PREMISE-DIST] — the two probes "
        "are disarmed by different assignments, which is the measurement the header records.",
    ),
]


def main():
    baseline_only = "--baseline-only" in sys.argv
    pristine = sha(ENGINE)
    print(f"engine.go sha256 (pristine) = {pristine}")
    print(f"DSN = {DSN}\npkg = {PKG}\n")

    code, out = run_tests()
    print(f"C0 BASELINE (no mutation): {'GREEN' if code == 0 else 'RED'}")
    if code != 0:
        print(out[-4000:])
        print("BASELINE IS RED — every verdict below would be confounded. Stopping.")
        return 1
    if baseline_only:
        return 0

    pristine_guard = sha(GUARD)
    print(f"report_ordering_realpg_test.go sha256 (pristine) = {pristine_guard}")
    originals = {}
    for path in (ENGINE, GUARD):
        with open(path, encoding="utf-8") as fh:
            originals[path] = fh.read()
    pristines = {ENGINE: pristine, GUARD: pristine_guard}

    results = []
    for cid, target, predicted, anchor, replacement, note in CONTROLS:
        original = originals[target]
        n = original.count(anchor)
        print(f"\n=== {cid} — PREDICTED: {predicted} ===\n    {note}")
        if n != 1:
            print(f"    !! ANCHOR MATCHES {n} TIMES, refusing to mutate (would report a false verdict)")
            results.append((cid, predicted, f"ANCHOR x{n}", []))
            continue
        try:
            with open(target, "w", encoding="utf-8") as fh:
                fh.write(original.replace(anchor, replacement))
            code, out = run_tests()
            tags = failing_tags(out)
            tests = failing_tests(out)
            verdict = "CAUGHT" if code != 0 else "NOT CAUGHT"
            print(f"    ACTUAL: {verdict}")
            print(f"    tags:  {tags if tags else '(none)'}")
            print(f"    tests: {tests if tests else '(none)'}")
            results.append((cid, predicted, verdict, tags))
        finally:
            with open(target, "w", encoding="utf-8") as fh:
                fh.write(original)
            back = sha(target)
            assert back == pristines[target], (
                f"{cid}: {os.path.basename(target)} NOT restored ({back} != {pristines[target]})")

    print("\n" + "=" * 78)
    print("SUMMARY (predicted -> actual)")
    for cid, predicted, verdict, tags in results:
        print(f"  {cid}: {predicted:20s} -> {verdict:12s} {tags}")
    print(f"engine.go sha256 (final) = {sha(ENGINE)}  (pristine = {pristine})")
    print(f"guard    sha256 (final) = {sha(GUARD)}  (pristine = {pristine_guard})")
    return 0


if __name__ == "__main__":
    sys.exit(main())
