#!/usr/bin/env python3
"""W6.34 control campaign — the toolchain security floor."""
import hashlib, os, re, signal, subprocess, sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
GOMOD = os.path.join(ROOT, "go.mod")
TEST = os.path.join(ROOT, "internal/migrate/toolchain_pin_test.go")
CI   = os.path.join(ROOT, ".github/workflows/ci.yaml")
FILES = [GOMOD, TEST, CI]

def sha(p): return hashlib.sha256(open(p, "rb").read()).hexdigest()

def run(test):
    r = subprocess.run(["go", "test", "-count=1", "-run", "^%s$" % test, "./internal/migrate/"],
                       cwd=ROOT, capture_output=True, text=True)
    return r.returncode == 0, r.stdout + r.stderr

class JobPin:
    """A ci.yaml `go-version` pin anchored by the JOB IT BELONGS TO, not by the literal.

    ⚠ WHY THIS EXISTS RATHER THAN A COUNT. V7 anchored on the bare `go-version: "1.26.6"` line and
    declared it should occur TWICE. It occurs three times now — ci.yaml grew a third job — so
    `anchored()` refused and this campaign printed `7/8 controls CAUGHT` with one arm silently
    unable to run. talyvor-docs W3.64 was the identical defect with different numbers.

    ⚠⚠ RAISING THE COUNT TO 3 IS NOT THE FIX AND NEITHER IS AN OCCURRENCE INDEX. A count mutates
    the FIRST pin and asserts only how many exist, so it never says WHICH job it dropped — and the
    three jobs are three different claims (lint's pin decides whether golangci-lint will start at
    all, vuln's decides what govulncheck grades the stdlib against, test's decides the runtime the
    suite runs on). An INDEX is worse in the quiet direction: reorder the jobs and it still applies
    cleanly, to the wrong one.

    ⚠⚠⚠ AND A TEXT ANCHOR ON THE SURROUNDING LINES DOES NOT WORK HERE, WHICH IS WHY THIS IS
    STRUCTURAL. The `lint` and `test` pins are BYTE-IDENTICAL for six lines in both directions —
    same comment, same `cache: true` — so the only thing that separates them is the job they sit
    in. Slicing by job header is the smallest anchor that actually distinguishes them, and if a
    job is renamed or grows a second pin this raises rather than mutating something arbitrary.
    """

    _PIN = re.compile(r'(?m)^(\s*go-version:\s*)"[^"]+"$')

    def __init__(self, job, new_version):
        self.job, self.new_version = job, new_version

    def __repr__(self):
        return "<go-version pin in job %r>" % self.job

    def _span(self, text):
        m = re.search(r"(?m)^  %s:\n" % re.escape(self.job), text)
        if not m:
            raise AssertionError("job %r not found in ci.yaml" % self.job)
        nxt = re.search(r"(?m)^  [A-Za-z0-9_-]+:\n", text[m.end():])
        return m.start(), (m.end() + nxt.start()) if nxt else len(text)

    def count_in(self, text):
        """How many go-version pins the job has. The expected answer is always exactly 1."""
        a, b = self._span(text)
        return len(self._PIN.findall(text[a:b]))

    def apply(self, text):
        a, b = self._span(text)
        block = text[a:b]
        hits = list(self._PIN.finditer(block))
        if len(hits) != 1:
            raise AssertionError("job %r has %d go-version pins, want 1" % (self.job, len(hits)))
        h = hits[0]
        return text[:a] + block[:h.start()] + h.group(1) + '"%s"' % self.new_version + block[h.end():] + text[b:]


def anchored(path, old, new, count=1):
    """Apply one control's mutation, refusing rather than guessing if the anchor is not what the
    control declared.

    `old` is either a literal (expected to occur exactly `count` times) or a JobPin, which anchors
    structurally — see that class for why the pin controls needed it.
    """
    s = open(path).read()
    if isinstance(old, JobPin):
        n = old.count_in(s)
        if n != count:
            raise AssertionError("job %r has %d go-version pin(s), want %d" % (old.job, n, count))
        open(path, "w").write(old.apply(s))
        return
    n = s.count(old)
    if n != count:
        raise AssertionError("anchor appears %d times, want %d: %r" % (n, count, old[:60]))
    open(path, "w").write(s.replace(old, new, 1))

PIN = "TestGoModPinsTheToolchainAtOrAboveTheSecurityFloor"
WHY = "TestTheToolchainPinCarriesItsReason"
RUN = "TestTheRunningToolchainActuallyHonoursThePin"
LCK = "TestCIGoVersionPinsAreAtLeastTheToolchainFloor"

