#!/usr/bin/env python3
"""W3.4 / tab-9a7c — POSITIVE CONTROLS for internal/analytics/aicost_counting_realpg_test.go.

The new file PASSED ON ITS FIRST RUN, so it is controlled both ways before it is believed. Each
control names its PREDICTED verdict and predicted catching TAG before it runs, is restored in a
`finally`, and the mutated file is sha256-verified back to pristine every time.

    A1..A4  the four BLIND terms the (B) census found, restored one at a time. Each must be
            CAUGHT, and by exactly ONE tag — a tag with two catchers is justified by neither.
    A5,A6   must-stay-green. Ordering is aicost_ordering_realpg_test.go's subject and this file
            must not quietly claim it; without these, "CAUGHT" is a catch-all.
    A7,A8   controls on the FIXTURE's own premise probes: break the property, the probe must red.
            A premise probe that cannot fail is the thing that let these four terms stay blind
            through five merges of "the aggregate is covered".
    A9      the blinding control — [A-AVG-COHORT]'s assertion removed with A1's defect on top,
            run over the WHOLE import closure of engine.go. It must be NOT CAUGHT: that is the
            measured blindness this file exists to close, re-run here rather than cited.

⚠ WHY THE TAGS CAN BE SCRAPED AT ALL, stated because tab-8f3d shipped a harness that could not
tell a log from a failure (Go prints `t.Logf` and `t.Errorf` identically, and the error ran toward
crediting guards that never fired). This script ASSERTS that the file under test contains ZERO
`t.Logf` before it scrapes anything, and it takes the pass/fail verdict from go test's exit status
rather than from the presence of a tag.

Usage:  TRACK_TEST_DATABASE_URL=... python3 scripts/w34-aicost-arithmetic-controls-9a7c.py
"""

import hashlib
import os
import re
import subprocess
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
ENGINE = os.path.join(REPO, "internal", "analytics", "engine.go")
TEST = os.path.join(REPO, "internal", "analytics", "aicost_counting_realpg_test.go")
TESTNAME = "TestGetAICostTrends_TheSQLsOwnArithmetic_RealPG"
DSN = os.environ.get(
    "TRACK_TEST_DATABASE_URL",
    "postgres://postgres:postgres@127.0.0.1:55471/postgres?sslmode=disable",
)
CLOSURE = ["./internal/analytics/", "./internal/importer/", "./internal/mcp/", "./cmd/track/"]


def sha(path):
    with open(path, "rb") as fh:
        return hashlib.sha256(fh.read()).hexdigest()


def run(args):
    env = dict(os.environ, TRACK_TEST_DATABASE_URL=DSN)
    p = subprocess.run(["go", "test", "-timeout", "600s", "-count=1"] + args,
                       cwd=REPO, env=env, capture_output=True, text=True)
    return p.returncode, p.stdout + p.stderr


def run_this_test():
    return run(["-run", TESTNAME, "./internal/analytics/"])


def tags(out):
    """The tag a FAILURE LINE OPENS WITH — not every tag that appears in the output.

    ⚠ THE FIRST SPELLING OF THIS WAS `findall` OVER THE WHOLE OUTPUT AND IT WAS WRONG IN THE WAY
    THIS REPOSITORY KEEPS FINDING. Several assertions NAME another tag in their prose (the
    day-split premise says "[A-DAY-KEY] below cannot fail"), so a bare findall counted a SENTENCE
    ABOUT a tag as that tag firing, and A7/A8 scored two catchers each. That is the same off-by-one
    the (A) census header records — `grep -c 'ExpectQuery('` counting a comment that quotes the
    call — and the direction of the error is always "more coverage than there is".

    Go prints an assertion as `    file_test.go:NNN: [TAG] message`, so the tag that OPENED the
    line is the one that fired.
    """
    return sorted(set(re.findall(r"\.go:\d+: (\[A-[A-Z0-9-]+\])", out)))


