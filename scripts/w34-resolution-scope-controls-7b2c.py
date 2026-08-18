#!/usr/bin/env python3
"""Positive controls for TestAnalytics_Resolution_WorkspaceScoped (W3.4, tab-7b2c).

Every control names its PREDICTED catcher before it runs, mutates one term, runs the FULL
suite, and restores the file in a `finally` with a sha256 comparison — a control that leaves
the tree mutated has poisoned every measurement after it.

WHY THE FULL SUITE AND NOT JUST ./internal/analytics: "only the new guard catches this" is a
claim about the WHOLE repo, and this repo has tenancy locks and authz sweeps outside the
analytics package that could plausibly cover the same predicate. Running the package alone
would let me assert exclusivity I had not measured.

⚠ internal/importer IS RED ON THIS MACHINE BEFORE ANY MUTATION AND IT IS NOT MINE. Its corpus
census tests read /tmp/w34-jira-corpus and /tmp/w34-linear-corpus-cache; both directories
EXIST but are EMPTY, so the tests correctly refuse to skip (the skip is keyed on the directory
being ABSENT, which is CI's case) and fail closed rather than reporting a clean answer from an
instrument that read nothing. Measured identical — 11 failures — on CLEAN main 48d9dee with
the working tree stashed. This harness therefore counts failures OUTSIDE internal/importer and
says so on every line; the merge gate remains CI, where those directories do not exist.
"""

import hashlib
import io
import json
import os
import re
import subprocess
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
ENGINE = os.path.join(REPO, "internal/analytics/engine.go")
SCOPE_TEST = os.path.join(REPO, "internal/analytics/scope_read_test.go")
DSN = os.environ.get(
    "TRACK_TEST_DATABASE_URL",
    "postgres://postgres:postgres@localhost:55432/postgres?sslmode=disable",
)

NEW_TEST = "TestAnalytics_Resolution_WorkspaceScoped"

# The report runs TWO independently-scoped queries. Anchors verified unique before use.
Q1_ANCHOR = "* $2::int)%s`, teamSQL),"          # the aggregate row (sample_size + percentiles)
Q2_ANCHOR = "* $2::int)%s\n        GROUP BY priority"  # the per-priority breakdown
SCOPE = "WHERE workspace_id = $1"


def sha(path):
    return hashlib.sha256(io.open(path, "rb").read()).hexdigest()


def read(path):
    return io.open(path, encoding="utf-8").read()


def write(path, s):
    io.open(path, "w", encoding="utf-8").write(s)


def mutate_nth_scope(src, which, replacement):
    """Replace the scope predicate of the 1st or 2nd resolution query only."""
    anchor = Q1_ANCHOR if which == 1 else Q2_ANCHOR
    idx = src.index(anchor)
    start = src.rindex(SCOPE, 0, idx)
    return src[:start] + replacement + src[start + len(SCOPE):]


ENV_PKG = "github.com/talyvor/track/internal/importer"


def run_suite():
    """Full suite. Returns (failures outside the environmental package, count inside it, raw).

    ⚠ THE ATTRIBUTION IS READ FROM `go test -json`'s Package FIELD, NOT GUESSED FROM THE TEST
    NAME OR ITS MESSAGE. I wrote this classifier twice by pattern-matching first the name
    (/Corpus|Census/, which misfiled TestJiraCSVLayoutSupport_… and reported 10 of 11) and then
    the failure text ("read nothing", which reported 5 of 11). Both looked plausible and both
    were wrong, in the direction that INVENTS a real failure out of an environmental one — the
    same shape as a census whose population boundary is assumed rather than measured. The
    package is a fact the toolchain already reports; nothing here needs to infer it.
    """
    env = dict(os.environ, TRACK_TEST_DATABASE_URL=DSN)
    p = subprocess.run(
        ["go", "test", "-json", "-timeout", "120s", "-race", "-count=1", "./..."],
        cwd=REPO, env=env, capture_output=True, text=True, timeout=1200,
    )
    out = p.stdout + p.stderr
    inside, outside = [], []
    for line in p.stdout.splitlines():
        try:
            ev = json.loads(line)
        except ValueError:
            continue
        if ev.get("Action") != "fail" or not ev.get("Test"):
            continue
        if "/" in ev["Test"]:  # subtest; its parent already counts
            continue
        (inside if ev.get("Package") == ENV_PKG else outside).append(ev["Test"])
    return outside, len(inside), out


def control(name, prediction, mutator, expect_caught):
    print(f"\n=== {name} ===")
    print(f"  PREDICTION (stated before running): {prediction}")
    before = {ENGINE: sha(ENGINE), SCOPE_TEST: sha(SCOPE_TEST)}
    originals = {p: read(p) for p in before}
    try:
        mutator()
        other, imp, out = run_suite()
        caught = NEW_TEST in other
        print(f"  internal/importer (environmental, pre-existing, by -json Package): {imp}")
        print(f"  failures outside importer: {other if other else 'NONE'}")
        verdict = "CAUGHT" if caught else "NOT CAUGHT"
        ok = caught == expect_caught
        print(f"  RESULT: {verdict} by {NEW_TEST}  -> prediction {'MATCHED' if ok else '*** MISMATCH ***'}")
        if not ok:
            print("  ---- suite tail ----")
            print("\n".join(out.splitlines()[-25:]))
        return ok, other
    finally:
        for p, s in originals.items():
            write(p, s)
        for p, h in before.items():
            assert sha(p) == h, f"RESTORE FAILED for {p}"
        print("  restored, sha256 verified")


