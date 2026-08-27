#!/usr/bin/env python3
"""W6.32 control campaign — the exempt-route census.

⚠ THIS CENSUS IS GREEN ON A CLEAN TREE: all 14 exempt routes already carry their own auth, so
there was nothing to fix. That makes the controls the ONLY evidence the guard is not vacuous.
"""
import hashlib, os, subprocess, sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MAIN = os.path.join(ROOT, "cmd/track/main.go")
FB   = os.path.join(ROOT, "internal/featureboard/handler.go")
TEST = os.path.join(ROOT, "cmd/track/exempt_route_census_test.go")
FILES = [MAIN, FB, TEST]

def sha(p): return hashlib.sha256(open(p, "rb").read()).hexdigest()

def run(test):
    r = subprocess.run(["go", "test", "-count=1", "-run", "^%s$" % test, "./cmd/track/"],
                       cwd=ROOT, capture_output=True, text=True)
    return r.returncode == 0, r.stdout + r.stderr

def anchored(path, old, new):
    s = open(path).read()
    n = s.count(old)
    if n != 1:
        raise AssertionError("anchor appears %d times, want 1: %r" % (n, old[:60]))
    open(path, "w").write(s.replace(old, new, 1))

REC = "TestEveryExemptRouteIsRecordedWithItsOwnAuth"
PRE = "TestExemptPrefixesAreTheOnesMainGoUses"
RES = "TestResolverFindsRoutesNestedInsideARouteBlock"
MNT = "TestEveryMountIsInsideTheV1RouteBlock"

CONTROLS = [
    ("Z1 a new anonymous route under an exempt prefix", FB,
     'r.Get("/posts", h.PublicListPosts)',
     'r.Get("/posts", h.PublicListPosts)\n\t\tr.Post("/admin-export", h.PublicListPosts)',
     REC, PRE, "a route added to the unauthenticated surface must declare what authenticates it"),

    ("Z2 the exemption widened by a seventh prefix", MAIN,
     'strings.HasPrefix(p, "/v1/guest/")',
     'strings.HasPrefix(p, "/v1/guest/") ||\n\t\t\tstrings.HasPrefix(p, "/v1/reports/")',
     PRE, RES, "widening gwExempt turns off gwAuth+wsAuthz for a whole subtree and is caught"),

    ("Z3 the resolver stops descending into nested Route", TEST,
     "\t\t\t\tcollectRoutes(fl.Body, prefix+lit, out)\n\t\t\t\treturn false",
     "\t\t\t\t_ = fl\n\t\t\t\treturn true",
     RES, MNT, "five anonymous routes (three of them WRITES) live only inside a nested Route"),

    ("Z4 a handler mounted outside the /v1 block", MAIN,
     "\tr.Route(\"/v1\", func(r chi.Router) {",
     "\tlabelHandler.Mount(r)\n\tr.Route(\"/v1\", func(r chi.Router) {",
     MNT, RES, "the /v1 base every Mount path assumes is actually checked"),

    ("Z5 an exempt route loses its recorded auth", TEST,
     '"Post /v1/webhooks/github":',
     '"Post /v1/webhooks/github_REMOVED":',
     REC, RES, "an unrecorded entry on the unauthenticated surface is caught"),

    ("Z6 the prefix parse returns nothing", TEST,
     'start := strings.Index(body, "gwExempt := func(p string) bool {")',
     'start := strings.Index(body, "NEVER_APPEARS_ANYWHERE")',
     REC, RES, "a broken predicate parse hits the floor, not an empty unauthenticated surface"),

    ("Z7 the file walk skips everything", TEST,
     '\t\tif !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {',
     '\t\tif true || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {',
     REC, MNT, "a broken walk reports zero routes rather than a clean census"),

    ("Z8 the recorded route total drifts", TEST,
     "const totalRoutes = 136", "const totalRoutes = 100",
     RES, REC, "the census cannot silently start covering a fraction of the tree"),
]

before = {p: sha(p) for p in FILES}
print("BASELINE sha256")
for p in FILES:
    print("  %-34s %s" % (os.path.basename(p), before[p]))
ok, out = run("TestEveryExemptRouteIsRecordedWithItsOwnAuth|TestExemptPrefixesAreTheOnesMainGoUses|TestResolverFindsRoutesNestedInsideARouteBlock|TestEveryMountIsInsideTheV1RouteBlock")
if not ok:
    sys.exit("not green before the campaign:\n" + out[-2500:])
print("\nbaseline: GREEN (nothing to fix — the controls are the evidence)\n")

results = []
for name, path, old, new, red, green, proves in CONTROLS:
    backup = open(path).read()
    try:
        anchored(path, old, new)
        red_ok, red_out = run(red)
        green_ok, _ = run(green)
        verdict = "CAUGHT" if (not red_ok and green_ok) else ("MISSED" if red_ok else "COLLATERAL")
    except AssertionError as e:
        verdict, red_out = "ANCHOR-FAILED: %s" % e, ""
    finally:
        open(path, "w").write(backup)
    print("%-52s %s" % (name, verdict))
    print("     proves: %s" % proves)
    if verdict == "CAUGHT":
        hit = [l for l in red_out.splitlines() if "_test.go:" in l]
        if hit:
            print("     red says: %s" % hit[0].strip()[:140])
    results.append(verdict)
    print()

after = {p: sha(p) for p in FILES}
clean = all(before[p] == after[p] for p in FILES)
print("RESTORE PROOF")
for p in FILES:
    print("  %-34s %s" % (os.path.basename(p), "IDENTICAL" if before[p] == after[p] else "!! MUTATED !!"))
ok, _ = run("TestEveryExemptRouteIsRecordedWithItsOwnAuth|TestExemptPrefixesAreTheOnesMainGoUses|TestResolverFindsRoutesNestedInsideARouteBlock|TestEveryMountIsInsideTheV1RouteBlock")
print("\ngreen after restore: %s" % ok)
c = results.count("CAUGHT")
print("\n%d/%d controls CAUGHT" % (c, len(results)))
sys.exit(0 if (c == len(results) and clean and ok) else 1)
