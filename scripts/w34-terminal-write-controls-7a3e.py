#!/usr/bin/env python3
"""w34-7a3e — positive controls for the terminal-write REPORT in internal/importer/runner.go.

The change is small and is entirely about observability: `_ = r.jobs.Finish(...)` at three call
sites becomes `r.finish(...)`, which logs when the terminal UPDATE fails. A change that only adds a
log line is exactly the shape whose guard can be green for the wrong reason — it can pass because
the report fires on EVERY import, because it fires at the wrong level, or because it carries
nothing an operator can act on. So every mutation below names BOTH the test it expects to red AND a
substring the failure output must contain.

  C1  restore the discard at the SUCCESS call site   -> the defect itself; the main test reds
  C2  restore the discard at the sourceFor call site -> proves per-CALL-SITE coverage: the main
                                                        test stays GREEN and only the source one reds
  C3  slog.Error -> slog.Warn                        -> the level is load-bearing: an unrecorded
                                                        import is not a warning, nothing retries it
  C4  drop the counts from the record                -> the line REPLACES the row nobody could
                                                        write; "finish failed" alone is not a report
  C5  log unconditionally (report on a CLEAN finish) -> THE DISCRIMINATION CONTROL. A report that
                                                        fires on every import reports nothing;
                                                        TestRunner_SuccessfulFinish_ReportsNothing
                                                        must catch it
  C6  ClaimNext also claims 'running' rows           -> reds the RESIDUAL PIN, which passes both
                                                        before and after the change. Without this
                                                        control that test is a decoration; with it,
                                                        it is a falsifiable statement that the row
                                                        is still stuck
  N1  a comment-only edit (bytes change, behaviour does not) -> everything must stay GREEN

C6 mutates internal/importer/jobs.go; every other control mutates runner.go. Both files are
sha256-verified byte-identical after every control.
"""
import hashlib
import os
import subprocess

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
RUNNER = os.path.join(REPO, "internal", "importer", "runner.go")
JOBS = os.path.join(REPO, "internal", "importer", "jobs.go")
PKG = "./internal/importer/"

MAIN = "TestRunner_TerminalWriteFailure_IsReported"
SOURCE = "TestRunner_TerminalWriteFailure_IsReportedForAFailedSource"
CLEAN = "TestRunner_SuccessfulFinish_ReportsNothing"
RESIDUAL = "TestRunner_TerminalWriteFailure_LeavesTheRowRunning"
# -run is a regex on the test NAME, and MAIN is a prefix of SOURCE, so this pattern selects all
# four whichever way Go anchors it.
ALL_TESTS = f"{MAIN}|{SOURCE}|{CLEAN}|{RESIDUAL}"

# ── anchors ───────────────────────────────────────────────────────────────────────────────────
# The two JobFailed call sites are BYTE-IDENTICAL lines, so the sourceFor one is anchored with the
# statement above it. An anchor that matched both would let C2 mutate the wrong site and still
# report a catch.
SUCCESS_SITE = ("\tr.finish(finishCtx, job, terminalStatus(out), out.Imported, out.Refused, "
                "out.Skipped, summary, out.Warnings)\n")
SUCCESS_DISCARD = ("\t_ = r.jobs.Finish(finishCtx, job.ID, job.WorkspaceID, terminalStatus(out), "
                   "out.Imported, out.Refused, out.Skipped, summary, out.Warnings)\n")

SOURCE_SITE = ("\tsrc, err := r.sourceFor(ctx, job)\n"
               "\tif err != nil {\n"
               "\t\tr.finish(finishCtx, job, JobFailed, 0, 0, 0, err.Error(), nil)\n")
SOURCE_DISCARD = ("\tsrc, err := r.sourceFor(ctx, job)\n"
                  "\tif err != nil {\n"
                  "\t\t_ = r.jobs.Finish(finishCtx, job.ID, job.WorkspaceID, JobFailed, 0, 0, 0, "
                  "err.Error(), nil)\n")

ERROR_CALL = "\t\tslog.Error(\"importer: recording an import job's terminal state failed"
IMPORTED_ATTR = "\t\t\tslog.Int(\"imported\", imported),\n"
FINISH_IF = ("\tif err := r.jobs.Finish(ctx, job.ID, job.WorkspaceID, status, imported, skipped, "
             "failed, errSummary, warnings); err != nil {\n")
