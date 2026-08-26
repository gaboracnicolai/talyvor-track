#!/usr/bin/env python3
"""w34-childinsert-exemption-controls-8x2m.py — controls for child-insert-tenancy.yml's
inline-comparison exemption, and for the behavioural half that covers its residual.

THE EXEMPTION released any function containing `if $X != workspaceID { ... }`. `...` matches ANY
body, so a comparison that was WRITTEN but did nothing bought it — #173's shape, third sighting in
this repo after #178. It now requires `return`.

WHAT IS AND IS NOT CLOSED, MEASURED AT f240c2f ON customfield.SetValue, which has TWO comparisons:
  A1 the SEC-5 caller check     (issueWS != workspaceID)        — the one this pattern matches
  A2 the field/issue check      (issueWS != field.WorkspaceID)  — a sibling it cannot see
"""
import hashlib, os, subprocess, sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
S = "internal/customfield/store.go"
SEMGREP = ["semgrep", "scan", "--config", ".semgrep/", "--error", "--metrics=off", "internal/", "cmd/"]
CF = ["go", "test", "-count=1", "./internal/customfield/"]

A1_OK = "\tif issueWS != workspaceID {\n\t\treturn ErrNotFound\n\t}"
A1_INERT = "\tif issueWS != workspaceID {\n\t\t_ = issueWS\n\t}"
A2_OK = ('\tif issueWS != field.WorkspaceID {\n\t\treturn errors.New("customfield: field and issue '
         'belong to different workspaces")\n\t}')
A2_INERT = "\tif issueWS != field.WorkspaceID {\n\t\t_ = issueWS\n\t}"

CONTROLS = [
 ("A1 the SEC-5 caller-workspace check made INERT (the comparison the exemption keys on)",
  "CAUGHT/CAUGHT", A1_OK, A1_INERT),
 ("A2 the SIBLING field/issue check made INERT (the exemption is bought by A1, which still returns)",
  "NOT CAUGHT/CAUGHT", A2_OK, A2_INERT),
]

def read(p): return open(os.path.join(REPO, p), encoding="utf-8").read()
def write(p, t): open(os.path.join(REPO, p), "w", encoding="utf-8").write(t)
def sha(p): return hashlib.sha256(open(os.path.join(REPO, p), "rb").read()).hexdigest()
def run(c):
    p = subprocess.run(c, cwd=REPO, capture_output=True, text=True)
    return p.returncode, p.stdout + p.stderr

def main():
    base, bsha = read(S), sha(S)
    sc, _ = run(SEMGREP); tc, _ = run(CF)
    print(f"U0 semgrep {'GREEN' if sc==0 else 'RED'} | customfield {'GREEN' if tc==0 else 'RED'}")
    if sc or tc: raise SystemExit("baseline not green")
    results = []
    for name, pred, old, new in CONTROLS:
        print(f"\n=== {name}\n    PREDICTED: semgrep/go-test = {pred}")
        try:
            assert base.count(old) == 1, (name, base.count(old))
            write(S, base.replace(old, new, 1))
            sc, _ = run(SEMGREP)
            tc, to = run(CF)
            sv = "CAUGHT" if sc else "NOT CAUGHT"
            tv = "CAUGHT" if (tc and "TestSetValue_ObjectGraph_RejectsCrossWorkspace" in to) else \
                 ("RED BUT NOT BY THAT TEST" if tc else "NOT CAUGHT")
            print(f"    ACTUAL:    {sv}/{tv}")
            results.append((name, pred, f"{sv}/{tv}"))
        finally:
            write(S, base); assert sha(S) == bsha, "RESTORE FAILED"
    sc, _ = run(SEMGREP); tc, _ = run(CF)
    print(f"\nU0' semgrep {'GREEN' if sc==0 else 'RED'} | customfield {'GREEN' if tc==0 else 'RED'}; "
          f"store.go sha256 identical: {sha(S)==bsha}")
    print("\n" + "="*76)
    ok = True
    for n, p, a in results:
        agree = p == a; ok &= agree
        print(f"{'OK ' if agree else 'XX '} {n}\n     predicted={p}  actual={a}")
    print("="*76)
    print("ALL CONTROLS AS PREDICTED" if ok else "MISMATCH — read the XX rows")
    return 0 if ok else 1

if __name__ == "__main__": sys.exit(main())
