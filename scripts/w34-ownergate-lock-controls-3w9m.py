#!/usr/bin/env python3
"""Positive controls for the EIGHT owner-gate rules in .semgrep/owner-gate.yml.

WHY THIS EXISTS.  `.semgrep/owner-gate.yml` is a regression lock over the privileged
operations of the multi-member tier model: delete a workspace, change its settings, delete
a team, delete a project, write a live provider credential, and the three member-roster
writes.  Every one of them is an authz decision, and the ONLY thing standing over them in
CI is this file.

The lock is GREEN on every run, and a lock that is green because it can never be red is
indistinguishable from one that works.  `scripts/w34-tenancy-lock-visibility-controls.py`
already asserts the SQL-text tenancy rules both directions, but its vacuity control (C4)
strips `pattern-not-inside` only from rule files that contain `metavariable-regex` --
owner-gate.yml contains none, so all eight of these rules are OUTSIDE every control this
repository has.  This harness closes that: for each rule, remove the guard it names and
assert THAT RULE ID fires, on THAT file.

METHOD.  One mutation at a time, applied to the working tree, scanned with the SHIPPED
config and the SHIPPED target set (`internal/ cmd/` -- the same two arguments the
`tenancy-lock` CI job passes), then restored in a `finally` and sha256-verified.  A control
asserts the RULE ID, not merely "something fired": a scan that reds for an unrelated rule
would otherwise be scored as a catch.

Run from the repo root:  python3 scripts/w34-ownergate-lock-controls-3w9m.py
Exit 0 = every prediction matched.  Exit 1 = a prediction was wrong; read the table.
"""

import hashlib
import json
import os
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
RULES = os.path.join(ROOT, ".semgrep")

FAILURES = []

OWNER_BLOCK = """\t\twriteErr(w, http.StatusForbidden, "OWNER_REQUIRED", "owner role required")
\t\treturn
\t}
"""


def scan(config=RULES, targets=("internal/", "cmd/")):
    """Return the set of (rule_id, relative path) the lock reports."""
    proc = subprocess.run(
        ["semgrep", "scan", "--config", config, "--metrics=off", "--json", *targets],
        cwd=ROOT, capture_output=True, text=True,
    )
    try:
        data = json.loads(proc.stdout)
    except json.JSONDecodeError:
        raise SystemExit(f"semgrep produced no JSON (rc={proc.returncode}):\n{proc.stderr[-2000:]}")
    if data.get("errors"):
        # A rule that fails to PARSE reports zero findings and would score every control
        # NOT CAUGHT.  That is the failure mode this whole harness exists to detect, so it
        # is fatal here rather than silent.
        raise SystemExit("semgrep reported errors: " + json.dumps(data["errors"])[:2000])
    return {(r["check_id"], r["path"]) for r in data["results"]}


class Mutation:
    """Apply an exact-text substitution to one file; restore it byte-for-byte on exit."""

    def __init__(self, rel, old, new, count=1):
        self.path = os.path.join(ROOT, rel)
        self.rel, self.old, self.new, self.count = rel, old, new, count

    def __enter__(self):
        with open(self.path, encoding="utf-8") as fh:
            self.src = fh.read()
        self.sha = hashlib.sha256(self.src.encode("utf-8")).hexdigest()
        n = self.src.count(self.old)
        if n != self.count:
            raise SystemExit(
                f"CONTROL IS STALE: expected {self.count} occurrence(s) of the guard in "
                f"{self.rel}, found {n}. Re-anchor before trusting any result."
            )
        with open(self.path, "w", encoding="utf-8") as fh:
            fh.write(self.src.replace(self.old, self.new))
        return self

    def __exit__(self, *exc):
        with open(self.path, "w", encoding="utf-8") as fh:
            fh.write(self.src)
        back = hashlib.sha256(open(self.path, "rb").read()).hexdigest()
        if back != self.sha:
            FAILURES.append(f"{self.rel} NOT restored byte-for-byte")
        return False


def check(name, got, want_rule, want_file):
    """want_rule None => predict NOT CAUGHT (no finding for this subject)."""
    if want_rule is None:
        ok = not got
        verdict = "GREEN" if ok else "RED"
        print(f"  {'PASS' if ok else 'FAIL'}  {name}: {verdict} ({sorted(got)})")
        if not ok:
            FAILURES.append(name)
        return
    hits = {(rid, path) for rid, path in got if rid.endswith(want_rule)}
    on_file = {(rid, path) for rid, path in hits if path.endswith(want_file)}
    ok = bool(on_file)
    print(f"  {'PASS' if ok else 'FAIL'}  {name}: "
          f"{'CAUGHT by ' + want_rule + ' on ' + want_file if ok else 'NOT CAUGHT'}"
          f"{'' if ok else '  (all findings: ' + str(sorted(got)) + ')'}")
    if not ok:
        FAILURES.append(name)


