#!/usr/bin/env python3
"""w34-jira-csv-resolution-probe.py — the provenance behind internal/importer/jira_csv_resolution.go.

A Jira CSV export carries a `Resolution` column that says whether resolved work was FINISHED or
ABANDONED. jiraRowMapper never read it, so every abandoned issue imported as Track `done` carrying a
completion time — which analytics' resolution-stats query counts as delivered work, because it
selects on `completed_at IS NOT NULL` with no status predicate.

⚠ THE FACT WAS ALREADY WRITTEN DOWN ONE FILE AWAY. jira_csv_dates.go's header for jiraCSVResolved
argues the completed_at gate matters on this transport precisely because "a Jira CSV export carries
`Resolution` for cancelled work too ... and every one of those rows has a Resolved date". Nobody
asked whether the gate ever CATCHES one. It does not: those rows are Status=Closed, which
mapJiraStatus maps to done, so the gate passes.

This script re-measures the three things that decided the merge:

  1. the instance's RESOLUTION vocabulary            (/rest/api/2/resolution)
  2. the instance's STATUS vocabulary, to show that mapJiraStatus's shipped cancellation branch
     ("won't do" / "won't fix") is UNREACHABLE there — those words are resolutions, not statuses
  3. the SIZE: how many resolved issues carry a status that maps to Track `done` while their
     resolution says the work was abandoned

    python3 scripts/w34-jira-csv-resolution-probe.py

⚠ IT IS DELIBERATELY NOT IN CI, for the same reason as w34-linear-schema-probe.py and
w34-jira-csv-export-probe.py: a gate that depends on a third party's uptime is one people re-run
rather than read. CI holds the LOCAL contract — internal/importer/jira_csv_resolution_test.go pins
the measured vocabulary and the three outcome classes by hand. This is the evidence behind it.

⚠ THE INSTANCE IS SERVER/DC AND THE SHIPPED API CLIENT CALLS CLOUD v3 — #75's second finding. It
applies to the API transport, NOT to this one: the CSV export view is a Jira feature, not a REST API
version, and the file this fetches is the artifact a user gets from "Export → CSV". What is genuinely
unproven is another tenant's RESOLUTION NAMES, which are per-instance configurable — and that is
exactly why a resolution Track cannot read is REPORTED rather than guessed at.
"""

import csv
import io
import json
import sys
import urllib.error
import urllib.parse
import urllib.request

HOST = "https://jira.atlassian.com"
VIEW = "jira.issueviews:searchrequest-csv-all-fields"
PROJECT = "JRASERVER"
TIMEOUT = 120

# The two words Track's own mapJiraStatus already maps to StatusCancelled and that this merge
# therefore acts on. They are NOT invented here — they are `case` literals in csv.go.
ACTED_ON = ["Won't Fix", "Won't Do"]


def fetch(url):
    req = urllib.request.Request(url, headers={"User-Agent": "talyvor-w34-probe"})
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT) as r:
            return r.status, r.headers.get("Content-Type", ""), r.read()
    except urllib.error.HTTPError as e:
        return e.code, e.headers.get("Content-Type", ""), e.read()
    except Exception as e:  # DNS failure, TLS failure, timeout
        return 0, f"{type(e).__name__}", str(e).encode()


def export_url(host, view, jql, limit):
    return (f"{host}/sr/{view}/temp/SearchRequest.csv"
            f"?jqlQuery={urllib.parse.quote(jql)}&tempMax={limit}")


def total(jql):
    status, _, body = fetch(f"{HOST}/rest/api/2/search?maxResults=0&jql={urllib.parse.quote(jql)}")
    if status != 200:
        return None
    return json.loads(body)["total"]