CONTROLS = [
    ("V1 the toolchain directive deleted", GOMOD,
     "\ntoolchain go1.26.6\n", "\n",
     PIN, WHY, "removing the pin silently restores eight reachable stdlib advisories"),

    ("V2 the pin lowered below the floor", GOMOD,
     "toolchain go1.26.6", "toolchain go1.26.5",
     PIN, WHY, "1.26.5 leaves GO-2026-{6218,6091,6090,6089,6088,5972,5026} reachable"),

    # V3's first version set `toolchain go1.27.1` in go.mod. That release does not exist, so the go
    # command could not resolve a toolchain and NOTHING RAN — reported MISSED, which is correct: a
    # control whose build fails proves nothing. Lowering the TEST's floor demonstrates the same
    # property (>= not ==) against a toolchain that actually exists.
    ("V3 the floor lowered below the real pin", TEST,
     "const wantMajor, wantMinor, wantPatch = 1, 26, 6",
     "const wantMajor, wantMinor, wantPatch = 1, 25, 0",
     None, PIN, "a pin ABOVE the floor must PASS — it is a floor, not an equality check"),

    ("V4 the rationale stripped", GOMOD,
     "GO-2026-6218, GO-2026-6091, GO-2026-6090, GO-2026-6089,\n// GO-2026-6088, GO-2026-5972 and GO-2026-5026;",
     "(rationale removed);",
     WHY, PIN, "a security floor with no advisory ids reads as a preference"),

    ("V5 the version regex stops matching", TEST,
     'regexp.MustCompile(`(?m)^toolchain go(\\d+)\\.(\\d+)\\.(\\d+)$`)',
     'regexp.MustCompile(`(?m)^NEVERMATCH go(\\d+)\\.(\\d+)\\.(\\d+)$`)',
     PIN, None, "a parse that finds nothing must fail loudly, not report a pinned repo"),

    # V7 is the control for the failure CI actually produced: ci.yaml below go.mod's floor makes
    # golangci-lint refuse to start and the tests run a runtime the release is not built with.
    # It was ONE control expecting TWO occurrences of a literal; ci.yaml has THREE jobs pinning Go
    # and the control had been unable to run. Split per job — see JobPin for why not a count and
    # why not an index. The guard was never the problem: each pin was lowered ALONE against the
    # unmodified guard and TestCIGoVersionPinsAreAtLeastTheToolchainFloor reds on all three, at
    # toolchain_pin_test.go:200. Measured before this was written, not assumed.
    ("V7a the lint job's pin dropped below the floor", CI, JobPin("lint", "1.25"), None,
     LCK, PIN, "golangci-lint REFUSES TO START when built with a Go older than the module's "
               "target — this pin is the one whose failure CI actually produced"),

    ("V7b the vuln job's pin dropped below the floor", CI, JobPin("vuln", "1.25"), None,
     LCK, PIN, "govulncheck grades the STDLIB against the toolchain it runs under, so this pin "
               "decides what the vulnerability report is even about"),

    ("V7c the test job's pin dropped below the floor", CI, JobPin("test", "1.25"), None,
     LCK, PIN, "setup-go exports GOTOOLCHAIN=local, so this pin — not go.mod's directive — is "
               "the runtime the suite actually exercises"),

    ("V8 the ci.yaml pin parse finds nothing", TEST,
     'regexp.MustCompile(`go-version:\\s*"(\\d+)\\.(\\d+)(\\.\\d+)?"`)',
     'regexp.MustCompile(`NEVERMATCH:\\s*"(\\d+)\\.(\\d+)(\\.\\d+)?"`)',
     LCK, PIN, "a parse that finds no pins must fail, not report perfect lockstep"),

    ("V6 the floor comparison inverted", TEST,
     "\tolder := major < wantMajor ||", "\tolder := false && major < wantMajor ||",
     None, PIN, "with the comparison disabled the guard still passes on a CORRECT pin — "
                "so V2 is what proves the comparison, and this shows V2 is load-bearing"),
]

# ─── --check-anchors: the cheap half, and the ONLY half CI can afford to run ───────────────
#
# ⚠ WHY THIS EXISTS. V7's anchor expected two occurrences of a literal and ci.yaml now has three.
# The control refused to fire, this campaign printed `7/8 controls CAUGHT`, and NOTHING NOTICED —
# because the only thing that reports an ANCHOR-FAILED is this script's own run, and this script is
# run by hand. An inert control looks exactly like a passing one to everyone except the person who
# happens to run it and read the count. talyvor-docs had the identical defect (W3.64, #225) and the
# same repair; this is the second instance, which is why the mode is being made a convention.
#
# The full campaign cannot go in CI: it mutates tracked files and runs the guard suite once per
# control. But the ANCHOR check is pure counting over three files — no mutation, no `go test`,
# milliseconds — and it is the part that rots. So CI runs this half on every push, and the day a
# fourth job pins Go the BUILD says so instead of a hand-run reporting 10/11.
#
# ⚠ IT DOES NOT CLAIM THE CONTROLS STILL CATCH ANYTHING. It claims only that each one can still be
# APPLIED. A control whose anchor matches but whose mutation has gone inert passes here and proves
# nothing — that is what the campaign is for, and why this mode is named for what it checks.
ANCHOR_FLOOR = 10  # controls whose anchors are checked. A loop over an empty list checks nothing.


