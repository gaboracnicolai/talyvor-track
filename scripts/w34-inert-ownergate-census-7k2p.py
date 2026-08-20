#!/usr/bin/env python3
"""Census: for each owner-gated op, is the REFUSAL guarded, or only the GATE's presence?

THE QUESTION.  `.semgrep/owner-gate.yml` USED TO assert only that a guard is WRITTEN --
`pattern` the handler AND `pattern-not` the handler containing `if !authz.IsOwner(...) {
... }`.  Semgrep's `...` matches any body, so a gate that wrote 403 and then FELL THROUGH
still satisfied the pattern-not and the rule stayed green.  scripts/
w34-ownergate-lock-controls-3w9m.py proves all eight rules fire when the gate is DELETED;
it said nothing about a gate that is present and inert.

An inert gate is the worse failure.  `writeErr` writes the 403 header first, so the
client is told "owner role required" while execution continues into the store call: the
response says refused and the write LANDS.  A test that asserts only `rr.Code == 403`
passes either way.

WHAT THIS MEASURED, BEFORE THE FIX IN THIS COMMIT (main 6ab790d, real Postgres, -race,
whole repository):

    site                    semgrep      go test
    I1 workspace Update     NOT CAUGHT   NOT CAUGHT   <- nothing in the repo saw it
    I2 workspace Delete     NOT CAUGHT   CAUGHT       (wsExists reads the effect back)
    I3 team Delete          NOT CAUGHT   CAUGHT       (teamExists)
    I4 project Delete       NOT CAUGHT   CAUGHT       (projExists)
    I5 integrations set     NOT CAUGHT   NOT CAUGHT   <- and this one stores a live token
    I6..I8 member writes    NOT CAUGHT   CAUGHT       (TestMemberMgmt_OwnerGated)

Eight of eight invisible to the lock; two of eight invisible to EVERYTHING.  I1 and I5
are the two whose tests asserted `rr.Code == 403` and never read the effect back -- and
their four siblings, which do read it back, are what proves this instrument can see a
catch at all.  I6..I8 falsified the prediction written before the run, in the
under-crediting direction: recorded here rather than tuned away.

THE FIX has two halves because the finding has two.  The rules now require the gate to
`return` (structural, all eight sites); the two tests now read the effect back
(behavioural, the two instances).  Every row above is CAUGHT/CAUGHT after it.

MUTATION.  One `return` removed from inside the named owner gate, one site at a time --
the smallest edit that keeps the gate present and makes its refusal do nothing.  This is
an ordinary Go regression shape: `writeErr` does not stop execution, and nothing in the
type system says the branch must return.

SCORING.  By SET SUBTRACTION of failing test names against a measured C0, never by an
exit code.  A build failure is VOID, not a catch -- a mutation that does not compile has
not been caught by anything.  A NOT CAUGHT verdict from the targeted package is re-asked
of the WHOLE REPOSITORY before it is believed, because a guard for a handler can live in
another package.

Run from the repo root with TRACK_TEST_DATABASE_URL set:
    python3 scripts/w34-inert-ownergate-census-7k2p.py
Exit 0 = every prediction matched.  Exit 1 = a prediction was wrong; read the table.
"""

import hashlib
import json
import os
import re
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
RULES = os.path.join(ROOT, ".semgrep")

FAILURES = []

GATE_RETURN = '\t\twriteErr(w, http.StatusForbidden, "OWNER_REQUIRED", "owner role required")\n\t\treturn\n\t}\n'
GATE_INERT = '\t\twriteErr(w, http.StatusForbidden, "OWNER_REQUIRED", "owner role required")\n\t}\n'


def semgrep_hits():
    """Rule ids the shipped lock reports over the shipped targets."""
    proc = subprocess.run(
        ["semgrep", "scan", "--config", RULES, "--metrics=off", "--json", "internal/", "cmd/"],
        cwd=ROOT, capture_output=True, text=True,
    )
    try:
        data = json.loads(proc.stdout)
    except json.JSONDecodeError:
        raise SystemExit(f"semgrep produced no JSON (rc={proc.returncode}):\n{proc.stderr[-2000:]}")
    if data.get("errors"):
        raise SystemExit("semgrep reported errors: " + json.dumps(data["errors"])[:2000])
    return {r["check_id"].split(".")[-1] for r in data["results"]}