def main():
    results = []

    # C0 — baseline. A guard that is green here and green everywhere is not a guard.
    print("\n=== C0 (baseline, no mutation) ===")
    other, imp, _ = run_suite()
    print(f"  internal/importer (environmental, pre-existing, by -json Package): {imp}")
    print(f"  failures outside importer: {other if other else 'NONE'}")
    results.append(("C0 baseline green outside importer", not other))

    # C1 — the defect this test exists for: the aggregate query's workspace scope neutralised.
    ok, _ = control(
        "C1  aggregate query scope neutralised (workspace_id = $1 OR TRUE)",
        f"CAUGHT by {NEW_TEST} — sample_size becomes 1 for a wsA caller naming a wsB team",
        lambda: write(ENGINE, mutate_nth_scope(read(ENGINE), 1, "WHERE (workspace_id = $1 OR TRUE)")),
        expect_caught=True,
    )
    results.append(("C1 aggregate scope", ok))

    # C2 — the SECOND scoped query alone. This is why one assertion was not enough.
    ok, _ = control(
        "C2  per-priority query scope neutralised ALONE",
        f"CAUGHT by {NEW_TEST} — via the by_priority assertion ONLY; sample_size does not move",
        lambda: write(ENGINE, mutate_nth_scope(read(ENGINE), 2, "WHERE (workspace_id = $1 OR TRUE)")),
        expect_caught=True,
    )
    results.append(("C2 per-priority scope", ok))

    # C3 — THE VACUITY CONTROL. Rewrite the assertions in the shape the two SIBLING tests use
    # (a canary STRING absent from the body) and put C1's real defect underneath. If this is
    # CAUGHT, the sibling shape would have worked here and my cohort-size argument is wrong.
    def c3():
        write(ENGINE, mutate_nth_scope(read(ENGINE), 1, "WHERE (workspace_id = $1 OR TRUE)"))
        src = read(SCOPE_TEST)
        start = src.index("\tgot := decode(t, rr)")
        end = src.index("\t// Positive, and NOT decoration")
        canary = (
            '\tif strings.Contains(rr.Body.String(), "RES-1") ||\n'
            '\t\tstrings.Contains(rr.Body.String(), "resolution 1") {\n'
            '\t\tt.Fatalf("CROSS-WS LEAK: %s", rr.Body.String())\n'
            "\t}\n"
            "\t_ = decode\n\n"
        )
        write(SCOPE_TEST, src[:start] + canary + src[end:])

    ok, _ = control(
        "C3  VACUITY: sibling-shaped canary-string assertion, WITH C1's defect underneath",
        f"NOT CAUGHT — ResolutionStats has no string field, so the canary is absent either way",
        c3,
        expect_caught=False,
    )
    results.append(("C3 vacuity of the sibling shape", ok))

    # C4 — must-stay-green companion. A real behaviour change that is a COHORT question, not a
    # SCOPE question. Without this, "CAUGHT" could just mean the test reds at any edit.
    ok, other4 = control(
        "C4  MUST-STAY-GREEN: report window doubled ($2::int -> $2::int * 2)",
        f"NOT CAUGHT by {NEW_TEST} — both seeded rows sit hours inside either window",
        lambda: write(ENGINE, read(ENGINE).replace(
            "AND created_at > NOW() - (INTERVAL '1 day' * $2::int)%s`, teamSQL),",
            "AND created_at > NOW() - (INTERVAL '1 day' * $2::int * 2)%s`, teamSQL),")),
        expect_caught=False,
    )
    results.append(("C4 must-stay-green (window)", ok))

    # C5 — the OTHER direction. Every leak assertion above is satisfied by an empty answer, so
    # the positive half has to be controlled too or the whole test passes on a dead query.
    ok, _ = control(
        "C5  scope broken the other way (workspace_id = $1 AND FALSE) — own rows vanish",
        f"CAUGHT by {NEW_TEST} — via the POSITIVE half only (sample_size 1 -> 0)",
        lambda: write(ENGINE, mutate_nth_scope(read(ENGINE), 1, "WHERE workspace_id = $1 AND FALSE")),
        expect_caught=True,
    )
    results.append(("C5 positive half", ok))

    print("\n================ SUMMARY ================")
    for name, ok in results:
        print(f"  {'PASS' if ok else 'FAIL'}  {name}")
    bad = [n for n, ok in results if not ok]
    print("=========================================")
    if bad:
        print(f"MISMATCHES: {bad}")
        sys.exit(1)
    print("all predictions matched")


if __name__ == "__main__":
    main()
