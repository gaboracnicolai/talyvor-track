#!/usr/bin/env python3
"""Positive controls for "an empty resolution cohort is not a measured zero" (W3.4, after #92).

THE FINDING. analytics.GetTimeToResolution windows on `created_at`, clamped to maxWindowDays
(365). Only an IMPORT writes a created_at from years ago — a native Track issue is created in
Track and is young by construction. MEASURED on a real Jira Cloud project (hibernate.atlassian.net,
project HHH, anonymous, whole-population counts from POST /rest/api/3/search/approximate-count —
scripts/w34-analytics-window-probe.py): 18,267 resolved issues, 756 of them (4.1%) created inside
the cap. The other 17,511 import correctly, carry real completion times, and cannot appear in that
report at ANY window a caller may ask for. Every aggregate in the query is wrapped in
COALESCE(..., 0), so the report answered that with `0` in all four fields and said nothing about
the cohort: a workspace holding a correctly-imported resolved backlog and a workspace holding NO
ISSUES AT ALL produced BYTE-IDENTICAL reports.

⚠ THE WINDOW ITSELF IS NOT CHANGED HERE. Whether it should key on completed_at, and whether 365
days is right for a workspace whose history was imported, are product decisions with the numbers
above attached — written up in the queue, not made in a session. What is merged is the cohort
size, so a zero that was never measured stops being served as one that was.

TestResolutionReport_AnImportedBacklogOlderThanTheWindowIsNotAMeasuredZero was RED before the fix,
at `analytics_window_job_test.go:208` — "the resolution report carries no \"sample_size\" field",
with the body it printed being exactly the shipped five keys. That is necessary and not
sufficient. Each control below removes exactly one thing and NAMES THE ASSERTION THAT MUST SPEAK,
predicted before the run.

⚠ THE GUARD READS THE WIRE, NOT THE STRUCT, AND C1 IS WHAT MAKES THAT A FACT rather than a
comment. It decodes the HTTP body into a map: a field absent from the JSON is INVISIBLE to a
struct decode (it silently zeroes) and plainly missing from a map. C1 sets the json tag to "-" —
the struct keeps the field, the engine keeps computing it, and the client receives nothing.

⚠ C1's MUST-GREEN LIST IS THE NON-REDUNDANCY CLAIM. With the field off the wire the entire
pre-existing analytics suite stays green, INCLUDING the one test that asserts SampleSize == 7 —
because that test reads the struct. Nothing in this repo but this guard can see the difference
between a field that is computed and a field a client receives.

⚠ C2 IS THE HALF-FIX A REVIEWER WOULD ACCEPT: count the workspace's resolved issues instead of
the cohort the four numbers were computed over. It reads as "sample_size = how much data you
have", which is the friendlier sentence and the wrong one — it would report 17,511 next to a
median computed from 756 rows.

⚠ C3 AND C4 EARN THE PREMISE ASSERTIONS. This guard's vacuity mode is NOT "the fixture was empty"
— it is "the import never landed" and "the row is not actually outside the window", either of
which would make every zero below true for the wrong reason. C3 empties the fixture; C4 blinds the
store's created_at gate so the row lands at the import instant, INSIDE the window.

⚠ C4 IS ALSO CAUGHT BY A PRE-EXISTING TEST AND THAT IS STATED, NOT HIDDEN. Blinding created_at
reds TestJobRow_JiraCSV_ImportedIssueKeepsTheDateJiraOpenedIt (#83's guard) as well, so C4 is NOT
evidence that this guard is non-redundant — C1 and C2 are. C4's job is to name WHICH assertion in
THIS test speaks, i.e. that the premise fires before the finding does.

⚠ C5 IS AN INVERTED CONTROL AND MUST STAY GREEN. `COUNT(*)` → `COUNT(1)` is semantically
identical; a guard that reds on any edit to the statement is not measuring the thing it names.

⚠ THE RUNNER AND ITS VERDICT LOGIC ARE CARRIED OVER FROM #86–#92's control scripts — a build
failure is never CAUGHT, a `-run` pattern matching nothing is never a pass, and every must-red
verdict names the file:line of the assertion that spoke. The logic is duplicated here rather than
imported, and it brings NONE of those runs' evidence with it: every verdict below is from this
campaign's own run.

⚠ THE BASELINE GATE IS LOAD-BEARING. Without TRACK_TEST_DATABASE_URL every real-Postgres control
here would SKIP, `go test` would exit 0, and this script would report a clean sweep of controls
that never ran.

    TRACK_TEST_DATABASE_URL=... python3 scripts/w34-analytics-window-controls.py
"""
import hashlib
import os
import pathlib
import re
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent

