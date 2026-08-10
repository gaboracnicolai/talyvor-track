#!/usr/bin/env python3
"""Positive controls for "the AI-cost report answers a migrated backlog with null" (W3.4, after #93).

THE FINDING. analytics.GetAICostTrends builds its four slice fields by `append` only, so a
sub-query that matches no rows leaves the field NIL — and Go marshals a nil slice as `null`.
MEASURED through the SHIPPED route on real Postgres, for a workspace whose ONLY content is a
correctly imported backlog opened 800 days ago and last touched 600 days ago:

    {"total_cost_usd":0,"daily_costs":null,"top_cost_issues":null,
     "cost_by_team":null,"cost_by_label":null,"projected_monthly_usd":0,"avg_cost_per_issue":0}

frontend/src/api/types.ts:429 declares every one of those four as a NON-NULLABLE array, and
frontend/src/components/analytics/AICostChart.tsx:23 is `trends.daily_costs.map(...)` with no
guard. Measured against that exact body in node:

    TypeError: Cannot read properties of null (reading 'map')

There is no ErrorBoundary anywhere in frontend/src, so the Analytics page does not degrade.

⚠ ONLY AN IMPORT REACHES THAT STATE WITH A FULL WORKSPACE, which is why this is W3.4's and not
analytics'. A native Track issue is written now, so its updated_at is always inside the window;
only an import writes one from years ago (#85/#86 landed the provider's updated_at precisely
because the main screen sorts by it), and a MIGRATED project stops receiving updates at all. The
empty workspace reaches the same shape and is pinned too — the difference is that the empty case
is obviously empty and the migrated one is a workspace full of correctly imported work.

⚠ NEITHER GATE IN THIS REPO CAN SEE IT, which is why both tests read the WIRE. `npm run typecheck`
(with `build`, the whole of the `frontend` CI job — that package has no test runner) believes the
state is unreachable because the type says non-nullable. On this side a decode into
analytics.AICostTrends is blind because `null` and `[]` both land in a nil slice; the BLINDNESS
MEASUREMENT below proves that rather than asserting it.

⚠ THE COHORT IS UNTOUCHED. Whether the AI-cost window should key on updated_at and whether
maxWindowDays should be 365 are the product decisions #93 wrote up with numbers. This merge changes
the SHAPE of an empty answer and nothing about which rows are in it.

RED FIRST: both tests, 8 assertions, at aicost_null_series_job_test.go:197 and :237 — "the ai-cost
report answers %q with null, not an array" — with both PREMISE assertions passing, so the reds were
for the right reason. That is necessary and not sufficient. Each control below removes exactly one
thing and NAMES THE ASSERTION THAT MUST SPEAK, predicted before the run.

⚠ THE RUNNER'S BUILD DETECTOR IS #93's CORRECTED ONE. A parse error in a _test.go file makes
`go test` print `FAIL <pkg> [setup failed]` rather than "build failed", so a script keying on that
literal scores a non-compiling package as CAUGHT. This keys on the `^# <package>` header Go prints
before any compile diagnostic. The logic is duplicated here rather than imported and brings NONE of
those runs' evidence with it: every verdict below is from this campaign's own run.

    TRACK_TEST_DATABASE_URL=... python3 scripts/w34-aicost-null-series-controls.py
"""

import hashlib
import os
import re
import subprocess
import sys
import textwrap
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent

ENGINE = "internal/analytics/engine.go"
GUARD = "internal/importer/aicost_null_series_job_test.go"
ANAHANDLER = "internal/analytics/handler.go"

IMP = "./internal/importer/"
ANA = "./internal/analytics/"
MCP = "./internal/mcp/"

T_MIGRATED = "TestAICostReport_AMigratedBacklogIsNotAnsweredWithNull"
T_EMPTY = "TestAICostReport_AnEmptyWorkspaceAnswersLikeItsSiblingEndpoints"

