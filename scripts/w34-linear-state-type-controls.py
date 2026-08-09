#!/usr/bin/env python3
"""w34-linear-state-type-controls.py — positive controls for the Linear state.type merge.

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
WIRE = IMP / "linear_state_type_test.go"


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
    # ── the query document: the one thing that can 400 a real import ─────────────────────────────
    Control(
        "C1  revert the query document to `state { name }`",
        LINEAR,
        "state { name type } priority",
        "state { name } priority",
        red="TestLinearRequest_WireContract",
        green="TestJobRow_LinearAPI_StateTypeResolvesTheStatusInPostgres",
        note="⚠ THE MUST-STAY-GREEN HERE IS THE FINDING, NOT A FORMALITY. Every fake in this package "
             "answers any query with the same body, so reverting the query leaves the FIXTURES still "
             "sending `type` and every behavioural test still passing. The wire contract is the ONLY "
             "guard that reds. That is precisely the hole #75 closed on the Jira side.",
    ),
    Control(
        "C2  blind the decoder (rename the json tag, do not delete the field)",
        LINEAR,
        '`json:"type"`',
        '`json:"talyvorNotType"`',
        red="TestLinearSource_StateTypeResolvesAnUnrecognisedName",
        green="TestLinearRequest_WireContract",
        note="Behavioural, not a build failure — #74's C1. Deleting the struct field would not "
             "compile and would red the must-stay-green test too.",
    ),
    Control(
        "C3  stop calling the resolver (revert mapLinearNodes to the empty fallback)",
        LINEAR,
        "\t\tvar fallback statusFallback\n\t\tif !statusOK {\n\t\t\tstatus, fallback = resolveLinearStateType(n.State.Type, status)\n\t\t}\n",
        "\t\tfallback := statusFallback{}\n\t\t_ = resolveLinearStateType\n",
        red="TestLinearSource_StateTypeResolvesAnUnrecognisedName",
        green="TestLinearRequest_WireContract",
        note="A mapper that is right while nothing calls it is this item's own structural-zero shape.",
    ),
    Control(
        "C4  let the type override a name the mapper KNOWS",
        LINEAR,
        "\t\tif !statusOK {\n\t\t\tstatus, fallback = resolveLinearStateType(n.State.Type, status)\n\t\t}",
        "\t\tif true {\n\t\t\tstatus, fallback = resolveLinearStateType(n.State.Type, status)\n\t\t}",
        red="TestLinearSource_ARecognisedNameStillWins",
        green="TestLinearSource_StateTypeResolvesAnUnrecognisedName",
        note="⚠ THIS CONTROLS A TEST THAT PASSED ON ITS FIRST RUN. Name-first is what makes the merge "
             "purely additive, and `started` is strictly coarser than the name for in_review — so "
             "without this the precedence assertion was green for a behaviour nobody had shown could "
             "break.",
    ),
    Control(
        "C5  report a type that NEVER ARRIVED as a resolution",
        LINEAR,
        "\t\treturn unresolved, statusFallback{via: viaNoStateType}",
        "\t\treturn unresolved, statusFallback{via: viaStateType, resolved: true}",
        red="TestJobRow_LinearAPI_NoStateTypeIsReportedNotHidden",
        green="TestLinearSource_StateTypeResolvesAnUnrecognisedName",
        note="The structural-zero guard itself: this is the edit that makes 'your Linear never sent "
             "one' and 'your types resolved everything' the same line in a production job row.",
    ),
    # ── the vocabulary ───────────────────────────────────────────────────────────────────────────
    Control(
        "C6  make `triage` a resolution",
        CSV,
        '\tcase "triage", "duplicate":\n\t\t// Measured, named, and deliberately not answered — see the header above.\n\t\treturn model.StatusBacklog, false',
        '\tcase "duplicate":\n\t\treturn model.StatusBacklog, false\n\tcase "triage":\n\t\treturn model.StatusTodo, true',
        red="TestLinearSource_TriageAndDuplicateAreNotResolutions",
        green="TestLinearSource_StateTypeResolvesAnUnrecognisedName",
        note="Widening the refusal is the exact edit #73's `undefined` rule forbids; it must be "
             "deliberate, not silent.",
    ),
    Control(
        "C7  delete a case the measurement recorded (`canceled`)",
        CSV,
        '\tcase "canceled":\n\t\treturn model.StatusCancelled, true\n',
        "",
        red="TestLinearStateTypeMapping_AgreesWithTheMeasuredVocabulary",
        green="TestMeasuredLinearVocabulary_IsNotEmptyAndCarriesDuplicate",
        note="Rule 1, direction one: a measured type the mapper stops naming.",
    ),
    Control(
        "C8  handle a type the measurement NEVER saw",
        CSV,
        # ⚠ ANCHORED ON `unstarted`, NOT `backlog`. The obvious anchor
        # (`case "backlog": return model.StatusBacklog, true`) appears THREE times in csv.go —
        # mapLinearStatus and mapJiraStatus carry the same clause — and the count assertion is what
        # caught it. An un-asserted control would have mutated whichever one came first.
        '\tcase "unstarted":\n\t\treturn model.StatusTodo, true',
        '\tcase "unstarted", "shipped":\n\t\treturn model.StatusTodo, true',
        red="TestLinearStateTypeMapping_AgreesWithTheMeasuredVocabulary",
        green="TestLinearSource_StateTypeResolvesAnUnrecognisedName",
        note="Rule 1, direction two: a guess wearing the measurement's authority.",
    ),
    Control(
        "C9  drop `duplicate` from the pinned vocabulary",
        WIRE,
        '"triage", "backlog", "unstarted", "started", "completed", "canceled", "duplicate",',
        '"triage", "backlog", "unstarted", "started", "completed", "canceled",',
        red="TestMeasuredLinearVocabulary_IsNotEmptyAndCarriesDuplicate",
        green="TestLinearSource_StateTypeResolvesAnUnrecognisedName",
        note="THE FLOOR. `duplicate` is the seventh value Linear's public docs omit — this is how the "
             "vocabulary silently becomes the six everyone remembers.",
    ),
    Control(
        "C10 break the AST parse path",
        WIRE,
        'parser.ParseFile(fset, "csv.go", nil, 0)',
        'parser.ParseFile(fset, "csv_does_not_exist.go", nil, 0)',
        red="TestLinearStateTypeMapping_AgreesWithTheMeasuredVocabulary",
        green="TestLinearSource_StateTypeResolvesAnUnrecognisedName",
        note="A file-reading test must FAIL on a broken path, never silently find nothing and pass — "
             "the usual way this class of guard goes blind.",
    ),
    # ── isolation: is the job test the unit test twice? ───────────────────────────────────────────
    Control(
        "C11 stop forwarding warnings to the job row",
        RUNNER,
        "summary, out.Warnings)",
        "summary, nil)",
        red="TestJobRow_LinearAPI_StateTypeResolvesTheStatusInPostgres",
        green="TestLinearSource_ResolvedRowSaysItCameFromTheStateType",
        note="⚠ THE ISOLATION CONTROL, and the reason the job file exists at all. This reds ONLY the "
             "real-Postgres job tests while every source-level assertion stays green — so the job "
             "test is not the unit test twice (#74's C9 argument, one layer over).",
    ),
    # ── the vacuity demo (#75's C6) ───────────────────────────────────────────────────────────────
    Control(
        "C12 VACUITY DEMO: compare the query to itself AND revert it",
        WIRE,
        'if !strings.Contains(gotBody, "state { name type }") {',
        "if !strings.Contains(gotBody, strings.ReplaceAll(linearIssuesQuery, \"\\n\", \"\\\\n\")) {",
        red="TestLinearRequest_WireContract",
        green="TestLinearSource_StateTypeResolvesAnUnrecognisedName",
        expect_red=False,
        extra=(LINEAR, "state { name type } priority", "state { name } priority"),
        note="⚠ RECORDED AS A DEMONSTRATION, NOT COUNTED AS A CATCH. The obvious form of the wire "
             "assertion references the constant, so it passes for EVERY possible value of that "
             "constant — including one with no `type` in it. It is expected to stay GREEN here, "
             "which is the whole point: that is why the literal is hardcoded.",
    ),
]


def main():
    if not os.environ.get("TRACK_TEST_DATABASE_URL"):
        print("TRACK_TEST_DATABASE_URL is not set — the job controls need a real Postgres.")
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

        if c.expect_red:
            caught = not red_ok
            verdict = "CAUGHT" if caught else "*** NOT CAUGHT ***"
        else:
            caught = red_ok  # the vacuity demo is expected to stay GREEN
            verdict = "STAYED GREEN (as designed)" if caught else "*** unexpectedly red ***"
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
