#!/usr/bin/env python3
"""w34-linear-schema-probe.py — re-run the measurement that unblocked Linear's state.type.

W3.4 recorded, across four separate merges, that reading Linear's canonical state category
"needs a GraphQL query change that 400s the WHOLE query if wrong, and the test fake accepts any
query, so no CI test in this repo can catch it — it needs one real call against a real tenant".

The first half is true. The conclusion is not, because it assumes validating a query document
requires a tenant. It does not: Linear validates the DOCUMENT BEFORE it AUTHENTICATES.

    an unknown field   ⇒ HTTP 400  GRAPHQL_VALIDATION_FAILED
    a valid document   ⇒ HTTP 401  AUTHENTICATION_ERROR

Those two answers are distinguishable from an empty machine, so the exact risk that blocked this
field is measurable with NO CREDENTIALS. This script re-measures it, reads the SHIPPED query
document straight out of internal/importer/linear.go rather than a copy, and re-derives the state
type vocabulary from the schema's own field description.

⚠ IT IS DELIBERATELY NOT WIRED INTO CI. A gate that depends on a third party's uptime is a gate
people re-run rather than read. What CI holds is the local wire contract in
internal/importer/linear_state_type_test.go; this is the provenance behind it.

    python3 scripts/w34-linear-schema-probe.py
"""

import json
import pathlib
import re
import sys
import urllib.error
import urllib.request

ENDPOINT = "https://api.linear.app/graphql"
ROOT = pathlib.Path(__file__).resolve().parent.parent
LINEAR_GO = ROOT / "internal" / "importer" / "linear.go"


def post(url, payload, timeout=25):
    req = urllib.request.Request(
        url, data=json.dumps(payload).encode(), headers={"Content-Type": "application/json"}
    )
    try:
        r = urllib.request.urlopen(req, timeout=timeout)
        return r.status, json.loads(r.read().decode())
    except urllib.error.HTTPError as e:
        return e.code, json.loads(e.read().decode())


def code_of(doc):
    errs = doc.get("errors") or [{}]
    return errs[0].get("extensions", {}).get("code", "-")


def shipped_query():
    """Read the query document from the SOURCE, so this cannot drift from what ships."""
    src = LINEAR_GO.read_text()
    m = re.search(r"const linearIssuesQuery = `(.*?)`", src, re.S)
    if not m:
        sys.exit("could not find linearIssuesQuery in linear.go")
    return m.group(1)


def main():
    print("── NEGATIVE CONTROLS FIRST, so a 200/401 is not a blanket answer ──")
    try:
        urllib.request.urlopen("https://api.linear-talyvor-fake.app/graphql", timeout=10)
        print("  ✗ a fabricated host RESOLVED — every result below is suspect")
        return 1
    except Exception as e:
        print(f"  ✓ fabricated host does not resolve ({type(e).__name__})")
    try:
        urllib.request.urlopen("https://api.linear.app/talyvorTotallyFakePath", timeout=15)
        print("  ✗ a fabricated path on the REAL host answered 200 — the host answers anything")
        return 1
    except urllib.error.HTTPError as e:
        print(f"  ✓ fabricated path on the real host ⇒ HTTP {e.code}")

    print("\n── THE DISCRIMINATOR: does this API validate before it authenticates? ──")
    st, doc = post(ENDPOINT, {"query": "query { thisFieldDoesNotExist }"})
    print(f"  unknown field       ⇒ HTTP {st}  {code_of(doc)}")
    if code_of(doc) != "GRAPHQL_VALIDATION_FAILED":
        print("  ✗ an invalid document did NOT come back as a validation failure — the whole method")
        print("    below is void. Do not ship a query change on this evidence.")
        return 1

    q = shipped_query()
    st, doc = post(ENDPOINT, {"query": q, "variables": {"teamId": "ENG"}})
    print(f"  the SHIPPED query   ⇒ HTTP {st}  {code_of(doc)}")
    shipped_ok = code_of(doc) == "AUTHENTICATION_ERROR"
    print(f"  {'✓' if shipped_ok else '✗'} the document Track sends is "
          f"{'well-formed against the live schema' if shipped_ok else 'REJECTED BY THE LIVE SCHEMA'}")

    print("\n── THE VOCABULARY, from the schema's own description (introspection, unauthenticated) ──")
    st, doc = post(ENDPOINT, {"query": '{ __type(name:"WorkflowState"){ fields { name description } } }'})
    if st != 200 or not (doc.get("data") or {}).get("__type"):
        print(f"  ✗ introspection unavailable (HTTP {st}) — the vocabulary pin cannot be re-derived here")
        return 1
    desc = next((f["description"] for f in doc["data"]["__type"]["fields"] if f["name"] == "type"), "")
    print(f"  WorkflowState.type: {desc}")
    found = re.findall(r'"([a-z]+)"', desc or "")
    print(f"  parsed vocabulary ({len(found)}): {found}")

    pinned = ["triage", "backlog", "unstarted", "started", "completed", "canceled", "duplicate"]
    if found != pinned:
        print(f"  ⚠ THIS DIFFERS FROM THE PIN IN linear_state_type_test.go: {pinned}")
        print("    Update measuredLinearStateTypes and mapLinearStateType TOGETHER — the guard")
        print("    checks both directions and will red until they agree.")
        return 1
    print("  ✓ matches measuredLinearStateTypes exactly")

    print("\n⚠ WHAT THIS DOES NOT PROVE, said rather than implied: that a customer's tenant POPULATES")
    print("  the field, or what their team's states are called. That still needs a real tenant —")
    print("  W3.4 item (3). It is why the importer REPORTS which path decided every unrecognised")
    print("  state instead of resolving silently: the first real import answers it out loud.")
    return 0 if shipped_ok else 1


if __name__ == "__main__":
    sys.exit(main())
