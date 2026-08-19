#!/usr/bin/env python3
"""Positive controls for TestTemplateDefaultAssignee_IsWorkspaceScoped.

tab-8f3b. The guard PASSED ON ITS FIRST RUN after the fix, so every assertion set in it
has to be justified by a mutation that only it catches — otherwise the file is decoration.

Discipline, per control:
  * the PREDICTED catcher is named BEFORE the run,
  * exactly one thing is mutated,
  * the FULL `go test ./...` decides membership by SET SUBTRACTION against a pristine
    baseline captured in this same run (never against a remembered number),
  * the tree is restored in a `finally` and the restore is verified by sha256.

C6 is a MUST-STAY-GREEN companion: a real tenancy defect that this guard must NOT claim
to catch. Without it "CAUGHT" could just mean the suite is fragile.
"""

import hashlib
import io
import re
import subprocess
import sys

STORE = "internal/template/store.go"
GUARD = "internal/template/default_assignee_tenancy_realpg_test.go"

CREATE_CALL = """	if err := s.assertAssigneeInWorkspace(ctx, t.DefaultAssignee, t.WorkspaceID); err != nil {
		return nil, err
	}
"""

UPDATE_CALL = """	if assignee, present := updateAssignee(updates); present {
		if err := s.assertAssigneeInWorkspace(ctx, &assignee, workspaceID); err != nil {
			return nil, err
		}
	}
"""

HELPER_BODY = """	if assignee == nil || *assignee == "" {
		return nil
	}
	return tenancy.AssertRefInWorkspace(ctx, s.pool, "members", *assignee, workspaceID)
"""

UPDATE_ASSIGNEE_BODY = """	raw, ok := updates["default_assignee"]
	if !ok || raw == nil {
		return "", false
	}
	switch v := raw.(type) {
	case string:
		return v, v != ""
	case *string:
		if v == nil || *v == "" {
			return "", false
		}
		return *v, true
	}
	return "", false
"""

TEAM_GUARD = """	if t.TeamID != nil && *t.TeamID != "" {
		if err := tenancy.AssertRefInWorkspace(ctx, s.pool, "teams", *t.TeamID, t.WorkspaceID); err != nil {
			return nil, err
		}
	}
"""

CONTROLS = [
    ("C1", "Create's guard call deleted",
     "CAUGHT by `Create refuses a member of another workspace` ONLY; every Update case stays green",
     [(STORE, CREATE_CALL, "\t_ = t.DefaultAssignee // C1\n")]),

    ("C2", "Update's guard call deleted",
     "CAUGHT by `Update refuses a member of another workspace and writes nothing` ONLY; Create stays green",
     [(STORE, UPDATE_CALL, "\t_ = updateAssignee // C2\n")]),

    ("C3", "the nil/empty skip removed — the guard refuses EVERY assignee, legitimate ones included",
     "CAUGHT by the ANTI-VACUITY halves (`accepts no assignee at all`, `can still clear the assignee`); "
     "the two refusal cases stay GREEN, which is exactly why the anti-vacuity halves exist",
     [(STORE, HELPER_BODY,
       "\treturn tenancy.AssertRefInWorkspace(ctx, s.pool, \"members\", \"\", workspaceID)\n")]),

    ("C4", "the guard checks the reference against ITS OWN workspace instead of the row's",
     "CAUGHT by BOTH refusal cases — the classic always-true tenancy check",
     [(STORE, HELPER_BODY,
       "\tif assignee == nil || *assignee == \"\" {\n\t\treturn nil\n\t}\n"
       "\tvar own string\n"
       "\tif err := s.pool.QueryRow(ctx, `SELECT workspace_id FROM members WHERE id = $1`, *assignee).Scan(&own); err != nil {\n"
       "\t\treturn err\n\t}\n"
       "\treturn tenancy.AssertRefInWorkspace(ctx, s.pool, \"members\", *assignee, own)\n")]),

    # ⚠ C5 WAS MIS-CUT ON ITS FIRST RUN AND SCORED VOID(build): replacing only the first four
    # lines left `raw` referenced by the type switch below and `undefined: raw` is a compile
    # error, not a caught mutation. Re-cut to replace the WHOLE body.
    ("C5", "updateAssignee always reports 'no reference present'",
     "CAUGHT by the Update refusal case ONLY — proves the two write paths are pinned independently",
     [(STORE, UPDATE_ASSIGNEE_BODY, "\t_ = updates\n\treturn \"\", false // C5\n")]),

    ("C6", "MUST STAY GREEN — template.Create's team_id guard deleted (a REAL tenancy defect, "
           "but a different reference)",
     "NOT CAUGHT by this new guard (it pins default_assignee), CAUGHT by the pre-existing "
     "cross-object tenancy closure test",
     [(STORE, TEAM_GUARD, "\tif t.TeamID != nil && *t.TeamID != \"\" {\n\t\t_ = t.TeamID // C6\n\t}\n")]),

    ("C7", "the refusal returns a bare error instead of the wrapped ErrCrossWorkspace sentinel",
     "CAUGHT by BOTH refusal cases — this is what justifies asserting errors.Is rather than 'an error came back'",
     [(STORE, HELPER_BODY,
       "\tif assignee == nil || *assignee == \"\" {\n\t\treturn nil\n\t}\n"
       "\tif err := tenancy.AssertRefInWorkspace(ctx, s.pool, \"members\", *assignee, workspaceID); err != nil {\n"
       "\t\treturn errors.New(\"template: bad assignee\")\n\t}\n\treturn nil\n")]),
]

