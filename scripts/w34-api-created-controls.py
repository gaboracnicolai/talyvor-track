#!/usr/bin/env python3
"""w34-api-created-controls.py — positive controls for the `created_at` guards on the two API
transports. Every control MUTATES A REAL FILE, runs a NAMED test that must go RED, runs a NAMED
COMPANION that must STAY GREEN, and restores the file sha256-identical.

THE RULES THIS HARNESS ENFORCES, each one bought by a previous session in this item:
  · ANCHOR COUNT ASSERTED BEFORE THE EDIT (#71). A substitution that matches nothing edits zero
    bytes and is byte-indistinguishable from a guard that works.
  · AN ANCHOR ASSERTION IS NOT ENOUGH (#83's C11). An edit can apply, change real bytes, and mean
    nothing. Where that risk is live it is called out on the control.
  · EVERY CONTROL NEEDS A COMPANION THAT MUST STAY GREEN (#83's C1/C2). A build failure reds
    everything, so "the guard caught it" and "nothing compiled" produce the same output.
  · THE BLINDING MUST BE BEHAVIOURAL (#74's C1). Deleting the consumer of a value is a compile
    error, not a caught mutation.
  · A CONTROL THAT CANNOT DISCRIMINATE IS RECORDED AS SUCH, NEVER COUNTED (#75's C3).

Run from the repo root with TRACK_TEST_DATABASE_URL set.
"""

import hashlib
import os
import pathlib
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
PKG_IMP = "./internal/importer/"
PKG_ISS = "./internal/issue/"


def sha(p):
    return hashlib.sha256(pathlib.Path(p).read_bytes()).hexdigest()


def run_test(pkg, pattern):
    r = subprocess.run(
        ["go", "test", "-timeout", "300s", "-count=1", "-run", pattern, pkg],
        cwd=ROOT, capture_output=True, text=True)
    return r.returncode == 0, (r.stdout + r.stderr)


class Control:
    def __init__(self, name, edits, red, red_pkg, green, green_pkg, note="", discriminating=True):
        self.name, self.edits = name, edits
        self.red, self.red_pkg = red, red_pkg
        self.green, self.green_pkg = green, green_pkg
        self.note, self.discriminating = note, discriminating


