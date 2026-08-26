#!/usr/bin/env python3
"""w34-operate-by-id-exemption-controls-8x2m.py — controls for operate-by-id-tenancy.yml's exemption.

THE FINDING. The rule flags a by-id DELETE/UPDATE and EXEMPTS it when the SQL matches
`(?s).*workspace_id` — the WORD anywhere in the statement, not a predicate. So a genuinely
unscoped by-id write that merely MENTIONS the column buys the exemption.

This is the FOURTH member of one family in this repo, and the first in TEXT rather than AST form:
  #173  owner-gate         `...` matches any body            — an inert gate satisfies it
  #178  workspace-authz A  metavariable receiver             — the WRONG INSTANCE buys it
  #180  child-insert       both at once
  here  operate-by-id      a substring match                 — a MENTION buys it, not a predicate
Root cause every time: the exemption is keyed on a construct being PRESENT, not on it doing the job.

MEASURED at 3eaf713 on project.Store.Delete (`DELETE FROM projects WHERE id = $1 AND workspace_id = $2`).
The exemption now requires `\\bworkspace_id\\s*=`, an equality rather than a mention.
"""
import hashlib, os, subprocess, sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
S = "internal/project/store.go"
SEMGREP = ["semgrep", "scan", "--config", ".semgrep/", "--error", "--metrics=off", "internal/", "cmd/"]
WHOLE = ["go", "test", "-timeout", "300s", "-count=1", "./..."]
RULE_ID = "operate-by-id-write-requires-workspace-scope"

OK = "`DELETE FROM projects WHERE id = $1 AND workspace_id = $2`"
CONTROLS = [
 ("P1 workspace_id REMOVED entirely — unscoped, no mention at all",
  "CAUGHT", "`DELETE FROM projects WHERE id = $1 AND $2 <> ''`"),
 ("P2 WHERE unscoped, workspace_id present only in RETURNING (the finding)",
  "CAUGHT", "`DELETE FROM projects WHERE id = $1 AND $2 <> '' RETURNING workspace_id`"),
 ("P3 RESIDUAL: workspace_id appears with an `=` but as a SET/assignment, not a filter",
  "NOT CAUGHT", "`UPDATE projects SET workspace_id = $2 WHERE id = $1`"),
]

def read(p): return open(os.path.join(REPO, p), encoding="utf-8").read()
def write(p, t): open(os.path.join(REPO, p), "w", encoding="utf-8").write(t)
def sha(p): return hashlib.sha256(open(os.path.join(REPO, p), "rb").read()).hexdigest()
def run(c):
    p = subprocess.run(c, cwd=REPO, capture_output=True, text=True)
    return p.returncode, p.stdout + p.stderr

def main():
    base, bsha = read(S), sha(S)
    assert base.count(OK) == 1
    sc, _ = run(SEMGREP); wc, _ = run(WHOLE)
    print(f"U0 semgrep {'GREEN' if sc==0 else 'RED'} | whole repo {'GREEN' if wc==0 else 'RED'}")
    if sc or wc: raise SystemExit("baseline not green")
    results = []
    for name, pred, new in CONTROLS:
        print(f"\n=== {name}\n    PREDICTED: semgrep {pred}")
        try:
            write(S, base.replace(OK, new, 1))
            sc, so = run(SEMGREP)
            wc, wo = run(WHOLE)
            sv = "CAUGHT" if (sc and RULE_ID in so) else ("RED BUT NOT BY THIS RULE" if sc else "NOT CAUGHT")
            tv = "CAUGHT" if wc else "NOT CAUGHT"
            print(f"    ACTUAL:    semgrep {sv} | go test {tv}")
            for l in wo.splitlines():
                if l.strip().startswith("--- FAIL"): print("      | " + l.strip()[:110]); break
            results.append((name, pred, sv, tv))
        finally:
            write(S, base); assert sha(S) == bsha, "RESTORE FAILED"
    sc, _ = run(SEMGREP); wc, _ = run(WHOLE)
    print(f"\nU0' semgrep {'GREEN' if sc==0 else 'RED'} | whole {'GREEN' if wc==0 else 'RED'}; "
          f"store.go sha256 identical: {sha(S)==bsha}")
    print("\n" + "="*78)
    ok = True
    for n, p, sv, tv in results:
        agree = p == sv; ok &= agree
        print(f"{'OK ' if agree else 'XX '} {n}\n     predicted semgrep={p:<11} actual semgrep={sv:<11} go-test={tv}")
    print("="*78)
    print("ALL CONTROLS AS PREDICTED" if ok else "MISMATCH — read the XX rows")
    return 0 if ok else 1

if __name__ == "__main__": sys.exit(main())
