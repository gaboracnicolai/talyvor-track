#!/usr/bin/env python3
"""w34-jira-api-created-probe.py — does the SHIPPED Jira API client's `fields` list carry the
issue's OPENING time, and what does its absence cost?

WHY THIS EXISTS SEPARATELY FROM scripts/w34-jira-csv-created-probe.py. That probe measured the CSV
transport and its finding merged as #83. The API transport is a different mapper reaching a
different SQL statement (UpsertByIdentifier, not issue.Store.Create), so it inherits NONE of that
evidence — #83's own handoff says the API halves "each need their own wire measurement". Re-running
a sibling's probe and quoting its numbers would be borrowing provenance it never gathered.

⚠ THIS ONE REACHES THE ENDPOINT THE CODE ACTUALLY CALLS, WHICH #75's CAVEAT DID NOT.
#75 recorded that this package's "measured against a real Jira" provenance is v2 / Server-DC
(jira.atlassian.com) while the shipped client POSTs /rest/api/3/search/jql against Jira CLOUD.
hibernate.atlassian.net is a real Jira Cloud site that answers that exact endpoint ANONYMOUSLY, so
this probe measures BOTH: Cloud v3 (the shipped path) and Server-DC v2 (the package's historical
provenance, kept because the two serialise the offset DIFFERENTLY and that difference is the
interesting part).

NEGATIVE CONTROLS RUN FIRST. If any control answers the way a success does, the probe FAILS rather
than reporting numbers — an instrument that cannot say "no" cannot say "yes" either.
"""

import json
import sys
import urllib.error
import urllib.request
from datetime import datetime, timezone

CLOUD = "https://hibernate.atlassian.net"  # real Jira CLOUD — the shipped v3 endpoint
SERVER = "https://jira.atlassian.com"      # Jira Server/DC — #74/#75's historical provenance
V3_PATH = "/rest/api/3/search/jql"         # MUST equal jira.go's jiraSearchPath
V2_PATH = "/rest/api/2/search"

TIMEOUT = 30
failures = []


