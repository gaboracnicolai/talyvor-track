#!/usr/bin/env python3
"""Positive controls for internal/importer/jira_wire_contract_schema_test.go.

Same contract as scripts/w34-linear-query-schema-controls.py: every control EDITS a real file, runs
the guard, asserts RED (or GREEN for the negative control), and restores the file sha256-verified.
Every substitution asserts its anchor count first — a control that edits zero bytes proves nothing,
and this package has already paid for one that did.

Run:  python3 scripts/w34-jira-contract-controls.py
"""

import hashlib
import pathlib
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1]
JIRA_GO = ROOT / "internal/importer/jira.go"
SNAPSHOT = ROOT / "internal/importer/testdata/jira_search_contract.json"
TEST_GO = ROOT / "internal/importer/jira_wire_contract_schema_test.go"
PROVIDERHTTP_GO = ROOT / "internal/importer/providerhttp.go"

RUN = ["go", "test", "-count=1", "-run",
       "TestJiraSearchRequest_|TestJiraSearchResponse_|TestJiraSearchEndpoint_",
       "./internal/importer/"]


def sha(p):
    return hashlib.sha256(p.read_bytes()).hexdigest()


def run_guard():
    proc = subprocess.run(RUN, cwd=ROOT, capture_output=True, text=True)
    return proc.returncode, proc.stdout + proc.stderr


def control(name, path, old, new, expect_red=True):
    original = path.read_text()
    before = sha(path)
    found = original.count(old)
    if found != 1:
        sys.exit(f"CONTROL DID NOT APPLY: anchor {old!r} appears {found} times in {path.name}, expected 1.")
    path.write_text(original.replace(old, new, 1))
    if sha(path) == before:
        sys.exit(f"{name}: the control edited zero bytes")
    code, out = run_guard()
    path.write_text(original)
    if sha(path) != before:
        sys.exit(f"{name}: FAILED TO RESTORE {path.name}")
    red = code != 0
    ok = red if expect_red else not red
    detail = ""
    for line in out.splitlines():
        s = line.strip()
        if s.startswith("--- FAIL") or s.startswith("jira_wire_contract_schema_test.go:"):
            detail = s[:150]
            break
    print(f"  {'PASS' if ok else '**FAILED**'}  {name}: went {'RED' if red else 'GREEN'} "
          f"(want {'RED' if expect_red else 'GREEN'})" + (f"\n            {detail}" if red and detail else ""))
    return ok


def main():
    print("D0 baseline: the guard must be GREEN on an untouched tree")
    code, out = run_guard()
    if code != 0:
        print(out[-2500:])
        sys.exit("BASELINE IS RED")
    print("  PASS  D0")

    r = []
    print("\nControls — each must turn the guard RED:")

    # The request half: a key Atlassian does not declare is a 400 on every page, and no fake server
    # in this package can see it.
    r.append(control("D1 request sends an undeclared key (startAt)",
                     JIRA_GO, '"maxResults": 100,', '"maxResults": 100,\n\t\t"startAt":   0,'))
    r.append(control("D2 request stops sending `fields`",
                     JIRA_GO, '"fields":     jiraFields,', ""))

    # The response half: a field the 200 schema does not declare decodes to its zero value forever.
    r.append(control("D3 jiraResp reads an undeclared response field (total)",
                     JIRA_GO, 'IsLast        bool        `json:"isLast"`',
                     'IsLast        bool        `json:"isLast"`\n\tTotal         int         `json:"total"`'))
    r.append(control("D4 jiraResp stops reading isLast (the ignored-field census must notice)",
                     JIRA_GO, 'IsLast        bool        `json:"isLast"`', 'IsLast        bool        `json:"-"`'))

    # The snapshot must be load-bearing in both directions.
    r.append(control("D5 snapshot is load-bearing (drop `warnings` from the response schema)",
                     SNAPSHOT, '    "warnings": "array"', '    "namesX": "object"'))
    r.append(control("D6 snapshot endpoint path moves away from jiraSearchPath",
                     SNAPSHOT, '"path": "/rest/api/3/search/jql"', '"path": "/rest/api/3/search/jqlv2"'))

    # Anti-vacuity: a reflection that reads nothing must fail rather than agree with every schema.
    r.append(control("D7 anti-vacuity (reflect over an empty struct instead of jiraResp)",
                     TEST_GO, "read := jsonTagsOf(t, reflect.TypeOf(jiraResp{}))",
                     "read := jsonTagsOf(t, reflect.TypeOf(struct{}{}))"))

    print("\nNegative control — an edit the guard must NOT punish:")
    r.append(control("D8 editing an unrelated comment leaves the guard GREEN",
                     PROVIDERHTTP_GO, "// parseRetryAfter reads a Retry-After",
                     "// parseRetryAfter (comment touched by control D8) reads a Retry-After",
                     expect_red=False))

    print()
    if all(r):
        print(f"ALL {len(r)} CONTROLS PASS — the guard fails for each class it claims to cover, and "
              "stays green for one it does not.")
        return 0
    print("SOME CONTROLS FAILED")
    return 1


if __name__ == "__main__":
    sys.exit(main())
