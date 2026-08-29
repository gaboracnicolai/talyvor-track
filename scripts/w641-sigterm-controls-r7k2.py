#!/usr/bin/env python3
"""W6.41 S1/S2 — does the handler actually restore the tree when the process is really killed?

`scripts/check-restore-signal-handlers.py` verifies a handler is INSTALLED. That is not the same as
verifying it WORKS, and six green scripts that were never killed would prove nothing. So for each
converted script this harness runs the pair that talyvor-suite's 5de27e3 established:

  S1  handler present  -> start the script for real, wait until it has ACTUALLY MUTATED a tracked
                          file, SIGTERM it, and require the tree to come back byte-clean.
  S2  handler removed  -> the identical run, and require the tree to be LEFT STRANDED.

S2 is the half that makes S1 mean something. Without it "the tree was clean after the kill" is also
what you get from a script that never mutated anything, or from a kill that arrived after the run
had already finished. S2 fails the whole harness if the mutation does NOT survive — a control that
cannot produce the bad outcome is not a control.

⚠ THE MUTATION IS DETECTED, NOT ASSUMED. The kill is only sent once `git status` reports a tracked
file outside scripts/ as modified, so the signal lands provably mid-mutation rather than at a
guessed moment. If no mutation appears inside the timeout the case is reported as a CONTROL DEFECT
and never scored.

⚠ THIS HARNESS MUTATES SCRIPTS ITSELF (S2 removes the handler installation), so it carries the same
two protections it is testing for: a `finally` and a signal handler. A control harness for this
defect that could strand the tree would be the joke that writes itself.
"""
from __future__ import annotations

import os
import pathlib
import signal
import subprocess
import sys
import time

REPO = pathlib.Path(__file__).resolve().parent.parent
SCRIPTS = REPO / "scripts"

TARGETS = [
    "w34-jira-contract-controls.py",
    "w34-linear-query-schema-controls.py",
    "w34-sprint-no-active-cycle-controls.py",
]

MUTATION_TIMEOUT = 600   # seconds to wait for the script to touch a tracked file
EXIT_TIMEOUT = 120       # seconds to wait for it to die after SIGTERM


def restore_on_signal(snapshot: dict) -> None:
    """Put every snapshotted file back, then die of the signal we were sent."""

    def handler(signum, _frame):
        for path, blob in snapshot.items():
            try:
                path.write_bytes(blob)
            except OSError:
                pass
        _revert_tree()
        sys.stderr.write("\n!! signal %d — restored %d script(s) and the tree before exiting\n"
                         % (signum, len(snapshot)))
        signal.signal(signum, signal.SIG_DFL)
        os.kill(os.getpid(), signum)

    for s in (signal.SIGTERM, signal.SIGINT, signal.SIGHUP):
        signal.signal(s, handler)


# Files already modified when this harness started. They are NOT this run's doing and must never be
# reverted by it. ⚠ THIS COST A SECOND RUN TO LEARN, AFTER THE SAME LESSON IN _revert_tree's own
# docstring: the first fix scoped the revert to "dirty outside scripts/", which still swept up an
# unrelated edit to .github/workflows/ci.yaml that was dirty before the harness ever ran. Detection
# already subtracted the baseline; the CLEANUP did not, and a cleanup that is broader than what it
# cleaned is the exact defect class this whole item is about.
BASELINE: set[str] = set()


def _revert_tree() -> None:
    """Revert ONLY the tracked files this harness's subjects mutate — never `git checkout -- .`.

    ⚠ THE BROAD FORM DESTROYED THIS HARNESS'S OWN SUBJECTS ON ITS FIRST RUN. `git checkout -- .`
    reverts every modified TRACKED file, and the six converted control scripts are exactly that
    until they are committed — so case 1 passed, wiped all six conversions, and cases 2-6 then ran
    against the UNCONVERTED scripts and reported five false strandings. A cleanup broader than the
    thing it is cleaning up is the same defect class this whole item is about, and it cost an entire
    run to notice because the false result looked like a finding.
    """
    paths = sorted(dirty_outside_scripts() - BASELINE)
    if paths:
        subprocess.run(["git", "checkout", "--"] + paths, cwd=REPO, capture_output=True)


