#!/usr/bin/env python3
"""W3.4 / tab-7c15 — positive controls for the get_sprint_status nil-cycle fix.

Every control names the test that MUST catch it and the test that MUST STAY GREEN, both
predicted BEFORE the run. Verdicts are read from the PRINTED ASSERTION MESSAGE, never from a
pass/fail tally: a crash and a real catch look identical in a list of test names.

Rules this harness follows, each of them a lesson this queue paid for:
  · the anchor is asserted UNIQUE before any edit — a substitution matching nothing is
    byte-indistinguishable from a working guard (#71);
  · a BUILD FAILURE is scored NOT-A-CATCH — a compile error proves the code moved, not that the
    product was wrong;
  · files are restored from SAVED BYTES and sha256-compared, never from git checkout;
  · the run target is the WHOLE REPO, so "which tests spoke" is measured rather than assumed;
  · every control carries a MUST-STAY-GREEN companion, or a control that breaks everything
    reads as a caught mutation.
"""

import hashlib
import re
import shutil
import subprocess
import os
import signal
import sys
import tempfile
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
SERVER = REPO / "internal/mcp/server.go"
CYCLE_HANDLER = REPO / "internal/cycle/handler.go"
CYCLE_STORE = REPO / "internal/cycle/store.go"

# ⚠ THIS WAS A HARDCODED `localhost:55437`, AND NOTHING HAS LISTENED THERE SINCE THE SESSION THAT
# WROTE IT. `run_tests` builds a CLEAN env containing only this DSN, so the port could not be
# overridden from outside: on any other machine — or the same machine an hour later — the baseline
# mass-failed and the script exited 1 before applying a single control. Measured 2026-08-29 while
# giving this script its SIGTERM control (W6.41): `nc -z localhost 55437` refused, and the baseline
# reported hundreds of failures that are simply "no database". Established as PRE-EXISTING by
# reading the same line at HEAD.
#
# ⚠⚠ IT IS THE SAME FAMILY AS THE DRIFTED ANCHORS (W3.64/W3.65/W6.42) AND FAILS THE SAME WAY: a
# control that CANNOT RUN looks nothing like a control that ran, unless somebody runs it and reads
# the output. Nothing in CI runs this script either.
#
# The env var is the one CI already defines for the real-Postgres suite, so the script now works
# wherever the suite does. The old literal is kept as the fallback rather than deleted: it costs
# nothing and it is what a developer with that container still running would expect.
PG = os.environ.get("TRACK_TEST_DATABASE_URL",
                    "postgres://postgres:postgres@localhost:55437/postgres?sslmode=disable")

NO_ACTIVE = "TestSprintStatus_NoActiveCycle_AnswersInsteadOfPanicking"
ACTIVE = "TestSprintStatus_ActiveCycle_StillAnswers"
SEAM = "TestSeam_BothGetActiveConsumersHandleNil"


def sha(p: Path) -> str:
    return hashlib.sha256(p.read_bytes()).hexdigest()


def run_tests(only=None):
    """Run the repo and report which tests PASSED and which FAILED.

    ⚠ `-v` IS LOAD-BEARING, NOT NOISE. Without it `go test` prints `--- FAIL:` lines and NO
    `--- PASS:` lines at all, so "this test is absent from both lists" — the signal that tells a
    never-ran apart from a green — is unobtainable. The second version of this harness detected
    never-ran without -v and duly reported all six controls UNREADABLE, including one whose own
    output said "OBSERVED FAIL: none". Twice now the instrument, not the control, produced the
    verdict; both times only reading the raw output caught it.

    `only` restricts to a -run regex. Used ONLY to re-read tests a panic aborted before they ran
    (a Go test binary is per PACKAGE, so one panicking test takes every later test in its package
    down with it). The scoped result is reported as scoped, never merged into the whole-repo one.
    """
    env = {"TRACK_TEST_DATABASE_URL": PG, "PATH": "/usr/bin:/bin:/usr/local/bin:/opt/homebrew/bin", "HOME": str(Path.home())}
    cmd = ["go", "test", "-v", "-timeout", "300s", "-count=1"]
    if only:
        cmd += ["-run", only]
    cmd += ["./..."]
    r = subprocess.run(cmd, cwd=REPO, env=env, capture_output=True, text=True)
    out = r.stdout + r.stderr
    # A build/vet failure is NOT a catch. Score it explicitly.
    build_ok = "build failed" not in out and "[build failed]" not in out and "cannot use" not in out and "undefined:" not in out
    failed = sorted(set(re.findall(r"--- FAIL: (\S+)", out)))
    passed = sorted(set(re.findall(r"--- PASS: (\S+)", out)))
    panicked = sorted(set(re.findall(r"panic: (.+)", out)))
    return build_ok, failed, passed, panicked, out