FAIL_RE = re.compile(r"^\s*--- FAIL: (\S+)", re.M)


def sha(p):
    return hashlib.sha256(io.open(p, "rb").read()).hexdigest()


def read(p):
    return io.open(p, encoding="utf-8").read()


def write(p, s):
    io.open(p, "w", encoding="utf-8").write(s)


def run_suite():
    r = subprocess.run(["go", "test", "-timeout", "300s", "-count=1", "./..."],
                       capture_output=True, text=True)
    out = r.stdout + r.stderr
    if "build failed" in out or "[build failed]" in out or "cannot use" in out:
        return r.returncode, set(), True, out
    return r.returncode, set(FAIL_RE.findall(out)), False, out


def main():
    files = sorted({f for _, _, _, muts in CONTROLS for f, _, _ in muts} | {GUARD})
    base_sha = {p: sha(p) for p in files}
    orig = {p: read(p) for p in files}

    print("=== U0 pristine baseline (the set every control is subtracted from) ===")
    code, base_fails, broken, out = run_suite()
    if broken or base_fails:
        print(f"BASELINE NOT CLEAN (exit={code}, failures={sorted(base_fails)}) — controls are void")
        print(out[-3000:])
        sys.exit(2)
    print(f"U0 -> exit={code}, 0 failures\n")

    summary = []
    try:
        only = set(sys.argv[1:])
        for cid, what, prediction, muts in CONTROLS:
            if only and cid not in only:
                continue
            print(f"--- {cid}: {what}")
            print(f"    PREDICTION: {prediction}")
            for p, old, new in muts:
                s = read(p)
                if s.count(old) != 1:
                    print(f"    VOID — anchor found {s.count(old)}x in {p}")
                    summary.append((cid, "VOID", set()))
                    break
                write(p, s.replace(old, new, 1))
            else:
                code, fails, broken, out = run_suite()
                for p in files:
                    write(p, orig[p])
                if broken:
                    print("    VOID — the mutation did not compile; a build error is not a caught mutation")
                    print("   ", [l for l in out.splitlines() if "cannot use" in l or "undefined" in l][:3])
                    summary.append((cid, "VOID(build)", set()))
                    print()
                    continue
                new_fails = fails - base_fails
                verdict = "CAUGHT" if new_fails else "NOT CAUGHT"
                print(f"    RESULT: {verdict} (exit={code}) new failures: {sorted(new_fails) or '(none)'}")
                summary.append((cid, verdict, new_fails))
            print()
    finally:
        for p in files:
            write(p, orig[p])
        ok = all(sha(p) == base_sha[p] for p in files)
        print(f"restore verified by sha256: {'OK' if ok else 'CORRUPT — DO NOT TRUST THE ABOVE'}")
        if not ok:
            sys.exit(2)

    print("\n=== SUMMARY ===")
    for cid, verdict, nf in summary:
        print(f"  {cid}: {verdict}")
        for f in sorted(nf):
            print(f"        {f}")


if __name__ == "__main__":
    main()