IMP = "./internal/importer/"
ANA = "./internal/analytics/"

ENGINE = "internal/analytics/engine.go"
TEST = "internal/importer/analytics_window_job_test.go"
CSV = "internal/importer/csv.go"
STORE = "internal/issue/store.go"

GUARD = "TestResolutionReport_AnImportedBacklogOlderThanTheWindowIsNotAMeasuredZero"

# Pre-existing companions. The first reads ResolutionStats through the STRUCT (so it cannot see a
# wire-level change); the other two are #83's created_at guards.
MOCK = "TestGetTimeToResolution_CalculatesMedianCorrectly"
CYCLE = "TestJobRow_JiraCSV_CycleTimeOfAnImportedIssueIsNotNegative"
KEEPS = "TestJobRow_JiraCSV_ImportedIssueKeepsTheDateJiraOpenedIt"

DATE_SUITE = [(CYCLE, IMP), (KEEPS, IMP)]
ANALYTICS_SUITE = [(MOCK, ANA)]

# (id, file, anchor, replacement, must_red, must_stay_green, note)
CONTROLS = [
    ("C1", ENGINE,
     '\tSampleSize  int                `json:"sample_size"`\n',
     '\tSampleSize  int                `json:"-"` // CONTROL\n',
     [(GUARD, IMP)], ANALYTICS_SUITE + DATE_SUITE,
     "THE FIX MADE INERT AT THE WIRE, in ONE edit: the engine still computes the cohort size and "
     "the struct still carries it — the client just never receives it. PREDICTED CATCHER, stated "
     "before the run: GUARD reds in num() at analytics_window_job_test.go:208, \"the resolution "
     "report carries no \\\"sample_size\\\" field\". The pre-existing analytics suite must stay "
     "GREEN, including the test asserting SampleSize == 7 — it reads the struct, which is exactly "
     "why nothing but this guard can see a field missing from the JSON."),

    ("C2", ENGINE,
     "            COUNT(*),\n",
     "            (SELECT COUNT(*) FROM issues i2 WHERE i2.workspace_id = $1"
     " AND i2.completed_at IS NOT NULL), -- CONTROL\n",
     [(GUARD, IMP)], ANALYTICS_SUITE + DATE_SUITE,
     "⚠ THE HALF-FIX A REVIEWER WOULD ACCEPT: report how many resolved issues the WORKSPACE holds "
     "rather than how many the four numbers were computed over. Every other assertion still "
     "passes — the in-window workspace reports 1 either way and the empty one reports 0 either "
     "way. PREDICTED CATCHER: GUARD reds at the out-of-window assertion, \"sample_size = 1 for a "
     "workspace whose issues are all outside the window, want 0\"."),

    ("C3", TEST,
     '"Imported from a real backlog,d,Closed,High,Fixed,%s,%s\\n"',
     # ⚠ NO TRAILING `// CONTROL` COMMENT HERE, and that is not tidiness. The anchor sits inside a
     # fmt.Sprintf argument list, so a comment after it swallows the following comma and the
     # package stops compiling — which is how this control first scored a CAUGHT it had not earned.
     '",d,Closed,High,Fixed,%s,%s\\n"',
     [(GUARD, IMP)], ANALYTICS_SUITE,
     "⚠ THE CONTROL THAT EARNS THE FIRST PREMISE. Empty the title: every row is refused, the "
     "import lands nothing, and a workspace with no rows reports zeros for the reason this test "
     "is NOT about. PREDICTED CATCHER: GUARD reds at PREMISE 1, \"0 imported rows with a "
     "completion time, want 1 — the import did not land\". The date suite is excluded from the "
     "must-green list only because it is unaffected, not because it is expected to red."),

    ("C4", STORE,
     "\t// path: Create, the MCP server, feature-board conversion and automation all leave it zero.\n"
     "\tvar createdAt *time.Time\n"
     "\tif !issue.CreatedAt.IsZero() && issue.CreatorID == model.ImporterCreatorID {\n",
     "\t// path: Create, the MCP server, feature-board conversion and automation all leave it zero.\n"
     "\tvar createdAt *time.Time\n"
     "\tif false && issue.CreatorID == model.ImporterCreatorID { // CONTROL\n",
     [(GUARD, IMP)], ANALYTICS_SUITE,
     "⚠ THE CONTROL THAT EARNS THE SECOND PREMISE — #83's defect restored at the STORE, in one "
     "edit, on Create only (the CSV write path; the Upsert copy is a separate block and is left "
     "alone). The provider's opening date is discarded, created_at DEFAULTs to the import "
     "instant, and the row lands INSIDE the window carrying a plausible timestamp. PREDICTED "
     "CATCHER: GUARD reds at PREMISE 1's age check, \"imported created_at is ~0 days old, want "
     "≈800 — a defaulted created_at would put the row INSIDE the window\". ⚠ THE DATE SUITE IS "
     "DELIBERATELY ABSENT FROM THE MUST-GREEN LIST: #83's own guards red here too, so this "
     "control is NOT a non-redundancy claim. C1 and C2 are."),

    ("C5", ENGINE,
     "            COUNT(*),\n",
     "            COUNT(1), -- CONTROL (inverted: semantically identical, must stay GREEN)\n",
     [], [(GUARD, IMP)] + ANALYTICS_SUITE + DATE_SUITE,
     "⚠ INVERTED CONTROL — NOTHING MAY RED. COUNT(1) counts exactly what COUNT(*) counts. A guard "
     "that reddens on any edit to the statement it watches is pinning bytes, not behaviour, and "
     "would report a working instrument for a mutation that changes nothing."),
]


