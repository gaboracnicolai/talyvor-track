#!/usr/bin/env python3
"""w34-jira-csv-labels-probe.py — the provenance behind the repeated-`Labels` fix in csv.go.

A real Jira "csv-all-fields" export emits ONE COLUMN PER VALUE for a multi-value field, every one of
them under the SAME header, padding narrower rows out with empties. buildIndex was a map, so the
columns collapsed to whichever index was assigned last — the padding, on every issue that is not the
widest in the result set.

    python3 scripts/w34-jira-csv-labels-probe.py

⚠ DELIBERATELY NOT IN CI, for the same reason as w34-jira-csv-export-probe.py and
w34-linear-schema-probe.py: a gate that depends on a third party's uptime is a gate people re-run
rather than read. What CI holds is the local contract in internal/importer/jira_csv_labels_test.go —
the measured header shape and the real label values, hardcoded there. This is the evidence behind
those literals.

⚠ WHAT THIS DOES AND DOES NOT PROVE. It proves the repetition on a REAL export, and it proves the
column count is a property of the RESULT SET rather than of the view (three queries, three widths).
It does NOT prove another tenant's export repeats — but the direction of that uncertainty is safe
here, because a single `Labels` column is read identically by the old code and the new.
"""

import collections
import csv
import io
import sys
import urllib.error
import urllib.parse
import urllib.request

HOST = "https://jira.atlassian.com"
VIEW = "jira.issueviews:searchrequest-csv-all-fields"
TIMEOUT = 90

# The three result sets measured on 2026-08-09, and the widths they answered with.
QUERIES = [
    ("project = JRASERVER AND labels IS NOT EMPTY ORDER BY created DESC", 6, 15),
    ("project = JRASERVER AND labels IS NOT EMPTY ORDER BY created ASC", 3, 2),
    ("project = JRASERVER ORDER BY created DESC", 5, 1),
]


def fetch(url):
    req = urllib.request.Request(url, headers={"User-Agent": "talyvor-w34-labels-probe"})
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT) as r:
            return r.status, r.headers.get("Content-Type", ""), r.read()
    except urllib.error.HTTPError as e:
        return e.code, e.headers.get("Content-Type", ""), e.read()
    except Exception as e:  # DNS failure, TLS failure, timeout
        return 0, type(e).__name__, str(e).encode()


def export_url(host, view, jql, limit):
    return (f"{host}/sr/{view}/temp/SearchRequest.csv"
            f"?jqlQuery={urllib.parse.quote(jql)}&tempMax={limit}")


def main():
    # ── NEGATIVE CONTROLS FIRST, so a 200 is not read as a blanket answer ────────────────
    print("negative controls")
    ok = True
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

    # ── THE MEASUREMENT ─────────────────────────────────────────────────────────────────
    print("\nthe same view, three result sets — the width tracks the WIDEST ROW, not the view:")
    total_present = total_read = 0
    for jql, limit, measured_width in QUERIES:
        status, ctype, body = fetch(export_url(HOST, VIEW, jql, limit))
        if status != 200 or "text/csv" not in ctype:
            print(f"  the export did not answer as CSV for {jql!r} — nothing measured")
            return 1
        rows = list(csv.reader(io.StringIO(body.decode("utf-8-sig"))))
        header, data = rows[0], rows[1:]
        counts = collections.Counter(h.strip().lower() for h in header)
        width = counts["labels"]
        flag = "" if width == measured_width else f"  ⚠ MOVED (2026-08-09 measured {measured_width})"
        print(f"  {len(header):4d} columns · {len(data)} issues · {width:3d} x 'Labels' "
              f"· {counts['comment']:3d} x 'Comment'{flag}")

        # What the OLD code read (last occurrence) against what the export actually carries.
        label_cols = [i for i, h in enumerate(header) if h.strip().lower() == "labels"]
        if not label_cols:
            continue
        for r in data:
            present = [r[i] for i in label_cols if i < len(r) and r[i].strip()]
            last = r[label_cols[-1]] if label_cols[-1] < len(r) else ""
            total_present += len(present)
            total_read += len([p for p in last.split(",") if p.strip()])

    print(f"\nlabel values the exports carry:                        {total_present}")
    print(f"label values the pre-merge mapper would have imported: {total_read}")
    print("\nthe fix reads EVERY column of that name (columnIndex.getAll), so both figures agree.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