FINISH_ALWAYS = ("\terr := r.jobs.Finish(ctx, job.ID, job.WorkspaceID, status, imported, skipped, "
                 "failed, errSummary, warnings)\n\tif true {\n")
ERR_ATTR = "\t\t\tslog.String(\"err\", err.Error()))\n"
ERR_ATTR_SAFE = "\t\t\tslog.String(\"err\", fmt.Sprint(err)))\n"

CLAIM_PENDING = "\t\t WHERE status = 'pending' ORDER BY created_at\n"
CLAIM_ANY = "\t\t WHERE status IN ('pending','running') ORDER BY created_at\n"

COMMENT_ANCHOR = "// finish records the job's terminal state and REPORTS a failure to record it.\n"


def sha(path):
    with open(path, "rb") as fh:
        return hashlib.sha256(fh.read()).hexdigest()


def read(path):
    with open(path, encoding="utf-8") as fh:
        return fh.read()


def write(path, text):
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(text)


def run_tests():
    env = dict(os.environ)
    if "TRACK_TEST_DATABASE_URL" not in env:
        raise SystemExit(
            "REFUSED: TRACK_TEST_DATABASE_URL is not set. Every one of these tests runs on real "
            "Postgres; scoring them against a skipped test would score nothing."
        )
    p = subprocess.run(["go", "test", "-count=1", "-v", "-run", ALL_TESTS, PKG],
                       cwd=REPO, capture_output=True, text=True, env=env)
    return p.returncode, p.stdout + p.stderr


def failed_tests(out):
    """The test NAMES that reported --- FAIL. Read from the verdict lines, not from -run."""
    return sorted({line.split()[2].rstrip(":")
                   for line in out.splitlines() if line.strip().startswith("--- FAIL:")})


def apply(name, path, mutate, expect_red, must_red, must_stay_green, must_contain):
    original = read(path)
    text = mutate(original)
    if text == original:
        print(f"  {name}: REFUSED — the mutation changed nothing; its anchor has decayed")
        return False
    write(path, text)
    try:
        code, out = run_tests()
    finally:
        write(path, original)
    red = code != 0
    reds = failed_tests(out)
    # A build failure is not the catch these controls are scored on — say so rather than count it.
    broke = "[build failed]" in out or "build failed" in out
    ok = (red == expect_red)
    # ⚠ EXACT NAME MATCH, NEVER `in`. The first draft scored membership with a substring test and
    # C2 reported ✗ for a control that had behaved exactly as predicted: MAIN
    # ("…_TerminalWriteFailure_IsReported") is a PREFIX of SOURCE
    # ("…_IsReportedForAFailedSource"), so "MAIN stayed green" read as false whenever SOURCE was
    # the intended red. The harness was the thing that was wrong, and it is recorded here rather
    # than quietly corrected — a control's verdict is only as true as the parser reading it.
    ok &= all(t in reds for t in must_red)
    ok &= not any(t in reds for t in must_stay_green)
    ok &= all(s in out for s in must_contain)
    ok &= not broke
    print(f"  {name}: {'RED' if red else 'GREEN'} (want {'RED' if expect_red else 'GREEN'}) "
          f"{'✓' if ok else '✗'}"
          f"{'  [COMPILE ERROR — not a catch]' if broke else ''}")
    if reds:
        print(f"      reds: {', '.join(reds)}")
    if must_contain:
        for s in must_contain:
            print(f"      evidence {'✓' if s in out else '✗'}: {s[:78]}")
    return ok


