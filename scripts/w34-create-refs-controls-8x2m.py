#!/usr/bin/env python3
"""w34-create-refs-controls-8x2m.py — controls for issue.Store.Create's cross-object ref guards.

UNDER TEST
  internal/issue/create_refs_tenancy_realpg_test.go
  TestCreate_CrossWorkspaceRefsAreRefused_RealPG

THE FINDING, MEASURED AT ae57b43 BEFORE THIS TEST EXISTED. Create loops a LITERAL field list and
calls assertRefInWorkspace on each. Dropping one field from that list at a time scored:

    project_id NOT CAUGHT · cycle_id NOT CAUGHT · assignee_id NOT CAUGHT · parent_id NOT CAUGHT
    milestone_id CAUGHT (milestone_attach_realpg_test.go)

FOUR of five guards held by nothing. semgrep is blind to all five: cross-object-tenancy.yml exempts
the function because `assertRefInWorkspace(` appears in it, which says the call is PRESENT and
nothing about WHICH fields it covers — one guarded field buys the exemption for its siblings.

PROTOCOL: predicted verdict declared first; sha256-verified restores; a CAUGHT must be attributable
to the field's OWN subtest, not merely to the parent test going red.
"""
import hashlib, os, re, subprocess, sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
S = "internal/issue/store.go"
T = ["go", "test", "-count=1", "-run", "TestCreate_CrossWorkspaceRefsAreRefused_RealPG", "./internal/issue/"]
FIELDS = ["project_id", "cycle_id", "assignee_id", "parent_id"]

def read(p):
    with open(os.path.join(REPO, p), encoding="utf-8") as f: return f.read()
def write(p, t):
    with open(os.path.join(REPO, p), "w", encoding="utf-8") as f: f.write(t)
def sha(p):
    with open(os.path.join(REPO, p), "rb") as f: return hashlib.sha256(f.read()).hexdigest()
def run(cmd):
    p = subprocess.run(cmd, cwd=REPO, capture_output=True, text=True)
    return p.returncode, p.stdout + p.stderr

def main():
    base, bsha = read(S), sha(S)
    ci = base.index("func (s *Store) Create(")
    code, out = run(T)
    print(f"U0 pristine -> {'GREEN' if code==0 else 'RED'}")
    if code: print(out[-2000:]); raise SystemExit("baseline not green")
    results = []

    # R1..R4 — each guard dropped from CREATE's literal list must red THAT FIELD'S subtest.
    for f in FIELDS:
        m = re.search(r'\n\t\t"%s":\s*\S+[^\n]*' % re.escape(f), base[ci:ci+2500])
        assert m, f
        name = f"R:{f} dropped from Create's guard list"
        print(f"\n=== {name}\n    PREDICTED: CAUGHT by the {f} subtest (was NOT CAUGHT by anything)")
        try:
            write(S, base[:ci] + base[ci:].replace(m.group(0), "", 1))
            c, o = run(T)
            own = f"TestCreate_CrossWorkspaceRefsAreRefused_RealPG/{f}" in o
            v = "CAUGHT" if (c and own) else ("RED BUT NOT THE FIELD'S OWN SUBTEST" if c else "NOT CAUGHT")
            print(f"    ACTUAL:    {v}")
            results.append((name, "CAUGHT", v))
        finally:
            write(S, base); assert sha(S) == bsha

    # C1 — THE COMPANION'S OWN MUTATION. Make Create refuse project_id for EVERYONE. The refusal
    # assertion still passes (it only wants an error naming the field); the COMPANION must red.
    # Without this the companion is present but justified by nothing.
    name = "C1 Create refuses project_id UNCONDITIONALLY (blanket, not tenancy)"
    print(f"\n=== {name}\n    PREDICTED: CAUGHT — the companion reds, the refusal assertion does not")
    try:
        anchor = "\t// Object-graph integrity: optional cross-object refs must be in this workspace.\n"
        inject = (anchor + '\tif issue.ProjectID != nil && *issue.ProjectID != "" {\n'
                  '\t\treturn nil, fmt.Errorf("issue: project_id refused")\n\t}\n')
        assert base.count(anchor) == 1
        write(S, base.replace(anchor, inject, 1))
        c, o = run(T)
        companion = "REFUSED the workspace's own project_id" in o
        v = "CAUGHT" if (c and companion) else ("RED BUT NOT THE COMPANION" if c else "NOT CAUGHT")
        print(f"    ACTUAL:    {v}")
        results.append((name, "CAUGHT", v))
    finally:
        write(S, base); assert sha(S) == bsha

    c, _ = run(T)
    print(f"\nU0' after restores -> {'GREEN' if c==0 else 'RED'}; store.go sha256 identical: {sha(S)==bsha}")
    print("\n" + "="*74)
    ok = True
    for n, p, a in results:
        agree = p == a; ok &= agree
        print(f"{'OK ' if agree else 'XX '} {n:<52} predicted={p:<8} actual={a}")
    print("="*74)
    print("ALL CONTROLS AS PREDICTED" if ok else "MISMATCH — read the XX rows")
    return 0 if ok else 1

if __name__ == "__main__": sys.exit(main())