CONTROLS = [
    # ── the two halves of the fix, severed one at a time ──────────────────────────────────────
    Control(
        "C1  jiraFields stops asking for `created` (the WIRE half, Jira)",
        [("internal/importer/jira.go",
          '"resolutiondate", jiraAPICreatedField}', '"resolutiondate"}')],
        red="TestJiraRequest_AsksForTheCreationTime", red_pkg=PKG_IMP,
        green="TestJiraRequest_AsksForTheDateFields", green_pkg=PKG_IMP,
        note="⚠ NO JOB TEST CATCHES THIS. The canned servers answer any body with the same page, so "
             "a fixture supplies `created` whether or not the request asked for it. This control is "
             "the entire reason the wire test exists — it was written because the control was "
             "predicted to escape everything else."),
    Control(
        "C2  the Linear document stops selecting createdAt (the WIRE half, Linear)",
        [("internal/importer/linear.go",
          "dueDate completedAt createdAt }", "dueDate completedAt }")],
        red="TestLinearRequest_AsksForTheCreationTime", red_pkg=PKG_IMP,
        green="TestLinearSource_DueDateAndCompletedAtLand", green_pkg=PKG_IMP),
    Control(
        "C3  the SQL half alone — the upsert stops naming created_at",
        [("internal/issue/store.go",
          "due_date, completed_at, lens_feature, labels, sort_order, created_at)\n"
          "    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18,\n"
          "            COALESCE($19::timestamptz, NOW()))\n"
          "    ON CONFLICT",
          "due_date, completed_at, lens_feature, labels, sort_order)\n"
          "    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)\n"
          "    ON CONFLICT"),
         ("internal/issue/store.go",
          "issue.SortOrder, createdAt),", "issue.SortOrder),"),
         # keep the variable consumed or this becomes a build failure rather than a mutation (#74 C1)
         ("internal/issue/store.go",
          "\tconst upsertSQL = `INSERT INTO issues",
          "\t_ = createdAt\n\tconst upsertSQL = `INSERT INTO issues")],
        red="TestUpsertByIdentifier_LandsTheProvidersOpeningTime", red_pkg=PKG_ISS,
        green="TestUpsertByIdentifier_InsertPath", green_pkg=PKG_ISS,
        note="THE PROOF THAT A MAPPER-ONLY FIX WOULD BE INVISIBLE. Both mappers still map perfectly; "
             "only the statement is severed, and the row still lands with a plausible timestamp."),
    Control(
        "C3b the SQL half alone, seen from the PRODUCT — same edit, analytics assertion",
        [("internal/issue/store.go",
          "due_date, completed_at, lens_feature, labels, sort_order, created_at)\n"
          "    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18,\n"
          "            COALESCE($19::timestamptz, NOW()))\n"
          "    ON CONFLICT",
          "due_date, completed_at, lens_feature, labels, sort_order)\n"
          "    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)\n"
          "    ON CONFLICT"),
         ("internal/issue/store.go",
          "issue.SortOrder, createdAt),", "issue.SortOrder),"),
         ("internal/issue/store.go",
          "\tconst upsertSQL = `INSERT INTO issues",
          "\t_ = createdAt\n\tconst upsertSQL = `INSERT INTO issues")],
        red="TestJobRow_JiraAPI_CycleTimeOfAnImportedIssueIsNotNegative", red_pkg=PKG_IMP,
        green="TestJobRow_JiraAPI_DatesLandInPostgres", green_pkg=PKG_IMP,
        note="The companion is #74's date test: completed_at and due_date must keep landing while "
             "created_at does not, or this control is just 'the upsert is broken'."),

    # ── arity-preserving mutations only the database can see (#83's C12) ──────────────────────
    Control(
        "C4  COALESCE arms swapped — compiles, same arity, every source assertion green",
        [("internal/issue/store.go",
          "            COALESCE($19::timestamptz, NOW()))\n    ON CONFLICT",
          "            COALESCE(NOW(), $19::timestamptz))\n    ON CONFLICT")],
        red="TestUpsertByIdentifier_LandsTheProvidersOpeningTime", red_pkg=PKG_ISS,
        green="TestUpsertByIdentifier_ClobbersDescriptive", green_pkg=PKG_ISS),
    Control(
        "C5  the landed instant shifted 30 minutes",
        [("internal/importer/api_created.go",
          "\t\treturn time.Time{}, []FieldNote{{Field: fieldCreated, Value: raw, Via: viaUnparseableDate}}\n"
          "\t}\n\treturn t, nil\n}\n\n// linearAPICreated",
          "\t\treturn time.Time{}, []FieldNote{{Field: fieldCreated, Value: raw, Via: viaUnparseableDate}}\n"
          "\t}\n\treturn t.Add(30 * time.Minute), nil\n}\n\n// linearAPICreated")],
        red="TestJobRow_JiraAPI_ImportedIssueKeepsTheDateJiraOpenedIt", red_pkg=PKG_IMP,
        green="TestJobRow_JiraAPI_CycleTimeOfAnImportedIssueIsNotNegative", green_pkg=PKG_IMP,
        note="30 minutes is INSIDE the analytics test's 24h tolerance and OUTSIDE the column test's "
             "1m one, so the two assertions are demonstrably not one assertion written twice."),

    # ── each transport's mapper, blinded independently ────────────────────────────────────────
    Control(
        "C6  the JIRA mapper alone returns no instant",
        [("internal/importer/jira.go",
          "created, createdNotes := jiraAPICreated(it.Fields.Created)",
          "created, createdNotes := jiraAPICreated(\"\")")],
        red="TestJobRow_JiraAPI_ImportedIssueKeepsTheDateJiraOpenedIt", red_pkg=PKG_IMP,
        green="TestJobRow_LinearAPI_ImportedIssueKeepsTheDateLinearOpenedIt", green_pkg=PKG_IMP,
        note="The companion is the OTHER transport: the two are covered independently, not by one "
             "shared assertion that either would satisfy."),
    Control(
        "C7  the LINEAR mapper alone returns no instant",
        [("internal/importer/linear.go",
          "created, createdNotes := linearAPICreated(n.CreatedAt)",
          "created, createdNotes := linearAPICreated(\"\")")],
        red="TestJobRow_LinearAPI_ImportedIssueKeepsTheDateLinearOpenedIt", red_pkg=PKG_IMP,
        green="TestJobRow_JiraAPI_ImportedIssueKeepsTheDateJiraOpenedIt", green_pkg=PKG_IMP),

    # ── the report channel: silence is the defect these lines exist to prevent ─────────────────
    Control(
        "C8  the absent-`created` warning is dropped (Jira)",
        [("internal/importer/api_created.go",
          'return time.Time{}, []FieldNote{{Field: fieldCreated, Via: viaNoCreatedField}}',
          'return time.Time{}, nil')],
        red="TestJobRow_JiraAPI_MissingCreatedIsReportedNotDefaulted", red_pkg=PKG_IMP,
        green="TestJobRow_JiraAPI_ImportedIssueKeepsTheDateJiraOpenedIt", green_pkg=PKG_IMP,
        note="Without this line an operator cannot distinguish 'Track read your opening times' from "
             "'Track recorded every one of these as opened today' — the rows are identical."),
    Control(
        "C9  the null-createdAt warning is dropped (Linear)",
        [("internal/importer/api_created.go",
          'return time.Time{}, []FieldNote{{Field: fieldCreated, Via: viaNullCreatedAt}}',
          'return time.Time{}, nil')],
        red="TestJobRow_LinearAPI_NullCreatedAtIsReported", red_pkg=PKG_IMP,
        green="TestJobRow_LinearAPI_ImportedIssueKeepsTheDateLinearOpenedIt", green_pkg=PKG_IMP),
    Control(
        "C10 the two Jira failure lines COLLAPSE into one",
        [("internal/importer/api_created.go",
          'return time.Time{}, []FieldNote{{Field: fieldCreated, Via: viaNoCreatedField}}',
          'return time.Time{}, []FieldNote{{Field: fieldCreated, Via: viaUnparseableDate}}')],
        red="TestJobRow_JiraAPI_MissingCreatedIsReportedNotDefaulted", red_pkg=PKG_IMP,
        green="TestJobRow_JiraAPI_ImportedIssueKeepsTheDateJiraOpenedIt", green_pkg=PKG_IMP,
        note="⚠ THE MUTATION THAT AN ANCHOR ASSERTION ALONE WOULD NOT VALIDATE. It edits real bytes "
             "and still produces A warning, so a test asserting merely 'the job said something' "
             "stays green. Only the two-distinct-lines assertion sees it."),

    # ── the fixture-realism claim, tested rather than asserted ────────────────────────────────
    Control(
        "C11 the shared Linear fixture stops carrying an opening time",
        [("internal/importer/linear_date_fields_test.go",
          '\tfields += fmt.Sprintf(`,"createdAt":%q`, fixtureLinearCreated)\n', '')],
        red="TestLinearSource_DueDateAndCompletedAtLand", red_pkg=PKG_IMP,
        green="TestJobRow_JiraAPI_ImportedIssueKeepsTheDateJiraOpenedIt", green_pkg=PKG_IMP,
        note="Proves the widened fixtures are load-bearing rather than decorative: remove the field "
             "and the guard fires, which is what happened when the mappers first landed."),
]