GUARD_TESTS = [(T_MIGRATED, IMP), (T_EMPTY, IMP)]

# The pre-existing companions. WHOLE PACKAGES, not named tests: the non-redundancy claim is that
# NOTHING already in this repo can tell `null` from `[]` on this endpoint, and a hand-picked list of
# test names could be the list that happens to miss the one that can.
PREEXISTING = [(None, ANA), (None, MCP)]

# The fixed initialiser, verbatim. Its indentation is a tab followed by the struct literal.
FIX = (
    "\tout := &AICostTrends{\n"
    "\t\tDailyCosts:    []DailyCost{},\n"
    "\t\tTopCostIssues: []IssueCost{},\n"
    "\t\tCostByTeam:    []TeamCost{},\n"
    "\t\tCostByLabel:   []LabelCost{},\n"
    "\t}\n"
)

FIXTURE = (
    '\treturn "Summary,Description,Status,Priority,Resolution,Created,Updated,Resolved\\n" +\n'
    '\t\tfmt.Sprintf("Imported from a migrated backlog,d,Closed,High,Fixed,%s,%s,%s\\n",\n'
    "\t\t\tat(aiCostMigratedCreatedDaysAgo), at(aiCostMigratedUpdatedDaysAgo),\n"
    "\t\t\tat(aiCostMigratedResolvedDaysAgo))\n"
)

