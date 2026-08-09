#!/usr/bin/env python3
"""w34-jira-csv-created-probe.py — the provenance behind internal/importer/jira_csv_created.go.

W3.4 has now taught jiraRowMapper eight fields (title, description, status, priority, labels, due
date, resolved, resolution). Every one of them describes the issue. NONE of them describes WHEN THE
ISSUE WAS OPENED — `created_at` on the Track row is whatever NOW() was when the import ran, because
neither issue.Store.Create's INSERT nor the importer's upsert names the column and Postgres defaults
it.

That is not a missing column on its own. It is a WRONG NUMBER, because #74 and #78 deliberately
landed `completed_at` from the provider: Track's analytics computes time-to-resolution as
`EXTRACT(EPOCH FROM completed_at - created_at)/3600`, so a provider completion time in the past
minus an import instant NOW is NEGATIVE.

This script measures what a real Jira CSV export actually carries in the `Created` column, and how
far apart Created and Resolved really are on a real instance — which is the size of the error.

    python3 scripts/w34-jira-csv-created-probe.py

⚠ DELIBERATELY NOT IN CI, for the same reason as the sibling probes: a gate that depends on a third
party's uptime is one people re-run rather than read. CI holds the LOCAL contract in
internal/importer/jira_csv_created_test.go — the measured spelling and the measured layout, both
hardcoded there. This is the evidence behind those literals.

⚠ NEGATIVE CONTROLS FIRST, and the script FAILS rather than proceeds if one of them answers 200: a
fabricated host, a fabricated view name on the real host, and a fabricated project in the JQL. A 200
that is not preceded by those is not a measurement.

⚠ THE INSTANCE IS SERVER/DC (#75's second finding). That applies to the REST API transport, not to
this one — the CSV export VIEW is a Jira feature rather than a REST version. What is genuinely
per-instance is the DATE RENDERING, which is a look-and-feel preference, and that is exactly why the
importer REPORTS a shape it cannot parse instead of writing a silent nil.
"""

import csv
import io
import statistics
import sys
import urllib.error
import urllib.parse
import urllib.request
from datetime import datetime

HOST = "https://jira.atlassian.com"
VIEW = "jira.issueviews:searchrequest-csv-all-fields"
TIMEOUT = 90

CREATED_COLUMN = "Created"
RESOLVED_COLUMN = "Resolved"

# The layouts jira_csv_dates.go already pins, re-stated here by hand rather than imported from the
# sibling probe: this script must be able to say "no pinned layout accepts these bytes" without
# borrowing another measurement's authority for it.
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


def main():
    print("── NEGATIVE CONTROLS (a 200 here invalidates everything below) ──")
    controls = [
        (
            "fabricated host",
            export_url("https://jira-talyvor-not-a-real-host.atlassian.com", VIEW,
                       "project = JRASERVER", 3),
        ),
        (
            "fabricated view on the real host",
            export_url(HOST, "jira.issueviews:searchrequest-talyvor-not-a-view",
                       "project = JRASERVER", 3),
        ),
        (
            "fabricated project in the JQL",
            export_url(HOST, VIEW, "project = TALYVORNOTAPROJECT", 3),
        ),
    ]
    for name, url in controls:
        status, ctype, body = fetch(url)
        print(f"  {name:38s} ⇒ HTTP {status} {ctype.split(';')[0]}")
        if status == 200 and "csv" in ctype:
            sys.exit(f"NEGATIVE CONTROL ANSWERED CSV 200 — the real 200 proves nothing. Stop.")

    print("\n── THE REAL EXPORT ──")
    url = export_url(HOST, VIEW, "project = JRASERVER AND resolution IS NOT EMPTY ORDER BY created DESC", 200)
    status, ctype, body = fetch(url)
    print(f"  resolved issues, tempMax=200            ⇒ HTTP {status} {ctype.split(';')[0]} "
          f"{len(body)} bytes")
    if status != 200 or "csv" not in ctype:
        sys.exit("the real request did not answer CSV 200 — nothing measured")

    rows = list(csv.reader(io.StringIO(body.decode("utf-8", "replace"))))
    header, data = rows[0], rows[1:]
    print(f"  {len(header)} columns, {len(data)} rows")

    for col in (CREATED_COLUMN, RESOLVED_COLUMN):
        n = header.count(col)
        print(f"  header {col!r:12s} occurrences: {n}")
        if n == 0:
            sys.exit(f"{col!r} is not in this export — the premise is wrong, stop")

    ci = header.index(CREATED_COLUMN)
    ri = header.index(RESOLVED_COLUMN)

    print("\n── THE BYTES ──")
    for r in data[:5]:
        print(f"  Created={r[ci]!r}  Resolved={r[ri]!r}")

    print("\n── WHICH PINNED LAYOUT ACCEPTS THEM ──")
    sample = data[0][ci]
    for lay in PINNED_LAYOUTS:
        try:
            datetime.strptime(sample, lay)
            print(f"  {lay!r} ACCEPTS {sample!r}")
        except ValueError as e:
            print(f"  {lay!r} refuses {sample!r} ({e})")
    for lay in ("%Y-%m-%dT%H:%M:%S%z", "%Y-%m-%d"):
        try:
            datetime.strptime(sample, lay)
            print(f"  ⚠ {lay!r} ACCEPTS {sample!r} — unexpected")
        except ValueError:
            print(f"  {lay!r} refuses {sample!r}  (RFC3339-shaped layouts do not apply)")

    print("\n── THE SIZE OF THE ERROR: how old is the work being imported ──")
    ages, spans = [], []
    now = datetime.now()
    parsed = 0
    for r in data:
        c, res = r[ci], r[ri]
        dc = dres = None
        for lay in PINNED_LAYOUTS:
            try:
                dc = datetime.strptime(c, lay)
                break
            except ValueError:
                pass
        for lay in PINNED_LAYOUTS:
            try:
                dres = datetime.strptime(res, lay)
                break
            except ValueError:
                pass
        if dc is None or dres is None:
            continue
        parsed += 1
        ages.append((now - dc).days)
        spans.append((dres - dc).total_seconds() / 3600.0)
    print(f"  rows with BOTH dates parsed: {parsed}/{len(data)}")
    if parsed:
        print(f"  age of the issue at import (days since Created): "
              f"min {min(ages)} · median {int(statistics.median(ages))} · max {max(ages)}")
        print(f"  TRUE cycle time Resolved-Created (hours): "
              f"min {min(spans):.1f} · median {statistics.median(spans):.1f} · max {max(spans):.1f}")
        print(f"  ⚠ Track would instead compute completed_at - NOW(), i.e. roughly:")
        neg = [-(now - dres).total_seconds() / 3600.0
               for r in data
               for dres in [_try(r[ri])] if dres]
        if neg:
            print(f"     min {min(neg):.1f}h · median {statistics.median(neg):.1f}h · max {max(neg):.1f}h")
            print(f"     — every one NEGATIVE, and {sum(1 for x in neg if x < 0)}/{len(neg)} are.")


def _try(s):
    for lay in PINNED_LAYOUTS:
        try:
            return datetime.strptime(s, lay)
        except ValueError:
            pass
    return None


if __name__ == "__main__":
    main()
