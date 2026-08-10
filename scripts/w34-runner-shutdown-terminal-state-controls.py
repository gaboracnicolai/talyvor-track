#!/usr/bin/env python3
"""Positive controls for the runner's terminal write surviving shutdown (W3.4, after #91).

THE FINDING. Runner.Start is launched as `go importRunner.Start(ctx, 0)` with the PROCESS
lifecycle context, so on SIGTERM it is cancelled mid-job. Runner.execute wrote the terminal
status through that SAME cancelled ctx — `_ = r.jobs.Finish(ctx, …)` — which makes the UPDATE
recording what happened the one write guaranteed to fail exactly when it is needed. The error
was discarded and ClaimNext selects `status = 'pending'` only. MEASURED on f0445e3 against real
Postgres: the row stays `running`, finished_at NULL, and a second drain on a healthy context
claims nothing. For every reader the import is still in progress, forever.

TestRunner_ShutdownMidImport_DoesNotLeaveTheJobRunningForever was RED before the fix, at the
assertion it exists for. That is necessary and not sufficient. Each control below removes
exactly one thing and NAMES THE ASSERTION THAT MUST SPEAK, predicted before the run.

⚠ THE TWO PREMISE CHECKS ARE THE VACUITY GUARD, AND C3/C4 ARE WHAT MAKE THEM FACTS. This test's
vacuity mode is not "an empty fixture reports zero" — it is "the shutdown never happened". A
fixture yielding no rows never reaches a write, never cancels anything, and the job finishes
cleanly; a creator that does not cancel measures an ordinary import. The test asserts BOTH that
Create was reached and that the runner's context really was cancelled. C3 empties the fixture,
C4 removes the cancellation, and each must red at ITS OWN premise assertion — not at the
finding's.

⚠ EVERY CONTROL CARRIES A MUST-STAY-GREEN COMPANION: the three pre-existing runner tests
(TestRunner_WritesOnlyIntoJobWorkspace, TestRunner_PartialImport_Observable,
TestRunner_ConcurrentJobs_NoCrossWorkspace). BOTH RED IS `SUSPECT`, NEVER `CAUGHT`.

⚠ C1's MUST-GREEN LIST IS THE CLAIM THAT THIS GUARD IS NOT REDUNDANT. Restoring the defect
leaves all three green: every existing runner test drives RunOnce on a healthy context, so
nothing here could see a terminal write that fails only under cancellation.

⚠ C2 IS THE OVER-FIX, NOT A HALF-FIX, because the plausible wrong answer here is "just use the
detached context everywhere". It earns the claim that ONLY the record is detached: with the
import itself detached, a dying process keeps importing and the job reports `succeeded`.

⚠ EVERY CONTROL IS ONE SINGLE-SITE EDIT ON PURPOSE. The fix touches three Finish call sites; a
control that had to edit all three could apply half of itself and the resulting green would be
read as a dead guard. Hoisting `finishCtx` to one line is what makes the defect restorable in
one anchor.

⚠ THE RUNNER AND ITS VERDICT LOGIC ARE #86–#91's, CARRIED OVER UNCHANGED — a build failure is
never CAUGHT, a `-run` pattern matching nothing is never a pass, and every must-red verdict
names the file:line of the assertion that spoke.

⚠ THE BASELINE GATE IS LOAD-BEARING. Without TRACK_TEST_DATABASE_URL every control here would
SKIP, `go test` would exit 0, and this script would report a clean sweep of controls that never
ran.

    TRACK_TEST_DATABASE_URL=... python3 scripts/w34-runner-shutdown-terminal-state-controls.py
"""
import hashlib
import os
import pathlib
import re
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent

IMP = "./internal/importer/"

RUNNER = "internal/importer/runner.go"
TEST = "internal/importer/runner_shutdown_terminal_state_test.go"

SHUTDOWN = "TestRunner_ShutdownMidImport_DoesNotLeaveTheJobRunningForever"
ONLY_WS = "TestRunner_WritesOnlyIntoJobWorkspace"
PARTIAL = "TestRunner_PartialImport_Observable"
CONCURRENT = "TestRunner_ConcurrentJobs_NoCrossWorkspace"

RUNNER_SUITE = [ONLY_WS, PARTIAL, CONCURRENT]

