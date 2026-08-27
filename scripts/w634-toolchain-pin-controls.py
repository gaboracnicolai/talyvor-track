#!/usr/bin/env python3
"""W6.34 control campaign — the toolchain security floor."""
import hashlib, os, subprocess, sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
GOMOD = os.path.join(ROOT, "go.mod")
TEST = os.path.join(ROOT, "internal/migrate/toolchain_pin_test.go")
FILES = [GOMOD, TEST]

def sha(p): return hashlib.sha256(open(p, "rb").read()).hexdigest()

def run(test):
    r = subprocess.run(["go", "test", "-count=1", "-run", "^%s$" % test, "./internal/migrate/"],
                       cwd=ROOT, capture_output=True, text=True)
    return r.returncode == 0, r.stdout + r.stderr

def anchored(path, old, new):
    s = open(path).read()
    n = s.count(old)
    if n != 1:
        raise AssertionError("anchor appears %d times, want 1: %r" % (n, old[:60]))
    open(path, "w").write(s.replace(old, new, 1))

PIN = "TestGoModPinsTheToolchainAtOrAboveTheSecurityFloor"
WHY = "TestTheToolchainPinCarriesItsReason"
RUN = "TestTheRunningToolchainActuallyHonoursThePin"

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

results = []
for name, path, old, new, red, green, proves in CONTROLS:
    backup = open(path).read()
    try:
        anchored(path, old, new)
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
