#!/usr/bin/env python3
"""Positive controls for the .semgrep tenancy locks' VISIBILITY, both directions.

WHY THIS EXISTS. The three SQL-text tenancy rules gate on a `metavariable-regex` over the
statement argument. That regex reads the SOURCE TEXT of the argument expression, so it sees
an inline literal but is structurally blind to SQL held in an identifier:

    const insertSQL = `INSERT INTO issues (workspace_id, project_id, ...) VALUES (...)`
    s.pool.QueryRow(ctx, insertSQL, ...)      // $SQL binds to the NAME "insertSQL"

Measured at b532e56, BEFORE the "=~/.../" arm was added: cross-object-insert-requires-tenancy-
guard was written for 18 statements and could see 16. The two it could not see were
internal/issue/store.go Create and UpsertByIdentifier — every issue this product creates or
imports. Both were correctly guarded; NOTHING WOULD HAVE NOTICED IF THEY STOPPED BEING.

Each control MUTATES the working tree, scans, and RESTORES it. It asserts the direction it
expects: a mutation that removes a guard must turn the lock RED, and a scan of the unmutated
tree must be GREEN. A control that only ever observes green cannot tell a working lock from
an inert one, so the inert-lock case (C5) is asserted explicitly against the OLD rule shape.

Run from the repo root:  python3 scripts/w34-tenancy-lock-visibility-controls.py
"""

import hashlib
import json
import os
import subprocess
import sys
import tempfile

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
RULES = os.path.join(ROOT, ".semgrep")

# The rule shape as it stood before this merge: the raw-text arm only. Controls C5/C6 run
# against it to prove the blindness was REAL rather than asserted.
OLD_SHAPE = """rules:
  - id: old-cross-object-raw-text-only
    languages: [go]
    severity: ERROR
    message: old shape
    patterns:
      - pattern-either:
          - pattern: $Q.QueryRow($CTX, $SQL, ...)
          - pattern: $Q.Exec($CTX, $SQL, ...)
      - metavariable-regex:
          metavariable: $SQL
          regex: >-
            (?s).*INSERT\\s+INTO\\s+\\w+\\s*\\([^)]*\\bworkspace_id\\b[^)]*\\b(team_id|project_id|board_id|member_id|issue_id|cycle_id|parent_id|assignee_id)\\b[^)]*\\)\\s*VALUES|.*INSERT\\s+INTO\\s+\\w+\\s*\\([^)]*\\b(team_id|project_id|board_id|member_id|issue_id|cycle_id|parent_id|assignee_id)\\b[^)]*\\bworkspace_id\\b[^)]*\\)\\s*VALUES
      - pattern-not-inside: |
          func $F(...) $R {
            ...
            tenancy.AssertRefInWorkspace(...)
            ...
          }
      - pattern-not-inside: |
          func $F(...) $R {
            ...
            $G.assertRefInWorkspace(...)
            ...
          }
      - pattern-not-inside: |
          func $F(...) $R {
            ...
            $G.assertIssueInWorkspace(...)
            ...
          }
"""

FAILURES = []

# Files this harness mutates. Snapshotted at import so C7 can prove each one came back.
MUTATED = ("internal/issue/store.go", "internal/project/store.go")
SNAPSHOT = {}


def scan(config=RULES, targets=("internal/", "cmd/")):
    """Return the list of (rule-id, path:line) the config reports."""
    out = subprocess.run(
        ["semgrep", "scan", "--config", config, "--metrics=off", "--json", "--quiet", *targets],
        cwd=ROOT, capture_output=True, text=True,
    )
    try:
        data = json.loads(out.stdout)
    except json.JSONDecodeError:
        print("    semgrep produced no JSON:", out.stderr[:400])
        return None
    errs = [e for e in data.get("errors", []) if e.get("level") != "warn"]
    if errs:
        print("    semgrep errors:", [str(e.get("message"))[:120] for e in errs[:2]])
        return None
    return sorted({(r["check_id"].split(".")[-1], f'{r["path"]}:{r["start"]["line"]}')
                   for r in data["results"]})


def check(name, got, want_nonempty, detail=""):
    if got is None:
        FAILURES.append(f"{name}: semgrep did not run cleanly")
        print(f"  [BROKEN] {name}")
        return
    ok = bool(got) == want_nonempty
    print(f"  [{'PASS' if ok else 'FAIL'}] {name}: {len(got)} finding(s) "
          f"(expected {'>=1' if want_nonempty else '0'}) {detail}")
    for rid, loc in got:
        print(f"           {rid}  {loc}")
    if not ok:
        FAILURES.append(name)