# (label, rule id suffix, file, old text, new text, occurrences)
# Each mutation is the SHAPE of the real regression: the owner gate is dropped and the
# handler proceeds.  The member three drop the gate for a plain workspace read, which is
# what a refactor toward "one way to get wsID" would actually produce.
CONTROLS = [
    ("O1 workspace Update loses its owner gate",
     "workspace-update-requires-owner", "internal/workspace/handler.go",
     '\tif !authz.IsOwner(r.Context()) { // owner-gated: changing workspace settings\n' + OWNER_BLOCK,
     "", 1),
    ("O2 workspace Delete loses its owner gate",
     "workspace-delete-requires-owner", "internal/workspace/handler.go",
     '\tif !authz.IsOwner(r.Context()) { // owner-gated: deleting the entire workspace\n' + OWNER_BLOCK,
     "", 1),
    ("O3 team Delete loses its owner gate",
     "team-delete-requires-owner", "internal/team/handler.go",
     '\tif !authz.IsOwner(r.Context()) { // owner-gated: deleting a team\n' + OWNER_BLOCK,
     "", 1),
    ("O4 project Delete loses its owner gate",
     "project-delete-requires-owner", "internal/project/handler.go",
     '\tif !authz.IsOwner(r.Context()) { // owner-gated: deleting a project\n' + OWNER_BLOCK,
     "", 1),
    ("O5 integrations set loses its owner gate",
     "integrations-set-requires-owner", "internal/integrations/handler.go",
     '\tif !authz.IsOwnerRole(m.Role) {\n' + OWNER_BLOCK,
     "", 1),
]

MEMBER_SITES = [
    ("O6 member Add stops calling requireOwner", "member-add-requires-owner", "Add"),
    ("O7 member ChangeRole stops calling requireOwner", "member-changerole-requires-owner", "ChangeRole"),
    ("O8 member Remove stops calling requireOwner", "member-remove-requires-owner", "Remove"),
]


def member_mutation(func_name):
    """Drop the gate in exactly ONE of the three member writes, by name."""
    rel = "internal/member/mgmt_handler.go"
    src = open(os.path.join(ROOT, rel), encoding="utf-8").read()
    marker = f"func (h *MgmtHandler) {func_name}(w http.ResponseWriter, r *http.Request) {{"
    i = src.index(marker)
    call = "\twsID, ok := h.requireOwner(w, r)\n"
    j = src.index(call, i)
    return rel, src, src[:j] + "\twsID, ok := authz.WorkspaceID(r.Context())\n" + src[j + len(call):]


def main():
    print("C0  unmutated tree, shipped rules, shipped targets — the lock must be GREEN")
    base = scan()
    check("C0 baseline", base, None, None)

    print("\nO1..O5  remove each named owner gate, one at a time. Each must red ITS OWN rule.")
    for name, rule, rel, old, new, count in CONTROLS:
        with Mutation(rel, old, new, count):
            check(name, scan(), rule, rel)

    print("\nO6..O8  the three member-roster writes. Only the mutated one may fire.")
    for name, rule, func in MEMBER_SITES:
        rel, src, mutated = member_mutation(func)
        path = os.path.join(ROOT, rel)
        sha = hashlib.sha256(src.encode("utf-8")).hexdigest()
        open(path, "w", encoding="utf-8").write(mutated)
        try:
            got = scan()
            check(name, got, rule, rel)
            others = {rid for rid, _ in got if rid.endswith("-requires-owner")
                      and not rid.endswith(rule)}
            if others:
                FAILURES.append(f"{name}: sibling rules also fired: {sorted(others)}")
                print(f"           ^ SIBLINGS ALSO FIRED (mutation is not site-specific): {sorted(others)}")
        finally:
            open(path, "w", encoding="utf-8").write(src)
            if hashlib.sha256(open(path, "rb").read()).hexdigest() != sha:
                FAILURES.append(f"{rel} NOT restored byte-for-byte")

    print("\nC9  MUST-STAY-GREEN companion: a change inside a gated handler that is NOT the")
    print("    gate must not fire anything. Otherwise 'CAUGHT' is a catch-all.")
    with Mutation("internal/project/handler.go",
                  'writeErr(w, http.StatusForbidden, "OWNER_REQUIRED", "owner role required")',
                  'writeErr(w, http.StatusForbidden, "OWNER_REQUIRED", "owner role is required")',
                  count=1):
        check("C9 message reworded, gate intact -> GREEN", scan(), None, None)

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