def apply(control):
    """Apply every edit, asserting each anchor count FIRST. Returns the originals for restore."""
    originals = {}
    for rel, old, new in control.edits:
        p = ROOT / rel
        if rel not in originals:
            originals[rel] = p.read_text()
        s = p.read_text()
        n = s.count(old)
        if n != 1:
            for r, o in originals.items():
                (ROOT / r).write_text(o)
            raise AssertionError(f"anchor count {n} (want 1) in {rel} for {old[:60]!r}")
        p.write_text(s.replace(old, new, 1))
    return originals


def main():
    if not os.environ.get("TRACK_TEST_DATABASE_URL"):
        print("TRACK_TEST_DATABASE_URL is unset — the database-level controls would SKIP and read as passes.")
        return 1

    print("── baseline: every named test must be GREEN before any mutation")
    for c in CONTROLS:
        ok, out = run_test(c.red_pkg, f"^{c.red}$")
        if not ok:
            print(f"  BASELINE FAIL {c.red}\n{out[-1500:]}")
            return 1
    print(f"  {len(CONTROLS)} named tests green\n")

    caught, escaped, nondiscriminating = [], [], []
    for c in CONTROLS:
        before = {rel: sha(ROOT / rel) for rel, _, _ in c.edits}
        try:
            originals = apply(c)
        except AssertionError as e:
            print(f"  {c.name}\n      HARNESS FAULT: {e}")
            escaped.append(c.name)
            continue
        try:
            red_ok, red_out = run_test(c.red_pkg, f"^{c.red}$")
            green_ok, green_out = run_test(c.green_pkg, f"^{c.green}$")
        finally:
            for rel, text in originals.items():
                (ROOT / rel).write_text(text)

        for rel, h in before.items():
            assert sha(ROOT / rel) == h, f"{rel} NOT restored byte-identical"

        # A build failure reds the companion too — that is not a caught mutation.
        if not red_ok and not green_ok:
            verdict = "BUILD FAILURE (companion also red — not a mutation)"
            escaped.append(c.name)
        elif not red_ok and green_ok:
            verdict = f"CAUGHT by {c.red} (companion {c.green} green)"
            (caught if c.discriminating else nondiscriminating).append(c.name)
        else:
            verdict = f"NOT CAUGHT — {c.red} stayed GREEN"
            escaped.append(c.name)

        print(f"  {c.name}\n      {verdict}")
        if c.note:
            print(f"      {c.note}")

    print(f"\n── {len(caught)} caught · {len(nondiscriminating)} non-discriminating · {len(escaped)} escaped")
    if escaped:
        print("   ESCAPED: " + "; ".join(escaped))
    return 1 if escaped else 0


if __name__ == "__main__":
    sys.exit(main())
