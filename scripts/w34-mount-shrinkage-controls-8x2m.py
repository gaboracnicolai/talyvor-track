#!/usr/bin/env python3
"""w34-mount-shrinkage-controls-8x2m.py — controls for the analytics sweep's shrinkage floor.

WHAT IS UNDER TEST
  internal/analytics/authz_refusal_sweep_test.go — the `mustStayMounted` containment floor.

THE FINDING IT CLOSES, MEASURED AT 15468c7 BEFORE THE FLOOR EXISTED. The sweep walks the mounted
router for its population, so it is tested BY the thing it tests. Removing one route from
Mount at a time and asking the WHOLE repository (real Postgres) scored:

    velocity NOT CAUGHT · burndown NOT CAUGHT · distribution NOT CAUGHT · resolution NOT CAUGHT
    ai-costs NOT CAUGHT · workload NOT CAUGHT · export NOT CAUGHT

SEVEN of seven. `export` was PREDICTED CAUGHT (export_refusal_test.go names the URL) and was NOT —
that file calls h.Export(rec, req) directly and never routes. The prediction was wrong in the
worse direction and is recorded rather than tuned away.

PROTOCOL
  · Predicted verdict declared BEFORE each run.
  · Restored in a `finally`, sha256-verified.
  · CAUGHT requires a FAIL whose message names the route — a red for the wrong reason is not a catch.
"""
import hashlib, os, subprocess, sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
H = "internal/analytics/handler.go"
SWEEP = ["go", "test", "-count=1", "-run",
         "TestAnalytics_EveryMountedRoute_RefusesAnUnauthorizedWorkspace", "./internal/analytics/"]
ROUTES = ["velocity", "burndown", "distribution", "resolution", "ai-costs", "workload", "export"]

def read(p):
    with open(os.path.join(REPO, p), encoding="utf-8") as f: return f.read()
def write(p, t):
    with open(os.path.join(REPO, p), "w", encoding="utf-8") as f: f.write(t)
def sha(p):
    with open(os.path.join(REPO, p), "rb") as f: return hashlib.sha256(f.read()).hexdigest()
def run(cmd):
    p = subprocess.run(cmd, cwd=REPO, capture_output=True, text=True)
    return p.returncode, p.stdout + p.stderr

def line_for(base, rt):
    ls = [l for l in base.splitlines() if f'r.Get("/{rt}"' in l]
    assert len(ls) == 1, (rt, ls)
    return ls[0]

def main():
    base, bsha = read(H), sha(H)
    code, _ = run(SWEEP)
    print(f"U0 pristine -> {'GREEN' if code==0 else 'RED'}")
    if code: raise SystemExit("baseline not green")
    results = []

    # S1..S7 — every route's removal must now be caught, BY NAME.
    for rt in ROUTES:
        name = f"S:{rt} removed from Mount"
        print(f"\n=== {name}\n    PREDICTED: CAUGHT (was NOT CAUGHT before the floor)")
        try:
            write(H, base.replace(line_for(base, rt) + "\n", "", 1))
            c, out = run(SWEEP)
            named = f"analytics/{rt}\" is NO LONGER MOUNTED" in out
            verdict = "CAUGHT" if (c and named) else ("RED BUT DID NOT NAME THE ROUTE" if c else "NOT CAUGHT")
            print(f"    ACTUAL:    {verdict}")
            results.append((name, "CAUGHT", verdict))
        finally:
            write(H, base); assert sha(H) == bsha

    # G1 — GROWTH MUST STAY GREEN. The floor must not turn into an equality by accident.
    name = "G1 a NEW route added to Mount"
    print(f"\n=== {name}\n    PREDICTED: NOT CAUGHT (growth is unconstrained on purpose)")
    try:
        anchor = line_for(base, "export")
        write(H, base.replace(anchor, anchor + '\n\t\tr.Get("/velocity-v2", h.Velocity)', 1))
        c, out = run(SWEEP)
        verdict = "NOT CAUGHT" if c == 0 else "CAUGHT"
        print(f"    ACTUAL:    {verdict}")
        results.append((name, "NOT CAUGHT", verdict))
    finally:
        write(H, base); assert sha(H) == bsha

    # X1 — SUBSTITUTION. One out, one in: a COUNT floor would not see this. Containment must.
    name = "X1 /workload RENAMED to /workload-v2 (count unchanged)"
    print(f"\n=== {name}\n    PREDICTED: CAUGHT — this is why the floor is containment, not a count")
    try:
        write(H, base.replace('r.Get("/workload", h.Workload)',
                              'r.Get("/workload-v2", h.Workload)', 1))
        c, out = run(SWEEP)
        named = 'analytics/workload" is NO LONGER MOUNTED' in out
        verdict = "CAUGHT" if (c and named) else ("RED BUT DID NOT NAME THE ROUTE" if c else "NOT CAUGHT")
        print(f"    ACTUAL:    {verdict}")
        results.append((name, "CAUGHT", verdict))
    finally:
        write(H, base); assert sha(H) == bsha

    c, _ = run(SWEEP)
    print(f"\nU0' after restores -> {'GREEN' if c==0 else 'RED'}; handler.go sha256 identical: {sha(H)==bsha}")
    print("\n" + "="*78)
    ok = True
    for n, p, a in results:
        agree = p == a; ok &= agree
        print(f"{'OK ' if agree else 'XX '} {n:<48} predicted={p:<11} actual={a}")
    print("="*78)
    print("ALL CONTROLS AS PREDICTED" if ok else "MISMATCH — read the XX rows")
    return 0 if ok else 1

if __name__ == "__main__": sys.exit(main())