class Mutation:
    """Apply a text mutation to a repo file, restore it on exit no matter what."""

    def __init__(self, relpath, before, after):
        self.path = os.path.join(ROOT, relpath)
        self.before, self.after = before, after

    def __enter__(self):
        self.original = open(self.path, encoding="utf-8").read()
        if self.before not in self.original:
            raise SystemExit(f"CONTROL IS STALE: anchor not found in {self.path}\n  {self.before[:90]}")
        open(self.path, "w", encoding="utf-8").write(
            self.original.replace(self.before, self.after, 1))
        return self

    def __exit__(self, *exc):
        open(self.path, "w", encoding="utf-8").write(self.original)
        return False


# The guard loop in internal/issue/store.go — present twice, once in Create and once in
# UpsertByIdentifier. Removing it is the regression the lock exists to catch.
GUARD_LOOP = """	for field, p := range map[string]*string{
		"project_id":   issue.ProjectID,
		"cycle_id":     issue.CycleID,
		"assignee_id":  issue.AssigneeID,
		"parent_id":    issue.ParentID,
		"milestone_id": issue.MilestoneID,
	} {
		if p == nil || *p == "" {
			continue
		}
		if err := s.assertRefInWorkspace(ctx, issueRefQueries[field], field, *p, issue.WorkspaceID); err != nil {"""


