#!/usr/bin/env python3
"""Positive controls for internal/analytics/groupby_gate_realpg_test.go.

THE QUESTION: can anything in this repository see the SQL-composition gate on
GetDistribution removed?

Every control runs the WHOLE repository (`go test ./...`) against real Postgres.
Membership is decided by SET SUBTRACTION against C0's own measured FAIL set — never by
an exit code and never by a test's NAME. This repo already fails 13 importer tests on a
machine holding the empty shared /tmp corpus dirs, so a control scoring `rc != 0` as
CAUGHT would score every mutation caught and report the guard as unnecessary.

Each mutation is restored in a `finally` and the file is sha256-verified afterwards.
Predictions are printed BEFORE the run.

Usage:  TRACK_TEST_DATABASE_URL=... python3 scripts/w34-groupby-gate-controls-5b91.py
"""
import hashlib
import os
import pathlib
import re
import shutil
import subprocess
import sys

REPO = pathlib.Path(__file__).resolve().parent.parent
ENGINE = REPO / "internal/analytics/engine.go"
GUARD = REPO / "internal/analytics/groupby_gate_realpg_test.go"
NEW_TEST = "TestGetDistribution_TheGroupByGateIsKeyedOnMembershipNotOnPostgresChoking_RealPG"

GATE_OLD = """	col, ok := allowedGroupBy[groupBy]
	if !ok {
		return nil, fmt.Errorf("analytics: unsupported group_by %q", groupBy)
	}
"""
GATE_OPEN = """	col, ok := allowedGroupBy[groupBy]
	if !ok {
		col = groupBy
	}
"""

# The guard's entire half 2, verbatim, so C3 removes the assertions rather than half-blinding them.
HALF2_OLD = """	// ── HALF 2: THE GATE. Membership, not validity.
	out, err := e.GetDistribution(ctx, wsA.ID, crossWorkspaceGroupBy, 30)
	if err == nil {
		t.Fatalf("the group_by gate ADMITTED a value that is not a key of allowedGroupBy: "+
			"%d bucket(s) came back for group_by=%s on a report scoped to workspace %s, and half 1 "+
			"just measured that this expression reads every workspace in the table. buckets=%v",
			len(out), crossWorkspaceGroupBy, wsA.ID, out)
	}
	if !strings.Contains(err.Error(), "unsupported group_by") {
		t.Fatalf("the call failed, but not at the gate: %v. An error from the database is what the "+
			"two pre-existing tests already accept, and it is not evidence that the caller's string "+
			"was kept out of the SQL.", err)
	}
"""
HALF2_GONE = """	// ── HALF 2 REMOVED BY CONTROL C3.
	_, _ = e.GetDistribution(ctx, wsA.ID, crossWorkspaceGroupBy, 30)
"""