# (id, [(file, anchor, replacement), ...], must_red, must_stay_green, note)
CONTROLS = [
    ("C1", [(ENGINE, FIX, "\tout := &AICostTrends{} // CONTROL\n")],
     GUARD_TESTS, PREEXISTING,
     "THE FIX REVERTED, AS ONE CONTIGUOUS MOVE. PREDICTED CATCHER: both guard tests red at the "
     "`answers %q with null` assertion (:197 and :237). ⚠ ITS MUST-GREEN LIST IS THE "
     "NON-REDUNDANCY CLAIM: the ENTIRE internal/analytics and internal/mcp packages stay green "
     "with all four fields back on null, including "
     "TestGetAICostTrends_ReturnsDailyCostsAndProjection — which reads the STRUCT, where a nil "
     "slice and an empty one have the same len. Nothing in this repo but this guard can tell a "
     "field a client receives as `[]` from one it receives as `null`."),

    ("C2", [(ENGINE, FIX,
             "\tout := &AICostTrends{DailyCosts: []DailyCost{}} // CONTROL: half fix\n")],
     [(T_MIGRATED, IMP), (T_EMPTY, IMP)], PREEXISTING,
     "THE HALF-FIX A REVIEWER WOULD ACCEPT — daily_costs only, because that is the one field the "
     "shipped chart maps and therefore the only one whose crash is demonstrable. PREDICTED "
     "CATCHER: BOTH guard tests red at :197 / :237, and the failing field names are "
     "top_cost_issues / cost_by_team / cost_by_label and NOT daily_costs. ⚠ THIS CONTROL'S FIRST "
     "RUN SCORED `NOT AS SPECIFIED` AND THE FAULT WAS THE CONTROL'S, NOT THE CODE'S: T_EMPTY was "
     "written into the MUST-GREEN list while the note beside it said in as many words that it "
     "still reds. It does — the half-fix leaves three fields null and T_EMPTY asserts all four. "
     "The substantive verdict was unchanged either way, and the reason it was unchanged is that "
     "the field-name line below is what carries this control's claim, not the pass/fail."),

    ("C3", [(GUARD, FIXTURE,
             '\treturn "Summary,Description,Status,Priority,Resolution,Created,Updated,Resolved\\n" +\n'
             '\t\tfmt.Sprintf(",d,Closed,High,Fixed,%s,%s,%s\\n",\n'
             "\t\t\tat(aiCostMigratedCreatedDaysAgo), at(aiCostMigratedUpdatedDaysAgo),\n"
             "\t\t\tat(aiCostMigratedResolvedDaysAgo))\n")],
     [(T_MIGRATED, IMP)], [(T_EMPTY, IMP)] + PREEXISTING,
     "THE CONTROL THAT EARNS PREMISE 1. An empty Summary is refused by errEmptyTitle, so the row "
     "is skipped and NOTHING lands — the workspace is then merely empty and every null below "
     "would be true for a reason that has nothing to do with an imported backlog. PREDICTED "
     "CATCHER: T_MIGRATED reds at PREMISE 1, \"imported rows = 0, want 1\"."),

    ("C4", [(GUARD, FIXTURE,
             '\treturn "Summary,Description,Status,Priority,Resolution,Created,Resolved\\n" +\n'
             '\t\tfmt.Sprintf("Imported from a migrated backlog,d,Closed,High,Fixed,%s,%s\\n",\n'
             "\t\t\tat(aiCostMigratedCreatedDaysAgo),\n"
             "\t\t\tat(aiCostMigratedResolvedDaysAgo))\n")],
     [(T_MIGRATED, IMP)], [(T_EMPTY, IMP)] + PREEXISTING,
     "THE CONTROL THAT EARNS PREMISE 2, AND IT IS THE MISTAKE THIS CAMPAIGN'S OWN FIRST PROBE "
     "MADE. Drop the `Updated` column: issues.updated_at is TIMESTAMPTZ DEFAULT NOW(), so the row "
     "lands INSIDE the window with a plausible timestamp however old its Created says it is, the "
     "report answers with data, and the test would pass while proving nothing. The probe behind "
     "this file printed `updated 0d ago` on its first run for exactly this reason. PREDICTED "
     "CATCHER: T_MIGRATED reds at PREMISE 2, \"last touched 0 days ago, which is INSIDE the "
     "365-day cap\"."),

    ("C5", [(ANAHANDLER, "\t\tout = []DistributionBucket{}\n",
             "\t\t_ = out // CONTROL: the sibling stops coercing\n")],
     [(T_EMPTY, IMP)], [(T_MIGRATED, IMP)],
     "THE CONTROL THAT PROVES THE SIBLING COMPARISON IS LIVE RATHER THAN DECORATION. T_EMPTY "
     "rests on distribution and workload answering `[]` on the same empty workspace; if that were "
     "asserted against a constant nobody reads, the whole 'this is a divergence inside one file' "
     "argument would be a comment. PREDICTED CATCHER: T_EMPTY reds at the REFERENCE POINT "
     "Fatalf, \"distribution on an empty workspace answered ...\". ⚠ THE ANALYTICS PACKAGE WAS "
     "LEFT OUT OF THE MUST-GREEN LIST IN CASE A PRE-EXISTING TEST PINNED THAT COERCION TOO. IT "
     "DOES NOT: grep for []DistributionBucket{} across internal/analytics/*_test.go returns "
     "nothing, so before this file NOTHING in the repo asserted that ANY analytics endpoint "
     "answers [] rather than null — C5 is a non-redundancy claim as well as a liveness one."),

    ("C6", [(ENGINE,
             "\t\tDailyCosts:    []DailyCost{},\n",
             "\t\tDailyCosts:    make([]DailyCost, 0), // CONTROL (inverted: identical, stay GREEN)\n")],
     [], GUARD_TESTS + PREEXISTING,
     "INVERTED CONTROL — NOTHING MAY RED. `make([]DailyCost, 0)` and `[]DailyCost{}` are the same "
     "non-nil empty slice and marshal to the same bytes. A guard that reddens on any edit to the "
     "line it watches is pinning bytes, not behaviour, and would report a working instrument for "
     "a mutation that changes nothing."),
]

