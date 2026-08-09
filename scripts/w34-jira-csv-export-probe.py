#!/usr/bin/env python3
"""w34-jira-csv-export-probe.py — the provenance behind internal/importer/jira_csv_dates.go.

W3.4 taught the *API* transports two date fields (#74 Jira, #77 Linear) against providers this
environment cannot authenticate to — item (3) is still open, and NO `*_api` import has ever been
proven end to end against a real tenant. The CSV transport needs no credentials, so it is the half a
customer can actually run today, and jiraRowMapper read FIVE fields: title, description, status,
priority, labels.

This script re-measures what a real Jira CSV export actually contains, which is what decided both
the column spellings and the date layout that merge pinned.

    python3 scripts/w34-jira-csv-export-probe.py

⚠ IT IS DELIBERATELY NOT IN CI, for the same reason as w34-linear-schema-probe.py: a gate that
depends on a third party's uptime is a gate people re-run rather than read. What CI holds is the
local contract in internal/importer/jira_csv_dates_test.go — the measured spellings and the measured
layout, both hardcoded there. This is the evidence behind those literals.

⚠ THE INSTANCE IS SERVER/DC AND THE SHIPPED API CLIENT CALLS CLOUD v3 — #75's second finding, and it
applies to the API transport, NOT to this one. The CSV *export view* is a Jira feature, not a REST
API version, and the file this script downloads is the same artifact a user gets from "Export →
CSV". What is genuinely unproven is whether ANOTHER instance formats its dates the same way: the
rendering uses the instance's look-and-feel date format, which is a per-instance preference. That is
precisely why the importer REPORTS a date shape it cannot parse instead of writing nil.
"""

import csv
import io
import sys
import urllib.error
import urllib.parse
import urllib.request

HOST = "https://jira.atlassian.com"
VIEW = "jira.issueviews:searchrequest-csv-all-fields"
TIMEOUT = 90

# The two columns the importer reads, and one it must NOT read (a neighbouring date column, so a
# mapper that grabs anything containing "date" is visible here as well as in the Go test).
WANTED = ["Due Date", "Resolved"]
MUST_NOT_READ = "Custom field (Target Release Date)"


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
    ok = True

    # ── NEGATIVE CONTROLS FIRST, so a 200 is not read as a blanket answer ──────────────
    print("negative controls")
    for label, url in [
        ("fabricated host", export_url("https://jira-talyvor-not-a-host.atlassian.com", VIEW, "project = JRASERVER", 1)),
        ("fabricated view on the real host", export_url(HOST, "jira.issueviews:searchrequest-talyvor-not-a-view", "project = JRASERVER", 1)),
        ("fabricated project in the JQL", export_url(HOST, VIEW, "project = TALYVORNOTAPROJECT", 1)),
    ]:
        status, ctype, _ = fetch(url)
        good = status != 200 or "text/csv" not in ctype
        print(f"  {'OK ' if good else 'BAD'}  {label:34s} => HTTP {status} {ctype}")
        ok &= good
    if not ok:
        print("\nA control answered 200 text/csv. Every measurement below would be unreadable — stopping.")
        return 1

    # ── THE MEASUREMENT ───────────────────────────────────────────────────────────────
    jql = ("project = JRASERVER AND resolution IS NOT EMPTY AND duedate IS NOT EMPTY "
           "ORDER BY resolved DESC")
    status, ctype, body = fetch(export_url(HOST, VIEW, jql, 4))
    print(f"\nexport => HTTP {status} {ctype} {len(body)} bytes")
    if status != 200 or "text/csv" not in ctype:
        print("  the export did not answer as CSV — nothing measured")
        return 1

    rows = list(csv.reader(io.StringIO(body.decode("utf-8-sig"))))
    header, data = rows[0], rows[1:]
    print(f"header: {len(header)} columns · {len(data)} issues\n")

    for name in WANTED + [MUST_NOT_READ]:
        mark = "reads" if name in WANTED else "MUST NOT read"
        print(f"  {'present' if name in header else 'ABSENT ':8s} {name!r}  ({mark})")
        if name in WANTED and name not in header:
            ok = False

    print("\nvalues, verbatim — these are the bytes jiraCSVTimeLayouts is pinned from:")
    idx = {h: i for i, h in enumerate(header)}
    seen = []
    for r in data:
        cells = {n: r[idx[n]] for n in WANTED + ["Issue key", "Status", "Resolution"] if n in idx}
        print("  " + " · ".join(f"{k}={v!r}" for k, v in cells.items()))
        seen += [cells.get(n, "") for n in WANTED]

    # The layout the Go side pins, restated here in strptime terms so the two can be compared by eye.
    # (%-d/%b/%Y %-I:%M %p — a non-padded day and hour, a three-letter month, an AM/PM marker.)
    print("\nGo layout pinned in internal/importer/jira_csv_dates.go:  \"2/Jan/2006 3:04 PM\"")
    import datetime
    for v in [s for s in seen if s]:
        try:
            datetime.datetime.strptime(v, "%d/%b/%Y %I:%M %p")
            print(f"  OK   {v!r}")
        except ValueError:
            print(f"  ⚠ REFUSED by the pinned shape: {v!r} — the layout list needs re-deriving")
            ok = False

    print("\n" + ("all measurements agree with what is pinned in the code"
                  if ok else "SOMETHING MOVED — re-derive before trusting the importer's layouts"))
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
