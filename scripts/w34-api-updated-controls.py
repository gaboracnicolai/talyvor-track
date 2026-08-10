#!/usr/bin/env python3
"""Positive controls for `updated_at` on the TWO API TRANSPORTS (W3.4, PR after #85).

WHAT THIS PROVES AND WHAT IT DOES NOT.  Every guard in this merge was RED before the fix (8 of
them, off by exactly 4800h).  That is necessary and not sufficient: it shows the guards can fail
on the ORIGINAL defect, not that they still fail on each INDIVIDUAL half of the fix.  Each control
below removes exactly ONE half and names the test that must speak.

⚠ EVERY CONTROL CARRIES A MUST-STAY-GREEN COMPANION.  Without one, "the target went red" is
equally consistent with a mutation that broke the build or reddened everything.  BOTH RED IS
`SUSPECT`, NEVER `CAUGHT` — a compile error reading as a caught mutation is how a control campaign
lies to you.

⚠ THE BASELINE GATE IS LOAD-BEARING.  Without TRACK_TEST_DATABASE_URL every job control would SKIP,
`go test` would exit 0, and this script would report a clean sweep of controls that never ran.
"""
import hashlib
import os
import pathlib
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent

# (id, file, anchor, replacement, must_red, must_stay_green, package, note, scope)
#
# ⚠ `scope` EXISTS BECAUSE TWO CONTROLS SILENTLY MATCHED TWICE ON THE FIRST RUN. Create's INSERT
# and UpsertByIdentifier's are BYTE-IDENTICAL in the column list and in the import-owned gate, so a
# bare anchor is ambiguous and the harness refused to run rather than guess — which is the behaviour
# you want, but it means the control never happened. `scope` restricts the search to the substring
# AFTER a marker (the function header), and the exactly-once assertion is then made THERE.
CONTROLS = [
    ("C1", "internal/importer/jira.go",
     'jiraAPICreatedField, jiraAPIUpdatedField}', 'jiraAPICreatedField}',
     ["TestJiraRequest_AsksForTheUpdatedField"],
     ["TestLinearQuery_AsksForUpdatedAt"],
     "./internal/importer/",
     "the shipped Jira request stops asking for the field (HTTP 200, key silently absent)", None),

    ("C2", "internal/importer/linear.go",
     "dueDate completedAt createdAt updatedAt }", "dueDate completedAt createdAt }",
     ["TestLinearQuery_AsksForUpdatedAt"],
     ["TestJiraRequest_AsksForTheUpdatedField",
      "TestJobRow_JiraAPI_ImportedIssueKeepsTheDateJiraLastUpdatedIt"],
     "./internal/importer/",
     "the shipped Linear query stops selecting updatedAt", None),

    # ⚠ C1b / C2b ARE DOCUMENTED-INERT CONTROLS AND THEY ARE THE MOST IMPORTANT ENTRIES HERE.
    # MEASURED, NOT PREDICTED: on the first run I listed the JOB test as a must-red for C2 and it
    # STAYED GREEN. That is not a weak guard, it is a REAL BLIND SPOT WITH A CAUSE — the whole job
    # suite drives a canned httptest server (cannedPages) that returns the fixture bytes REGARDLESS
    # of what the query or the `fields` list asked for. So no end-to-end test in this package can
    # ever see a request that stops asking for a field. Neither transport is exempt: C1b measures
    # the same thing for Jira's `fields` list, which the real provider answers with HTTP 200 and a
    # silently absent key.
    #
    # ⚠ THIS IS WHY TestJiraRequest_AsksForTheUpdatedField AND TestLinearQuery_AsksForUpdatedAt
    # EXIST AT ALL, and it is the whole of what stands between this merge and a silent regression:
    # they read the SHIPPED REQUEST rather than the response. A GREEN C1b/C2b MUST NEVER BE READ AS
    # A CAUGHT MUTATION — it is the recorded limit of the fixture, kept so nobody "strengthens" the
    # job tests to cover a case they are structurally incapable of covering.
    ("C1b", "internal/importer/jira.go",
     'jiraAPICreatedField, jiraAPIUpdatedField}', 'jiraAPICreatedField}',
     [],
     ["TestJobRow_JiraAPI_ImportedIssueKeepsTheDateJiraLastUpdatedIt"],
     "./internal/importer/",
     "DOCUMENTED-INERT: the canned server ignores `fields`, so the job test cannot see this",
     None),

    ("C2b", "internal/importer/linear.go",
     "dueDate completedAt createdAt updatedAt }", "dueDate completedAt createdAt }",
     [],
     ["TestJobRow_LinearAPI_ImportedIssueKeepsTheDateLinearLastUpdatedIt"],
     "./internal/importer/",
     "DOCUMENTED-INERT: the canned server ignores the query, so the job test cannot see this",
     None),

    # ⚠ THE FIRST DRAFT OF C3 DELETED `updated_at` FROM THE COLUMN LIST AND SCORED `SUSPECT`.
    # That mutation leaves COALESCE($20::timestamptz, NOW()) in VALUES, so the statement binds 20
    # parameters for 19 placeholders, Postgres rejects it, and EVERY test in the file goes red
    # including the created_at companion. The harness refused to call that CAUGHT, which is exactly
    # what the must-stay-green companion is for. This mutation reproduces the #85 STATE instead —
    # the provider's value never reaches the statement, so COALESCE falls through to NOW().
    ("C3", "internal/issue/store.go",
     "\tvar updatedAt *time.Time\n\tif !issue.UpdatedAt.IsZero()",
     "\tvar updatedAt *time.Time\n\tif false && !issue.UpdatedAt.IsZero()",
     ["TestUpsertByIdentifier_LandsTheProvidersLastTouchedTime"],
     ["TestUpsertByIdentifier_LandsTheProvidersOpeningTime"],
     "./internal/issue/",
     "THE SQL HALF: the provider's instant never reaches the statement (the #85 state)",
     "func (s *Store) UpsertByIdentifier"),

    ("C4", "internal/importer/api_updated.go",
     "\tt, ok := parseJiraTime(raw)", "\tt, ok := parseJiraTime(raw)\n\tt = time.Time{}",
     ["TestJobRow_JiraAPI_ImportedIssueKeepsTheDateJiraLastUpdatedIt",
      "TestJobRow_JiraAPI_AStaleImportDoesNotOutrankTodaysWork"],
     ["TestJobRow_LinearAPI_ImportedIssueKeepsTheDateLinearLastUpdatedIt"],
     "./internal/importer/",
     "THE JIRA MAPPER HALF: the value is parsed and then dropped", None),

    ("C5", "internal/importer/api_updated.go",
     "\tt, ok := parseLinearTime(raw)", "\tt, ok := parseLinearTime(raw)\n\tt = time.Time{}",
     ["TestJobRow_LinearAPI_ImportedIssueKeepsTheDateLinearLastUpdatedIt",
      "TestJobRow_LinearAPI_AStaleImportDoesNotOutrankTodaysWork"],
     ["TestJobRow_JiraAPI_ImportedIssueKeepsTheDateJiraLastUpdatedIt"],
     "./internal/importer/",
     "THE LINEAR MAPPER HALF: the value is parsed and then dropped", None),

    ("C6", "internal/issue/store.go",
     "if !issue.UpdatedAt.IsZero() && issue.CreatorID == model.ImporterCreatorID {",
     "if !issue.UpdatedAt.IsZero() {",
     ["TestUpsertByIdentifier_IgnoresAnUpdatedAtFromANonImporter"],
     ["TestUpsertByIdentifier_IgnoresACreatedAtFromANonImporter"],
     "./internal/issue/",
     "the import-owned GATE is blinded — any caller may choose its own recency",
     "func (s *Store) UpsertByIdentifier"),

    ("C7", "internal/importer/api_updated.go",
     "\t\treturn time.Time{}, []FieldNote{{Field: fieldUpdated, Via: viaNoUpdatedField}}",
     "\t\treturn time.Time{}, nil",
     ["TestJobRow_JiraAPI_MissingUpdatedIsReportedNotDefaulted"],
     ["TestJobRow_JiraAPI_ImportedIssueKeepsTheDateJiraLastUpdatedIt"],
     "./internal/importer/",
     "an ABSENT Jira `updated` stops being reported — silently defaulted", None),

    ("C8", "internal/importer/api_updated.go",
     "\t\treturn time.Time{}, []FieldNote{{Field: fieldUpdated, Via: viaNullUpdatedAt}}",
     "\t\treturn time.Time{}, nil",
     ["TestJobRow_LinearAPI_NullUpdatedAtIsReportedNotDefaulted"],
     ["TestJobRow_LinearAPI_ImportedIssueKeepsTheDateLinearLastUpdatedIt"],
     "./internal/importer/",
     "a NULL Linear updatedAt stops being reported — the transport-changed signal", None),

    # ⚠ MUST-STAY-GREEN. Without this, "every mutation went red" is equally consistent with a
    # harness that reds on ANY edit to these files. #85's C10, kept.
    ("C9", "internal/issue/store.go",
     "// updated_at: THE PROVIDER'S LAST-TOUCHED INSTANT",
     "// updated_at: the provider's last-touched instant",
     [],
     ["TestUpsertByIdentifier_LandsTheProvidersLastTouchedTime",
      "TestUpsertByIdentifier_IgnoresAnUpdatedAtFromANonImporter"],
     "./internal/issue/",
     "MUST-STAY-GREEN: a comment reworded in the file every other control edits", None),

    # ⚠ OUTCOME NOT PREDICTED — this one is MEASURED, and its verdict is recorded either way.
    # The conflict arm is the rule #85 left undecided and this merge deliberately did not touch.
    ("C10", "internal/issue/store.go",
     "        updated_at  = NOW()", "        updated_at  = EXCLUDED.updated_at",
     [],
     [],
     "./internal/issue/",
     "THE UNDECIDED CONFLICT ARM: clobber with the provider's instant instead of NOW()",
     "func (s *Store) UpsertByIdentifier"),
]


