#!/usr/bin/env python3
"""w34-9c27 — positive controls for the null-`team` refusal in internal/importer/linear.go.

The change is two lines of production code (a value struct becomes a pointer; a nil check returns
an error). Two lines is exactly the size at which a guard can be green for the wrong reason, so
every mutation below names BOTH the test it expects to fail AND a substring the failure must
contain — the tests in linear_null_team_test.go share a file, and a control that only checked
"something went red" could not say WHICH assertion caught it.

  C1  restore the VALUE struct + drop the nil check     -> the defect itself; source + job tests red
  C2  keep the pointer, drop only the nil check         -> a nil deref would PANIC, not report;
                                                          proves the pointer alone is not the fix
  C3  refuse on nil AND on an empty node list           -> the over-refusal; the EMPTY-TEAM controls
                                                          must go red. This is the mutation that
                                                          proves the guard discriminates rather than
                                                          alarming on every zero.
  C4  check the team BEFORE the errors[] arm            -> a 200 carrying a GraphQL error must keep
                                                          its own sentence
  C5  drop the team key from the message                -> the operator-facing half; the sentence is
                                                          the deliverable, not the status
  N1  a comment-only edit (bytes change, behaviour does not) -> everything must stay GREEN

Every mutation is applied to a byte-exact copy, sha256-verified on restore.
"""
import hashlib
import os
import shutil
import subprocess
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SRC = os.path.join(REPO, "internal", "importer", "linear.go")
PKG = "./internal/importer/"

SOURCE_TESTS = ("TestLinearSource_ANullTeam|TestLinearSource_ATeamThatResolved|"
                "TestLinearSource_AGraphQLErrorBesideANullTeam")
JOB_TESTS = "TestJobRow_LinearAPI_ANullTeam|TestJobRow_LinearAPI_AnEmptyTeam"
ALL_TESTS = f"{SOURCE_TESTS}|{JOB_TESTS}"

POINTER_DECL = "\t\tTeam *struct {"
VALUE_DECL = "\t\tTeam struct {"

NIL_CHECK = """		if parsed.Data.Team == nil {
			return linearPage{}, fmt.Errorf(
				"the team %q did not resolve — Linear answered data.team = null with no errors[], so this "+
					"credential can see no team under that id/key and NOTHING was imported; check the team "+
					"key on the Linear integration", c.team)
		}
"""

ERRORS_ARM = """		if len(parsed.Errors) > 0 {
			return linearPage{}, fmt.Errorf("linear: api error: %s", firstLinearError(parsed))
		}
"""


def sha(path):
    with open(path, "rb") as fh:
        return hashlib.sha256(fh.read()).hexdigest()


def read():
    with open(SRC, encoding="utf-8") as fh:
        return fh.read()


def write(text):
    with open(SRC, "w", encoding="utf-8") as fh:
        fh.write(text)


def run_tests(pattern):
    """Returns (exit_code, combined_output)."""
    env = dict(os.environ)
    if "TRACK_TEST_DATABASE_URL" not in env:
        raise SystemExit(
            "REFUSED: TRACK_TEST_DATABASE_URL is not set. The job-level halves of these controls "
            "run on real Postgres; scoring them against a skipped test would score nothing."
        )
    p = subprocess.run(
        ["go", "test", "-count=1", "-run", pattern, PKG],
        cwd=REPO, capture_output=True, text=True, env=env,
    )
    return p.returncode, p.stdout + p.stderr


def apply(name, mutate, expect_red, must_contain, original):
    """mutate: str -> str. expect_red: True if the test run must FAIL."""
    text = mutate(original)
    if text == original:
        print(f"  {name}: REFUSED — the mutation changed nothing; its anchor has decayed")
        return False
    write(text)
    try:
        code, out = run_tests(ALL_TESTS)
    finally:
        write(original)
    red = code != 0
    verdict_ok = red == expect_red
    found = all(s in out for s in must_contain) if red else not must_contain
    # A compile failure is NOT the catch these controls are scored on, except where stated.
    compiled = "build failed" not in out and "cannot use" not in out
    print(f"  {name}: {'RED' if red else 'GREEN'} "
          f"(want {'RED' if expect_red else 'GREEN'}) "
          f"{'✓' if verdict_ok else '✗'}"
          f"{'' if not must_contain else '  evidence ' + ('✓' if found else '✗')}"
          f"{'' if compiled else '  [compile error — see note]'}")
    if red and must_contain and not found:
        for line in out.splitlines():
            if "FAIL:" in line or "---" in line:
                print(f"      {line.strip()}")
    return verdict_ok and (found or not red)


