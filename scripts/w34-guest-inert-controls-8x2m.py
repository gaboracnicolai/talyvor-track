#!/usr/bin/env python3
"""w34-guest-inert-controls-8x2m.py — controls for the guest WS_MISMATCH gate, both halves.

UNDER TEST
  structural : .semgrep/workspace-authz.yml rule A, whose guest exemption now requires `return`
  behavioural: internal/guest/read_tenancy_test.go (the two READ routes)
               internal/guest/write_test.go#TestGuestComment_CrossWorkspace_403 (the write route)

THE FINDING, MEASURED AT bceb6c5 BEFORE EITHER HALF EXISTED. Rule A exempts a function containing
`if $CLAIMS.WorkspaceID != $WS { ... }`. Semgrep's `...` matches ANY body, so a gate that writes
its 403 and FALLS THROUGH bought the exemption. Deleting one `return` per site scored:

    GuestCreateComment  semgrep NOT CAUGHT · go test CAUGHT
    GuestListIssues     semgrep NOT CAUGHT · go test NOT CAUGHT
    GuestGetIssue       semgrep NOT CAUGHT · go test NOT CAUGHT

Three of three invisible to the lock; the TWO READ routes invisible to EVERYTHING — and those are
the routes that return another workspace's rows in the body under the 403.

PROTOCOL: predicted verdict declared before each run; sha256-verified restores; a CAUGHT must be
attributable (semgrep must name the rule id; a test red must name the route's own test).
"""
import hashlib, os, subprocess, sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
G = "internal/guest/handler.go"
RULES = ".semgrep/workspace-authz.yml"
SEMGREP = ["semgrep", "scan", "--config", ".semgrep/", "--error", "--metrics=off", "internal/", "cmd/"]
GUEST = ["go", "test", "-count=1", "./internal/guest/"]

GATE = """	if claims.WorkspaceID != wsID {
		writeErr(w, http.StatusForbidden, "WS_MISMATCH", "workspace mismatch")
		return
	}"""
INERT = """	if claims.WorkspaceID != wsID {
		writeErr(w, http.StatusForbidden, "WS_MISMATCH", "workspace mismatch")
	}"""

SITES = {"GuestCreateComment": "TestGuestComment_CrossWorkspace_403",
         "GuestListIssues": "TestGuestListIssues_CrossWorkspace_403",
         "GuestGetIssue": "TestGuestGetIssue_CrossWorkspace_403"}

def read(p):
    with open(os.path.join(REPO, p), encoding="utf-8") as f: return f.read()
def write(p, t):
    with open(os.path.join(REPO, p), "w", encoding="utf-8") as f: f.write(t)
def sha(p):
    with open(os.path.join(REPO, p), "rb") as f: return hashlib.sha256(f.read()).hexdigest()
def run(cmd):
    p = subprocess.run(cmd, cwd=REPO, capture_output=True, text=True)
    return p.returncode, p.stdout + p.stderr

def mutate_one(base, fn):
    i = base.index(f"func (h *Handler) {fn}(")
    j = base.index(GATE, i)
    return base[:j] + INERT + base[j+len(GATE):]