def check_anchors(ci_mode):
    bad = []
    if len(CONTROLS) < ANCHOR_FLOOR:
        bad.append("FLOOR: only %d controls to check, floor is %d — a loop over a shrunken list "
                   "reports clean anchors rather than a missing campaign. If controls were "
                   "deleted, lower the floor in the same diff." % (len(CONTROLS), ANCHOR_FLOOR))
    for ctl in CONTROLS:
        name, path, old = ctl[0], ctl[1], ctl[2]
        expect = ctl[7] if len(ctl) > 7 else 1
        text = open(path).read()
        try:
            n = old.count_in(text) if isinstance(old, JobPin) else text.count(old)
            err = None
        except AssertionError as e:
            n, err = -1, str(e)
        status = "ok" if n == expect else "STALE"
        print("  %-52s %s (%s, want %d)" % (name, status, err or "%dx" % n, expect))
        if n != expect:
            bad.append(
                "%s: %s in %s, want %d — this control CANNOT RUN. It is not a failing guard, it is "
                "an ABSENT one, and the campaign would report a score with it silently missing. "
                "Re-anchor it on something that still identifies exactly what it means to mutate; "
                "do NOT just raise the expected count, which stops the control saying WHICH of the "
                "duplicates it dropped."
                % (name, err or "its anchor occurs %dx" % n, os.path.basename(path), expect))
    if bad:
        for b in bad:
            print(("::error::" if ci_mode else "") + b)
        return 1
    print("anchor check: all %d control anchors still apply" % len(CONTROLS))
    return 0


if "--check-anchors" in sys.argv:
    sys.exit(check_anchors("--ci" in sys.argv))


before = {p: sha(p) for p in FILES}
print("BASELINE sha256")
for p in FILES:
    print("  %-26s %s" % (os.path.basename(p), before[p]))
ok, out = run("TestGoModPins|TestTheToolchainPin|TestTheRunningToolchain")
if not ok:
    sys.exit("not green before the campaign:\n" + out[-2000:])
print("\nbaseline: GREEN\n")


def restore_on_signal(snapshot):
    """Put every snapshotted file back, then die of the signal we were sent.

    A `finally` DOES NOT RUN ON SIGTERM. Measured in talyvor-suite (W1.7, 78c69c8): a 2-minute
    command timeout killed a control mid-mutation and left a GATE REMOVED in the working tree,
    with a green suite and a `git status` showing only files the session had edited on purpose.
    Reproduced on demand there (5de27e3) and again in talyvor-docs (ffe9063), where the file left
    mutated was go.mod.

    Re-raising with SIG_DFL keeps the exit status honest: a caller that killed this process still
    sees it die of that signal rather than exit 0 with a tidy tree. SIGKILL still strands and
    nothing in Python can change that.

    Deliberately self-contained rather than an import, so the next script is a paste. The
    population and the rule live in scripts/check-restore-signal-handlers.py.
    """
    def handler(signum, _frame):
        for path, blob in snapshot.items():
            try:
                open(path, "wb").write(blob)
            except OSError:
                pass
        sys.stderr.write("\n!! signal %d — restored %d mutated file(s) before exiting\n"
                         % (signum, len(snapshot)))
        signal.signal(signum, signal.SIG_DFL)
        os.kill(os.getpid(), signum)

    for s in (signal.SIGTERM, signal.SIGINT, signal.SIGHUP):
        signal.signal(s, handler)


results = []
for ctl in CONTROLS:
    name, path, old, new, red, green, proves = ctl[:7]
    expect = ctl[7] if len(ctl) > 7 else 1
    backup = open(path).read()
    # Installed AFTER the snapshot exists and re-installed each control, because `path`
    # differs per control. The `finally` below is the normal path; this is the one a
    # SIGTERM takes.
    restore_on_signal({path: backup.encode('utf-8')})
    try:
        anchored(path, old, new, expect)
        if red is None:
            # An "must still pass" control: the named green test must stay GREEN.
            green_ok, _ = run(green)
            verdict = "CAUGHT" if green_ok else "MISSED"
        else:
            red_ok, red_out = run(red)
            green_ok = True
            if green:
                green_ok, _ = run(green)
            verdict = "CAUGHT" if (not red_ok and green_ok) else ("MISSED" if red_ok else "COLLATERAL")
            if verdict == "CAUGHT":
                hit = [l for l in red_out.splitlines() if "_test.go:" in l]
                if hit:
                    name = name  # keep
    except AssertionError as e:
        verdict = "ANCHOR-FAILED: %s" % e
    finally:
        open(path, "w").write(backup)
    print("%-42s %s" % (name, verdict))
    print("     proves: %s" % proves)
    results.append(verdict)
    print()

after = {p: sha(p) for p in FILES}
clean = all(before[p] == after[p] for p in FILES)
print("RESTORE PROOF")
for p in FILES:
    print("  %-26s %s" % (os.path.basename(p), "IDENTICAL" if before[p] == after[p] else "!! MUTATED !!"))
ok, _ = run("TestGoModPins|TestTheToolchainPin|TestTheRunningToolchain")
print("\ngreen after restore: %s" % ok)
c = results.count("CAUGHT")
print("\n%d/%d controls CAUGHT" % (c, len(results)))
sys.exit(0 if (c == len(results) and clean and ok) else 1)
