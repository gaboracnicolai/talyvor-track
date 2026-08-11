#!/usr/bin/env python3
"""w34-linear-api-priority-probe.py — the provenance behind decoding Linear's priority as a number.

THE QUESTION. `linearNode` decodes every leaf of Linear's response as a string, a bool or raw
bytes, so that a shape this importer does not expect is REPORTED rather than becoming a decode
error that fails the whole page — linear.go says exactly that in its comment on DueDate and
CompletedAt. A census of both providers' response structs finds ONE scalar exception, and this
script measures what the schema declares for it.

    Issue.priority : Float!
    "The priority of the issue. 0 = No priority, 1 = Urgent, 2 = High, 3 = Medium, 4 = Low."

A GraphQL Float is a double. `2`, `2.0` and `2e0` are all legal JSON serialisations of the double
2, and Go's encoding/json accepts only the FIRST into an `int`:

    json: cannot unmarshal number 2.0 into Go struct field ... of type int

⚠ AND THE COST IS NOT THE FIELD. linearClient.fetchPage returns on any decode error, so one such
value discarded the entire 100-issue page — the sibling nodes decode correctly and are thrown away
with it — and linearSource then surfaced one error row and STOPPED, abandoning every later page.

⚠ WHAT THIS CANNOT MEASURE, STATED RATHER THAN IMPLIED, AND IT IS THE SAME LIMIT linearTimeLayouts
RECORDS FOR ITS OWN FIELD: how Linear's server SERIALISES a Float. The API authenticates before it
executes, so the shipped query answers 401 and no response body is observable from an empty
machine. That still needs a real tenant — W3.4 item (3). What IS measured here is that the
DECLARED contract permits spellings the decoder refused, which is why the decoder is the wrong
place to decide the question.

⚠ IT IS DELIBERATELY NOT WIRED INTO CI, for the reason w34-linear-schema-probe.py gives: a gate
that depends on a third party's uptime is a gate people re-run rather than read. What CI holds is
internal/importer/linear_api_priority_test.go, which drives the SHIPPED source over the wire.

    python3 scripts/w34-linear-api-priority-probe.py
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

# The scale is pinned BY HAND from the field description, not parsed out of the Go source, so this
# cannot agree with the mapper by construction. A guard that reads its subject out of the thing it
# measures cannot see a deletion — W3.4's own C5 lesson, one field over.
EXPECTED_SCALE = {0: "No priority", 1: "Urgent", 2: "High", 3: "Medium", 4: "Low"}


def post(url, payload, timeout=25):
    req = urllib.request.Request(
        url, data=json.dumps(payload).encode(), headers={"Content-Type": "application/json"}
    )
    try:
        r = urllib.request.urlopen(req, timeout=timeout)
        return r.status, json.loads(r.read().decode())
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.loads(e.read().decode())
        except Exception:
            return e.code, {}


def fail(msg):
    print(f"  ✗ {msg}")
    sys.exit(1)


def main():
    print("── NEGATIVE CONTROLS FIRST, so a 200 is not a blanket answer ──")
    try:
        urllib.request.urlopen("https://api-linear-fabricated-zzz.app/graphql", timeout=15)
        fail("a fabricated host ANSWERED — this machine is behind something that answers everything")
    except urllib.error.HTTPError:
        fail("a fabricated host returned an HTTP status — not a clean non-resolution")
    except Exception:
        print("  ✓ fabricated host does not resolve")

    code, _ = post(ENDPOINT + "-FABRICATED", {"query": "{__typename}"})
    if code != 404:
        fail(f"fabricated path on the real host ⇒ HTTP {code}, want 404")
    print("  ✓ fabricated path on the real host ⇒ HTTP 404")

    code, doc = post(ENDPOINT, {"query": 'query{ __type(name:"Issue"){ nameFABRICATED } }'})
    if code != 400:
        fail(f"an unknown field ⇒ HTTP {code}, want 400 — validation is not answering")
    print("  ✓ unknown field ⇒ HTTP 400 GRAPHQL_VALIDATION_FAILED")

    print("\n── THE DECLARED TYPE OF THE ONE NON-STRING LEAF (unauthenticated introspection) ──")
    q = (
        'query{ __type(name:"Issue"){ fields(includeDeprecated:true){ name description '
        "type{ kind name ofType{ kind name } } } } }"
    )
    code, doc = post(ENDPOINT, {"query": q})
    if code != 200:
        fail(f"introspection ⇒ HTTP {code}")
    fields = {f["name"]: f for f in doc["data"]["__type"]["fields"]}
    if "priority" not in fields:
        fail("Issue.priority is ABSENT from the schema — the shipped query would 400")

    t = fields["priority"]["type"]
    declared = ((t.get("ofType") or {}).get("name") or t.get("name") or "") + (
        "!" if t.get("kind") == "NON_NULL" else ""
    )
    desc = (fields["priority"]["description"] or "").strip()
    print(f"  Issue.priority : {declared}")
    print(f"  {desc}")
    if declared != "Float!":
        fail(f"declared type is {declared!r}, not 'Float!' — this probe's whole premise moved")
    print("  ✓ declared FRACTIONAL (Float), so 2, 2.0 and 2e0 are all legal for the same value")

    print("\n── THE SCALE, from the field's own description ──")
    found = {int(n): label.strip() for n, label in re.findall(r"(\d+)\s*=\s*([A-Za-z ]+?)(?=,|\.|$)", desc)}
    if found != EXPECTED_SCALE:
        fail(f"scale parsed {found}, pinned {EXPECTED_SCALE} — the vocabulary moved")
    print(f"  parsed ({len(found)}): {found}")
    print("  ✓ matches the scale linearPriorityFromNumber maps, pinned by hand here")

    print("\n── THE SHIPPED DECODER, read out of linear.go rather than a copy ──")
    src = LINEAR_GO.read_text()
    m = re.search(r"Priority\s+(\S+)\s+`json:\"priority\"`", src)
    if not m:
        fail("no `priority` field found in linearNode — this probe has lost its subject")
    go_type = m.group(1)
    print(f"  linearNode.Priority is decoded as {go_type}")
    if go_type == "int":
        fail(
            "an `int` cannot accept 2.0 or 2e0, and fetchPage returns on any decode error — "
            "one such value discards the whole page and stops the source"
        )
    if go_type != "json.Number":
        fail(f"unexpected decode type {go_type!r} — check it accepts every spelling of a Float")
    print("  ✓ json.Number accepts every legal spelling and hands the verbatim bytes to the warning")

    print(
        "\n⚠ WHAT THIS DOES NOT PROVE: which spelling Linear's server actually emits. The API\n"
        "  authenticates before it executes, so no response body is observable without a tenant —\n"
        "  W3.4 item (3). The point is that the decoder must not be what decides."
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
