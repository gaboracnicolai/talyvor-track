#!/usr/bin/env python3
"""Positive controls for the `tenancy-lock` blindness over issue_templates.default_assignee.

tab-8f3b. Every control names its PREDICTION before it runs, mutates one thing, runs the
EXACT CI command (`semgrep scan --config .semgrep/ --error --metrics=off internal/ cmd/`),
and restores in a finally. sha256 of every touched file is verified after restore, so a
control that corrupted the tree cannot be read as a clean result.

The question each control answers:
  S0  pristine                          -> how many findings does the lock report today?
  S1  `default_assignee` added to the rule's column alternation
                                        -> does NAMING the column make the lock see it?
  S2  S1 + template.Create's team_id guard removed
                                        -> with the column named AND the function's other
                                           guard gone, does it fire? (isolates the
                                           per-FUNCTION exclusion from the name list)
  S3  pristine rule + template.Create's team_id guard removed
                                        -> does the lock see this STATEMENT at all?
                                           (a fire here proves the INSERT is in scope and
                                           was matched-then-EXCLUDED, not invisible)
"""

import hashlib
import io
import subprocess
import sys

RULE = ".semgrep/cross-object-tenancy.yml"
TPL = "internal/template/store.go"

# The alternation appears four times in the rule (two metavariable-regex spellings x two
# column orders, plus the two string-literal forms). Replace every occurrence.
ALT_OLD = "team_id|project_id|board_id|member_id|issue_id|cycle_id|parent_id|assignee_id|milestone_id|source_id|target_id"
ALT_NEW = ALT_OLD + "|default_assignee"

GUARD_OLD = """	if t.TeamID != nil && *t.TeamID != "" {
		if err := tenancy.AssertRefInWorkspace(ctx, s.pool, "teams", *t.TeamID, t.WorkspaceID); err != nil {
			return nil, err
		}
	}
"""
GUARD_NEW = """	if t.TeamID != nil && *t.TeamID != "" {
		_ = t.TeamID // CONTROL: guard removed
	}
"""

CI_CMD = ["semgrep", "scan", "--config", ".semgrep/", "--error", "--metrics=off", "internal/", "cmd/"]


def sha(p):
    return hashlib.sha256(io.open(p, "rb").read()).hexdigest()


def read(p):
    return io.open(p, encoding="utf-8").read()


def write(p, s):
    io.open(p, "w", encoding="utf-8").write(s)


def run_lock():
    """Returns (exit_code, finding_count, matched_lines)."""
    r = subprocess.run(CI_CMD, capture_output=True, text=True)
    out = r.stdout + r.stderr
    n = 0
    for line in out.splitlines():
        s = line.strip()
        if s.startswith("• Findings:"):
            n = int(s.split("Findings:")[1].split("(")[0].strip())
    hits = [l.strip() for l in out.splitlines() if "cross-object-insert-requires-tenancy-guard" in l]
    return r.returncode, n, hits


def main():
    base = {p: sha(p) for p in (RULE, TPL)}
    orig = {p: read(p) for p in (RULE, TPL)}

    results = {}
    try:
        print("S0 pristine — PREDICTION: 0 findings (the lock is green on main)")
        code, n, hits = run_lock()
        results["S0"] = n
        print(f"   S0 -> exit={code} findings={n}\n")

        print("S1 `default_assignee` added to the alternation — PREDICTION: STILL 0.")
        print("   Reason: template.Create already calls tenancy.AssertRefInWorkspace for team_id,")
        print("   and `pattern-not-inside` excludes the whole FUNCTION, not the reference.")
        r = orig[RULE].replace(ALT_OLD, ALT_NEW)
        assert r != orig[RULE], "alternation not found — control is void"
        write(RULE, r)
        code, n, hits = run_lock()
        results["S1"] = n
        print(f"   S1 -> exit={code} findings={n}\n")

        print("S2 S1 + template.Create's team_id guard removed — PREDICTION: FIRES (>=1).")
        t = orig[TPL].replace(GUARD_OLD, GUARD_NEW)
        assert t != orig[TPL], "guard block not found — control is void"
        write(TPL, t)
        code, n, hits = run_lock()
        results["S2"] = n
        print(f"   S2 -> exit={code} findings={n}")
        for h in hits[:3]:
            print(f"        {h}")
        print()

        print("S3 pristine rule + template.Create's team_id guard removed — PREDICTION: FIRES.")
        print("   This is the control that proves the INSERT is IN SCOPE and merely EXCLUDED.")
        write(RULE, orig[RULE])
        code, n, hits = run_lock()
        results["S3"] = n
        print(f"   S3 -> exit={code} findings={n}")
        for h in hits[:3]:
            print(f"        {h}")
        print()
    finally:
        for p, s in orig.items():
            write(p, s)
        ok = all(sha(p) == base[p] for p in base)
        print(f"restore verified by sha256: {'OK' if ok else 'CORRUPT — DO NOT TRUST THE ABOVE'}")
        if not ok:
            sys.exit(2)

    print("\n=== SUMMARY ===")
    for k in ("S0", "S1", "S2", "S3"):
        print(f"  {k}: {results.get(k)} finding(s)")


if __name__ == "__main__":
    main()
