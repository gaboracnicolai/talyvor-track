#!/usr/bin/env python3
"""Does anything in the repository notice when a report STOPS CLAMPING its window?

`clampDays` has a unit test (`TestClampDays_BoundsRespected`, engine_test.go) and
#154's own header records D7 — "clampDays' ceiling" — as CAUGHT by that test and by
nothing else.  That test calls the FUNCTION.  It cannot see whether any report still
CALLS it.  This script asks the wiring question instead, one call site at a time.

METHOD, and the two rules this harness family keeps being caught by:

  * membership in "CAUGHT" is decided by SET SUBTRACTION against C0's own measured
    FAIL set — never by an exit code, never by a test's NAME.  `./internal/importer/`
    fails on a pristine tree on any machine holding the empty /tmp corpus dirs, so an
    exit-code harness would score every mutation CAUGHT and report the guard as
    already present.
  * every mutation is applied to a line ADDRESSED BY NUMBER (the three clamp call
    sites are byte-identical), restored in a `finally`, and the restore is verified by
    sha256 against the pre-run digest.

Scope is the whole call closure of the clamped reports — the three packages that name
GetDistribution / GetTimeToResolution / GetAICostTrends / GetVelocity anywhere:
internal/analytics, internal/importer, internal/mcp.

Run:  TRACK_TEST_DATABASE_URL=... python3 scripts/w34-window-clamp-wiring-controls-6c1a.py
"""

import hashlib
import os
import re
import subprocess
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
ENGINE = os.path.join(REPO, "internal", "analytics", "engine.go")
SCOPE = ["./internal/analytics/", "./internal/importer/", "./internal/mcp/"]

FAIL_RE = re.compile(r"^\s*--- FAIL: (\S+)", re.M)


def digest(path):
    with open(path, "rb") as fh:
        return hashlib.sha256(fh.read()).hexdigest()


def read_lines():
    with open(ENGINE, "r", encoding="utf-8") as fh:
        return fh.read().split("\n")


def write_lines(lines):
    with open(ENGINE, "w", encoding="utf-8") as fh:
        fh.write("\n".join(lines))


def fail_set():
    """The set of failing test names, measured. Compile errors are surfaced loudly:
    a mutation that does not build is not a measurement, and scoring it CAUGHT would
    credit the guard for the compiler's work."""
    proc = subprocess.run(
        ["go", "test", "-timeout", "600s", "-count=1"] + SCOPE,
        cwd=REPO, capture_output=True, text=True,
    )
    out = proc.stdout + proc.stderr
    if "[build failed]" in out or "cannot use" in out or "undefined:" in out:
        return None, out
    return set(FAIL_RE.findall(out)), out


def locate(needle, expect_count):
    """Line indices (0-based) of an exact-stripped source line. The count is asserted
    so a refactor that moves or duplicates a site fails the harness rather than
    silently measuring a different line."""
    lines = read_lines()
    hits = [i for i, ln in enumerate(lines) if ln.strip() == needle]
    if len(hits) != expect_count:
        raise SystemExit(
            "ANCHOR MOVED: %r found %d times, expected %d — refusing to measure"
            % (needle, len(hits), expect_count)
        )
    return hits