def main():
    runner0, jobs0 = read(RUNNER), read(JOBS)
    before = {RUNNER: sha(RUNNER), JOBS: sha(JOBS)}

    # Anchor census FIRST: every anchor exactly once. An anchor present twice is the failure mode a
    # str.replace(..., 1) patch reports as fully applied; present zero times is a control that
    # no-ops and is byte-indistinguishable from a guard that works (this item, #71).
    anchors = [("success site", runner0, SUCCESS_SITE), ("source site", runner0, SOURCE_SITE),
               ("slog.Error call", runner0, ERROR_CALL), ("imported attr", runner0, IMPORTED_ATTR),
               ("finish if", runner0, FINISH_IF), ("err attr", runner0, ERR_ATTR),
               ("finish comment", runner0, COMMENT_ANCHOR), ("claim pending", jobs0, CLAIM_PENDING)]
    for label, text, anchor in anchors:
        n = text.count(anchor)
        if n != 1:
            raise SystemExit(f"REFUSED: anchor {label!r} occurs {n} times, want exactly 1")
    print(f"anchors: all {len(anchors)} unique")
    print(f"  runner.go sha256 {before[RUNNER][:12]}…   jobs.go sha256 {before[JOBS][:12]}…")

    print("\nBASELINE (unmutated tree):")
    code, out = run_tests()
    print(f"  baseline: {'GREEN' if code == 0 else 'RED'} (want GREEN)")
    if code != 0:
        print(out[-2500:])
        raise SystemExit("REFUSED: the baseline is not green; controls would be unscoreable")

    ok = True
    print("\nCONTROLS:")

    ok &= apply("C1 discard restored at the SUCCESS call site (the defect)", RUNNER,
                lambda s: s.replace(SUCCESS_SITE, SUCCESS_DISCARD),
                expect_red=True, must_red=[MAIN], must_stay_green=[CLEAN],
                must_contain=["and NOTHING was logged"])

    # C2 — the main test must STAY GREEN here. That is the whole point: it proves the two call
    # sites are covered separately, and that C1's red was not just "something in the file broke".
    ok &= apply("C2 discard restored at the sourceFor call site", RUNNER,
                lambda s: s.replace(SOURCE_SITE, SOURCE_DISCARD),
                expect_red=True, must_red=[SOURCE], must_stay_green=[MAIN, CLEAN],
                must_contain=["a job that failed BEFORE its source opened"])

    ok &= apply("C3 slog.Error -> slog.Warn", RUNNER,
                lambda s: s.replace(ERROR_CALL, ERROR_CALL.replace("slog.Error(", "slog.Warn(")),
                expect_red=True, must_red=[MAIN], must_stay_green=[CLEAN, SOURCE],
                must_contain=["want ERROR"])

    ok &= apply("C4 counts dropped from the record", RUNNER,
                lambda s: s.replace(IMPORTED_ATTR, ""),
                expect_red=True, must_red=[MAIN], must_stay_green=[CLEAN, SOURCE],
                must_contain=["must carry the counts the row could not"])

    # C5 — THE DISCRIMINATION CONTROL. `if true` keeps the record firing on a clean finish; the err
    # attribute is made nil-safe so the control measures an over-report and not a panic.
    ok &= apply("C5 report on EVERY finish, clean or not", RUNNER,
                lambda s: s.replace(FINISH_IF, FINISH_ALWAYS).replace(ERR_ATTR, ERR_ATTR_SAFE),
                expect_red=True, must_red=[CLEAN], must_stay_green=[MAIN, SOURCE],
                must_contain=["cannot be read as a report of anything"])

    # C6 — the residual pin's falsifier. It mutates the CLAIM query, not the reporting path.
    ok &= apply("C6 ClaimNext also claims 'running' rows (residual pin falsifier)", JOBS,
                lambda s: s.replace(CLAIM_PENDING, CLAIM_ANY),
                expect_red=True, must_red=[RESIDUAL], must_stay_green=[CLEAN],
                must_contain=["want did=false"])

    ok &= apply("N1 comment-only edit (negative control)", RUNNER,
                lambda s: s.replace(COMMENT_ANCHOR, COMMENT_ANCHOR + "//\n"),
                expect_red=False, must_red=[], must_stay_green=[MAIN, SOURCE, CLEAN, RESIDUAL],
                must_contain=[])

    print("\nRESTORE:")
    for path, label in ((RUNNER, "runner.go"), (JOBS, "jobs.go")):
        after = sha(path)
        same = after == before[path]
        print(f"  {label} sha256 {after[:12]}…  {'byte-identical ✓' if same else 'CHANGED ✗'}")
        ok &= same

    print("\nRESULT:", "ALL CONTROLS SCORED AS PREDICTED" if ok else "AT LEAST ONE CONTROL DID NOT")
    raise SystemExit(0 if ok else 1)


if __name__ == "__main__":
    main()