def go_test(targets):
    """Return (failing test names, build_failed). Build failure is VOID, not a catch."""
    proc = subprocess.run(
        ["go", "test", "-race", "-count=1", *targets],
        cwd=ROOT, capture_output=True, text=True,
    )
    out = proc.stdout + proc.stderr
    build_failed = "[build failed]" in out or "[setup failed]" in out
    fails = set(re.findall(r"^--- FAIL: (\S+)", out, re.M))
    fails |= {f"PKG:{p}" for p in re.findall(r"^FAIL\s+(\S+/\S+)\s", out, re.M) if not fails}
    return fails, build_failed


class Mutation:
    """Exact-text substitution in one file; restored byte-for-byte and sha256-verified."""

    def __init__(self, rel, old, new):
        self.path = os.path.join(ROOT, rel)
        self.rel, self.old, self.new = rel, old, new

    def __enter__(self):
        with open(self.path, encoding="utf-8") as fh:
            self.src = fh.read()
        self.sha = hashlib.sha256(self.src.encode("utf-8")).hexdigest()
        n = self.src.count(self.old)
        if n != 1:
            raise SystemExit(
                f"CONTROL IS STALE: expected exactly 1 occurrence in {self.rel}, found {n}. "
                "Re-anchor before trusting any result."
            )
        with open(self.path, "w", encoding="utf-8") as fh:
            fh.write(self.src.replace(self.old, self.new))
        return self

    def __exit__(self, *exc):
        with open(self.path, "w", encoding="utf-8") as fh:
            fh.write(self.src)
        if hashlib.sha256(open(self.path, "rb").read()).hexdigest() != self.sha:
            FAILURES.append(f"{self.rel} NOT restored byte-for-byte")
        return False


# (label, file, package under test, anchor that must precede the gate, predicted semgrep,
#  predicted go-test verdict)
#   AFTER the fix in this commit every site is (True, True): the rule requires the gate to
#   return, and the two tests that read only the status code now read the effect back.
#   The pre-fix column is in the docstring above -- it is what this file measured.
SITES = [
    ("I1 workspace Update", "internal/workspace/handler.go", "./internal/workspace/",
     "\tif !authz.IsOwner(r.Context()) { // owner-gated: changing workspace settings\n",
     True, True),
    ("I2 workspace Delete", "internal/workspace/handler.go", "./internal/workspace/",
     "\tif !authz.IsOwner(r.Context()) { // owner-gated: deleting the entire workspace\n",
     True, True),
    ("I3 team Delete", "internal/team/handler.go", "./internal/team/",
     "\tif !authz.IsOwner(r.Context()) { // owner-gated: deleting a team\n",
     True, True),
    ("I4 project Delete", "internal/project/handler.go", "./internal/project/",
     "\tif !authz.IsOwner(r.Context()) { // owner-gated: deleting a project\n",
     True, True),
    ("I5 integrations set", "internal/integrations/handler.go", "./internal/integrations/",
     "\tif !authz.IsOwnerRole(m.Role) {\n",
     True, True),
]

MEMBER_SITES = [
    ("I6 member Add", "Add", True, True),
    ("I7 member ChangeRole", "ChangeRole", True, True),
    ("I8 member Remove", "Remove", True, True),
]

MEMBER_GATE = "\twsID, ok := h.requireOwner(w, r)\n\tif !ok {\n\t\treturn\n\t}\n"
MEMBER_INERT = "\twsID, ok := h.requireOwner(w, r)\n\t_ = ok\n"


def member_mutation(func_name):
    """Make the gate inert in exactly ONE of the three member writes, by name."""
    rel = "internal/member/mgmt_handler.go"
    src = open(os.path.join(ROOT, rel), encoding="utf-8").read()
    marker = f"func (h *MgmtHandler) {func_name}(w http.ResponseWriter, r *http.Request) {{"
    i = src.index(marker)
    j = src.index(MEMBER_GATE, i)
    return rel, src, src[:j] + MEMBER_INERT + src[j + len(MEMBER_GATE):]


