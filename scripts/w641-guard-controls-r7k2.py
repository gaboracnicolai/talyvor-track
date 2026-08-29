#!/usr/bin/env python3
"""W6.41 — controls for the WIDENED check-restore-signal-handlers.py in talyvor-track.

This repo's guard carried the original `finally`-only definition. It has been replaced with the
corrected one (indirect restore through a helper, context manager, `finally` that shells out to
`git checkout`), which moved the recognised population from ~63 to 95.

⚠ THE WIDENING IS THE THING UNDER TEST, NOT THE RULES. G1-G3 each blind ONE of the three idioms the
old detector could not see, and require the guard to notice — because a widened detector that
quietly narrows again is exactly how this repo ended up with a 60-entry allowlist over a population
of 95. G4/G5 are the per-detector floors: a SINGLE floor over the union is satisfied by either
detector alone, which was measured as a real defect in talyvor-code #75.

⚠ G8 IS INVERTED: `signal.signal(...)` written into a COMMENT must NOT count as a handler. That is
the whole argument for `ast` over grep.
⚠ G9 IS INVERTED THE OTHER WAY: a correctly converted script must NOT red.

Mutates tracked files, restores them in a `finally` with sha256 compared, and installs a signal
handler — a `finally` does not run on SIGTERM, which is what this guard is about.
"""
import hashlib
import os
import pathlib
import signal
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
SCRIPTS = ROOT / "scripts"
GUARD = SCRIPTS / "check-restore-signal-handlers.py"
PROBE = SCRIPTS / "w641-probe-generated.py"


def restore_on_signal(snapshot):
    """Put every snapshotted file back, then die of the signal we were sent."""

    def handler(signum, _frame):
        for path, blob in snapshot.items():
            try:
                path.write_bytes(blob)
            except OSError:
                pass
        PROBE.unlink(missing_ok=True)
        sys.stderr.write("\n!! signal %d — restored %d file(s) before exiting\n" % (signum, len(snapshot)))
        signal.signal(signum, signal.SIG_DFL)
        os.kill(os.getpid(), signum)

    for s in (signal.SIGTERM, signal.SIGINT, signal.SIGHUP):
        signal.signal(s, handler)


def sha(p):
    return hashlib.sha256(p.read_bytes()).hexdigest()


def run_guard():
    r = subprocess.run([sys.executable, str(GUARD)], cwd=ROOT, capture_output=True, text=True)
    return r.returncode == 0, r.stdout + r.stderr


# Probe scripts, each in one of the shapes the OLD detector was blind to. Every one of these
# restores safely and installs NO handler, so a detector that sees it must red with R1; a detector
# blind to the idiom sees "no `finally`" and reds with R6 instead — a different rule, which is why
# the verdict below checks WHICH rule fired, not merely that something did.
INDIRECT = '''import pathlib
p = pathlib.Path("x")
original = ""
def restore():
    p.write_text(original)
try:
    original = p.read_text()
    p.write_text("mutated")
finally:
    restore()
'''

CONTEXT_MANAGER = '''import pathlib
class Keeper:
    def __enter__(self):
        self.p = pathlib.Path("x")
        self.orig = self.p.read_text()
        return self
    def __exit__(self, *exc):
        self.p.write_text(self.orig)
with Keeper() as k:
    k.p.write_text("mutated")
'''

GIT_FINALLY = '''import pathlib, subprocess
p = pathlib.Path("x")
try:
    original = p.read_text()
    p.write_text("mutated")
finally:
    subprocess.run(["git", "checkout", "--", "x"])
'''

COMMENTED_HANDLER = '''import pathlib
# Protected: this script calls signal.signal(signal.SIGTERM, handler) at startup, and
# signal.signal(signal.SIGINT, handler) too, so a signal cannot strand the tree.
p = pathlib.Path("x")
try:
    original = p.read_text()
    p.write_text("mutated")
finally:
    p.write_text(original)
'''

GOOD = '''import pathlib, signal
signal.signal(signal.SIGTERM, lambda *a: None)
p = pathlib.Path("x")
try:
    original = p.read_text()
    p.write_text("mutated")
finally:
    p.write_text(original)
'''

