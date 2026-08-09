#!/usr/bin/env python3
"""w34-wire-contract-controls.py — positive controls for wire_contract_test.go.

The guard PASSED ON ITS FIRST RUN, so it is suspected rather than trusted. Each control below:
  1. ASSERTS ITS ANCHOR COUNT BEFORE EDITING (#71's lesson: a substitution that matches nothing is
     byte-indistinguishable from a working guard),
  2. applies exactly one mutation,
  3. runs the guard AND a test that must STAY GREEN (so a control cannot "pass" by breaking the
     build or reddening everything),
  4. restores the file and verifies it is sha256-identical.

Run from the repo root:  python3 scripts/w34-wire-contract-controls.py
"""
import hashlib
import pathlib
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1]
JIRA = ROOT / "internal/importer/jira.go"
HTTPX = ROOT / "internal/importer/providerhttp.go"
GUARD = ROOT / "internal/importer/wire_contract_test.go"

GUARD_TESTS = "TestJiraRequest_PinsTheEndpointAndMethod|TestJiraSearchPath_ConstantMatchesTheMeasuredEndpoint"
# A test that must stay green through every control: #74's field-list wire test. If a mutation reds
# this too, the control proved nothing about the endpoint guard specifically.
STAYS_GREEN = "TestJiraRequest_AsksForTheDateFields"


def sha(p):
    return hashlib.sha256(p.read_bytes()).hexdigest()


def run_tests(pattern):
    r = subprocess.run(
        ["go", "test", "./internal/importer/", "-run", pattern, "-count=1"],
        cwd=ROOT, capture_output=True, text=True,
    )
    return r.returncode == 0, (r.stdout + r.stderr)


CONTROLS = [
    # name, file, old, new, why
    ("C1 endpoint reverted to the 410-Gone v2 path", JIRA,
     '"/rest/api/3/search/jql"', '"/rest/api/2/search"',
     "the realistic regression: the endpoint has already moved once"),
    ("C2 endpoint emptied", JIRA,
     '"/rest/api/3/search/jql"', '""',
     "the degenerate value a tautological guard would accept"),
    ("C3 leading slash dropped", JIRA,
     '"/rest/api/3/search/jql"', '"rest/api/3/search/jql"',
     "TrimRight'd base + no slash silently yields hostrest/api/..."),
    ("C4 method flipped to GET", HTTPX,
     "http.MethodPost", "http.MethodGet",
     "every fake in the package answers any method, so nothing else notices"),
    ("C5 Authorization header dropped", JIRA,
     'map[string]string{"Authorization": "Basic " + c.auth}', "map[string]string{}",
     "an unauthenticated search 401s against every real tenant"),
]

# C6 is special: it rewrites the GUARD ITSELF into the vacuous form to demonstrate that hardcoding
# the literal is load-bearing rather than stylistic.
VACUOUS_OLD = "if gotPath != measuredJiraCloudSearchPath {"
VACUOUS_NEW = "if gotPath != jiraSearchPath {"


def apply(path, old, new):
    text = path.read_text()
    n = text.count(old)
    if n != 1:
        raise SystemExit(f"ANCHOR ASSERTION FAILED: {path.name} contains {n} copies of {old!r}, want exactly 1")
    path.write_text(text.replace(old, new, 1))
    return n


def main():
    baseline = {p: sha(p) for p in (JIRA, HTTPX, GUARD)}

    ok, _ = run_tests(GUARD_TESTS)
    print(f"BASELINE guard on unmodified tree: {'GREEN' if ok else 'RED'}")
    if not ok:
        raise SystemExit("baseline must be green before controlling anything")

    failures = []
    for name, path, old, new, why in CONTROLS:
        apply(path, old, new)
        try:
            guard_ok, _ = run_tests(GUARD_TESTS)
            green_ok, _ = run_tests(STAYS_GREEN)
        finally:
            for p, h in baseline.items():
                pass
            path.write_text(path.read_text().replace(new, old, 1))
        restored = sha(path) == baseline[path]
        verdict = "RED (caught)" if not guard_ok else "GREEN — BLIND!"
        print(f"{name}: guard {verdict} | {STAYS_GREEN} {'green' if green_ok else 'RED'} | "
              f"restored={'sha-identical' if restored else 'MISMATCH'}  <- {why}")
        if guard_ok:
            failures.append(name)
        if not restored:
            failures.append(name + " (restore)")

    # C6 — the vacuity demonstration.
    apply(GUARD, VACUOUS_OLD, VACUOUS_NEW)
    apply(JIRA, '"/rest/api/3/search/jql"', '"/rest/api/2/search"')
    try:
        vac_ok, _ = run_tests("TestJiraRequest_PinsTheEndpointAndMethod")
    finally:
        GUARD.write_text(GUARD.read_text().replace(VACUOUS_NEW, VACUOUS_OLD, 1))
        JIRA.write_text(JIRA.read_text().replace('"/rest/api/2/search"', '"/rest/api/3/search/jql"', 1))
    both_restored = sha(GUARD) == baseline[GUARD] and sha(JIRA) == baseline[JIRA]
    print(f"C6 VACUITY DEMO (guard rewritten to compare the constant to itself, endpoint ALSO wrong): "
          f"{'PASSES — which is exactly why the literal is hardcoded' if vac_ok else 'reds (unexpected)'} | "
          f"restored={'sha-identical' if both_restored else 'MISMATCH'}")
    if not vac_ok:
        failures.append("C6 vacuity demo did not reproduce")
    if not both_restored:
        failures.append("C6 (restore)")

    print()
    for p, h in baseline.items():
        print(f"final {p.name}: {'sha-identical' if sha(p) == h else 'MISMATCH'}")
    if failures:
        print("\nFAILED CONTROLS:", failures)
        return 1
    print("\nAll controls behaved.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
