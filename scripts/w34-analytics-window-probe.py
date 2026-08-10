#!/usr/bin/env python3
"""w34-analytics-window-probe.py — what a REAL imported backlog looks like to the report that
every date merge on W3.4 was justified by.

THE QUESTION NOBODY ASKED.  #83/#84/#85 landed the provider's `created_at` on all four transports
because analytics computes `EXTRACT(EPOCH FROM completed_at - created_at)` and a defaulted
created_at made that NEGATIVE (measured: -2400.0 h).  Every one of those merges verified itself
through analytics.GetTimeToResolution.  Every one of them used a fixture dated 200 days ago, and
internal/importer/jira_csv_created_job_test.go:31 SAYS WHY IN WRITING:

    "both analytics queries filter `created_at > NOW() - INTERVAL '1 day' * $2` with $2 clamped to
     365, so a hardcoded date would silently age out of the window"

The window was known.  It was designed AROUND — the fixtures were computed so they would land
inside it.  What was never measured is where a REAL export's issues land.  That is this probe.

WHAT IT MEASURES, on the endpoint the shipped client actually POSTs to (`/rest/api/3/search/jql`,
Jira CLOUD, anonymous — the host #84 found), for real RESOLVED issues:

    · the age of `created` in days, per issue
    · how many fall INSIDE  the 365-day cap analytics clamps every request to
    · how many fall OUTSIDE it — i.e. rows that are in the database, carry a real completion time,
      and cannot appear in the report at any window a caller is allowed to ask for
    · the TRUE median cycle time over all of them, against the median over the visible subset

⚠ maxWindowDays IS READ OUT OF THE SHIPPED SOURCE, not copied.  #76's rule: a probe that hardcodes
the number it is testing against compares a constant to itself.  If the extraction finds nothing
this FAILS rather than silently probing 365.

⚠ EVERY CONTROL IS CHECKED BEFORE ANY NUMBER IS PRINTED and the script exits non-zero if one
answers the way a success does — a 200 indistinguishable from a control's 200 measures nothing.

    python3 scripts/w34-analytics-window-probe.py

⚠ DELIBERATELY NOT IN CI (#76's reason): a gate that depends on a third party's uptime gets re-run
rather than read.  CI holds the LOCAL contract; this is the evidence behind it.
"""

import datetime as dt
import json
import pathlib
import re
import statistics
import sys
import urllib.error
import urllib.request

HOST = "https://hibernate.atlassian.net"
PROJECT = "HHH"
PATH = "/rest/api/3/search/jql"
WANT = 2000  # enough pages to be a distribution rather than an anecdote

ROOT = pathlib.Path(__file__).resolve().parent.parent
ENGINE = ROOT / "internal" / "analytics" / "engine.go"


def fail(msg):
    print(f"FAIL: {msg}", file=sys.stderr)
    sys.exit(1)


def max_window_days():
    """Read maxWindowDays out of the shipped engine rather than trusting a copy here."""
    if not ENGINE.exists():
        fail(f"{ENGINE} not found — cannot read the shipped window cap")
    m = re.search(r"^\s*maxWindowDays\s*=\s*(\d+)\s*$", ENGINE.read_text(), re.M)
    if not m:
        fail("maxWindowDays not found in internal/analytics/engine.go — extraction is stale")
    return int(m.group(1))


def post(host, path, body, timeout=40):
    req = urllib.request.Request(
        host + path,
        data=json.dumps(body).encode(),
        headers={"Content-Type": "application/json", "Accept": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=timeout) as r:
        return r.status, json.loads(r.read().decode())


def controls():
    """A 200 is only evidence if a request that SHOULD fail does not also return 200."""
    try:
        post("https://talyvor-not-a-real-jira-site.atlassian.net", PATH, {"jql": "order by created"})
        fail("CONTROL 1 (fabricated host) answered instead of failing")
    except Exception:
        pass
    try:
        st, _ = post(HOST, "/rest/api/3/search/talyvor-not-a-path", {"jql": "order by created"})
        fail(f"CONTROL 2 (fabricated path on the real host) answered {st}")
    except urllib.error.HTTPError as e:
        if e.code == 200:
            fail("CONTROL 2 answered 200")
    try:
        st, _ = post(HOST, PATH, {"jql": "talyvorTotallyFakeField = 1", "maxResults": 1})
        fail(f"CONTROL 3 (nonsense JQL) answered {st} instead of 400")
    except urllib.error.HTTPError as e:
        if e.code != 400:
            fail(f"CONTROL 3 answered {e.code}, expected 400")
    print("controls: fabricated host refused · fabricated path refused · nonsense JQL 400")


def fetch():
    issues, token = [], None
    while len(issues) < WANT:
        body = {
            "jql": f"project = {PROJECT} AND resolutiondate IS NOT EMPTY ORDER BY created DESC",
            "maxResults": 100,
            "fields": ["created", "resolutiondate"],
        }
        if token:
            body["nextPageToken"] = token
        st, page = post(HOST, PATH, body)
        if st != 200:
            fail(f"search answered {st}")
        got = page.get("issues", [])
        if not got:
            break
        issues.extend(got)
        token = page.get("nextPageToken")
        if page.get("isLast") or not token:
            break
    return issues[:WANT]


def parse(ts):
    # Jira Cloud: "2024-01-31T12:34:56.000+0000"
    return dt.datetime.strptime(ts, "%Y-%m-%dT%H:%M:%S.%f%z")


def main():
    cap = max_window_days()
    print(f"shipped cap read from internal/analytics/engine.go: maxWindowDays = {cap}")
    controls()

    issues = fetch()
    if not issues:
        fail("zero issues returned — nothing measured")
    now = dt.datetime.now(dt.timezone.utc)

    ages, cycles, visible_cycles = [], [], []
    for it in issues:
        f = it.get("fields") or {}
        c, r = f.get("created"), f.get("resolutiondate")
        if not c or not r:
            continue
        created, resolved = parse(c), parse(r)
        age = (now - created).total_seconds() / 86400.0
        cyc = (resolved - created).total_seconds() / 3600.0
        ages.append(age)
        cycles.append(cyc)
        if age < cap:
            visible_cycles.append(cyc)

    n = len(ages)
    inside = len(visible_cycles)
    print(f"\nreal resolved issues sampled ({HOST}, project {PROJECT}): {n}")
    print(f"  created-age in days: median {statistics.median(ages):.0f} · "
          f"min {min(ages):.0f} · max {max(ages):.0f}")
    print(f"  INSIDE  the {cap}-day window (visible to GetTimeToResolution): {inside}")
    print(f"  OUTSIDE it — real rows, real completion times, unreachable at ANY "
          f"window a caller may ask for: {n - inside}  ({100.0*(n-inside)/n:.1f}%)")
    print(f"\n  TRUE median time to resolution over all {n}: {statistics.median(cycles):.1f} h")
    if visible_cycles:
        print(f"  median over the visible {inside}: {statistics.median(visible_cycles):.1f} h")
    else:
        print("  median over the visible subset: THERE IS NO VISIBLE SUBSET — the query matches "
              "zero rows and COALESCE(...,0) renders that as 0.0 hours")


if __name__ == "__main__":
    main()
