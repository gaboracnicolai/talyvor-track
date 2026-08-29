#!/usr/bin/env python3
"""W6.34 control campaign — the toolchain security floor."""
import hashlib, os, signal, subprocess, sys

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

def anchored(path, old, new, count=1):
    """count: how many occurrences the anchor is EXPECTED to have. ci.yaml carries the same
    go-version pin in two jobs, and demanding uniqueness there was the control's bug, not the
    file's — dropping ONE of two pins below the floor is exactly the regression to catch."""
    s = open(path).read()
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
    ("V7 a ci.yaml pin dropped below the floor", CI,
     '          go-version: "1.26.6"\n', '          go-version: "1.25"\n',
     LCK, PIN, "the two version numbers nothing but a human keeps equal are now checked", 2),

    ("V8 the ci.yaml pin parse finds nothing", TEST,
     'regexp.MustCompile(`go-version:\\s*"(\\d+)\\.(\\d+)(\\.\\d+)?"`)',
     'regexp.MustCompile(`NEVERMATCH:\\s*"(\\d+)\\.(\\d+)(\\.\\d+)?"`)',
     LCK, PIN, "a parse that finds no pins must fail, not report perfect lockstep"),

    ("V6 the floor comparison inverted", TEST,
     "\tolder := major < wantMajor ||", "\tolder := false && major < wantMajor ||",
     None, PIN, "with the comparison disabled the guard still passes on a CORRECT pin — "
                "so V2 is what proves the comparison, and this shows V2 is load-bearing"),
]

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