def main():
    for rel in MUTATED:
        SNAPSHOT[rel] = hashlib.sha256(open(os.path.join(ROOT, rel), "rb").read()).hexdigest()

    print("C0  unmutated tree, shipped rules — the lock must be GREEN")
    check("C0 baseline", scan(), want_nonempty=False)

    print("\nC1  remove the ref guard from internal/issue/store.go (BOTH Create and Upsert)")
    print("    — the statement the old rule could not see. The shipped rule must go RED.")
    src = open(os.path.join(ROOT, "internal/issue/store.go"), encoding="utf-8").read()
    n = src.count(GUARD_LOOP)
    if n != 2:
        raise SystemExit(f"CONTROL IS STALE: expected the guard loop twice in issue/store.go, found {n}")
    with Mutation("internal/issue/store.go", GUARD_LOOP, GUARD_LOOP.replace(
            "if err := s.assertRefInWorkspace(ctx, issueRefQueries[field], field, *p, issue.WorkspaceID); err != nil {",
            "if err := error(nil); err != nil {")):
        got = scan()
        check("C1 issue store unguarded -> RED", got, want_nonempty=True)
        if got:
            hits = {loc for rid, loc in got if "issue/store.go" in loc}
            if not hits:
                FAILURES.append("C1 fired, but not on internal/issue/store.go")
                print("           ^ WRONG SUBJECT: no finding on internal/issue/store.go")

    print("\nC2  remove the guard from a statement the old rule COULD see (project/store.go).")
    print("    This is the control's control: if C2 does not fire, C1 proves nothing.")
    with Mutation("internal/project/store.go",
                  'if err := tenancy.AssertRefInWorkspace(ctx, s.pool, "teams", p.TeamID, p.WorkspaceID); err != nil {',
                  "if err := error(nil); err != nil {"):
        check("C2 project store unguarded -> RED", scan(), want_nonempty=True)

    print("\nC3  the exclusion still excludes: put the guard back by hand in a THIRD store.")
    print("    (label/store.go is guarded on main; a green here means the not-inside arm works.)")
    check("C3 guarded tree stays GREEN", scan(targets=("internal/label/",)), want_nonempty=False)

    print("\nC4  VACUITY: a rule that matches nothing would pass C0/C3 for free. Assert the")
    print("    positive arms actually bind by scanning with every guard exclusion stripped.")
    with tempfile.TemporaryDirectory() as td:
        for name in os.listdir(RULES):
            if not name.endswith(".yml"):
                continue
            text = open(os.path.join(RULES, name), encoding="utf-8").read()
            if "metavariable-regex" not in text:
                continue
            out = [ln for ln in text.split("\n")]
            keep, skip_indent = [], None
            for ln in out:
                stripped = ln.strip()
                if skip_indent is not None:
                    if stripped and (len(ln) - len(ln.lstrip())) > skip_indent:
                        continue
                    skip_indent = None
                if stripped.startswith("- pattern-not-inside:"):
                    skip_indent = len(ln) - len(ln.lstrip())
                    continue
                keep.append(ln)
            open(os.path.join(td, name), "w", encoding="utf-8").write("\n".join(keep))
        vis = scan(config=td)
        check("C4 guards stripped -> the rules DO bind", vis, want_nonempty=True,
              detail="(this is the visible population, not a defect)")

    print("\nC5  THE INERTNESS THIS MERGE REPAIRS, asserted rather than described: the OLD")
    print("    rule shape, on the SAME unguarded issue store, must find NOTHING.")
    with tempfile.TemporaryDirectory() as td:
        open(os.path.join(td, "old.yml"), "w", encoding="utf-8").write(OLD_SHAPE)
        with Mutation("internal/issue/store.go", GUARD_LOOP, GUARD_LOOP.replace(
                "if err := s.assertRefInWorkspace(ctx, issueRefQueries[field], field, *p, issue.WorkspaceID); err != nil {",
                "if err := error(nil); err != nil {")):
            old = scan(config=td)
            check("C5 OLD shape on unguarded issue store -> BLIND (0 expected)", old,
                  want_nonempty=False)
        print("\nC6  and the old shape was not inert everywhere — same config, project/store.go")
        print("    unguarded, must be RED. Otherwise C5's zero is a broken config, not blindness.")
        with Mutation("internal/project/store.go",
                      'if err := tenancy.AssertRefInWorkspace(ctx, s.pool, "teams", p.TeamID, p.WorkspaceID); err != nil {',
                      "if err := error(nil); err != nil {"):
            check("C6 OLD shape on unguarded project store -> RED", scan(config=td),
                  want_nonempty=True)

    print("\nC8  THE RESIDUAL THIS MERGE DOES NOT CLOSE, asserted so it cannot be forgotten.")
    print("    UpsertByIdentifier's SQL is `const upsertSQL = <literals> + attribution + ...`,")
    print("    where `attribution` is a RUNTIME value. Constant propagation cannot fold it, so")
    print("    the '=~/.../' arm does not see it, and the raw-text arm sees only the name")
    print("    `upsertSQL`. Removing the guard from THAT function alone is still not caught.")
    print("    ⚠ IF THIS CONTROL FAILS BECAUSE THE LOCK GOT BETTER: that is good news — delete")
    print("      C8 and correct the note in .semgrep/cross-object-tenancy.yml. Do not 'fix' it")
    print("      by weakening the rule.")
    src = open(os.path.join(ROOT, "internal/issue/store.go"), encoding="utf-8").read()
    first = src.index(GUARD_LOOP)
    second = src.index(GUARD_LOOP, first + 1)
    unguarded = GUARD_LOOP.replace(
        "if err := s.assertRefInWorkspace(ctx, issueRefQueries[field], field, *p, issue.WorkspaceID); err != nil {",
        "if err := error(nil); err != nil {")
    mutated = src[:second] + unguarded + src[second + len(GUARD_LOOP):]
    path = os.path.join(ROOT, "internal/issue/store.go")
    open(path, "w", encoding="utf-8").write(mutated)
    try:
        check("C8 Upsert-only unguarded -> STILL GREEN (known residual)", scan(),
              want_nonempty=False)
    finally:
        open(path, "w", encoding="utf-8").write(src)

    print("\nC7  every mutated file restored byte-for-byte")
    # NOT `git status`: this harness is expected to run on a tree that already carries
    # uncommitted work, and a dirty-git check would fail for reasons that have nothing to do
    # with the mutations. The invariant is that each file this run touched is byte-identical
    # to what it was when the run started.
    for rel, digest in SNAPSHOT.items():
        now = hashlib.sha256(open(os.path.join(ROOT, rel), "rb").read()).hexdigest()
        ok = now == digest
        print(f"  [{'PASS' if ok else 'FAIL'}] {rel} unchanged by this harness")
        if not ok:
            FAILURES.append(f"C7 {rel} not restored")
    check("C7 baseline again", scan(), want_nonempty=False)

    print()
    if FAILURES:
        print("FAILED CONTROLS:", ", ".join(FAILURES))
        sys.exit(1)
    print("ALL CONTROLS PASSED")


if __name__ == "__main__":
    main()
