#!/usr/bin/env python3
"""Refresh internal/importer/testdata/linear_schema_snapshot.json from the REAL Linear API.

WHY THIS EXISTS. `linearIssuesQuery` in internal/importer/linear.go is a GraphQL document that
NOTHING in CI could validate: every Linear test in this package answers a canned body from an
httptest server that never looks at the query it was sent, so a document with a misspelled field
passes the whole suite and 400s on the first real import. W3.4 states that hole in its own words —
"adding `type` to the Linear GraphQL query would 400 the WHOLE query if wrong, and no CI test can
catch that (the tests use a fake server that accepts any query). DO NOT SHIP THAT BLIND."

WHAT MAKES IT POSSIBLE WITHOUT A TENANT. Linear runs GraphQL VALIDATION BEFORE AUTHENTICATION, and
serves __schema/__type introspection to unauthenticated callers. Both measured from this machine on
2026-08-13, and both are recorded here as controls so a future reader can re-run them:

    POST {"query":"query { thisFieldDoesNotExistXyz }"}   -> HTTP 400  GRAPHQL_VALIDATION_FAILED
    POST {"query":"query { __typename }"}                 -> HTTP 401  AUTHENTICATION_ERROR
    POST the SHIPPED linearIssuesQuery, verbatim          -> HTTP 401  AUTHENTICATION_ERROR
    POST {"query":"query { __schema { queryType { name } } }"} -> HTTP 200 {"data":{...}}

400-vs-401 is therefore a DISCRIMINATOR: 401 means the document is valid against Linear's live
schema, 400 means it is not. That is what makes the snapshot below a measurement rather than a
transcription of the docs.

THE TEST THAT READS IT DOES NOT USE THE NETWORK. linear_query_schema_test.go validates the shipped
document against this file only, so the suite stays hermetic and CI never depends on a third party
being up. This script is the manual refresh path, and the snapshot carries the date it was taken.

Usage:  python3 scripts/w34-linear-schema-snapshot.py [--check]

    --check  fetch and DIFF against the committed snapshot without writing (prints what moved).
"""

import json
import pathlib
import sys
import urllib.error
import urllib.request

ENDPOINT = "https://api.linear.app/graphql"
OUT = pathlib.Path(__file__).resolve().parents[1] / "internal/importer/testdata/linear_schema_snapshot.json"

# The types linearIssuesQuery walks. Kept as a list rather than derived from the query on purpose:
# a snapshot derived from the document could never contradict the document.
TYPES = [
    "Query",
    "Team",
    "IssueConnection",
    "PageInfo",
    "Issue",
    "WorkflowState",
    "IssueLabelConnection",
    "IssueLabel",
]

TYPE_QUERY = """query($n: String!) {
  __type(name: $n) {
    name
    kind
    fields(includeDeprecated: true) {
      name
      isDeprecated
      type { kind name ofType { kind name ofType { kind name ofType { kind name } } } }
      args { name type { kind name ofType { kind name ofType { kind name } } } }
    }
  }
}"""


def post(query, variables=None):
    req = urllib.request.Request(
        ENDPOINT,
        data=json.dumps({"query": query, "variables": variables or {}}).encode(),
        headers={"Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            return resp.status, json.loads(resp.read())
    except urllib.error.HTTPError as exc:
        return exc.code, json.loads(exc.read())


def render_type(t):
    """GraphQL type ref -> the spelling this snapshot uses: Team, String!, [Issue!]!."""
    if t is None:
        return ""
    kind, name, of = t.get("kind"), t.get("name"), t.get("ofType")
    if kind == "NON_NULL":
        return render_type(of) + "!"
    if kind == "LIST":
        return "[" + render_type(of) + "]"
    return name or ""


def fetch():
    controls = {}
    status, _ = post("query { thisFieldDoesNotExistXyz }")
    controls["invalid_document_status"] = status
    status, _ = post("query { __typename }")
    controls["valid_document_status"] = status
    if controls["invalid_document_status"] != 400 or controls["valid_document_status"] != 401:
        sys.exit(
            "REFUSING TO WRITE A SNAPSHOT: the 400/401 discriminator no longer holds "
            f"(invalid={controls['invalid_document_status']}, valid={controls['valid_document_status']}). "
            "Without it, a 401 no longer proves a document is valid and this whole approach needs re-measuring."
        )

    types = {}
    for name in TYPES:
        status, body = post(TYPE_QUERY, {"n": name})
        node = (body.get("data") or {}).get("__type")
        if status != 200 or not node:
            sys.exit(f"introspection of {name} failed: HTTP {status} {json.dumps(body)[:300]}")
        fields = {}
        for f in node["fields"]:
            fields[f["name"]] = {
                "type": render_type(f["type"]),
                "deprecated": f["isDeprecated"],
                "args": {a["name"]: render_type(a["type"]) for a in f["args"]},
            }
        types[name] = {"kind": node["kind"], "fields": fields}
    return {
        "_provenance": {
            "endpoint": ENDPOINT,
            "fetched_utc": "2026-08-13",
            "authenticated": False,
            "how": "unauthenticated __type introspection; see scripts/w34-linear-schema-snapshot.py",
            "controls": controls,
        },
        "types": types,
    }


def main():
    snap = fetch()
    text = json.dumps(snap, indent=2, sort_keys=True) + "\n"
    if "--check" in sys.argv:
        old = OUT.read_text() if OUT.exists() else ""
        if old == text:
            print("snapshot is current")
            return
        old_types = json.loads(old)["types"] if old else {}
        for tname, t in snap["types"].items():
            old_fields = set((old_types.get(tname) or {}).get("fields", {}))
            new_fields = set(t["fields"])
            for gone in sorted(old_fields - new_fields):
                print(f"REMOVED {tname}.{gone}")
            for added in sorted(new_fields - old_fields):
                print(f"added   {tname}.{added}")
        print("snapshot DIFFERS from the committed file (re-run without --check to update)")
        sys.exit(1)
    OUT.write_text(text)
    print(f"wrote {OUT} ({len(text)} bytes)")


if __name__ == "__main__":
    main()