def post(base, path, body, timeout=TIMEOUT):
    req = urllib.request.Request(
        base + path,
        data=json.dumps(body).encode(),
        headers={"Content-Type": "application/json", "Accept": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.status, r.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()
    except Exception as e:  # DNS failure, refused connection, timeout
        return None, str(e).encode()


def get(url, timeout=TIMEOUT):
    try:
        with urllib.request.urlopen(url, timeout=timeout) as r:
            return r.status, r.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()
    except Exception as e:
        return None, str(e).encode()


def control(name, ok, detail):
    print(f"  control {name:<44} {'PASS' if ok else 'FAIL'}  {detail}")
    if not ok:
        failures.append(name)


print("── NEGATIVE CONTROLS (an instrument that cannot say no cannot say yes)")

code, body = post("https://hibernate-zzz-nope.atlassian.invalid", V3_PATH,
                  {"jql": "project = HHH", "maxResults": 1}, timeout=15)
control("fabricated host resolves nothing", code is None, f"code={code}")

code, body = post(CLOUD, "/rest/api/3/search/jql-zzz-nope",
                  {"jql": "project = HHH", "maxResults": 1})
control("fabricated path on the real host", code == 404, f"HTTP {code}")

code, body = post(CLOUD, V3_PATH, {"jql": "project = ZZZNOPE", "maxResults": 1})
n_fab = len(json.loads(body).get("issues", [])) if code == 200 else -1
control("fabricated project ⇒ ZERO issues", code == 200 and n_fab == 0, f"HTTP {code} issues={n_fab}")
# ⚠ MEASURED ASYMMETRY, AND THE FIRST FORM OF THIS CONTROL FAILED ON IT. On Server v2 a fabricated
# project is HTTP 400 ("The value 'ZZZNOPE' does not exist for the field 'project'"); on Cloud v3
# — the endpoint the code calls — it is HTTP 200 with an EMPTY result set. A typo'd project key in
# an import JQL therefore imports NOTHING and says nothing, on the shipped path only. Kept as a
# control because empty-vs-populated still discriminates, and recorded because it is a fact about
# the shipped transport that nobody in this item had measured.

code, body = post(CLOUD, V3_PATH, {"jql": "this is not jql ((", "maxResults": 1})
control("malformed JQL is rejected", code == 400, f"HTTP {code}")

# THE DECISIVE CONTROL. "created came back" only means "the fields list carries it" if the server
# actually honours the list. Ask for summary ALONE: if created still arrives, this probe proves
# nothing about jiraFields.
code, body = post(CLOUD, V3_PATH,
                  {"jql": "project = HHH ORDER BY created DESC", "maxResults": 1,
                   "fields": ["summary"]})
keys = sorted(json.loads(body)["issues"][0]["fields"].keys()) if code == 200 else []
control("fields list is HONOURED (summary alone ⇒ only summary)",
        code == 200 and keys == ["summary"], f"HTTP {code} keys={keys}")

# ⚠ RECORDED AS A FACT ABOUT THE SERVER, NOT COUNTED AS A CONTROL: an unknown field name is
# IGNORED, not rejected. So a misspelling in jiraFields cannot be caught by an error code — the
# only thing that catches it is the value coming back, which is what the control above licenses.
code, body = post(CLOUD, V3_PATH,
                  {"jql": "project = HHH", "maxResults": 1, "fields": ["summary", "notafield_zzz"]})
print(f"  fact    unknown field name is SILENTLY IGNORED       HTTP {code} "
      f"(so a typo in jiraFields is invisible at the wire)")

if failures:
    print(f"\nFAILED: {len(failures)} control(s) did not behave: {failures}")
    print("Refusing to report measurements from an instrument that cannot say no.")
    sys.exit(1)

print("\n── MEASUREMENT 1: does the SHIPPED endpoint serve `created`, and in what bytes?")
for label, base, is_v3 in (("Cloud  v3 (SHIPPED path)", CLOUD, True),
                           ("Server v2 (#74/#75 provenance)", SERVER, False)):
    if is_v3:
        code, body = post(base, V3_PATH,
                          {"jql": "project = HHH ORDER BY created DESC", "maxResults": 3,
                           "fields": ["summary", "created", "resolutiondate", "duedate"]})
    else:
        code, body = get(base + V2_PATH +
                         "?jql=project%3DJRASERVER%20ORDER%20BY%20created%20DESC"
                         "&maxResults=3&fields=summary,created,resolutiondate,duedate")
    if code != 200:
        print(f"  {label}: HTTP {code} — NOT MEASURED")
        continue
    issues = json.loads(body)["issues"]
    print(f"  {label}: HTTP 200, {len(issues)} issues")
    for it in issues[:2]:
        print(f"      {it['key']:<16} created={it['fields'].get('created')!r}")

print("\n── MEASUREMENT 2: what the absent field costs, on 100 REAL RESOLVED Cloud issues")
# Track computes EXTRACT(EPOCH FROM completed_at - created_at)/3600. With created_at defaulted to
# the import instant, that is (a past resolutiondate) − NOW.
code, body = post(CLOUD, V3_PATH,
                  {"jql": "project = HHH AND resolutiondate IS NOT NULL ORDER BY resolutiondate DESC",
                   "maxResults": 100, "fields": ["created", "resolutiondate"]})
if code != 200:
    print(f"  HTTP {code} — NOT MEASURED")
    sys.exit(1)

LAYOUTS_NOTE = "parsed with %Y-%m-%dT%H:%M:%S.%f%z, the shape jiraTimeLayouts[0] pins"
now = datetime.now(timezone.utc)
true_h, track_h, ages = [], [], []
for it in json.loads(body)["issues"]:
    c = datetime.strptime(it["fields"]["created"], "%Y-%m-%dT%H:%M:%S.%f%z")
    r = datetime.strptime(it["fields"]["resolutiondate"], "%Y-%m-%dT%H:%M:%S.%f%z")
    true_h.append((r - c).total_seconds() / 3600)
    track_h.append((r - now).total_seconds() / 3600)
    ages.append((now - c).days)


def stats(v):
    v = sorted(v)
    return v[0], v[len(v) // 2], v[-1]


n = len(true_h)
print(f"  n = {n} real resolved issues ({LAYOUTS_NOTE})")
print("  age at import (days since created)   min %d · median %d · max %d" % stats(ages))
print("  TRUE cycle time (resolved−created,h) min %.1f · median %.1f · max %.1f" % stats(true_h))
print("  what Track computes instead (h)      min %.1f · median %.1f · max %.1f" % stats(track_h))
neg = sum(1 for h in track_h if h < 0)
print(f"  NEGATIVE under today's code          {neg} of {n}")
print(f"  correct answers today                {sum(1 for a, b in zip(true_h, track_h) if abs(a - b) < 1)} of {n}")