# (id, file, old, new, predicted verdict, predicted tags, why)
CONTROLS = [
    ("A1", ENGINE,
     "SELECT COALESCE(SUM(ai_cost_usd), 0), COUNT(*) FILTER (WHERE ai_cost_usd > 0)",
     "SELECT COALESCE(SUM(ai_cost_usd), 0), COUNT(*)",
     "CAUGHT", ["[A-AVG-COHORT]"],
     "engine_test.go:187 — avg divides by every issue in the window"),
    ("A2", ENGINE,
     "SELECT date_trunc('day', updated_at) AS day,",
     "SELECT date_trunc('day', created_at) AS day,",
     "CAUGHT", ["[A-DAY-KEY]"],
     "engine_test.go:191 — the series is keyed off a column the window does not filter"),
    ("A3", ENGINE,
     "SELECT t.id, t.name, COALESCE(SUM(i.ai_cost_usd), 0)",
     "SELECT t.id, t.name, COALESCE(MAX(i.ai_cost_usd), 0)",
     "CAUGHT", ["[A-TEAM-SUM]"],
     "engine_test.go:207 — a team's cost becomes its most expensive issue"),
    ("A4", ENGINE,
     "SELECT label, COALESCE(SUM(ai_cost_usd), 0)",
     "SELECT label, COALESCE(MAX(ai_cost_usd), 0)",
     "CAUGHT", ["[A-LABEL-SUM]"],
     "engine_test.go:212 — a label's cost becomes its most expensive issue"),
    ("A5", ENGINE,
     "ORDER BY SUM(i.ai_cost_usd) DESC NULLS LAST",
     "ORDER BY SUM(i.ai_cost_usd) ASC NULLS LAST",
     "NOT CAUGHT", [],
     "cost_by_team ranked backwards — ORDERING, which is another file's subject"),
    ("A6", ENGINE,
     "ORDER BY ai_cost_usd DESC LIMIT 10",
     "ORDER BY ai_cost_usd ASC LIMIT 10",
     "NOT CAUGHT", [],
     "the leaderboard ranked backwards — #160's subject, not this file's"),
    ("A7", TEST,
     'seedArithCostIssue(t, d, ws.ID, eng.ID, 7, 2.00, []string{"shared"}, bornLongBefore, touched)',
     'seedArithCostIssue(t, d, ws.ID, eng.ID, 7, 2.00, []string{"shared"}, touched, touched)',
     "CAUGHT", ["[A-PREMISE-DAYSPLIT]"],
     "FIXTURE: no issue has a created_at day unlike its updated_at day — [A-DAY-KEY] would then "
     "be unfalsifiable, and the probe must say so"),
    ("A8", TEST,
     'seedArithCostIssue(t, d, ws.ID, eng.ID, 5, 0.00, []string{"free"}, touched, touched)\n'
     '\tseedArithCostIssue(t, d, ws.ID, eng.ID, 6, 0.00, []string{"free"}, touched, touched)',
     'seedArithCostIssue(t, d, ws.ID, eng.ID, 5, 0.01, []string{"free"}, touched, touched)\n'
     '\tseedArithCostIssue(t, d, ws.ID, eng.ID, 6, 0.01, []string{"free"}, touched, touched)',
     "CAUGHT", ["[A-PREMISE-COHORT]"],
     "FIXTURE: every issue now costs something, so the two denominators are the same integer and "
     "[A-AVG-COHORT] would be unfalsifiable — the probe must say so BEFORE the assertion runs"),
]


def apply(path, old, new):
    with open(path, encoding="utf-8") as fh:
        src = fh.read()
    n = src.count(old)
    if n != 1:
        return None, f"anchor occurs {n}x, want 1"
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(src.replace(old, new))
    return src, ""


