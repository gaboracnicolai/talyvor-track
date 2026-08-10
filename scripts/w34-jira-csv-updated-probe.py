#!/usr/bin/env python3
"""w34-jira-csv-updated-probe.py — the provenance behind internal/importer/jira_csv_updated.go.

⚠ THIS FIELD EXISTS BECAUSE A STOP REASON WAS WRONG. #83 scoped `updated_at` out with "nothing in
Track reads updated_at for a report", and #84 repeated it while flagging it as never measured. It is
false. Enumerated at `d3aaaca` by searching for READS of the column rather than by reading the
importer:

    frontend/src/components/issue/IssueRow.tsx:58   relativeTime(issue.updated_at) — on EVERY row
    frontend/src/components/issue/IssueList.tsx:48  sorts the issue list by updated_at DESC
    internal/issue/store.go:1135                    Search ORDER BY updated_at DESC
    internal/issue/store.go:648                     updated_at is in the API's sort whitelist
    internal/analytics/engine.go:416,433,483,508    the AI-cost report's window and its x-axis

The largest consumer is not a report. It is the issue list — the product's main screen.

This script measures what a real Jira CSV export carries in `Updated`, and HOW STALE a real backlog
is, which is the size of the error introduced by defaulting the column to the import instant.

    python3 scripts/w34-jira-csv-updated-probe.py

⚠ DELIBERATELY NOT IN CI, for the same reason as its four sibling probes: a gate that depends on a
third party's uptime is one people re-run rather than read. CI holds the LOCAL contract in
internal/importer/jira_csv_updated_test.go — the measured spelling and the pinned layouts, both
hardcoded there. This script is the evidence behind those literals.

⚠ NEGATIVE CONTROLS FIRST, and this script FAILS rather than proceeds if one answers CSV 200: a
fabricated host, a fabricated view on the real host, and a fabricated project in the JQL. A 200 that
is not preceded by those is not a measurement.

⚠ THE LAYOUTS ARE RE-STATED BY HAND rather than imported from the sibling probe. This script must be
able to say "no pinned layout accepts these bytes" without borrowing another measurement's authority
for it.
"""

import csv
import io
import statistics
import sys
import urllib.error
import urllib.parse
import urllib.request
from datetime import datetime, timezone

HOST = "https://jira.atlassian.com"
VIEW = "jira.issueviews:searchrequest-csv-all-fields"
TIMEOUT = 90

UPDATED_COLUMN = "Updated"
CREATED_COLUMN = "Created"

PINNED_LAYOUTS = ["%d/%b/%y %I:%M %p", "%d/%b/%Y %I:%M %p"]


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
    return (
        f"{host}/sr/{view}/temp/SearchRequest.csv"
        f"?jqlQuery={urllib.parse.quote(jql)}&tempMax={limit}"
    )


def parse(raw):
    for layout in PINNED_LAYOUTS:
        try:
            return datetime.strptime(raw, layout).replace(tzinfo=timezone.utc)
        except ValueError:
            continue
    return None


def main():
    print("── NEGATIVE CONTROLS (a CSV 200 here invalidates everything below) ──")
    controls = [
        ("fabricated host",
         export_url("https://jira-talyvor-not-a-real-host.atlassian.com", VIEW,
                    "project = JRASERVER", 3)),
        ("fabricated view on the real host",
         export_url(HOST, "jira.issueviews:searchrequest-talyvor-not-a-view",
                    "project = JRASERVER", 3)),
        ("fabricated project in the JQL",
         export_url(HOST, VIEW, "project = TALYVORNOTAPROJECT", 3)),
    ]
    for name, url in controls:
        status, ctype, body = fetch(url)
        print(f"  {name:38s} ⇒ HTTP {status} {ctype.split(';')[0]}")
        if status == 200 and "csv" in ctype:
            sys.exit("NEGATIVE CONTROL ANSWERED CSV 200 — the real 200 proves nothing. Stop.")

    print("\n── THE REAL EXPORT ──")
    # ⚠ THE ORDERING IS PART OF THE MEASUREMENT AND THE FIRST ONE I TRIED WAS WRONG. `ORDER BY
    # updated DESC` selects the FRESHEST rows in the project, and it answered "median 2.7 days
    # stale" — a fact about the query, not about a backlog. Ordering by KEY is neutral with respect
    # to the column being measured, which is the only way this number means anything.
    url = export_url(HOST, VIEW, "project = JRASERVER ORDER BY key ASC", 200)
    status, ctype, body = fetch(url)
    print(f"  200 issues by key ASC (neutral w.r.t. Updated) ⇒ HTTP {status} "
          f"{ctype.split(';')[0]} {len(body)} bytes")
    if status != 200 or "csv" not in ctype:
        sys.exit("the real request did not answer CSV 200 — nothing measured")

    rows = list(csv.reader(io.StringIO(body.decode("utf-8", "replace"))))
    header, data = rows[0], rows[1:]
    print(f"  {len(header)} columns, {len(data)} rows")

    for col in (UPDATED_COLUMN, CREATED_COLUMN):
        n = header.count(col)
        print(f"  header {col!r:10s} occurrences: {n}")
        if n == 0:
            sys.exit(f"{col!r} is not in this export — the premise is wrong, stop")
    if header.count(UPDATED_COLUMN) > 1:
        sys.exit("Updated is MULTI-valued — `get` would be wrong, as it was for Labels (#79). Stop.")

    ui = header.index(UPDATED_COLUMN)
    ci = header.index(CREATED_COLUMN)

    print("\n── THE BYTES ──")
    for r in data[:3]:
        print(f"  Updated {r[ui]!r}   Created {r[ci]!r}")

    print("\n── HOW STALE IS A REAL BACKLOG (the size of the error) ──")
    now = datetime.now(timezone.utc)
    ages, unparsed = [], 0
    for r in data:
        t = parse(r[ui])
        if t is None:
            unparsed += 1
            continue
        ages.append((now - t).total_seconds() / 86400.0)
    if unparsed:
        print(f"  ⚠ {unparsed} of {len(data)} Updated values no pinned layout accepts")
    if not ages:
        sys.exit("no Updated value parsed — nothing measured")
    ages.sort()
    print(f"  days since Updated   min {ages[0]:.1f} · median {statistics.median(ages):.1f} · "
          f"max {ages[-1]:.1f}   (n={len(ages)})")
    print(f"  Track records 0.0 for every one of them — the import instant.")
    older = sum(1 for a in ages if a > 1.0)
    print(f"  rows whose real Updated is more than a DAY old: {older} of {len(ages)} "
          f"({100.0*older/len(ages):.1f}%) — each of these is printed as "
          f"'updated just now' and sorts above genuinely recent work")


if __name__ == "__main__":
    main()