def main():
    if not os.environ.get("TRACK_TEST_DATABASE_URL"):
        raise SystemExit("TRACK_TEST_DATABASE_URL is required — the real-PG tests must RUN")

    before = digest(ENGINE)
    clamp_sites = locate("days = clampDays(days)", 3)
    print("clampDays call sites at engine.go lines: %s"
          % ", ".join(str(i + 1) for i in clamp_sites))

    # Mutations. Each is (id, prediction, description, mutate(lines) -> lines).
    def drop_line(idx):
        def m(lines):
            out = list(lines)
            out[idx] = "\t// MUTATED: clamp call removed"
            return out
        return m

    def replace_line(idx, text):
        def m(lines):
            out = list(lines)
            out[idx] = text
            return out
        return m

    vel = locate("if cycles > 50 {", 1)[0]
    velfloor = locate("if cycles <= 0 {", 1)[0]
    ceiling = locate("if days > maxWindowDays {", 1)[0]

    # (id, PRE-MERGE verdict — MEASURED on 1fa10d0 before window_clamp_wiring_realpg_test.go
    #  existed, recorded as fact not as a prediction, REQUIRED verdict now, description, mutate)
    muts = [
        ("M1", "NOT CAUGHT", "CAUGHT", "GetDistribution stops clamping its window",
         drop_line(clamp_sites[0])),
        ("M2", "CAUGHT", "CAUGHT", "GetTimeToResolution stops clamping its window",
         drop_line(clamp_sites[1])),
        ("M3", "NOT CAUGHT", "CAUGHT", "GetAICostTrends stops clamping its window",
         drop_line(clamp_sites[2])),
        ("M4", "NOT CAUGHT", "CAUGHT", "GetVelocity loses its 50-cycle ceiling",
         replace_line(vel, "\tif cycles > 50000 {")),
        ("M5", "NOT CAUGHT", "CAUGHT", "GetVelocity loses its non-positive floor",
         replace_line(velfloor, "\tif cycles < -1 {")),
        ("P1", "CAUGHT", "CAUGHT", "POSITIVE CONTROL: clampDays' own ceiling moved to 100000",
         replace_line(ceiling, "\tif days > 100000 {")),
        ("V0", "NOT CAUGHT", "NOT CAUGHT",
         "VOID CONTROL: the distribution clamp applied TWICE (idempotent)",
         replace_line(clamp_sites[0], "\tdays = clampDays(clampDays(days))")),
    ]

    pristine = read_lines()
    try:
        base, out = fail_set()
        if base is None:
            raise SystemExit("C0 DID NOT BUILD:\n" + out[-4000:])
        print("C0 baseline: %d failing tests on the pristine tree" % len(base))
        for name in sorted(base):
            print("      pre-existing FAIL: %s" % name)
        print()

        results = []
        for mid, pre, want, desc, mutate in muts:
            write_lines(mutate(pristine))
            got, out = fail_set()
            if got is None:
                verdict = "DID NOT BUILD"
                new = set()
            else:
                new = got - base
                verdict = "CAUGHT" if new else "NOT CAUGHT"
            results.append((mid, pre, want, desc, verdict, sorted(new)))
            print("%-3s %-11s %s" % (mid, verdict, desc))
            for n in sorted(new)[:6]:
                print("        by: %s" % n)
            write_lines(pristine)
    finally:
        write_lines(pristine)
        after = digest(ENGINE)
        print("\nrestore: sha256 %s -> %s  %s"
              % (before[:12], after[:12], "OK" if before == after else "MISMATCH"))
        if before != after:
            raise SystemExit("RESTORE FAILED — the tree is dirty, fix before anything else")

    print("\n%-3s %-11s %-11s %-11s %s"
          % ("ID", "PRE-MERGE", "REQUIRED", "MEASURED", "OK"))
    bad = 0
    for mid, pre, want, desc, verdict, _ in results:
        ok = "yes" if want == verdict else "NO"
        if ok == "NO":
            bad += 1
        print("%-3s %-11s %-11s %-11s %s" % (mid, pre, want, verdict, ok))

    # Self-check. A harness whose positive control does not fire has measured nothing,
    # and a harness whose VOID control fires is scoring noise.
    p1 = [r for r in results if r[0] == "P1"][0][4]
    v0 = [r for r in results if r[0] == "V0"][0][4]
    if p1 != "CAUGHT":
        raise SystemExit("SELF-CHECK FAILED: the positive control was not caught — "
                         "this harness cannot distinguish a guard from its absence")
    if v0 != "NOT CAUGHT":
        raise SystemExit("SELF-CHECK FAILED: the VOID control fired — the score is noise")
    print("\nself-check: P1 CAUGHT and V0 NOT CAUGHT — the instrument separates the two.")
    if bad:
        raise SystemExit(
            "%d mutation(s) did not meet the REQUIRED verdict. The PRE-MERGE column is what was "
            "measured on 1fa10d0; REQUIRED is what window_clamp_wiring_realpg_test.go exists to "
            "make true. A NO here means a clamp call site is unguarded again." % bad)
    return 0


if __name__ == "__main__":
    sys.exit(main())