def sha(path):
    return hashlib.sha256((ROOT / path).read_bytes()).hexdigest()


ASSERTION = re.compile(r"^\s+(\w+_test\.go:\d+):", re.M)


def run(targets, pkg):
    """(passed, output). passed is None for a BUILD failure or a pattern that matched nothing."""
    cmd = ["go", "test", "-timeout", "300s", "-count=1"]
    if targets:
        cmd += ["-run", "^(" + "|".join(targets) + ")$"]
    cmd.append(pkg)
    p = subprocess.run(cmd, cwd=ROOT, capture_output=True, text=True)
    out = p.stdout + p.stderr
    # ⚠ A BUILD FAILURE IS NOT A CAUGHT MUTATION and must never be scored as one.
    #
    # ⚠⚠ AND THE INHERITED DETECTOR HAD A HOLE THAT THIS CAMPAIGN WALKED STRAIGHT INTO. #86–#92's
    # scripts test for the literal "build failed"; a PARSE error in a _test.go file makes `go test`
    # print `FAIL <pkg> [setup failed]` instead, which that list does not match — so the run scored
    # non-zero and C3 was reported CAUGHT on its first pass when the package had not compiled at
    # all. MEASURED here: C3's original replacement appended a `// CONTROL` comment inside a
    # fmt.Sprintf argument list, and the answer was
    #   internal/importer/analytics_window_job_test.go:95:97: missing ',' before newline
    #   FAIL github.com/talyvor/track/internal/importer [setup failed]
    # The `^# <package>` header Go prints before any compile diagnostic is the reliable signal and
    # is what this now keys on; the substrings are kept as a belt.
    if re.search(r"^# \S+", out, re.M):
        return None, out
    if ("build failed" in out or "setup failed" in out or "cannot use" in out
            or "undefined:" in out or "declared and not used" in out or "syntax error" in out):
        return None, out
    # ⚠ NO TESTS MATCHED IS NOT A PASS. `go test -run` exits 0 when the pattern matches nothing.
    if targets and "no tests to run" in out:
        return None, out
    return p.returncode == 0, out