def messages_for(out: str, test: str, limit=3):
    """Pull the printed assertion messages for one test, so the verdict is READ, not inferred."""
    msgs = []
    for line in out.splitlines():
        s = line.strip()
        if s.startswith(("sprint_no_active_cycle_test.go:",)) and len(msgs) < limit:
            msgs.append(s)
    return msgs


CONTROLS = [
    dict(
        name="C1 revert the fix (the RED-FIRST run)",
        file=SERVER,
        old="if err != nil || active == nil {",
        new="if err != nil {",
        must_catch=[NO_ACTIVE],
        must_stay_green=[ACTIVE, SEAM],
        note="the defect itself. The SEAM test staying green is the load-bearing half: it proves "
             "the HTTP copy is independent of the MCP copy, which is what makes this a defect "
             "rather than an undecided contract.",
    ),
    dict(
        name="C2 answer active:false unconditionally",
        file=SERVER,
        old="if err != nil || active == nil {",
        new="if true {",
        must_catch=[ACTIVE],
        must_stay_green=[NO_ACTIVE, SEAM],
        note="THE CONTROL THAT JUSTIFIES THE MUST-STAY-GREEN COMPANION. Without it, a 'fix' that "
             "never reports a sprint at all would pass the no-sprint test.",
    ),
    dict(
        name="C3 break the OTHER copy of the seam (HTTP)",
        file=CYCLE_HANDLER,
        old='\tif out == nil {\n\t\twriteJSON(w, http.StatusNotFound, apiError{Error: "no active cycle", Code: "NO_ACTIVE_CYCLE"})\n\t\treturn\n\t}\n',
        new="",
        must_catch=[SEAM],
        must_stay_green=[NO_ACTIVE, ACTIVE],
        note="THE MUTATION ONLY ONE GUARD CAN SEE. Both MCP tests stay green because they never "
             "touch the HTTP handler — so the seam test is measured non-redundant rather than "
             "argued to be.",
    ),
    dict(
        name="C4 let the no-sprint answer carry sprint figures",
        file=SERVER,
        old='\t\treturn map[string]any{\n\t\t\t"active":  false,\n\t\t\t"team_id": in.TeamID,\n\t\t}, nil',
        new='\t\treturn map[string]any{\n\t\t\t"active":  false,\n\t\t\t"team_id": in.TeamID,\n\t\t\t"ai_cost_usd": 0.0,\n\t\t}, nil',
        must_catch=[NO_ACTIVE],
        must_stay_green=[ACTIVE, SEAM],
        note="behaviourally SUBTLE and deliberately so: the tool still answers, still says "
             "active:false, and never panics. Only the 'must not carry sprint figures' loop can "
             "see it. A $0.00 beside active:false is a measured zero that was never measured.",
    ),
    dict(
        name="C5 blind the premise (GetActive errors instead of (nil,nil))",
        file=CYCLE_STORE,
        old="\t\tif errors.Is(err, pgx.ErrNoRows) {\n\t\t\treturn nil, nil\n\t\t}\n\t\treturn nil, err",
        new="\t\treturn nil, err",
        must_catch=[NO_ACTIVE, SEAM],
        must_stay_green=[ACTIVE],
        note="⚠ THE CONTROL THE PREMISE ASSERTION EXISTS FOR. With GetActive erroring, the MCP "
             "tool takes the no-sprint branch for the WRONG REASON and every payload assertion "
             "would PASS. Only the explicit premise check turns that into a red. The HTTP twin "
             "goes 500 instead of 404 and the seam test says so.",
    ),
    dict(
        name="C6 INVERTED — same predicate, different spelling",
        file=SERVER,
        old="if err != nil || active == nil {",
        new="if active == nil || err != nil {",
        must_catch=[],
        must_stay_green=[NO_ACTIVE, ACTIVE, SEAM],
        note="the guards pin BEHAVIOUR, not bytes. All three must stay green.",
    ),
]


