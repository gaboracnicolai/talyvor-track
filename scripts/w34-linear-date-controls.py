#!/usr/bin/env python3
"""w34-linear-date-controls.py — positive controls for the Linear dueDate/completedAt merge.

Every control does the same four things, in this order, because each one has been paid for by a
session that skipped it:

  1. ASSERT THE ANCHOR COUNT BEFORE EDITING (#71's lesson, paid for twice in this repo). A
     substitution that matches nothing edits zero bytes and is byte-indistinguishable from a guard
     that works — the "control" then passes and gets written down as evidence.
  2. Apply the mutation and RUN the named test. It must go RED.
  3. Run a test that must STAY GREEN (#73's rule), so no control can pass by breaking the build.
     #74's C1 measured why this matters: deleting a struct field is a COMPILE error, which reds
     everything including the must-stay-green test, and a control that cannot tell "the guard caught
     it" from "nothing compiled" is not a control. Every mutation here is behavioural.
  4. Restore and verify the file is sha256-IDENTICAL to how it started.

Run with a real Postgres, the same one CI uses:
  docker run -d --name w34-linear-pg -e POSTGRES_USER=postgres -e POSTGRES_PASSWORD=postgres \
      -e POSTGRES_DB=track -p 55434:5432 pgvector/pgvector:pg16
  TRACK_TEST_DATABASE_URL='postgres://postgres:postgres@localhost:55434/track?sslmode=disable' \
      python3 scripts/w34-linear-state-type-controls.py
"""

import hashlib
import os
import pathlib
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
IMP = ROOT / "internal" / "importer"
LINEAR = IMP / "linear.go"
CSV = IMP / "csv.go"
RUNNER = IMP / "runner.go"
WIRE = IMP / "linear_date_fields_test.go"
STORE = ROOT / "internal" / "issue" / "store.go"


def sha(p):
    return hashlib.sha256(p.read_bytes()).hexdigest()


def run(pattern):
    """Run one -run pattern; return True if the package passed."""
    env = dict(os.environ)
    r = subprocess.run(
        ["go", "test", "-count=1", "-run", pattern, "./internal/importer/"],
        cwd=ROOT, env=env, capture_output=True, text=True,
    )
    return r.returncode == 0, (r.stdout + r.stderr)


class Control:
    def __init__(self, name, path, old, new, red, green, expect_red=True, note="", extra=None):
        self.name, self.path, self.old, self.new = name, path, old, new
        self.red, self.green, self.expect_red, self.note = red, green, expect_red, note
        # extra = (path, old, new) — a SECOND file edited in the same control. Only C12 needs it:
        # demonstrating that a self-referential assertion is vacuous requires changing the constant
        # it references, which lives in another file. Without this the control would be named for
        # something it did not do.
        self.extra = extra

    def apply(self):
        src = self.path.read_text()
        count = src.count(self.old)
        # (1) THE ANCHOR ASSERTION. Without it a no-op edit reads as a working guard.
        assert count == 1, f"{self.name}: anchor appears {count}x in {self.path.name}, want exactly 1"
        mutated = src.replace(self.old, self.new, 1)
        # ⚠ AND THE ANCHOR ASSERTION IS NOT ENOUGH ON ITS OWN — C6 measured this the hard way. It
        # proves the substitution APPLIED; it says nothing about whether the replacement MEANT
        # anything. C6's first form matched its anchor, edited real bytes, and left `triage`
        # returning exactly what it returned before, so the control reported "NOT CAUGHT" for a
        # guard that was working perfectly. This only catches the degenerate case; the real defence
        # is that a control naming a specific test must be READ, not just run.
        assert mutated != src, f"{self.name}: the mutation changed no bytes"
        self.path.write_text(mutated)
        if self.extra:
            xp, xo, xn = self.extra
            xsrc = xp.read_text()
            xc = xsrc.count(xo)
            assert xc == 1, f"{self.name}: extra anchor appears {xc}x in {xp.name}, want exactly 1"
            xp.write_text(xsrc.replace(xo, xn, 1))

    def restore_extra(self, original):
        if self.extra:
            self.extra[0].write_text(original)
            assert sha(self.extra[0]) == hashlib.sha256(original.encode()).hexdigest(), \
                f"{self.name}: {self.extra[0].name} NOT restored"

    def restore(self, original):
        # ⚠ RESTORE FROM THE ORIGINAL BYTES, NEVER BY REVERSE SUBSTITUTION. The first version of this
        # harness did `text.replace(new, old, 1)`, which for a DELETION control (new == "") is
        # `str.replace("", old, 1)` — Python inserts at position 0. It silently PREPENDED the deleted
        # case to the top of csv.go, above the package comment. The sha assertion is what caught it;
        # without that this campaign would have gone on mutating an already-corrupt file and every
        # subsequent verdict would have been noise.
        self.path.write_text(original)
        after = sha(self.path)
        assert after == hashlib.sha256(original.encode()).hexdigest(), \
            f"{self.name}: {self.path.name} NOT restored (now {after})"