# ── the blindness measurement ────────────────────────────────────────────────────────────────
# The header of the guard claims a struct decode cannot tell `null` from `[]`. That is a claim
# about encoding/json, not about this repo, so it is MEASURED here rather than asserted.
BLINDNESS = textwrap.dedent("""\
    package analytics

    import (
    \t"encoding/json"
    \t"testing"
    )

    func TestZZBlindness(t *testing.T) {
    \tvar a, b AICostTrends
    \tif err := json.Unmarshal([]byte(`{"daily_costs":null}`), &a); err != nil {
    \t\tt.Fatal(err)
    \t}
    \tif err := json.Unmarshal([]byte(`{"daily_costs":[]}`), &b); err != nil {
    \t\tt.Fatal(err)
    \t}
    \tt.Logf("null  -> len=%d nil=%v", len(a.DailyCosts), a.DailyCosts == nil)
    \tt.Logf("[]    -> len=%d nil=%v", len(b.DailyCosts), b.DailyCosts == nil)
    \tt.Logf("len() can distinguish them: %v", len(a.DailyCosts) != len(b.DailyCosts))
    }
    """)


def sha(path):
    return hashlib.sha256((ROOT / path).read_bytes()).hexdigest()


ASSERTION = re.compile(r"^\s+(\w+_test\.go:\d+):", re.M)


def run(target, pkg, verbose=False):
    """(passed, output). passed is None for a BUILD failure or a -run pattern matching nothing."""
    cmd = ["go", "test", "-timeout", "300s", "-count=1"]
    if verbose:
        cmd.append("-v")  # t.Logf output is DISCARDED without it — the first run of the blindness
        # measurement printed nothing at all and read as "the probe found nothing".
    if target:
        cmd += ["-run", "^" + target + "$"]
    cmd.append(pkg)
    p = subprocess.run(cmd, cwd=ROOT, capture_output=True, text=True)
    out = p.stdout + p.stderr
    # ⚠ A BUILD FAILURE IS NEVER A CAUGHT MUTATION. `^# <pkg>` is the header Go prints before any
    # compile diagnostic and is the reliable signal; the substrings are a belt (see the docstring).
    if re.search(r"^# \S+", out, re.M):
        return None, out
    if ("build failed" in out or "setup failed" in out or "cannot use" in out
            or "undefined:" in out or "declared and not used" in out or "syntax error" in out):
        return None, out
    # ⚠ NO TESTS MATCHED IS NOT A PASS. `go test -run` exits 0 when the pattern matches nothing.
    if target and "no tests to run" in out:
        return None, out
    return p.returncode == 0, out


def first_assertion(out):
    m = ASSERTION.search(out)
    return m.group(1) if m else "no assertion line"


def label(target, pkg):
    return target or ("whole " + pkg)


