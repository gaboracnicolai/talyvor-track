#!/usr/bin/env python3
"""Positive controls for the two API sources' PAGE-TERMINATION guards, both directions.

WHY THIS EXISTS. api_pagination_termination_test.go asserts things that are easy to write and
easy to write VACUOUSLY: "the import did not stop early", "the source terminated", "an error was
reported". Every one of those passes on a source that happens to do the right thing for a reason
the test never touches — and three of the six cases were GREEN on the unmutated tree the moment
the fix landed, which is exactly when a guard deserves to be doubted.

So each control MUTATES the shipped source back to the behaviour it replaced, runs ONLY the tests
that name that behaviour, and asserts they turn RED. A control that observes green in both
directions has proved nothing, and this script FAILS on it rather than reporting it.

The mutations are the defect, restated one at a time:

  C1  jira   exhausted := isLast || nextToken == ""   ->  isLast alone
             (the shipped bug: terminate on the field Atlassian's spec makes OPTIONAL and ignore
              the one it documents as the terminator. Measured: 3 issues yielded 12+ times.)
  C2  jira   an empty page ends the source, unconditionally
             (the shipped bug: page 2 is never requested and the job records succeeded imported=0)
  C3  linear an empty page ends the source, unconditionally
  C4  linear drop the hasNextPage-with-no-endCursor error  (re-reads page 1 for ever)
  C5  jira   drop the repeated-token error                 (walks one page for ever)
  C6  bound  maxConsecutiveEmptyPages 200 -> 100000        (the bound stops bounding)

  C0  the baseline: the unmutated tree must be GREEN. Without it every RED below could be a
      compile error rather than a caught defect.
  C7  the anti-vacuity control on the JOB test: it must read POSTGRES, not the ImportResult. The
      mutation makes the runner count a row it never wrote; the DB assertion is what catches it.

Run from the repo root, with a real Postgres for C0/C2/C7:

    TRACK_TEST_DATABASE_URL=postgres://... python3 scripts/w34-pagination-termination-controls.py
"""

import os
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
JIRA = os.path.join(ROOT, "internal", "importer", "jira.go")
LINEAR = os.path.join(ROOT, "internal", "importer", "linear.go")
SOURCE = os.path.join(ROOT, "internal", "importer", "source.go")
RUNNER = os.path.join(ROOT, "internal", "importer", "runner.go")

SRC_ONLY = ("TestJiraSource_TerminatesOnTheDocumentedTerminatorNotOnIsLast|"
            "TestJiraSource_AnEmptyPageThatIsNotTheLastDoesNotEndTheImport|"
            "TestLinearSource_AnEmptyPageThatIsNotTheLastDoesNotEndTheImport|"
            "TestLinearSource_HasNextPageWithNoCursorIsReportedNotSwallowed|"
            "TestJiraSource_ARepeatedPageTokenIsReportedNotSwallowed|"
            "TestJiraSource_EndlessEmptyPagesEndAsAnErrorNotALoop")
JOB_ONLY = "TestJobRow_JiraAPI_AnEmptyFirstPageIsNotACompletedImport"
ALL_CASES = SRC_ONLY + "|" + JOB_ONLY

failures = []


def run_tests(pattern):
    """True when the named tests PASS."""
    p = subprocess.run(
        ["go", "test", "./internal/importer/", "-run", pattern, "-count=1", "-timeout", "180s"],
        cwd=ROOT, capture_output=True, text=True)
    return p.returncode == 0, (p.stdout + p.stderr)


def patch(path, old, new):
    with open(path, encoding="utf-8") as f:
        s = f.read()
    n = s.count(old)
    if n != 1:
        raise SystemExit(f"CONTROL HARNESS BROKEN: {os.path.basename(path)} contains {n} copies of\n{old!r}\n"
                         "A mutation that edits zero bytes is byte-indistinguishable from one that works.")
    with open(path, "w", encoding="utf-8") as f:
        f.write(s.replace(old, new, 1))
    return s