def main():
    base, bsha = read(G), sha(G)
    rbase, rsha = read(RULES), sha(RULES)
    assert base.count(GATE) == 3, base.count(GATE)
    sc, _ = run(SEMGREP); gc, _ = run(GUEST)
    print(f"U0 semgrep {'GREEN' if sc==0 else 'RED'} | guest tests {'GREEN' if gc==0 else 'RED'}")
    if sc or gc: raise SystemExit("baseline not green")
    results = []

    for fn, test in SITES.items():
        name = f"I:{fn} gate made INERT (`return` deleted, writeErr kept)"
        print(f"\n=== {name}\n    PREDICTED: semgrep CAUGHT (was NOT CAUGHT) + test {test} CAUGHT")
        try:
            write(G, mutate_one(base, fn))
            sc, sout = run(SEMGREP)
            gc, gout = run(GUEST)
            sv = "CAUGHT" if (sc and "workspace-from-url-param-bypasses-membership" in sout) else \
                 ("RED BUT NOT BY RULE A" if sc else "NOT CAUGHT")
            tv = "CAUGHT" if (gc and f"--- FAIL: {test}" in gout) else \
                 ("RED BUT NOT BY ITS OWN TEST" if gc else "NOT CAUGHT")
            print(f"    ACTUAL:    semgrep {sv} | test {tv}")
            results.append((name, "CAUGHT/CAUGHT", f"{sv}/{tv}"))
        finally:
            write(G, base); assert sha(G) == bsha

    # V1 — VACUITY CONTROL ON THE RULE ITSELF. If rule A stopped matching chi.URLParam at all it
    # would report a clean product no matter what the guards did. Blind it and demand it goes quiet
    # WHILE the defect is present, proving the CAUGHTs above come from the rule and not from luck.
    name = "V1 rule A blinded (its chi.URLParam pattern renamed) WITH an inert gate present"
    print(f"\n=== {name}\n    PREDICTED: semgrep NOT CAUGHT — the rule matching nothing is silent")
    try:
        write(G, mutate_one(base, "GuestListIssues"))
        write(RULES, rbase.replace('- pattern: chi.URLParam($R, "wsID")',
                                   '- pattern: chi.URLParam($R, "wsIDNOPE")', 1))
        sc, sout = run(SEMGREP)
        sv = "NOT CAUGHT" if sc == 0 else "CAUGHT"
        print(f"    ACTUAL:    semgrep {sv}")
        results.append((name, "NOT CAUGHT", sv))
    finally:
        write(G, base); write(RULES, rbase)
        assert sha(G) == bsha and sha(RULES) == rsha, "RESTORE FAILED"

    # C-NAME — THE HONEST LIMIT OF PINNING THE EXEMPTION TO THE LITERAL `claims`. Rename that
    # binding on a CORRECT route: the exemption stops matching and the rule FIRES on innocent code.
    # That is fail-OPEN (noisy) rather than fail-CLOSED (silent), which is the trade this rule takes
    # on purpose — but it is scored here so the limit is measured rather than discovered.
    name = "C-NAME the `claims` binding renamed on CORRECT code (exemption is name-keyed)"
    print(f"\n=== {name}\n    PREDICTED: semgrep CAUGHT — fails OPEN (a false positive), not a miss")
    try:
        # Rename EVERY `claims` inside GuestGetIssue only, so the mutation COMPILES. A build
        # failure is not a caught mutation — this repo learned that the hard way on #175's N1.
        i = base.index("func (h *Handler) GuestGetIssue(")
        nxt = base.find("\nfunc ", i + 1)
        j = nxt if nxt != -1 else len(base)  # GuestGetIssue is the last func in the file
        body = base[i:j].replace("claims", "tok")
        write(G, base[:i] + body + base[j:])
        bc, bout = run(["go", "build", "./internal/guest/"])
        if bc:
            print("    (mutation did not compile — reported, not hidden)")
            results.append((name, "CAUGHT", "DID NOT COMPILE"))
        else:
            sc, sout = run(SEMGREP)
            sv = "CAUGHT" if (sc and "workspace-from-url-param-bypasses-membership" in sout) else \
                 ("RED BUT NOT BY RULE A" if sc else "NOT CAUGHT")
            print(f"    ACTUAL:    semgrep {sv}")
            results.append((name, "CAUGHT", sv))
    finally:
        write(G, base); assert sha(G) == bsha

    sc, _ = run(SEMGREP); gc, _ = run(GUEST)
    print(f"\nU0' semgrep {'GREEN' if sc==0 else 'RED'} | guest {'GREEN' if gc==0 else 'RED'}; "
          f"sha256 identical: handler={sha(G)==bsha} rules={sha(RULES)==rsha}")
    print("\n" + "="*78)
    ok = True
    for n, p, a in results:
        agree = p == a; ok &= agree
        print(f"{'OK ' if agree else 'XX '} {n}\n     predicted={p}  actual={a}")
    print("="*78)
    print("ALL CONTROLS AS PREDICTED" if ok else "MISMATCH — read the XX rows")
    return 0 if ok else 1

if __name__ == "__main__": sys.exit(main())
