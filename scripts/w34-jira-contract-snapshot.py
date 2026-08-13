#!/usr/bin/env python3
"""Refresh internal/importer/testdata/jira_search_contract.json from Atlassian's PUBLISHED v3 spec.

WHY THIS EXISTS — it is the Jira half of what scripts/w34-linear-schema-snapshot.py does for Linear.
`jiraClient.fetchPage` builds a request body by hand and decodes a response into `jiraResp`, and
nothing in this repo could say whether either agrees with the endpoint Atlassian documents. The
fake servers in this package answer whatever they are asked, so a key Jira does not accept, or a
response field Jira does not send, is invisible to the whole suite.

WHAT IS AND IS NOT MEASURED HERE, SAID PLAINLY. This is the PUBLISHED CONTRACT, not a live tenant.
No Jira credentials exist in this environment, so what a real instance emits is still unmeasured
(W3.4 records that as its open question (3)). What the spec declares is a fact about the contract
Atlassian commits to, and a transport that reads a field the contract does not declare — or misses
one it does — is worth knowing about without a tenant.

Source: https://developer.atlassian.com/cloud/jira/platform/swagger-v3.v3.json (~2.4 MB).

Usage:  python3 scripts/w34-jira-contract-snapshot.py [--check]
"""

import json
import pathlib
import sys
import urllib.error
import urllib.request

SPEC_URL = "https://developer.atlassian.com/cloud/jira/platform/swagger-v3.v3.json"
OUT = pathlib.Path(__file__).resolve().parents[1] / "internal/importer/testdata/jira_search_contract.json"

# The path the shipped client POSTs to, and the one it deliberately does NOT.
SEARCH_PATH = "/rest/api/3/search/jql"
OLD_SEARCH_PATH = "/rest/api/3/search"


def fetch_spec():
    req = urllib.request.Request(SPEC_URL, headers={"Accept": "application/json"})
    with urllib.request.urlopen(req, timeout=120) as resp:
        if resp.status != 200:
            sys.exit(f"spec fetch: HTTP {resp.status}")
        return json.loads(resp.read())


def props(schema):
    return {name: (p.get("type") or p.get("$ref", "?")) for name, p in (schema.get("properties") or {}).items()}


def main():
    spec = fetch_spec()
    paths, schemas = spec["paths"], spec["components"]["schemas"]

    if SEARCH_PATH not in paths or "post" not in paths[SEARCH_PATH]:
        sys.exit(f"REFUSING TO WRITE: {SEARCH_PATH} POST is not in the published spec any more. "
                 "That is a finding, not a snapshot — take it to the queue.")

    post = paths[SEARCH_PATH]["post"]
    req_ref = post["requestBody"]["content"]["application/json"]["schema"]["$ref"].split("/")[-1]
    resp_ref = post["responses"]["200"]["content"]["application/json"]["schema"]["$ref"].split("/")[-1]

    snap = {
        "_provenance": {
            "source": SPEC_URL,
            "fetched_utc": "2026-08-13",
            "spec_version": spec["info"].get("version"),
            "openapi": spec.get("openapi"),
            "note": "the PUBLISHED contract, not a live tenant; no Jira credentials exist in this environment",
        },
        "endpoint": {
            "path": SEARCH_PATH,
            "methods": sorted(m for m in paths[SEARCH_PATH] if m in ("get", "post")),
            "post_operation_id": post.get("operationId"),
            "post_deprecated": bool(post.get("deprecated", False)),
            "request_schema": req_ref,
            "response_schema": resp_ref,
        },
        "old_endpoint": {
            "path": OLD_SEARCH_PATH,
            "present": OLD_SEARCH_PATH in paths,
            "post_deprecated": bool(paths.get(OLD_SEARCH_PATH, {}).get("post", {}).get("deprecated", False)),
            "get_deprecated": bool(paths.get(OLD_SEARCH_PATH, {}).get("get", {}).get("deprecated", False)),
        },
        "request_properties": props(schemas[req_ref]),
        "request_required": schemas[req_ref].get("required") or [],
        "response_properties": props(schemas[resp_ref]),
        "response_required": schemas[resp_ref].get("required") or [],
        "error_collection_properties": props(schemas["ErrorCollection"]),
        "issue_bean_properties": sorted(props(schemas["IssueBean"])),
    }

    text = json.dumps(snap, indent=2, sort_keys=True) + "\n"
    if "--check" in sys.argv:
        old = OUT.read_text() if OUT.exists() else ""
        if old == text:
            print("snapshot is current")
            return
        print("snapshot DIFFERS from the committed file (re-run without --check to update)")
        if old:
            o = json.loads(old)
            for side in ("request_properties", "response_properties"):
                gone = set(o[side]) - set(snap[side])
                added = set(snap[side]) - set(o[side])
                for g in sorted(gone):
                    print(f"REMOVED {side}.{g}")
                for a in sorted(added):
                    print(f"added   {side}.{a}")
        sys.exit(1)
    OUT.write_text(text)
    print(f"wrote {OUT} ({len(text)} bytes)")


if __name__ == "__main__":
    main()