def dirty_outside_scripts() -> set[str]:
    """Tracked files reported modified by git, excluding scripts/ (which this harness edits)."""
    out = subprocess.run(["git", "status", "--porcelain"], cwd=REPO,
                         capture_output=True, text=True).stdout
    names = set()
    for line in out.splitlines():
        if not line or line.startswith("??"):
            continue
        name = line[3:].strip()
        if not name.startswith("scripts/"):
            names.add(name)
    return names


def run_case(script: str, with_handler: bool, baseline: set[str]) -> tuple[str, str]:
    """Returns (outcome, detail) where outcome is 'restored' | 'stranded' | 'defect'."""
    path = SCRIPTS / script
    original = path.read_bytes()
    try:
        if not with_handler:
            src = original.decode()
            if "restore_on_signal(" not in src:
                return "defect", "no restore_on_signal( call to remove — S2 lands nowhere"
            # Comment out the INSTALLATION only. The function stays; nothing installs it.
            lines = src.splitlines(keepends=True)
            hit = False
            for i, ln in enumerate(lines):
                if ln.lstrip().startswith("restore_on_signal("):
                    lines[i] = ln.replace("restore_on_signal(", "pass  # restore_on_signal(", 1)
                    hit = True
                    break
            if not hit:
                return "defect", "restore_on_signal( appears but never as a statement"
            path.write_bytes("".join(lines).encode())

        proc = subprocess.Popen([sys.executable, str(path)], cwd=REPO,
                                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        deadline = time.time() + MUTATION_TIMEOUT
        saw = set()
        while time.time() < deadline:
            if proc.poll() is not None:
                return "defect", f"script exited (rc={proc.returncode}) before mutating anything"
            saw = dirty_outside_scripts() - baseline
            if saw:
                break
            time.sleep(0.2)
        else:
            proc.terminate()
            proc.wait(timeout=EXIT_TIMEOUT)
            return "defect", f"no tracked file was mutated within {MUTATION_TIMEOUT}s"

        proc.send_signal(signal.SIGTERM)
        try:
            proc.wait(timeout=EXIT_TIMEOUT)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()
            return "defect", "script did not exit after SIGTERM"
        time.sleep(0.4)  # let the OS settle the writes the handler made
        left = dirty_outside_scripts() - baseline
        detail = f"killed while {sorted(saw)} was modified; after exit: {sorted(left) or 'clean'}"
        return ("restored" if not left else "stranded"), detail
    finally:
        path.write_bytes(original)
        # Whatever the case left behind, the tree goes back. S2 strands ON PURPOSE — and the revert
        # is scoped to the mutated files, never `git checkout -- .`; see _revert_tree.
        _revert_tree()


def main() -> int:
    snapshot = {p: p.read_bytes() for p in SCRIPTS.glob("*.py")}
    restore_on_signal(snapshot)

    global BASELINE
    BASELINE = dirty_outside_scripts()
    baseline = BASELINE
    print(f"baseline modified-outside-scripts (never reverted by this harness): "
          f"{sorted(baseline) or 'clean'}\n")

    results = []
    for script in TARGETS:
        for label, with_handler, predicted in (("S1 handler present", True, "restored"),
                                               ("S2 handler removed", False, "stranded")):
            t0 = time.time()
            outcome, detail = run_case(script, with_handler, baseline)
            verdict = "OK" if outcome == predicted else (
                "CONTROL DEFECT" if outcome == "defect" else "!! UNEXPECTED")
            results.append((script, label, verdict, outcome, predicted, detail))
            print(f"  [{verdict:14s}] {script:34s} {label:19s} -> {outcome} "
                  f"(predicted {predicted}) [{time.time()-t0:.0f}s]")
            print(f"                   {detail}")

    bad = [r for r in results if r[2] != "OK"]
    print(f"\n{len(results)-len(bad)}/{len(results)} SIGTERM controls behaved as predicted")
    tail = subprocess.run(["git", "status", "--porcelain"], cwd=REPO,
                          capture_output=True, text=True).stdout.strip()
    print("git status at exit:\n" + (tail or "(clean)"))
    return 1 if bad else 0


sys.exit(main())