def control(name, path, old, new, pattern, why):
    original = patch(path, old, new)
    try:
        passed, out = run_tests(pattern)
    finally:
        with open(path, "w", encoding="utf-8") as f:
            f.write(original)
    if passed:
        failures.append(f"{name}: MUTATION DID NOT RED — {why}")
        print(f"  {name}  ✗ still green with the defect reinstated: {why}")
        print("\n".join(out.splitlines()[-6:]))
    else:
        first = next((l for l in out.splitlines() if l.strip().startswith("--- FAIL")), "(red)")
        print(f"  {name}  ✓ red as required — {first.strip()}")


print("C0  baseline: the unmutated tree must be GREEN")
ok, out = run_tests(ALL_CASES)
if not ok:
    print("\n".join(out.splitlines()[-25:]))
    raise SystemExit("C0 FAILED: the tree is not green before any mutation — every RED below would be meaningless.")
print("  C0  ✓ green")

print("\nC1  jira: terminate on isLast alone (ignore the documented terminator)")
control("C1", JIRA,
        "s.exhausted = page.isLast || page.nextToken == \"\"",
        "s.exhausted = page.isLast",
        "TestJiraSource_TerminatesOnTheDocumentedTerminatorNotOnIsLast",
        "a final page without the OPTIONAL isLast must not restart the import")

print("\nC2  jira: an empty page ends the source, unconditionally (the shipped behaviour)")
control("C2", JIRA,
        """		if len(page.issues) > 0 {
			s.emptyPages = 0
			break
		}
		if s.exhausted {""",
        """		if len(page.issues) == 0 {
			s.done = true
			return SourceRow{}, false
		}
		if len(page.issues) > 0 {
			s.emptyPages = 0
			break
		}
		if s.exhausted {""",
        "TestJiraSource_AnEmptyPageThatIsNotTheLastDoesNotEndTheImport|" + JOB_ONLY,
        "an empty non-final page must not abandon the pages after it")

print("\nC3  linear: an empty page ends the source, unconditionally (the shipped behaviour)")
control("C3", LINEAR,
        """		if len(page.issues) > 0 {
			s.emptyPages = 0
			break
		}""",
        """		if len(page.issues) == 0 {
			s.done = true
			return SourceRow{}, false
		}
		if len(page.issues) > 0 {
			s.emptyPages = 0
			break
		}""",
        "TestLinearSource_AnEmptyPageThatIsNotTheLastDoesNotEndTheImport",
        "an empty non-final page must not abandon the pages after it")

print("\nC4  linear: drop the hasNextPage-with-no-endCursor error")
control("C4", LINEAR,
        "		if s.started && s.cursor == \"\" {\n			s.done = true",
        "		if false {\n			s.done = true",
        "TestLinearSource_HasNextPageWithNoCursorIsReportedNotSwallowed",
        "an unfetchable next page must be reported, not walked for ever")

print("\nC5  jira: drop the repeated-token (no progress) error")
control("C5", JIRA,
        "		if page.nextToken == prevToken {\n			s.done = true",
        "		if false {\n			s.done = true",
        "TestJiraSource_ARepeatedPageTokenIsReportedNotSwallowed",
        "a provider that hands back its own token must be reported, not looped on")

print("\nC6  bound: maxConsecutiveEmptyPages 200 -> 100000 (the bound stops bounding)")
control("C6", SOURCE,
        "const maxConsecutiveEmptyPages = 200",
        "const maxConsecutiveEmptyPages = 100000",
        "TestJiraSource_EndlessEmptyPagesEndAsAnErrorNotALoop",
        "the walk must end at the bound, and the test must be able to tell")

print("\nC7  anti-vacuity: the job case must read POSTGRES, not the ImportResult")
control("C7", RUNNER,
        "	_ = r.jobs.Finish(finishCtx, job.ID, job.WorkspaceID, terminalStatus(out), out.Imported,",
        "	_ = r.jobs.Finish(finishCtx, job.ID, job.WorkspaceID, terminalStatus(out), out.Imported+7,",
        JOB_ONLY,
        "a job row that claims 9 imported rows while Postgres holds 2 must fail the case")

print()
if failures:
    for f in failures:
        print("FAILED:", f)
    sys.exit(1)
print("ALL CONTROLS PASSED — every guard was watched to fail in the direction it exists for.")
