#!/usr/bin/env python3
"""w34-jira-api-resolution-probe.py — the provenance behind internal/importer/api_resolution.go.

#82 taught this package that a Jira `Resolution` says whether closed work was FINISHED or ABANDONED,
and fixed the CSV transport. The JIRA API transport never asked: `resolution` was not in jiraFields
and mapJiraIssues never read it, so every abandoned issue imported as Track `done` carrying a
completion time — which analytics' resolution-stats query counts as delivered work, because it
selects on `completed_at IS NOT NULL` with no status predicate.

⚠ THIS MEASURES THE ENDPOINT THE CODE ACTUALLY CALLS. #82's numbers came from a SERVER/DC instance
read through a CSV export view; the shipped client POSTs `/rest/api/3/search/jql` against Jira
CLOUD. #84 found that `hibernate.atlassian.net` answers that exact endpoint ANONYMOUSLY, so the
shipped path is measurable from an empty machine. A helper's evidence does not transfer with its
logic, and the rule this merge reuses is #82's while the numbers below are not.

⚠ IT READS jiraFields AND jiraSearchPath OUT OF THE SHIPPED SOURCE rather than a copy (#76's rule),
so the thing measured is the thing that ships. If an extraction finds nothing, or a constant cannot
be resolved, this FAILS rather than silently probing a hardcoded string.

⚠ EVERY CONTROL IS CHECKED BEFORE ANY NUMBER IS PRINTED, and the script exits non-zero if one
answers the way a success does. A 200 that is not distinguishable from a control's 200 measures
nothing.

    python3 scripts/w34-jira-api-resolution-probe.py

⚠ DELIBERATELY NOT IN CI, for #76's reason: a gate that depends on a third party's uptime is one
people re-run rather than read. CI holds the LOCAL contract — internal/importer/api_resolution_test.go
and api_resolution_job_test.go pin the vocabulary, the three outcome classes and the structural zero
by hand. This is the evidence behind them.
"""

import json
import pathlib
import re
import sys
import urllib.error
import urllib.request

HOST = "https://hibernate.atlassian.net"
PROJECT = "HHH"
TIMEOUT = 120
IMPORTER = pathlib.Path(__file__).resolve().parent.parent / "internal" / "importer"

# Track's OWN word→status table, the two branches this rule acts on. NOT invented here — they are
# `case` literals in csv.go's mapJiraStatus, which is the whole of what applyJiraResolution consults.
ABANDONED_WORDS = ["Won't Fix", "Won't Do", "Cancelled", "Canceled"]

failures = []


def control(name, ok, detail):
    print(f"  control {name:<52} {'PASS' if ok else 'FAIL'}  {detail}")
    if not ok:
        failures.append(name)


