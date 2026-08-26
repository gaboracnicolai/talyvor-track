#!/usr/bin/env python3
"""w34-suppression-census-controls-8x2m.py — controls for the nosemgrep suppression census.

UNDER TEST
  internal/tenancy/suppression_census_test.go
  TestSemgrepSuppressions_AreCountedAndJustified

WHY. .semgrep/operate-by-id-tenancy.yml claimed "the only remaining suppressions are three INLINE
nosemgrep lines". MEASURED at 74ca01b: NINE across four rule ids, FIVE of them in the family that
sentence describes, and its named list omits workspace/store.go:276 entirely. A prose census of
holes goes stale the moment someone adds a hole. This test replaces the sentence.

THE GUARD PASSED ON ITS FIRST RUN, so every claim it makes is mutated here and scored.
PROTOCOL: predicted verdict declared first; sha256-verified restores; a CAUGHT must name the
assertion that fired, not merely be a red.
"""
import hashlib, os, subprocess, sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
GUARD_SRC = "internal/tenancy/suppression_census_test.go"
VICTIM = "internal/label/store.go"          # any production file with no suppression today
GUARD = ["go", "test", "-count=1", "-run",
         "TestSemgrepSuppressions_AreCountedAndJustified", "./internal/tenancy/"]

def read(p): return open(os.path.join(REPO, p), encoding="utf-8").read()
def write(p, t): open(os.path.join(REPO, p), "w", encoding="utf-8").write(t)
def sha(p): return hashlib.sha256(open(os.path.join(REPO, p), "rb").read()).hexdigest()
def run(c):
    p = subprocess.run(c, cwd=REPO, capture_output=True, text=True)
    return p.returncode, p.stdout + p.stderr

def main():
    vb, vsha = read(VICTIM), sha(VICTIM)
    gb, gsha = read(GUARD_SRC), sha(GUARD_SRC)
    anchor = "package label\n"
    assert vb.count(anchor) == 1
    code, out = run(GUARD)
    print(f"U0 pristine -> {'GREEN' if code==0 else 'RED'}")
    if code: print(out[-2000:]); raise SystemExit("baseline not green")
    results = []

    def score(name, pred, edits, must):
        print(f"\n=== {name}\n    PREDICTED: {pred}")
        try:
            for p, txt in edits: write(p, txt)
            c, o = run(GUARD)
            v = "CAUGHT" if (c and must in o) else ("RED BUT NOT THAT ASSERTION" if c else "NOT CAUGHT")
            print(f"    ACTUAL:    {v}")
            for l in o.splitlines():
                if must[:28] in l: print("      | " + l.strip()[:150]); break
            results.append((name, pred, v))
        finally:
            write(VICTIM, vb); write(GUARD_SRC, gb)
            assert sha(VICTIM) == vsha and sha(GUARD_SRC) == gsha, "RESTORE FAILED"

    # S1 — a BARE suppression with no justification anywhere.
    score("S1 a new suppression with NO INVALIDATED IF clause", "CAUGHT",
          [(VICTIM, vb.replace(anchor, anchor + "\n// nosemgrep: child-insert-requires-parent-workspace-guard\n", 1))],
          "with no INVALIDATED IF clause")

    # S2 — a FULLY JUSTIFIED new suppression. Still a new hole; the ceiling must say so.
    score("S2 a new suppression that IS justified (still a new hole -> the ceiling must fire)", "CAUGHT",
          [(VICTIM, vb.replace(anchor, anchor + "\n// INVALIDATED IF anything at all.\n"
            "// nosemgrep: child-insert-requires-parent-workspace-guard\n", 1))],
          "ceiling is")

    # S3 — a suppression of a rule the census has never heard of.
    score("S3 a suppression of an UNKNOWN rule id", "CAUGHT",
          [(VICTIM, vb.replace(anchor, anchor + "\n// INVALIDATED IF anything at all.\n"
            "// nosemgrep: some-rule-nobody-counted\n", 1))],
          "a rule this census has never seen")

    # S4 — BLIND THE SCAN. A census that matches nothing must not report a clean product.
    score("S4 the scan blinded (its nosemgrep token renamed) — the FLOOR must fire", "CAUGHT",
          [(GUARD_SRC, gb.replace('regexp.MustCompile(`nosemgrep:\\s*([\\w-]+)`)',
                                  'regexp.MustCompile(`nosemgrepNOPE:\\s*([\\w-]+)`)', 1))],
          "does not match its own sample")

    # S5 — MUST STAY GREEN: REMOVING a suppression closes a hole and may never red a build.
    print("\n=== S5 MUST STAY GREEN: a suppression REMOVED (a hole closed)\n    PREDICTED: NOT CAUGHT")
    try:
        t = read("internal/featureboard/store.go")
        i = t.index("// nosemgrep: operate-by-id-write-requires-workspace-scope")
        j = t.index("\n", i)
        fb, fbsha = t, sha("internal/featureboard/store.go")
        write("internal/featureboard/store.go", t[:i] + "//" + t[j:])
        c, _ = run(GUARD)
        v = "NOT CAUGHT" if c == 0 else "CAUGHT"
        print(f"    ACTUAL:    {v}")
        results.append(("S5 a suppression REMOVED (a hole closed)", "NOT CAUGHT", v))
    finally:
        write("internal/featureboard/store.go", fb)
        assert sha("internal/featureboard/store.go") == fbsha, "RESTORE FAILED"

    c, _ = run(GUARD)
    print(f"\nU0' after restores -> {'GREEN' if c==0 else 'RED'}; all files sha256-identical")
    print("\n" + "="*76)
    ok = True
    for n, p, a in results:
        agree = p == a; ok &= agree
        print(f"{'OK ' if agree else 'XX '} {n}\n     predicted={p:<11} actual={a}")
    print("="*76)
    print("ALL CONTROLS AS PREDICTED" if ok else "MISMATCH — read the XX rows")
    return 0 if ok else 1

if __name__ == "__main__": sys.exit(main())