def first_assertion(out):
    m = ASSERTION.search(out)
    return m.group(1) if m else "no assertion line"


def main():
    if not os.environ.get("TRACK_TEST_DATABASE_URL"):
        print("REFUSING TO RUN: TRACK_TEST_DATABASE_URL is unset. Every real-Postgres control "
              "would SKIP, go test would exit 0, and this script would report a clean sweep of "
              "controls that never ran.", file=sys.stderr)
        return 3

    files = sorted({c[1] for c in CONTROLS})
    # ⚠ RESTORED FROM SAVED BYTES, NEVER `git checkout`: every file mutated below carries the
    # uncommitted fix, so a checkout would revert the merge instead of the mutation.
    saved = {f: (ROOT / f).read_text() for f in files}
    before = {f: sha(f) for f in files}

    print("BASELINE — the suites must be green before any mutation means anything")
    for pkg in (ANA, IMP):
        ok, out = run([], pkg)
        if not ok:
            print(f"  BASELINE RED in {pkg} — stopping. A control campaign on a red tree proves nothing.")
            print(out[-3000:])
            return 2
    print("  baseline green\n")

    verdicts = {}
    for cid, path, anchor, repl, must_red, must_green, note in CONTROLS:
        p = ROOT / path
        src = p.read_text()
        n = src.count(anchor)
        if n != 1:
            verdicts[cid] = f"ANCHOR {n} != 1 — NOT RUN"
            print(f"{cid}  ANCHOR COUNT {n} != 1 in {path} — not run\n")
            continue
        p.write_text(src.replace(anchor, repl, 1))
        # ⚠ THE BYTES MUST HAVE CHANGED ON DISK. A control that silently no-ops is
        # byte-indistinguishable from a guard that works.
        if sha(path) == before[path]:
            p.write_text(saved[path])
            verdicts[cid] = "EDIT DID NOT CHANGE THE FILE — NOT RUN"
            print(f"{cid}  edit left {path} byte-identical — not run\n")
            continue
        try:
            red_ok, red_detail = True, []
            for t, pkg in must_red:
                passed, o = run([t], pkg)
                if passed is None:
                    red_detail.append(f"{t}=BUILD/NOMATCH")
                    red_ok = False
                elif passed:
                    red_detail.append(f"{t}=STILL GREEN")
                    red_ok = False
                else:
                    red_detail.append(f"{t}=red@{first_assertion(o)}")

            green_ok, green_detail = True, []
            for t, pkg in must_green:
                passed, o = run([t], pkg)
                if passed is None:
                    green_detail.append(f"{t}=BUILD/NOMATCH")
                    green_ok = False
                elif not passed:
                    green_detail.append(f"{t}=RED@{first_assertion(o)}")
                    green_ok = False
                else:
                    green_detail.append(f"{t}=green")
        finally:
            p.write_text(saved[path])
            if sha(path) != before[path]:
                print(f"⚠ {path} NOT restored byte-identically — STOP", file=sys.stderr)
                return 2

        if not must_red:
            verdict = "INERT AS SPECIFIED" if green_ok else "SUSPECT — an inert edit reddened something"
        elif red_ok and green_ok:
            verdict = "CAUGHT"
        elif red_ok:
            verdict = "SUSPECT — the mutation reddened a companion too"
        else:
            verdict = "NOT CAUGHT"
        verdicts[cid] = verdict
        print(f"{cid}  {verdict}")
        print(f"    {note}")
        if red_detail:
            print(f"    must-red:   {' · '.join(red_detail)}")
        print(f"    must-green: {' · '.join(green_detail)}\n")

    print("── SUMMARY ─────────────────────────────")
    for cid, v in verdicts.items():
        print(f"  {cid}  {v}")
    for f in files:
        print(f"  restored byte-identical: {f} ({'ok' if sha(f) == before[f] else 'MISMATCH'})")
    bad = [c for c, v in verdicts.items() if v not in ("CAUGHT", "INERT AS SPECIFIED")]
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