def main():
    if not os.environ.get("TRACK_TEST_DATABASE_URL"):
        print("REFUSING TO RUN: TRACK_TEST_DATABASE_URL is unset. Every real-Postgres control "
              "would fail to provision, and a campaign whose controls never reached the database "
              "proves nothing.", file=sys.stderr)
        return 3

    files = sorted({f for c in CONTROLS for (f, _, _) in c[1]})
    # ⚠ RESTORED FROM SAVED BYTES, NEVER `git checkout`: every file mutated below carries the
    # uncommitted fix, so a checkout would revert the merge instead of the mutation.
    saved = {f: (ROOT / f).read_text() for f in files}
    before = {f: sha(f) for f in files}

    print("BASELINE — the suites must be green before any mutation means anything")
    for target, pkg in GUARD_TESTS + PREEXISTING:
        ok, out = run(target, pkg)
        if not ok:
            print(f"  BASELINE RED in {label(target, pkg)} — stopping. "
                  "A control campaign on a red tree proves nothing.")
            print(out[-3000:])
            return 2
    print("  baseline green\n")

    # ── BLINDNESS MEASUREMENT ────────────────────────────────────────────────────────────────
    probe = ROOT / "internal/analytics/zz_blindness_probe_test.go"
    probe.write_text(BLINDNESS)
    try:
        _, out = run("TestZZBlindness", ANA, verbose=True)
        print("BLINDNESS MEASUREMENT — can a struct decode tell `null` from `[]`?")
        for line in out.splitlines():
            if "->" in line or "distinguish" in line:
                print("   " + line.strip())
        print()
    finally:
        probe.unlink()

    verdicts = {}
    for cid, edits, must_red, must_green, note in CONTROLS:
        # ⚠ EVERY ANCHOR IS ASSERTED BEFORE ANY WRITE. A multi-edit control that applies half of
        # itself mutates the tree and reports a verdict about a state nobody described.
        counts = [( f, (ROOT / f).read_text().count(a)) for (f, a, _) in edits]
        if any(n != 1 for _, n in counts):
            verdicts[cid] = "ANCHOR COUNT != 1 — NOT RUN"
            print(f"{cid}  anchor counts {counts} — not run\n")
            continue
        for (f, a, r) in edits:
            p = ROOT / f
            p.write_text(p.read_text().replace(a, r, 1))
        # ⚠ THE BYTES MUST HAVE CHANGED ON DISK. A control that silently no-ops is
        # byte-indistinguishable from a guard that works.
        unchanged = [f for (f, _, _) in edits if sha(f) == before[f]]
        if unchanged:
            for f in {f for (f, _, _) in edits}:
                (ROOT / f).write_text(saved[f])
            verdicts[cid] = f"EDIT LEFT {unchanged} BYTE-IDENTICAL — NOT RUN"
            print(f"{cid}  edit left {unchanged} byte-identical — not run\n")
            continue
        try:
            red_ok, red_detail, red_out = True, [], ""
            for t, pkg in must_red:
                passed, o = run(t, pkg)
                red_out += o
                if passed is None:
                    red_detail.append(f"{label(t, pkg)}=BUILD/NOMATCH")
                    red_ok = False
                elif passed:
                    red_detail.append(f"{label(t, pkg)}=STILL GREEN")
                    red_ok = False
                else:
                    red_detail.append(f"{label(t, pkg)}=red@{first_assertion(o)}")

            green_ok, green_detail = True, []
            for t, pkg in must_green:
                passed, o = run(t, pkg)
                if passed is None:
                    green_detail.append(f"{label(t, pkg)}=BUILD/NOMATCH")
                    green_ok = False
                elif not passed:
                    green_detail.append(f"{label(t, pkg)}=RED@{first_assertion(o)}")
                    green_ok = False
        finally:
            for f in {f for (f, _, _) in edits}:
                (ROOT / f).write_text(saved[f])
            assert all(sha(f) == before[f] for (f, _, _) in edits), f"{cid}: restore failed"

        want_red = bool(must_red)
        ok = (red_ok if want_red else True) and green_ok
        verdicts[cid] = ("CAUGHT" if want_red else "INERT AS SPECIFIED") if ok else "NOT AS SPECIFIED"
        print(f"{cid}  {verdicts[cid]}")
        print("     " + textwrap.fill(note, 96, subsequent_indent="     "))
        if red_detail:
            print("     must-red : " + " · ".join(red_detail))
        if green_detail:
            print("     must-green VIOLATED: " + " · ".join(green_detail))
        elif must_green:
            print(f"     must-green: all {len(must_green)} stayed green")
        # C2's claim is about WHICH fields spoke, not merely that something did.
        if cid == "C2":
            spoke = sorted({m for m in re.findall(r'answers "(\w+)" with null', red_out)})
            print(f"     fields that spoke: {spoke}  (daily_costs absent ⇒ the half-fix is what "
                  "the other three assertions caught)")
        print()

    print("SUMMARY: " + " · ".join(f"{k}={v}" for k, v in verdicts.items()))
    bad = [k for k, v in verdicts.items() if v not in ("CAUGHT", "INERT AS SPECIFIED")]
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