def restore_on_signal(snapshot):
    """Put every snapshotted file back, then die of the signal we were sent.

    A `finally` DOES NOT RUN ON SIGTERM. talyvor-suite W1.7 (78c69c8) lost a shell gate to exactly
    this — a 2-minute command timeout killed a control mid-mutation, with a green suite and a
    `git status` showing only files the session had edited on purpose. Re-raising with SIG_DFL keeps
    the exit status honest. SIGKILL still strands and nothing in Python can change that.

    Deliberately pasted rather than imported: scripts/check-restore-signal-handlers.py detects the
    handler in this file's OWN ast, and an import is invisible to it.
    """

    def handler(signum, _frame):
        for path, blob in snapshot.items():
            try:
                path.write_bytes(blob)
            except OSError:
                pass
        sys.stderr.write("\n!! signal %d — restored %d mutated file(s) before exiting\n"
                         % (signum, len(snapshot)))
        signal.signal(signum, signal.SIG_DFL)
        os.kill(os.getpid(), signum)

    for s in (signal.SIGTERM, signal.SIGINT, signal.SIGHUP):
        signal.signal(s, handler)


def main():
    saved = {}
    for p in (SERVER, CYCLE_HANDLER, CYCLE_STORE):
        saved[p] = p.read_bytes()
    restore_on_signal(dict(saved))
    backup_dir = Path(tempfile.mkdtemp(prefix="w34-controls-"))
    for p, b in saved.items():
        (backup_dir / p.name).write_bytes(b)

    print("=" * 100)
    print("BASELINE — whole repo, unmutated")
    ok, failed, passed, panicked, out = run_tests()
    print(f"  build_ok={ok}  failures={failed or 'none'}  panics={panicked or 'none'}  tests_run={len(passed)+len(failed)}")
    if failed or panicked or not ok:
        print("  BASELINE IS NOT CLEAN — every verdict below would be unreadable. Stopping.")
        return 1
    baseline_sha = {p: sha(p) for p in saved}

    results = []
    for c in CONTROLS:
        p: Path = c["file"]
        print("=" * 100)
        print(f"{c['name']}   [{p.relative_to(REPO)}]")
        print(f"  PREDICT CATCH      : {c['must_catch'] or '(none — inverted control)'}")
        print(f"  PREDICT STAY GREEN : {c['must_stay_green']}")

        src = p.read_text()
        n = src.count(c["old"])
        if n != 1:
            print(f"  ✗ ANCHOR COUNT = {n}, want exactly 1 — control NOT APPLIED, scored as INVALID")
            results.append((c["name"], "INVALID-ANCHOR"))
            continue
        try:
            p.write_text(src.replace(c["old"], c["new"], 1))
            ok, failed, passed, panicked, out = run_tests()
        finally:
        # ⚠ THIS `finally` IS THE OTHER HALF AND IT IS NOT REDUNDANT WITH THE HANDLER.
        # The handler covers a KILL; this covers an EXCEPTION — the guard subprocess
        # blowing up, a KeyboardInterrupt inside it. Before this, the restore ran only
        # on the happy path, which strands the tree on any error and is invisible to a
        # population keyed on `finally`.
            for pth, b in saved.items():
                pth.write_bytes(b)
        assert sha(p) == baseline_sha[p], "restore failed — bytes differ from baseline"

        if not ok:
            print("  ✗ BUILD FAILED — scored NOT A CATCH (a compile error proves the code moved, "
                  "not that the product was wrong)")
            results.append((c["name"], "BUILD-FAIL / NOT A CATCH"))
            continue

        spoke = set(failed)
        ran = set(failed) | set(passed)
        print(f"  OBSERVED FAIL : {sorted(spoke) or 'none'}")
        if panicked:
            print(f"  OBSERVED PANIC: {panicked[0][:90]}")
        for m in messages_for(out, NO_ACTIVE):
            print(f"  ASSERTION MSG : {m[:150]}")

        # ⚠ A PANIC ABORTS THE TEST BINARY, so every test ordered after it NEVER RAN — and a test
        # that never ran is absent from BOTH the PASS and the FAIL list. Reading that absence as
        # "stayed green" is how a control harness reports a guard it never exercised. The first
        # version of this script did exactly that: it attributed any panic to one fixed test name
        # and scored C2 "PREDICTION WRONG" on evidence it had invented. Never-ran is now its own
        # verdict, and it is UNREADABLE rather than pass or fail.
        never_ran = [t for t in (c["must_catch"] + c["must_stay_green"]) if t not in ran]
        scoped_note = ""
        if never_ran:
            # Re-apply and re-read ONLY the predicted tests, so a pre-existing panicking test in
            # the same package cannot hide them. Reported as scoped, and the whole-repo failure
            # list above still stands as the record of who else spoke.
            print(f"  ⚠ NEVER RAN   : {never_ran} — a panic aborted the package binary first")
            src2 = p.read_text()
            assert src2.count(c["old"]) == 1
            p.write_text(src2.replace(c["old"], c["new"], 1))
            # ⚠ SCOPE THE RE-RUN TO THE TESTS THAT NEVER RAN, NOT TO ALL PREDICTED ONES. When the
            # catcher is ITSELF the panicking test (C1: the defect is a nil dereference), including
            # it re-aborts the binary and the companions are unreadable a second time. The catcher
            # already spoke in the whole-repo run above; what is missing is only the companions.
            only = "^(" + "|".join(never_ran) + ")$"
            _, sfailed, spassed, spanicked, sout = run_tests(only=only)
            for pth, b in saved.items():
                pth.write_bytes(b)
            assert sha(p) == baseline_sha[p], "restore failed after scoped re-run"
            # ⚠ UNION WITH THE WHOLE-REPO RUN. The scoped re-run deliberately EXCLUDES tests that
            # already ran, so checking the predicted set against the scoped result alone reports
            # the catcher — the one test that definitely spoke — as "never ran".
            sran = set(sfailed) | set(spassed) | ran
            still = [t for t in (c["must_catch"] + c["must_stay_green"]) if t not in sran]
            if still:
                print(f"  ✗ STILL NEVER RAN even scoped: {still} — UNREADABLE")
                results.append((c["name"], "UNREADABLE"))
                continue
            print(f"  SCOPED RE-RUN : FAIL={sorted(sfailed) or 'none'}  (excludes the pre-existing panicking test)")
            for m in messages_for(sout, NO_ACTIVE):
                print(f"  SCOPED MSG    : {m[:150]}")
            # Union: the whole-repo run is the record for tests that DID run, the scoped run
            # supplies only the ones it aborted before.
            spoke = set(failed) | set(sfailed)
            scoped_note = " [companions read from a SCOPED re-run]"

        caught = all(t in spoke for t in c["must_catch"])
        green = all(t not in spoke for t in c["must_stay_green"])
        extra = spoke - set(c["must_catch"])
        verdict = ("AS PREDICTED" if (caught and green) else "PREDICTION WRONG") + scoped_note
        if extra and c["must_catch"]:
            verdict += f"  (+unpredicted, and each is a guard that ALSO speaks: {sorted(extra)})"
        elif extra:
            verdict += f"  (+unpredicted: {sorted(extra)})"
        print(f"  VERDICT       : {verdict}")
        print(f"  WHY           : {c['note']}")
        results.append((c["name"], verdict))

    print("=" * 100)
    for name, v in results:
        print(f"  {v:20s}  {name}")
    shutil.rmtree(backup_dir, ignore_errors=True)
    return 0 if all("AS PREDICTED" in v for _, v in results) else 1


if __name__ == "__main__":
    sys.exit(main())
