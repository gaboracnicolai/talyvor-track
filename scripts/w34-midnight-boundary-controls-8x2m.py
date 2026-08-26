#!/usr/bin/env python3
"""w34-midnight-boundary-controls-8x2m.py — positive controls for the midnight-boundary guard.

WHAT IS UNDER TEST
  internal/analytics/burndown_midnight_boundary_realpg_test.go —
  TestBurndown_AMidnightCompletionBelongsToTheDayItOPENS_RealPG.

WHY A HARNESS AT ALL. THE TEST PASSED ON ITS FIRST RUN. Everything it asserts is true of the
engine as shipped, so a green tells you nothing about whether the assertions can ever go red —
and this repository has shipped three guards that could not fail. Each assertion is mutated here
against the thing it names, and the companion is mutated separately so it is justified by its own
mutation rather than by being present.

PROTOCOL
  · Every control declares its PREDICTED verdict BEFORE it runs.
  · Restored in a `finally`, sha256-verified.
  · CAUGHT requires the run to FAIL *and* the named assertion tag to appear in the output.
    A red for the wrong reason is not a catch.
"""
import hashlib, os, subprocess, sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
ENG = "internal/analytics/engine.go"
GUARD = ["go", "test", "-count=1", "-run",
         "TestBurndown_AMidnightCompletionBelongsToTheDayItOPENS_RealPG", "./internal/analytics/"]
# The test the engine comment USED to name as the strictness guard. Kept in the harness so its
# blindness to the walk's `<=` is re-measured here rather than quoted from a previous session.
NAMED = ["go", "test", "-count=1", "-run",
         "TestBurndown_TheFinalSecondOfADayIsInThatDay_RealPG", "./internal/analytics/"]

WALK_STRICT = "for completed < len(completions) && completions[completed].Before(eod) {"
WALK_LOOSE = "for completed < len(completions) && !completions[completed].After(eod) {"
SQL_STRICT = "AND completed_at < $2\n        ORDER BY completed_at"
SQL_INERT = "AND (completed_at < $2 OR TRUE)\n        ORDER BY completed_at"

def read(p):
    with open(os.path.join(REPO, p), encoding="utf-8") as f: return f.read()
def write(p, t):
    with open(os.path.join(REPO, p), "w", encoding="utf-8") as f: f.write(t)
def sha(p):
    with open(os.path.join(REPO, p), "rb") as f: return hashlib.sha256(f.read()).hexdigest()
def run(cmd):
    p = subprocess.run(cmd, cwd=REPO, capture_output=True, text=True)
    return p.returncode, p.stdout + p.stderr

# (name, predicted, cmd, edits, must_name, must_not_name)
CONTROLS = [
 ("N1 THE DEFECT THE GUARD EXISTS FOR: the walk compares with `<=` against an EXCLUSIVE bound",
  "CAUGHT", GUARD, [(ENG, WALK_STRICT, WALK_LOOSE)], "[M-BOUNDARY]", "[M-COMPANION]"),

 ("N1' THE SAME MUTATION, ASKED OF THE TEST engine.go USED TO NAME AS THE GUARD",
  "NOT CAUGHT — this is the measured blindness that justifies the new file",
  NAMED, [(ENG, WALK_STRICT, WALK_LOOSE)], None, None),

 ("N2 THE COMPANION'S OWN MUTATION: the walk counts NOTHING, so no day ever advances",
  "CAUGHT", GUARD, [(ENG, WALK_STRICT,
   "for completed < len(completions) && completions[completed].Before(eod) && false {")],
  "[M-COMPANION]", None),

 ("N3 THE PREMISE'S OWN MUTATION: move the fixture 1ns OFF midnight",
  "CAUGHT", GUARD, [("internal/analytics/burndown_midnight_boundary_realpg_test.go",
   "\t\tstart.Location()).AddDate(0, 0, 1)",
   "\t\tstart.Location()).AddDate(0, 0, 1).Add(time.Nanosecond)")],
  "[M-PREMISE]", None),

 ("N5 THE VACUITY THE PREMISE EXISTS FOR: fixture shifted 1 MICROsecond off the boundary",
  "CAUGHT", GUARD, [("internal/analytics/burndown_midnight_boundary_realpg_test.go",
   "\t\tstart.Location()).AddDate(0, 0, 1)",
   "\t\tstart.Location()).AddDate(0, 0, 1).Add(time.Microsecond)")],
  "[M-PREMISE]", None),

 ("N4 MUST STAY GREEN: the SQL early-out made inert (the file says it deliberately asserts nothing here)",
  "NOT CAUGHT", GUARD, [(ENG, SQL_STRICT, SQL_INERT)], None, None),
]

def main():
    files = sorted({p for c in CONTROLS for (p, _, _) in c[3]})
    snap = {p: read(p) for p in files}
    base = {p: sha(p) for p in files}
    for label, cmd in (("U0 new guard", GUARD), ("U0b previously-named test", NAMED)):
        code, out = run(cmd)
        print(f"{label}, pristine -> {'GREEN' if code==0 else 'RED'}")
        if code: print(out[-2500:]); raise SystemExit(f"{label} baseline not green")
    results = []
    for name, pred, cmd, edits, must, mustnot in CONTROLS:
        print(f"\n=== {name}\n    PREDICTED: {pred}")
        try:
            for p, old, new in edits:
                t = read(p)
                if t.count(old) != 1:
                    raise SystemExit(f"ANCHOR DEAD in {p}: {t.count(old)} occurrences\n{old}")
                write(p, t.replace(old, new, 1))
            code, out = run(cmd)
            if code == 0:
                verdict = "NOT CAUGHT"
            elif must and must not in out:
                verdict = f"RED BUT DID NOT NAME {must}"
            elif mustnot and mustnot in out:
                verdict = f"CAUGHT BUT ALSO REDDED {mustnot} — not assertion-specific"
            else:
                verdict = "CAUGHT"
            print(f"    ACTUAL:    {verdict}")
            for l in out.splitlines():
                if "[M-" in l: print("      | " + l.strip()[:150])
            results.append((name, pred, verdict))
        finally:
            for p in files:
                write(p, snap[p]); assert sha(p) == base[p], f"RESTORE FAILED {p}"
    code, _ = run(GUARD)
    print(f"\nU0' after restores -> {'GREEN' if code==0 else 'RED'}; all files sha256-identical: "
          f"{all(sha(p)==base[p] for p in files)}")
    print("\n" + "="*78)
    ok = True
    for n, p, a in results:
        agree = a.split(" ")[0] == p.split(" ")[0]
        ok &= agree
        print(f"{'OK ' if agree else 'XX '} {n}\n     predicted={p}\n     actual   ={a}")
    print("="*78)
    print("ALL CONTROLS AS PREDICTED" if ok else "MISMATCH — read the rows marked XX")
    return 0 if ok else 1

if __name__ == "__main__": sys.exit(main())
