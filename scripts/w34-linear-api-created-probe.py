#!/usr/bin/env python3
"""w34-linear-api-created-probe.py — is `createdAt` a field the SHIPPED Linear query may ask for,
and what does its declared type say about the states an import must distinguish?

#76 proved the technique this probe uses: LINEAR VALIDATES A GRAPHQL DOCUMENT BEFORE IT
AUTHENTICATES, so "this query is well-formed" is provable from an empty machine with no tenant and
no credentials. This is a RE-RUN of that technique for a DIFFERENT FIELD, not a citation of #76's
result — #76 measured `state { type }`, and a probe's verdict is about the field it asked for.

⚠ IT READS THE QUERY OUT OF linear.go RATHER THAN A COPY (#76's rule), so the thing proved
well-formed is the thing that ships. If the extraction finds nothing, this FAILS rather than
silently proving a hardcoded string — that is the way a file-reading check goes blind.
"""

import json
import pathlib
import re
import sys
import urllib.error
import urllib.request

URL = "https://api.linear.app/graphql"
SRC = pathlib.Path(__file__).resolve().parent.parent / "internal" / "importer" / "linear.go"
TIMEOUT = 30
failures = []


def gql(query, url=URL, timeout=TIMEOUT):
    req = urllib.request.Request(
        url,
        data=json.dumps({"query": query, "variables": {"teamId": "probe-no-such-team"}}).encode(),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.status, json.loads(r.read())
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.loads(e.read())
        except Exception:
            return e.code, {}
    except Exception as e:
        return None, {"transport": str(e)}


def codes(body):
    return sorted({e.get("extensions", {}).get("code", "?") for e in body.get("errors", [])})


def control(name, ok, detail):
    print(f"  control {name:<46} {'PASS' if ok else 'FAIL'}  {detail}")
    if not ok:
        failures.append(name)


# ── extract the SHIPPED query from source, never a copy
m = re.search(r"const linearIssuesQuery = `(.*?)`", SRC.read_text(), re.S)
if not m:
    print(f"FAILED: could not extract linearIssuesQuery from {SRC}. Refusing to probe a copy.")
    sys.exit(1)
shipped = m.group(1)
print(f"── extracted linearIssuesQuery from {SRC.name} ({len(shipped)} bytes)")
already = "createdAt" in shipped
print(f"   does the SHIPPED query already ask for createdAt? {already}")

print("\n── NEGATIVE CONTROLS (validation must be able to say no)")

st, body = gql("{ __typename }", url="https://api-zzz-nope.linear.invalid/graphql", timeout=15)
control("fabricated host resolves nothing", st is None, f"status={st}")

st, body = gql("query { team(id: \"x\") { issues(first: 1) { nodes { identifier zzzNotAField } } } }")
control("an unknown Issue field is REJECTED", st == 400 and "GRAPHQL_VALIDATION_FAILED" in codes(body),
        f"HTTP {st} {codes(body)}")

st, body = gql(shipped)
control("the SHIPPED query VALIDATES (401, not 400)", st == 401 and "AUTHENTICATION_ERROR" in codes(body),
        f"HTTP {st} {codes(body)}")

if failures:
    print(f"\nFAILED: {failures}\nRefusing to report a verdict from an instrument that cannot say no.")
    sys.exit(1)

print("\n── MEASUREMENT: the shipped query WITH createdAt in the node selection")
# Add the field exactly where the mapper will read it: the issue node selection set.
candidate = shipped.replace("dueDate completedAt", "dueDate completedAt createdAt", 1)
if candidate == shipped and not already:
    print("  could not place createdAt into the node selection — the query's shape moved. NOT MEASURED.")
    sys.exit(1)
st, body = gql(candidate)
ok = st == 401 and "AUTHENTICATION_ERROR" in codes(body)
print(f"  shipped + createdAt  ⇒  HTTP {st} {codes(body)}   {'WELL-FORMED' if ok else 'REJECTED'}")
if not ok:
    print("  createdAt is NOT a valid selection here. Do not ship a mapper for it.")
    sys.exit(1)

print("\n── MEASUREMENT: the DECLARED TYPE, by unauthenticated introspection")
st, body = gql('{ __type(name: "Issue") { fields { name description type { kind name ofType { name } } } } }')
if st != 200:
    print(f"  introspection HTTP {st} — NOT MEASURED")
    sys.exit(1)
for f in body["data"]["__type"]["fields"]:
    if f["name"] not in ("createdAt", "completedAt", "dueDate"):
        continue
    t = f["type"]
    name = t["name"] or (t["ofType"] or {}).get("name")
    bang = "!" if t["kind"] == "NON_NULL" else ""
    print(f"  Issue.{f['name']:<12} {name}{bang:<2}  \"{(f['description'] or '')[:78]}\"")

print("""
⚠ THE ASYMMETRY THAT DECIDES THE WARNING CHANNEL. Issue.createdAt is DateTime! — NON_NULL — while
Issue.completedAt is DateTime (nullable). #74's three-distinguishable-lines rule was written for a
field the provider may legitimately not send. For Linear's createdAt the provider CANNOT not send
it: a node that parses but carries an empty createdAt means the TRANSPORT changed, not that the
issue has no opening time. That is a different report line and this probe is the reason to write it.
⚠ WHAT THIS STILL DOES NOT PROVE, said rather than implied: the OUTPUT SERIALISATION. linear.go's
header already records that DateTime's description describes what the API ACCEPTS, not what it
EMITS, and that is unmeasurable from here without a tenant. The refusal path — report, never nil —
is what carries that gap, exactly as it does for completedAt.""")