CONTROLS = [
    # ── the query document: the only thing that can 400 a real import ────────────────────────────
    Control(
        "D1  drop both date fields from the query document",
        LINEAR,
        "labels { nodes { name } } dueDate completedAt }",
        "labels { nodes { name } } }",
        red="TestLinearRequest_AsksForTheDateFields",
        green="TestJobRow_LinearAPI_DateFieldsLandInPostgres",
        note="⚠ THE MUST-STAY-GREEN IS THE FINDING AGAIN. Every fake answers any query with the same "
             "body, so narrowing the selection leaves every FIXTURE still sending both dates and "
             "every behavioural and job test still passing. The wire contract is the only guard that "
             "reds — #74's argument for the Jira `fields` list, on the provider it was never applied to.",
    ),
    Control(
        "D2  blind the dueDate decoder (rename the tag, do not delete the field)",
        LINEAR,
        'DueDate     string `json:"dueDate"`',
        'DueDate     string `json:"talyvorNotDueDate"`',
        red="TestLinearSource_DueDateAndCompletedAtLand",
        green="TestLinearRequest_AsksForTheDateFields",
        note="Behavioural, not a build failure — #74's C1.",
    ),
    Control(
        "D3  blind the completedAt decoder",
        LINEAR,
        'CompletedAt string `json:"completedAt"`',
        'CompletedAt string `json:"talyvorNotCompletedAt"`',
        red="TestLinearSource_DueDateAndCompletedAtLand",
        green="TestLinearRequest_AsksForTheDateFields",
    ),
    # ── the layouts: #74's trap, re-injected on the provider whose bytes nobody has seen ──────────
    Control(
        "D4  reach for time.RFC3339 alone (drop the date-only layout)",
        LINEAR,
        '\t"2006-01-02", // TimelessDate',
        '\t// "2006-01-02", // TimelessDate',
        red="TestLinearDateShapes_AllPinnedShapesAreAccepted",
        green="TestLinearRequest_AsksForTheDateFields",
        note="⚠ THIS IS #74's ENTIRE FINDING, RE-INJECTED. Reaching for the obvious constant refuses "
             "a TimelessDate, and every fabricated RFC3339 fixture in the package would still pass. "
             "The difference here is that the refusal is now REPORTED rather than nil'd — which is "
             "what makes shipping on a declared type instead of observed bytes honest.",
    ),
    Control(
        "D5  accept anything (a parser that never refuses)",
        LINEAR,
        "\tfor _, layout := range linearTimeLayouts {",
        "\tif s != \"\" {\n\t\treturn time.Now().UTC(), true\n\t}\n\tfor _, layout := range linearTimeLayouts {",
        red="TestLinearDateShapes_AllPinnedShapesAreAccepted",
        green="TestLinearRequest_AsksForTheDateFields",
        note="A pinned list is only meaningful if something is outside it: the ambiguous D/M/Y shape "
             "must still be refused, or 'accept everything' would satisfy the accept-all loop.",
    ),
    # ── the two decisions ────────────────────────────────────────────────────────────────────────
    Control(
        "D6  stop refusing a completion time on a non-done issue",
        LINEAR,
        "\tif status != model.StatusDone {\n\t\treturn nil, []FieldNote{{Field: fieldCompletionTime, Value: raw, Mapped: string(status), Via: viaStatusNotDone}}\n\t}\n",
        "",
        red="TestLinearSource_CompletedAtRefusedUnlessDone",
        green="TestLinearRequest_AsksForTheDateFields",
        note="#74's decision, inherited: a non-done row carrying completed_at is a state no Track path "
             "can produce, and analytics counts any non-null as delivered work.",
    ),
    Control(
        "D7  nil an unparseable date SILENTLY instead of reporting it",
        LINEAR,
        "\t\treturn nil, []FieldNote{{Field: fieldDueDate, Value: raw, Via: viaUnparseableDate}}",
        "\t\treturn nil, nil",
        red="TestLinearSource_UnparseableDateIsReportedNotNilled",
        green="TestLinearRequest_AsksForTheDateFields",
        note="⚠ THE MERGE'S HONESTY GUARANTEE. The output serialisation of these scalars is NOT "
             "measurable from this environment, so the reported refusal is the entire reason shipping "
             "on a declared type is defensible. This is the edit that turns it back into a silent drop.",
    ),
    Control(
        "D8  report an ABSENT due date as a loss",
        LINEAR,
        "func linearDueDate(raw string) (*time.Time, []FieldNote) {\n\tif strings.TrimSpace(raw) == \"\" {\n\t\treturn nil, nil\n\t}\n",
        "func linearDueDate(raw string) (*time.Time, []FieldNote) {\n",
        red="TestLinearSource_AbsentDatesAreNotReported",
        green="TestLinearSource_DueDateAndCompletedAtLand",
        note="⚠ CONTROLS A TEST THAT PASSED ON ITS FIRST RUN. If every issue without a due date "
             "warned, the channel that carries the two REAL losses would become noise nobody reads.",
    ),
    # ── isolation: is the job test the unit test twice? ───────────────────────────────────────────
    Control(
        "D9  swap the two date columns in the shared upsert SQL",
        STORE,
        "         due_date, completed_at, lens_feature, labels, sort_order)",
        "         completed_at, due_date, lens_feature, labels, sort_order)",
        red="TestJobRow_LinearAPI_DateFieldsLandInPostgres",
        green="TestLinearSource_DueDateAndCompletedAtLand",
        note="⚠ THE ISOLATION CONTROL, and #74's C9 one provider over. It is arity-preserving, so it "
             "compiles and every source-level assertion stays green while the COLUMNS in Postgres "
             "hold each other's values. #74 found this SQL omitting completed_at entirely; the Linear "
             "path inherits that fix, and 'inherits' is an assumption this proves rather than states.",
    ),
]