# (id, file, anchor, replacement, must_red, must_stay_green, package, note)
CONTROLS = [
    ("C1", RUNNER,
     "\tfinishCtx := context.WithoutCancel(ctx)\n",
     "\tfinishCtx := ctx // CONTROL\n",
     [SHUTDOWN], RUNNER_SUITE, IMP,
     "THE DEFECT ITSELF, restored in ONE edit: every terminal write goes back through the "
     "cancellable context. PREDICTED CATCHER, stated before the run: SHUTDOWN reds at the "
     "`job.Status == JobRunning` assertion (\"a job interrupted by shutdown is recorded as "
     "%q\"), NOT at either premise check and NOT at the second-claim check. All three "
     "pre-existing runner tests must stay green — they drive RunOnce on a healthy context, "
     "which is why none of them could ever see this."),

    ("C2", RUNNER,
     "\tout, err := r.imp.run(ctx, job.WorkspaceID, job.TeamID, src)\n",
     "\tout, err := r.imp.run(finishCtx, job.WorkspaceID, job.TeamID, src) // CONTROL\n",
     [SHUTDOWN], RUNNER_SUITE, IMP,
     "⚠ THE OVER-FIX A REVIEWER WOULD ACCEPT: detach the IMPORT as well, not just the record. "
     "A dying process then keeps writing issues and the job reports itself clean. It earns the "
     "claim that only the RECORD is detached — an import must stop when the process is going "
     "down. PREDICTED CATCHER: SHUTDOWN reds at the `job.Status != JobFailed` assertion (\"no "
     "row landed, so the job must not report otherwise\"), having passed the JobRunning and "
     "finished_at checks."),

    ("C3", TEST,
     '\t"Interrupted Issue,a description,Todo,High,bug\\n"\n',
     '\t"" // CONTROL\n',
     [SHUTDOWN], RUNNER_SUITE, IMP,
     "⚠ THE CONTROL THAT EARNS THE FIRST PREMISE. Empty the fixture: the source yields no rows, "
     "Create is never reached, the context is never cancelled and the job finishes cleanly. "
     "Every assertion about the FINDING would pass on an import that was never interrupted. "
     "PREDICTED CATCHER: SHUTDOWN reds at `sc.calls == 0` (\"the fixture yielded no rows … this "
     "test measured an uninterrupted import\")."),

    ("C4", TEST,
     "\ts.cancel()\n",
     "\t_ = s.cancel // CONTROL — the process does NOT go down\n",
     [SHUTDOWN], RUNNER_SUITE, IMP,
     "⚠ THE CONTROL THAT EARNS THE SECOND PREMISE. Keep the fixture but remove the shutdown: "
     "Create runs, returns a live context's nil error, the row imports and the job succeeds. "
     "A terminal status written under a LIVE context proves nothing about the case this test "
     "exists for, and only the context-cancelled premise can say so. PREDICTED CATCHER: "
     "SHUTDOWN reds at `runCtx.Err() == nil` (\"the runner's context is still live — the "
     "shutdown this test exists for did not happen\")."),
]


def sha(path):
    return hashlib.sha256((ROOT / path).read_bytes()).hexdigest()


ASSERTION = re.compile(r"^\s+(\w+_test\.go:\d+):", re.M)


def run(targets, pkg):
    """Return (passed, output). passed is None for BUILD failure or a pattern that matched nothing."""
    cmd = ["go", "test", "-timeout", "300s", "-count=1"]
    if targets:
        cmd += ["-run", "^(" + "|".join(targets) + ")$"]
    cmd.append(pkg)
    p = subprocess.run(cmd, cwd=ROOT, capture_output=True, text=True)
    out = p.stdout + p.stderr
    # ⚠ A BUILD FAILURE IS NOT A CAUGHT MUTATION and must never be scored as one.
    if "build failed" in out or "cannot use" in out or "undefined:" in out or "declared and not used" in out:
        return None, out
    # ⚠ NO TESTS MATCHED IS NOT A PASS. `go test -run` exits 0 when the pattern matches nothing.
    if targets and "no tests to run" in out:
        return None, out
    return p.returncode == 0, out


def first_assertion(out):
    """The file:line of the first failing assertion — so a CAUGHT verdict names the sentence that
    spoke, rather than merely reporting that the test exited non-zero."""
    m = ASSERTION.search(out)
    return m.group(1) if m else "no assertion line"


def main():
    if not os.environ.get("TRACK_TEST_DATABASE_URL"):
        print("REFUSING TO RUN: TRACK_TEST_DATABASE_URL is unset. Every real-Postgres control "
              "would SKIP, go test would exit 0, and this script would report a clean sweep of "
              "controls that never ran.", file=sys.stderr)
        return 3

    files = sorted({c[1] for c in CONTROLS})
    before = {f: sha(f) for f in files}

    print("BASELINE — the suite must be green before any mutation means anything")
    ok, out = run([], IMP)
    if not ok:
        print("  BASELINE RED — stopping. A control campaign on a red tree proves nothing.")
        print(out[-3000:])
        return 2
    print("  baseline green\n")

    verdicts = {}
    for cid, path, anchor, repl, must_red, must_green, pkg, note in CONTROLS:
        p = ROOT / path
        src = p.read_text()
        n = src.count(anchor)
        if n != 1:
            verdicts[cid] = f"ANCHOR {n} != 1 — NOT RUN"
            print(f"{cid}  ANCHOR COUNT {n} != 1 in {path} — not run")
            continue
        p.write_text(src.replace(anchor, repl, 1))
        # ⚠ THE BYTES MUST HAVE CHANGED ON DISK. #83 lost a control whose edit never applied and
        # read the resulting green as a dead guard.
        if sha(path) == before[path]:
            p.write_text(src)
            verdicts[cid] = "EDIT DID NOT CHANGE THE FILE — NOT RUN"
            print(f"{cid}  edit left {path} byte-identical — not run")
            continue
        try:
            red_ok, red_detail = True, []
            for t in must_red:
                passed, o = run([t], pkg)
                if passed is None:
                    red_detail.append(f"{t}=BUILD/NOMATCH")
                    red_ok = False
                elif passed:
                    red_detail.append(f"{t}=STILL GREEN")
                    red_ok = False
                else:
                    red_detail.append(f"{t}=red@{first_assertion(o)}")

            green_ok, green_detail = True, []
            for t in must_green:
                passed, _ = run([t], pkg)
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

        restored = sha(path) == before[path]

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

    print("\nSUMMARY")
    for cid, v in verdicts.items():
        print(f"  {cid}: {v}")

    bad = [c for c, v in verdicts.items()
           if "NOT RESTORED" in v or v.startswith("NOT CAUGHT") or v.startswith("SUSPECT")
           or v.startswith("ANCHOR") or v.startswith("EDIT DID NOT") or v == "COMPANION WENT RED"]
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