def sha(path):
    return hashlib.sha256((ROOT / path).read_bytes()).hexdigest()


def run(targets, pkg):
    """Return True if the named tests PASS. An empty target list means the whole package."""
    cmd = ["go", "test", "-timeout", "300s", "-count=1"]
    if targets:
        cmd += ["-run", "^(" + "|".join(targets) + ")$"]
    cmd.append(pkg)
    p = subprocess.run(cmd, cwd=ROOT, capture_output=True, text=True)
    out = p.stdout + p.stderr
    # ⚠ A BUILD FAILURE IS NOT A CAUGHT MUTATION and must never be scored as one.
    if "build failed" in out or "cannot use" in out or "undefined:" in out:
        return None, out
    # ⚠ NO TESTS MATCHED IS NOT A PASS. `go test -run` exits 0 when the pattern matches nothing,
    # so a renamed test would score every control CAUGHT-then-restored and mean nothing.
    if targets and "no tests to run" in out:
        return None, out
    return p.returncode == 0, out


def main():
    if not os.environ.get("TRACK_TEST_DATABASE_URL"):
        print("REFUSING TO RUN: TRACK_TEST_DATABASE_URL is unset. Every real-Postgres control "
              "would SKIP, go test would exit 0, and this script would report a clean sweep of "
              "controls that never ran.", file=sys.stderr)
        return 3

    files = sorted({c[1] for c in CONTROLS})
    before = {f: sha(f) for f in files}

    print("BASELINE — the suite must be green before any mutation means anything")
    ok, out = run([], "./internal/importer/ ./internal/issue/".split()[0])
    ok2, _ = run([], "./internal/issue/")
    if not ok or not ok2:
        print("  BASELINE RED — stopping. A control campaign on a red tree proves nothing.")
        print(out[-3000:])
        return 2
    print("  baseline green\n")

    verdicts = {}
    for cid, path, anchor, repl, must_red, must_green, pkg, note, scope in CONTROLS:
        p = ROOT / path
        src = p.read_text()
        head, body = "", src
        if scope:
            i = src.find(scope)
            if i < 0:
                verdicts[cid] = f"SCOPE MARKER {scope!r} NOT FOUND — NOT RUN"
                print(f"{cid}  scope marker not found in {path} — not run")
                continue
            head, body = src[:i], src[i:]
        n = body.count(anchor)
        if n != 1:
            verdicts[cid] = f"ANCHOR {n} != 1 — NOT RUN"
            print(f"{cid}  ANCHOR COUNT {n} != 1 in {path} — not run")
            continue
        p.write_text(head + body.replace(anchor, repl, 1))
        try:
            red_ok = True
            red_detail = []
            for t in must_red:
                passed, out = run([t], pkg)
                if passed is None:
                    red_detail.append(f"{t}=BUILD/NOMATCH")
                    red_ok = False
                elif passed:
                    red_detail.append(f"{t}=STILL GREEN")
                    red_ok = False
                else:
                    red_detail.append(f"{t}=red")

            green_ok = True
            green_detail = []
            for t in must_green:
                passed, out = run([t], pkg)
                if passed is None:
                    green_detail.append(f"{t}=BUILD/NOMATCH")
                    green_ok = False
                elif passed:
                    green_detail.append(f"{t}=green")
                else:
                    green_detail.append(f"{t}=WENT RED")
                    green_ok = False
        finally:
            p.write_text(src)

        after = sha(path)
        restored = after == before[path]

        if not must_red and not must_green:
            v = "MEASURED-ONLY"
        elif not must_red:
            v = "STAYED GREEN (as specified)" if green_ok else "COMPANION WENT RED"
        elif red_ok and green_ok:
            v = "CAUGHT"
        elif red_ok and not green_ok:
            v = "SUSPECT — companion also red; a broken build reads like a caught mutation"
        else:
            v = "NOT CAUGHT"
        if not restored:
            v += "  ⚠ TREE NOT RESTORED"
        verdicts[cid] = v
        print(f"{cid}  {v}\n     {note}")
        if red_detail:
            print(f"     must-red   : {'; '.join(red_detail)}")
        if green_detail:
            print(f"     must-green : {'; '.join(green_detail)}")
        print(f"     restored   : {restored}")

    print("\n--- C10 is measured separately below (no predicted outcome) ---")
    print("\nSUMMARY")
    for cid, v in verdicts.items():
        print(f"  {cid}: {v}")

    bad = [c for c, v in verdicts.items()
           if "NOT RESTORED" in v or v.startswith("NOT CAUGHT") or v.startswith("SUSPECT")
           or v.startswith("ANCHOR") or v == "COMPANION WENT RED"]
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