def main():
    ok = True

    # ── NEGATIVE CONTROLS FIRST, so no 200 is read as a blanket answer ────────────────
    print("negative controls")
    for label, url in [
        ("fabricated host", export_url("https://jira-talyvor-not-a-host.atlassian.com", VIEW, f"project = {PROJECT}", 1)),
        ("fabricated view on the real host", export_url(HOST, "jira.issueviews:searchrequest-talyvor-not-a-view", f"project = {PROJECT}", 1)),
        ("fabricated project in the JQL", export_url(HOST, VIEW, "project = TALYVORNOTAPROJECT", 1)),
        ("fabricated REST path on the real host", f"{HOST}/rest/api/2/talyvornotaresource"),
    ]:
        status, ctype, _ = fetch(url)
        good = status != 200
        print(f"  {'OK ' if good else 'BAD'}  {label:38s} => HTTP {status} {ctype}")
        ok &= good
    if not ok:
        print("\nA control answered 200. Every measurement below would be unreadable — stopping.")
        return 1

    # ── 1. THE RESOLUTION VOCABULARY ─────────────────────────────────────────────────
    status, ctype, body = fetch(f"{HOST}/rest/api/2/resolution")
    print(f"\n/rest/api/2/resolution => HTTP {status} {ctype}")
    if status != 200:
        print("  no vocabulary measured — stopping")
        return 1
    resolutions = [r["name"] for r in json.loads(body)]
    print(f"  {len(resolutions)} resolutions: " + " · ".join(sorted(resolutions)))

    # ── 2. THE SHIPPED CANCELLATION BRANCH IS UNREACHABLE VIA `Status` ───────────────
    status, _, body = fetch(f"{HOST}/rest/api/2/status")
    statuses = {s["name"].lower() for s in json.loads(body)} if status == 200 else set()
    print(f"\n/rest/api/2/status => {len(statuses)} distinct status names")
    print("  mapJiraStatus's cancellation `case` literals, checked against the STATUS vocabulary:")
    for word in ["cancelled", "canceled", "won't do", "won't fix"]:
        print(f"    {'PRESENT' if word in statuses else 'ABSENT ':8s} a status named {word!r}")
    print("  POSITIVE CONTROL — cases that SHOULD be present, so the lookup is not blind:")
    for word in ["closed", "open", "in progress", "resolved"]:
        mark = "PRESENT" if word in statuses else "ABSENT "
        print(f"    {mark:8s} a status named {word!r}")
        if word not in statuses:
            ok = False

    # ── 3. THE SIZE ──────────────────────────────────────────────────────────────────
    done_like = f'project = {PROJECT} AND status in (Closed, Done, Resolved)'
    print(f"\nsize on project {PROJECT}:")
    print(f"  resolved issues                               {total(f'project = {PROJECT} AND resolution IS NOT EMPTY')}")
    print(f"  ... whose status maps to Track done           {total(done_like + ' AND resolution IS NOT EMPTY')}")
    print(f"  status = Cancelled (the only signal today)    {total(f'project = {PROJECT} AND status = Cancelled')}")
    print("\n  per-resolution, among done-mapping statuses — the numbers pinned in the Go test:")
    for name in sorted(resolutions):
        n = total(f"{done_like} AND resolution = {json.dumps(name)}")
        mark = "  ← ACTED ON" if name in ACTED_ON else ""
        print(f"    {name:26s} {n if n is not None else '?':>7}{mark}")

    # ── 4. THE CSV EXPORT ACTUALLY CARRIES THE COLUMN, spelled as the Go const says ──
    jql = (f'project = {PROJECT} AND resolution in ("Won\'t Fix", "Won\'t Do", "Duplicate", '
           f'"Cannot Reproduce", "Incomplete") ORDER BY resolved DESC')
    status, ctype, body = fetch(export_url(HOST, VIEW, jql, 12))
    print(f"\nexport => HTTP {status} {ctype} {len(body)} bytes")
    if status != 200 or "text/csv" not in ctype:
        print("  the export did not answer as CSV — the column spelling is unmeasured")
        return 1
    rows = list(csv.reader(io.StringIO(body.decode("utf-8-sig"))))
    header, data = rows[0], rows[1:]
    for col in ["Status", "Resolution", "Resolved"]:
        present = col in header
        print(f"  {'present' if present else 'ABSENT ':8s} column {col!r}")
        ok &= present
    idx = {}
    for i, h in enumerate(header):
        idx.setdefault(h, i)
    print("\n  the rows themselves — every one Status=Closed, which is why this was invisible:")
    for r in data:
        print("    " + " · ".join(f"{c}={r[idx[c]]!r}" for c in ["Issue key", "Status", "Resolution", "Resolved"] if c in idx))

    print("\n" + ("all measurements agree with what is pinned in internal/importer/jira_csv_resolution_test.go"
                  if ok else "SOMETHING MOVED — re-derive before trusting the pinned table"))
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