# (id, description, [(path, old, new), ...], prediction)
CONTROLS = [
    ("C1", "THE DEFECT — the gate alone is opened; every mapped column stays byte-identical, "
           "an UNMAPPED group_by falls through into the SQL string",
     [(ENGINE, GATE_OLD, GATE_OPEN)],
     f"CAUGHT by {NEW_TEST} and by NOTHING ELSE"),

    ("C2", "C1 with the new guard file DELETED — the measured blindness on main",
     [(ENGINE, GATE_OLD, GATE_OPEN), (GUARD, None, None)],
     "NOT CAUGHT (green): nothing else in this repository can see the gate removed"),

    # ⚠ C3'S FIRST FORM WAS WRONG AND IS RECORDED HERE RATHER THAN QUIETLY REPLACED. It blinded
    # only the `err == nil` arm (`if false {`) and left the message assertion live, so with the
    # gate open `err.Error()` dereferenced nil and the run failed by PANIC. It scored CAUGHT and
    # answered a different question. A control that reds because the test crashed is not evidence
    # that the test ASSERTED anything. Half 2 is removed outright below.
    ("C3", "C1 with the guard's HALF 2 REMOVED outright — which half of the new file is load-bearing",
     [(ENGINE, GATE_OLD, GATE_OPEN), (GUARD, HALF2_OLD, HALF2_GONE)],
     "NOT CAUGHT: half 2 is the assertion that catches C1; halves 1 and 3 cannot"),

    # ⚠ C4'S PREDICTED CATCHER WAS WRONG, AND THE REASON IS THE FINDING-SHAPED PART.
    # TestClampDays_BoundsRespected (engine_test.go:298) asserts `clampDays(99999) == maxWindowDays`
    # — it compares the constant to ITSELF, so moving the constant leaves it green. What actually
    # reds are the WIRING tests #165 added. The half of the prediction this control exists for
    # (NOT caught by the new file) held.
    ("C4", "MUST-STAY-GREEN COMPANION — clampDays' ceiling 365 -> 3650 (a real defect the new "
           "file has no business seeing)",
     [(ENGINE, "\tmaxWindowDays     = 365\n", "\tmaxWindowDays     = 3650\n")],
     f"CAUGHT by the window-clamp WIRING tests (not by TestClampDays_BoundsRespected, which "
     f"compares the constant to itself), NOT by {NEW_TEST}"),

    ("C5", "THE MAPPING, NOT THE GATE — assignee loses its COALESCE. This is the half M6 changed "
           "by accident, and the new file must NOT be what catches it",
     [(ENGINE, '\t"assignee": "COALESCE(assignee_id, \'unassigned\')",\n',
       '\t"assignee": "assignee_id",\n')],
     f"CAUGHT by the distribution counting guard, NOT by {NEW_TEST}"),

    ("C6", "VOID / STATED LIMIT — a synonym key mapped to the identical expression. Every existing "
           "input behaves identically and the gate still refuses non-members; the new file pins ONE "
           "non-member, not the map's key SET",
     [(ENGINE, '\t"status":   "status",\n', '\t"status":   "status",\n\t"state":    "status",\n')],
     "NOT CAUGHT — recorded as a limit of this guard, not as a catch"),
]


def sha(p):
    return hashlib.sha256(p.read_bytes()).hexdigest() if p.exists() else "ABSENT"


def run(tag):
    r = subprocess.run(
        ["go", "test", "-timeout", "600s", "-count=1", "./..."],
        cwd=REPO, capture_output=True, text=True, env=os.environ,
    )
    names = set(re.findall(r"^\s*--- FAIL: (\S+)", r.stdout, re.M))
    build = "[build failed]" in r.stdout
    print(f"    [{tag}] rc={r.returncode} fails={len(names)} build_failed={build}", flush=True)
    if build:
        print(r.stdout[-1200:])
    return names, build


def main():
    if not os.environ.get("TRACK_TEST_DATABASE_URL"):
        sys.exit("TRACK_TEST_DATABASE_URL is required — these are real-Postgres controls")
    backups = {p: (sha(p), p.read_text() if p.exists() else None) for p in (ENGINE, GUARD)}

    print("C0 — baseline, no mutation, whole repository")
    base, berr = run("C0")
    if berr:
        sys.exit("C0 does not build")
    print(f"    C0 FAIL set = {len(base)} pre-existing (empty shared /tmp corpus dirs):")
    for n in sorted(base):
        print("      ", n)
    if NEW_TEST in base:
        sys.exit("the new guard is RED on an unmutated tree")

    rows = []
    for cid, desc, edits, pred in CONTROLS:
        print(f"\n{cid} — {desc}")
        print(f"    PREDICTION: {pred}")
        try:
            for path, old, new in edits:
                if old is None:              # delete the file entirely
                    path.unlink()
                    continue
                src = path.read_text()
                if src.count(old) != 1:
                    raise AssertionError(f"{cid}: anchor miss in {path.name} ({src.count(old)})")
                path.write_text(src.replace(old, new, 1))
            got, berr2 = run(cid)
            if berr2:
                verdict = "BUILD-FAILED"
            else:
                fresh = sorted(got - base)
                verdict = f"CAUGHT by {fresh}" if fresh else "NOT CAUGHT"
        finally:
            for path, (h, text) in backups.items():
                if text is None:
                    if path.exists():
                        path.unlink()
                else:
                    path.write_text(text)
                assert sha(path) == h, f"{path} NOT RESTORED"
        print(f"    MEASURED:   {verdict}")
        rows.append((cid, pred, verdict))

    print("\n===== SUMMARY =====")
    for cid, pred, verdict in rows:
        print(f"{cid}  predicted: {pred}\n     measured: {verdict}")
    print("\nfiles restored and sha256-verified:")
    for p, (h, _) in backups.items():
        print(f"  {p.relative_to(REPO)}  {sha(p)}  (== {h})")


if __name__ == "__main__":
    main()