def post(path, body, host=HOST):
    req = urllib.request.Request(
        host + path,
        data=json.dumps(body).encode(),
        headers={"Content-Type": "application/json", "User-Agent": "talyvor-w34-probe"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT) as r:
            return r.status, json.loads(r.read())
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.loads(e.read())
        except Exception:
            return e.code, {}
    except Exception as e:  # DNS failure, TLS failure, timeout
        return 0, {"transport": f"{type(e).__name__}: {e}"}


def shipped():
    """Return (search path, fields list) read out of the shipped source. Refuses to guess."""
    src = "".join(p.read_text() for p in sorted(IMPORTER.glob("*.go"))
                  if not p.name.endswith("_test.go"))
    m = re.search(r'const jiraSearchPath = "([^"]+)"', src)
    if not m:
        sys.exit("FAILED: could not extract jiraSearchPath. Refusing to probe a copy.")
    path = m.group(1)
    m = re.search(r"var jiraFields = \[\]string\{(.*?)\}", src, re.S)
    if not m:
        sys.exit("FAILED: could not extract jiraFields. Refusing to probe a copy.")
    consts = dict(re.findall(r'(\w+)\s*=\s*"([^"]+)"', src))
    fields = []
    for tok in (t.strip() for t in m.group(1).split(",") if t.strip()):
        if tok.startswith('"'):
            fields.append(tok.strip('"'))
        elif tok in consts:
            fields.append(consts[tok])
        else:
            sys.exit(f"FAILED: jiraFields names {tok!r} and this script cannot resolve it. "
                     "A probe that silently drops a field measures the wrong request.")
    return path, fields


def main():
    path, fields = shipped()
    print(f"SHIPPED REQUEST (read from source): POST {path}")
    print(f"SHIPPED fields: {fields}\n")

    jql = f'project = "{PROJECT}"'

    print("NEGATIVE CONTROLS — run FIRST, so no 200 below is read as a blanket answer")
    st, _ = post(path, {"jql": jql, "fields": ["summary"], "maxResults": 1},
                 host="https://talyvor-no-such-site-xyzzy.atlassian.net")
    # ⚠ NOT a DNS failure: *.atlassian.net is a wildcard, so a fabricated SITE resolves and the edge
    # answers. What matters is that it does not answer the way a real one does.
    control("fabricated site on the real domain is not a 200", st != 200, f"HTTP {st}")
    st, _ = post("/rest/api/3/search/talyvorNoSuchPath", {"jql": jql})
    control("fabricated path on the real host is not a 200", st != 200, f"HTTP {st}")

    print("\nDECISIVE CONTROLS — without these the whole measurement proves nothing")
    st, b = post(path, {"jql": jql, "fields": ["summary"], "maxResults": 1})
    keys = sorted(b.get("issues", [{}])[0].get("fields", {}).keys()) if st == 200 else []
    control("fields=[summary] ALONE returns ONLY summary", st == 200 and keys == ["summary"],
            f"HTTP {st} keys={keys}")

    st, b = post(path, {"jql": jql, "fields": ["summary", "talyvorTotallyFakeField"], "maxResults": 1})
    keys = sorted(b.get("issues", [{}])[0].get("fields", {}).keys()) if st == 200 else []
    control("an UNKNOWN field name is HTTP 200 with the key simply absent",
            st == 200 and keys == ["summary"], f"HTTP {st} keys={keys}")

    st, b = post(path, {"jql": f"{jql} AND resolution IS NOT EMPTY", "fields": fields, "maxResults": 1})
    keys = sorted(b.get("issues", [{}])[0].get("fields", {}).keys()) if st == 200 else []
    control("the SHIPPED fields list brings `resolution` back", "resolution" in keys,
            f"HTTP {st} keys={keys}")

    st, b = post(path, {"jql": f"{jql} AND resolution IS EMPTY",
                        "fields": ["summary", "resolution"], "maxResults": 1})
    iss = b.get("issues", [{}])[0].get("fields", {}) if st == 200 else {}
    control("an UNRESOLVED issue answers `resolution: null`, key PRESENT",
            "resolution" in iss and iss.get("resolution") is None,
            f"HTTP {st} resolution={json.dumps(iss.get('resolution'))}")

    if failures:
        print(f"\nCONTROLS FAILED: {failures}. NO NUMBERS REPORTED — a measurement whose controls "
              "answer like its subject is not a measurement.", file=sys.stderr)
        return 1

    print("\nTHE WIRE SHAPE")
    st, b = post(path, {"jql": f"{jql} AND resolution IS NOT EMPTY", "fields": ["status", "resolution"],
                        "maxResults": 1})
    f = b["issues"][0]["fields"]
    print(f"  status     {json.dumps(f['status'].get('name'))}")
    print(f"  resolution {json.dumps(f['resolution'])}")

    print("\nTHE SIZE — every count is a JQL approximate-count against the same host")

    def count(q):
        s, body = post("/rest/api/3/search/approximate-count", {"jql": q})
        if s != 200:
            sys.exit(f"FAILED: count query returned HTTP {s} for {q!r}")
        return body["count"]

    words = ",".join(f'"{w}"' for w in ABANDONED_WORDS)
    rows = [
        (f"project {PROJECT}, issues", jql),
        ("  ... resolved (statusCategory = Done) — all import as Track `done`",
         f"{jql} AND statusCategory = Done"),
        ("  ... whose resolution Track reads as ABANDONED", f"{jql} AND resolution IN ({words})"),
        ("  ... of those, carrying a resolutiondate",
         f"{jql} AND resolution IN ({words}) AND resolutiondate IS NOT EMPTY"),
        ("issues whose STATUS is Cancelled/Canceled — the only signal",
         f'{jql} AND status IN ("Cancelled","Canceled")'),
        ("resolutions Track REFUSES to interpret (the open decision)",
         f'{jql} AND resolution IN ("Rejected","Out of Date","Duplicate","Cannot Reproduce","Incomplete")'),
    ]
    for label, q in rows:
        print(f"  {label:<62} {count(q):>7,}")

    print("\n⚠ THE REFUSED SET IS NOT A GAP IN THIS MERGE. Which further resolutions mean CANCELLED "
          "is a product decision with those numbers behind it, and #82 and #76 both left it "
          "standing rather than invent thirteen mappings. It is REPORTED on the first import.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
