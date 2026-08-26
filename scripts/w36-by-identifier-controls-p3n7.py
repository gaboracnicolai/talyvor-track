#!/usr/bin/env python3
"""Positive controls for W3.6's by-identifier route guards.

THE ONE THAT MATTERS IS M1. TestByIdentifier_ForeignWorkspaceIs404_AndIndistinguishableFromAbsent
PASSED BEFORE THE ROUTE EXISTED — everything 404s when nothing is mounted, so at that moment it was
a guard that could not fail. It is only worth anything if a route that LEAKS makes it red, and that
is what M1 checks: replace the shared 404 with a distinguishing 403, i.e. the cross-tenant existence
oracle SEC-5 removed for ids and this route must not reintroduce for guessable identifiers.

PREDICTED BEFORE THE RUN, disjoint:

  M1  foreign workspace answers 403 instead of the shared 404  -> ONLY the no-oracle test
  M2  the lookup ignores the workspace scope (store call gets
      the identifier as its own workspace)                     -> ONLY the no-oracle test, and for
                                                                  the OTHER reason: the foreign row
                                                                  becomes readable
  M3  the route returns the first issue rather than selecting  -> the siblings test (the
                                                                  population-of-one trap)
  M4  the route is unmounted                                   -> every test that needs it to work,
                                                                  and NOT the neighbour test

DISCIPLINE: needs a database and refuses without one; refuses on a dirty target or a non-green
tree; asserts each mutation changed bytes; restores in a `finally`; sha256-verifies; re-runs green.
"""
from __future__ import annotations

import hashlib
import os
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
HANDLER = ROOT / "internal" / "issue" / "handler.go"
PATTERN = "TestByIdentifier_"

ORACLE = "ByIdentifier_ForeignWorkspaceIs404_AndIndistinguishableFromAbsent"
OWN = "ByIdentifier_OwnWorkspaceResolves"
SIBLINGS = "ByIdentifier_SelectsAmongSiblings"
NEIGHBOURS = "ByIdentifier_DoesNotShadowTheExistingRoutes"
PARITY = "ByIdentifier_PayloadMatchesTheByIDRoute"


def sha(p: Path) -> str:
    return hashlib.sha256(p.read_bytes()).hexdigest()


def run() -> str:
    r = subprocess.run(["go", "test", "-count=1", "-run", PATTERN, "-v", "./internal/issue/"],
                       cwd=ROOT, capture_output=True, text=True)
    return r.stdout + r.stderr


def failed(out: str) -> set[str]:
    names = set()
    for ln in out.splitlines():
        ln = ln.strip()
        if ln.startswith("--- FAIL:"):
            n = ln[len("--- FAIL:"):].strip().split()[0]
            names.add(n[4:] if n.startswith("Test") else n)
    if not names and "--- FAIL:" in out:
        raise SystemExit("REFUSE: '--- FAIL:' present but no name parsed — classifier broken.")
    # ⚠ A MUTATION THAT DOES NOT COMPILE PRODUCES NO `--- FAIL:` LINES AT ALL, AND THE FIRST
    # VERSION OF THIS SCORED THAT AS "no test failed" — a NOT-CAUGHT verdict for a defect that was
    # never introduced. It happened on the first run, to two of three arms. A build failure is now
    # a REFUSAL: a campaign that cannot tell "the guard missed it" from "the code did not build"
    # is measuring nothing.
    if not names and ("[build failed]" in out or "# github.com" in out):
        raise SystemExit("REFUSE: the mutated tree DID NOT COMPILE, so no guard was given the "
                         "chance to catch anything. Fix the mutation; do not read this as a pass.\n"
                         + out[-1500:])
    return names


def mutate(old: str, new: str) -> str:
    before = HANDLER.read_text(encoding="utf-8")
    if before.count(old) != 1:
        raise SystemExit(f"REFUSE: anchor occurs {before.count(old)}x, want 1 — it has drifted.")
    HANDLER.write_text(before.replace(old, new, 1), encoding="utf-8")
    return before