def main():
    if not os.environ.get("TRACK_TEST_DATABASE_URL"):
        print("TRACK_TEST_DATABASE_URL is not set — the job control needs a real Postgres.")
        return 2

    print("baseline: the whole importer package must be green before any mutation")
    ok, out = run(".")
    if not ok:
        print(out[-3000:])
        return 1
    print("  baseline OK\n")

    failures = []
    for c in CONTROLS:
        original = c.path.read_text()
        extra_original = c.extra[0].read_text() if c.extra else None
        before = sha(c.path)
        c.apply()
        try:
            red_ok, red_out = run(c.red)
            green_ok, green_out = run(c.green)
        finally:
            c.restore(original)
            c.restore_extra(extra_original)

        caught = (not red_ok) if c.expect_red else red_ok
        verdict = ("CAUGHT" if caught else "*** NOT CAUGHT ***") if c.expect_red else \
                  ("STAYED GREEN (as designed)" if caught else "*** unexpectedly red ***")
        isolated = "must-stay-green OK" if green_ok else "*** MUST-STAY-GREEN BROKE ***"
        print(f"{c.name}\n    {verdict} | {isolated}")
        if c.note:
            print(f"    {c.note}")
        if not caught or not green_ok:
            failures.append(c.name)
            print((red_out + green_out)[-1500:])
        print()

    print(f"{len(CONTROLS) - len(failures)}/{len(CONTROLS)} controls behaved as designed")
    if failures:
        print("FAILED:", ", ".join(failures))
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