def score(label, pred_semgrep, pred_test, hits, fails, build_failed, base_hits, base_fails):
    new_hits = hits - base_hits
    new_fails = fails - base_fails
    sg = bool(new_hits)
    tst = bool(new_fails)
    if build_failed:
        FAILURES.append(f"{label}: VOID — the mutation did not compile")
        print(f"  VOID  {label}: did not compile; a build error is not a caught mutation")
        return
    ok = (sg == pred_semgrep) and (tst == pred_test)
    print(f"  {'PASS' if ok else 'FAIL'}  {label}: "
          f"semgrep {'CAUGHT ' + str(sorted(new_hits)) if sg else 'NOT CAUGHT'} · "
          f"go test {'CAUGHT ' + str(sorted(new_fails)) if tst else 'NOT CAUGHT'}"
          f"{'' if ok else f'  (predicted semgrep={pred_semgrep} test={pred_test})'}")
    if not ok:
        FAILURES.append(label)


def main():
    if not os.environ.get("TRACK_TEST_DATABASE_URL"):
        raise SystemExit("TRACK_TEST_DATABASE_URL is unset; the real-Postgres tests would "
                         "fail for the wrong reason and every verdict would be noise.")

    print("C0  unmutated tree — the lock is green and the owner-gate tests pass")
    base_hits = semgrep_hits()
    pkgs = sorted({s[2] for s in SITES} | {"./internal/member/"})
    base_fails, base_build = go_test(pkgs)
    print(f"  semgrep findings: {sorted(base_hits) or '(none)'}")
    print(f"  failing tests:    {sorted(base_fails) or '(none)'}")
    if base_hits or base_fails or base_build:
        FAILURES.append("C0 is not clean; every verdict below would be measured against noise")

    print("\nS+  INSTRUMENT CONTROL: delete the gate entirely (not merely its return).")
    print("    semgrep MUST fire, or every NOT CAUGHT below is an instrument that reads nothing.")
    with Mutation("internal/workspace/handler.go",
                  "\tif !authz.IsOwner(r.Context()) { // owner-gated: changing workspace settings\n" + GATE_RETURN,
                  ""):
        hits = semgrep_hits() - base_hits
        ok = "workspace-update-requires-owner" in hits
        print(f"  {'PASS' if ok else 'FAIL'}  S+ gate deleted: "
              f"{'CAUGHT by ' + str(sorted(hits)) if ok else 'NOT CAUGHT — INSTRUMENT IS DEAD'}")
        if not ok:
            FAILURES.append("S+ instrument control")

    print("\nI1..I5  the gate stays, its `return` goes. 403 is written; execution continues.")
    for label, rel, pkg, anchor, pred_sg, pred_t in SITES:
        with Mutation(rel, anchor + GATE_RETURN, anchor + GATE_INERT):
            hits = semgrep_hits()
            fails, bf = go_test([pkg])
            score(label, pred_sg, pred_t, hits, fails, bf, base_hits, base_fails)

    print("\nI6..I8  the member writes: requireOwner is still called, its answer is ignored.")
    for label, func, pred_sg, pred_t in MEMBER_SITES:
        rel, src, mutated = member_mutation(func)
        path = os.path.join(ROOT, rel)
        sha = hashlib.sha256(src.encode("utf-8")).hexdigest()
        open(path, "w", encoding="utf-8").write(mutated)
        try:
            hits = semgrep_hits()
            fails, bf = go_test(["./internal/member/"])
            score(label, pred_sg, pred_t, hits, fails, bf, base_hits, base_fails)
        finally:
            open(path, "w", encoding="utf-8").write(src)
            if hashlib.sha256(open(path, "rb").read()).hexdigest() != sha:
                FAILURES.append(f"{rel} NOT restored byte-for-byte")

    print("\n" + "=" * 78)
    if FAILURES:
        print(f"{len(FAILURES)} PREDICTION(S) WRONG:")
        for f in FAILURES:
            print("  - " + f)
        return 1
    print("all predictions matched")
    return 0


if __name__ == "__main__":
    sys.exit(main())