CALL = '\tout, err := h.store.GetByIdentifier(r.Context(), chi.URLParam(r, "identifier"), wsID)\n\tif err != nil || out == nil {\n\t\twriteErr(w, http.StatusNotFound, "NOT_FOUND", "not found")\n\t\treturn\n\t}'

MUTATIONS = [
    # ⚠ M1 AS FIRST WRITTEN WAS NOT A MUTATION AT ALL AND THE RUN SAID SO. It changed the 404's
    # message text — but BOTH the foreign and the absent case take that same branch, so both
    # messages changed together and the two responses stayed IDENTICAL. Nothing went red, and
    # nothing should have. A mutation for this property has to make the two responses DIFFER.
    ("M1 the 404 echoes the identifier, so the two cases differ",
     CALL,
     '\tident := chi.URLParam(r, "identifier")\n'
     '\tout, err := h.store.GetByIdentifier(r.Context(), ident, wsID)\n'
     '\tif err != nil || out == nil {\n'
     '\t\twriteErr(w, http.StatusNotFound, "NOT_FOUND", "no issue "+ident+" here")\n'
     '\t\treturn\n\t}',
     {ORACLE}),

    # ⚠ M3 AS FIRST WRITTEN CALLED A FUNCTION SIGNATURE THAT DOES NOT EXIST, so the tree did
    # not compile and the campaign scored it NOT-CAUGHT. That is the hole the classifier above
    # now refuses on. This version compiles and is the realistic shape of the defect: resolve by
    # WORKSPACE and hand back the first row — precisely what the inert `?identifier=` filter
    # measured in W4.20 does.
    ("M3 return the first issue rather than selecting one",
     CALL,
     '\tlist, lerr := h.store.List(r.Context(), IssueFilter{WorkspaceID: wsID})\n'
     '\tif lerr != nil || len(list) == 0 {\n'
     '\t\twriteErr(w, http.StatusNotFound, "NOT_FOUND", "not found")\n'
     '\t\treturn\n\t}\n\tout := &list[0]',
     {SIBLINGS}),

    ("M4 the route is unmounted",
     '\t\tr.Get("/by-identifier/{identifier}", h.GetByIdentifier)\n',
     "",
     {OWN, SIBLINGS, PARITY}),
]


def main() -> int:
    if not os.environ.get("TRACK_TEST_DATABASE_URL"):
        print("REFUSE: TRACK_TEST_DATABASE_URL unset — every test here needs real Postgres, and a "
              "run that skipped them would report controls it never scored.")
        return 3
    dirty = subprocess.run(["git", "status", "--porcelain", "--", str(HANDLER)],
                           cwd=ROOT, capture_output=True, text=True).stdout.strip()
    if dirty:
        print("REFUSE: internal/issue/handler.go is already modified:\n" + dirty)
        return 3
    base = sha(HANDLER)

    out = run()
    if failed(out):
        print(f"REFUSE: not green on the untouched tree: {sorted(failed(out))}")
        return 3
    print(f"clean tree: {PATTERN}* GREEN\n")

    bad = 0
    for name, old, new, want in MUTATIONS:
        original = None
        try:
            original = mutate(old, new)
            got = failed(run())
            ok = got == want
            bad += 0 if ok else 1
            print(f"  [{'ok ' if ok else 'BAD'}] {name:<44} -> RED: {', '.join(sorted(got)) or '(none)'}")
            if not ok:
                print(f"      expected exactly: {', '.join(sorted(want))}")
        finally:
            if original is not None:
                HANDLER.write_text(original, encoding="utf-8")

    if sha(HANDLER) != base:
        print("\nBAD: handler.go not restored")
        bad += 1
    if failed(run()):
        print("\nBAD: not green again after restore")
        bad += 1
    print(f"\n{len(MUTATIONS) - bad} of {len(MUTATIONS)} controls behaved as predicted; "
          "handler.go restored and sha256-verified.")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