def main():
    pristine = {ENGINE: sha(ENGINE), TEST: sha(TEST)}

    with open(TEST, encoding="utf-8") as fh:
        tf = fh.read()
    logf = tf.count("t.Logf")
    print(f"INSTRUMENT PRECONDITION: `t.Logf` in the file under test = {logf} "
          f"({'ok — a tag can only come from a failure' if logf == 0 else '!! TAGS ARE NOT EVIDENCE'})")
    if logf:
        return 1

    code, out = run_this_test()
    print(f"\nC0 the new test on the PRISTINE product: {'GREEN' if code == 0 else 'RED'}")
    if code != 0:
        print(out[-3000:])
        return 1

    # C0' — the closure's pre-existing failure SET, measured on the pristine tree. A9 subtracts
    # this rather than pattern-matching test names.
    _, cout = run(CLOSURE)
    baseline = sorted(set(re.findall(r"^\s*--- FAIL: (\S+)", cout, re.M)))
    print(f"C0' import-closure baseline failures (empty corpus dirs): {len(baseline)}")

    ok = True
    for cid, path, old, new, want, wanttags, why in CONTROLS:
        print(f"\n=== {cid}  predicted {want:<10s} by {wanttags if wanttags else '(nothing)'}")
        print(f"    {why}")
        src, err = apply(path, old, new)
        if src is None:
            print(f"    !! {err}")
            ok = False
            continue
        try:
            code, out = run_this_test()
            got = "CAUGHT" if code != 0 else "NOT CAUGHT"
            gottags = tags(out)
            match = got == want and gottags == wanttags
            ok = ok and match
            print(f"    -> {got:<10s} tags {gottags if gottags else '(none)'}  "
                  f"{'OK' if match else '!! PREDICTION MISS'}")
        finally:
            with open(path, "w", encoding="utf-8") as fh:
                fh.write(src)
            back = sha(path)
            assert back == pristine[path], f"{path} NOT restored ({back} != {pristine[path]})"

    # A9 — the blinding control, over the WHOLE import closure rather than this one test.
    print("\n=== A9  predicted NOT CAUGHT anywhere in the import closure")
    print("    [A-AVG-COHORT]'s assertion removed AND A1's defect applied — the measured blindness")
    esrc, err = apply(ENGINE, CONTROLS[0][2], CONTROLS[0][3])
    tsrc = None
    try:
        if esrc is None:
            print(f"    !! {err}")
            ok = False
        else:
            with open(TEST, encoding="utf-8") as fh:
                tsrc = fh.read()
            start = tsrc.index("\tif !arithNearly(out.AvgCostPerIssue, wantAvg) {")
            end = tsrc.index("\t// ── [A-DAY-KEY]")
            with open(TEST, "w", encoding="utf-8") as fh:
                fh.write(tsrc[:start] + "\n" + tsrc[end:])
            code, out = run(CLOSURE)
            reds = sorted(set(re.findall(r"^\s*--- FAIL: (\S+)", out, re.M)))
            other = [r for r in reds if r not in baseline]
            print(f"    -> {'NOT CAUGHT' if not other else 'CAUGHT'}   "
                  f"reds C0 did not already have: {other if other else '(none)'}")
            print(f"       ({len(baseline)} pre-existing empty-corpus importer failures subtracted "
                  f"from a MEASURED C0, not guessed from their names — the first spelling filtered "
                  f"on 'Corpus'/'Census' in the test name and scored "
                  f"TestJiraCSVLayoutSupport_EveryPinnedLayoutHasADistinctExportBehindIt as a real "
                  f"red, which would have read as 'the blindness is already covered')")
            ok = ok and not other
    finally:
        if esrc is not None:
            with open(ENGINE, "w", encoding="utf-8") as fh:
                fh.write(esrc)
        if tsrc is not None:
            with open(TEST, "w", encoding="utf-8") as fh:
                fh.write(tsrc)
        for p, want in pristine.items():
            back = sha(p)
            assert back == want, f"{p} NOT restored ({back} != {want})"

    print("\n" + "=" * 78)
    print("ALL CONTROLS AS PREDICTED" if ok else "!! AT LEAST ONE PREDICTION MISSED — read it")
    for p, want in pristine.items():
        print(f"  {os.path.basename(p)} sha256 = {sha(p)}  (pristine {want})")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