def main():
    if not os.path.exists(SRC):
        raise SystemExit(f"REFUSED: no {SRC}")
    original = read()
    before = sha(SRC)

    # Anchor checks: every anchor must occur EXACTLY once. Present-twice is the failure mode a
    # str.replace(..., 1) patch reports as fully applied — see W1.1's #212.
    for label, anchor in (("pointer decl", POINTER_DECL), ("nil check", NIL_CHECK),
                          ("errors arm", ERRORS_ARM)):
        n = original.count(anchor)
        if n != 1:
            raise SystemExit(f"REFUSED: anchor {label!r} occurs {n} times, want exactly 1")
    print(f"anchors: all 3 unique  ·  {SRC} sha256 {before[:12]}…")

    print("\nBASELINE (unmutated tree):")
    code, out = run_tests(ALL_TESTS)
    print(f"  baseline: {'GREEN' if code == 0 else 'RED'} (want GREEN)")
    if code != 0:
        print(out[-2000:])
        raise SystemExit("REFUSED: the baseline is not green; controls would be unscoreable")

    ok = True
    print("\nCONTROLS:")

    # C1 — the defect itself, restored.
    ok &= apply(
        "C1 value struct + no nil check (the defect)",
        lambda s: s.replace(POINTER_DECL, VALUE_DECL).replace(NIL_CHECK, ""),
        expect_red=True,
        must_contain=["TestLinearSource_ANullTeamIsReportedNotImportedAsAnEmptyOne",
                      "TestJobRow_LinearAPI_ANullTeamDoesNotRecordASucceededImport"],
        original=original,
    )

    # C2 — pointer kept, nil check dropped. The deref panics; the point is that the type change
    # alone reports NOTHING, so the two halves are not redundant.
    ok &= apply(
        "C2 pointer kept, nil check dropped",
        lambda s: s.replace(NIL_CHECK, ""),
        expect_red=True,
        must_contain=["TestLinearSource_ANullTeamIsReportedNotImportedAsAnEmptyOne"],
        original=original,
    )

    # C3 — THE OVER-REFUSAL. Refuse a team that resolved but holds nothing. The EMPTY-team controls
    # must catch this; if they do not, the guard is an alarm on every empty team and the empty-team
    # tests are decoration.
    ok &= apply(
        "C3 also refuse a resolved-but-empty team (over-refusal)",
        lambda s: s.replace(
            "if parsed.Data.Team == nil {",
            "if parsed.Data.Team == nil || len(parsed.Data.Team.Issues.Nodes) == 0 {"),
        expect_red=True,
        must_contain=["TestLinearSource_ATeamThatResolvedAndIsEmptyStaysClean",
                      "TestJobRow_LinearAPI_AnEmptyTeamStillRecordsASucceededImport"],
        original=original,
    )

    # C4 — order. Move the team check ABOVE the errors[] arm; a 200 carrying a GraphQL error would
    # then be reported as a missing team. Scored as a fact, not a prediction: see the note printed
    # after the run.
    ok &= apply(
        "C4 team check moved above the errors[] arm",
        lambda s: s.replace(ERRORS_ARM, "").replace(NIL_CHECK, NIL_CHECK + ERRORS_ARM),
        expect_red=True,
        must_contain=["TestLinearSource_AGraphQLErrorBesideANullTeamKeepsTheErrorSentence"],
        original=original,
    )

    # C5 — the sentence. Drop the team key from the message; the status is still right and the
    # operator still cannot act.
    # ⚠ THE FIRST DRAFT OF C5 DELETED THE `%q` VERB AND LEFT `c.team` AS AN ARGUMENT, so it went RED
    # on a COMPILE ERROR and scored a catch the assertions never made. It is recorded rather than
    # quietly fixed, because "the control went red" and "the guard caught it" are different claims
    # and only one of them was true. This version keeps the format string well-formed and changes
    # only the VALUE, which is the thing the assertion is about.
    ok &= apply(
        "C5 message no longer quotes the team key",
        lambda s: s.replace('key on the Linear integration", c.team)',
                            'key on the Linear integration", "redacted")'),
        expect_red=True,
        must_contain=["TestLinearSource_ANullTeamIsReportedNotImportedAsAnEmptyOne",
                      "TestJobRow_LinearAPI_ANullTeamDoesNotRecordASucceededImport"],
        original=original,
    )

    # N1 — NEGATIVE control: bytes change, behaviour does not. Everything must stay green.
    ok &= apply(
        "N1 comment-only edit (negative control)",
        lambda s: s.replace("// A NULL TEAM IS NOT AN EMPTY TEAM.",
                            "// A NULL TEAM IS NOT AN EMPTY TEAM. (n1)"),
        expect_red=False,
        must_contain=[],
        original=original,
    )

    after = sha(SRC)
    print(f"\nrestore: sha256 {after[:12]}…  {'IDENTICAL' if after == before else 'DIFFERS — TREE DIRTY'}")
    if after != before:
        ok = False
    print(f"\nCONTROLS: {'ALL SCORED AS PREDICTED' if ok else 'AT LEAST ONE OFF-PREDICTION'}")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