CONTROLS = [
    ("G1 WIDENING: an INDIRECT restore (`finally: restore()`)", "probe", INDIRECT, "R1", True,
     "the idiom that made this repo's old allowlist look 29 entries shorter than the truth"),
    ("G2 WIDENING: a CONTEXT MANAGER restore", "probe", CONTEXT_MANAGER, "R1", True,
     "`with` is what a `finally` is for; a detector blind to it reports no `try` at all"),
    ("G3 WIDENING: a `finally` that shells out to `git checkout`", "probe", GIT_FINALLY, "R1", True,
     "not a Python write, so the `finally` looked empty to the old walk"),
    ("G4 VACUITY: the narrow detector blinded", "guard",
     ("def _restores_in_finally(tree: ast.AST) -> bool:\n    if _restores_via_context_manager(tree):",
      "def _restores_in_finally(tree: ast.AST) -> bool:\n    return False\n    if _restores_via_context_manager(tree):"),
     "R3", True, "a per-detector floor, because a union floor is satisfied by the other one alone"),
    ("G5 VACUITY: the wide detector blinded", "guard",
     ("    return reads and any(_write_call(n) for n in ast.walk(tree))",
      "    return False"),
     "R3b", True, "the same, for the half the old guard did not have at all"),
    # ⚠ G6'S FIRST DRAFT WAS VOID AND THE HARNESS SAID SO RATHER THAN SCORING IT. It tried to
    # REMOVE "w34-jira-contract-controls.py" from UNPROTECTED — but that script was converted in
    # this same merge, so it is PROTECTED and was never in the list; the needle occurred 0x and the
    # probe landed nowhere. R2's condition is a script that IS protected and IS still listed, so
    # the probe now ADDS one. A mutation that changes nothing accuses a correct subject.
    ("G6 a fixed script left listed in UNPROTECTED", "guard",
     ("UNPROTECTED = {\n", 'UNPROTECTED = {\n    "w34-jira-contract-controls.py",\n'),
     "R2", True, "an entry that has been fixed must leave the list, or the list rots into an excuse"),
    ("G7 NOT_MUTATORS entry with no reason", "guard",
     ('    "w34-jira-contract-snapshot.py":\n', '    "w34-jira-contract-snapshot.py": "",\n    "_unused":\n'),
     "R7", True, "an unexplained exemption is how the wide net gets quietly narrowed back"),
    ("G8 INVERTED: signal.signal in a COMMENT is not a handler", "probe",
     COMMENTED_HANDLER, "R1", True, "a grep reads documentation as implementation; ast does not"),
    ("G9 INVERTED: a correctly converted script must NOT red", "probe", GOOD, None, False,
     "a guard that reds on correct code gets relaxed until it reds on nothing"),
]


def main():
    originals = {GUARD: GUARD.read_bytes()}
    hashes = {p: sha(p) for p in originals}
    restore_on_signal(originals)
    PROBE.unlink(missing_ok=True)

    ok, out = run_guard()
    if not ok:
        print("BASELINE IS NOT GREEN — every verdict below would be unreadable:\n" + out)
        return 2
    print("baseline: GREEN\n")

    results = []
    try:
        for name, kind, payload, rule, predict_red, why in CONTROLS:
            try:
                if kind == "probe":
                    PROBE.write_text(payload, encoding="utf-8")
                else:
                    find, repl = payload
                    src = originals[GUARD].decode()
                    if src.count(find) != 1:
                        results.append((name, "CONTROL DEFECT",
                                        f"needle occurs {src.count(find)}x, want 1 — probe lands nowhere"))
                        print(f"  [CONTROL DEFECT] {name}")
                        continue
                    GUARD.write_text(src.replace(find, repl, 1), encoding="utf-8")
                passed, out = run_guard()
                red = not passed
                named = (rule is None) or any(l.strip().startswith(rule + ":") for l in out.splitlines())
                behaved = (red == predict_red) and (not red or named)
                verdict = ("OK" if behaved else
                           "!! BLIND" if not red else "!! WRONG RULE")
                detail = ("RED" if red else "green") + (f" via {rule}" if red and named else "")
                results.append((name, verdict,
                                f"{detail} (predicted {'RED' if predict_red else 'green'}) — {why}"))
                print(f"  [{verdict:14s}] {name}: {detail}")
            finally:
                PROBE.unlink(missing_ok=True)
                GUARD.write_bytes(originals[GUARD])
    finally:
        for p, b in originals.items():
            p.write_bytes(b)
        PROBE.unlink(missing_ok=True)
        bad = [p.name for p in originals if sha(p) != hashes[p]]
        print("\nrestore: " + ("BYTE-IDENTICAL" if not bad else "MISMATCH " + ",".join(bad)))

    ok, out = run_guard()
    print("post-control baseline: " + ("GREEN" if ok else "RED\n" + out))
    defects = [r for r in results if r[1] != "OK"]
    print(f"\n{len(results)-len(defects)}/{len(results)} controls behaved as predicted")
    for n, v, d in results:
        print(f"  [{v}] {n}: {d}")
    return 1 if defects or not ok else 0


sys.exit(main())
