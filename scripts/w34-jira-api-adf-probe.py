#!/usr/bin/env python3
"""w34-jira-api-adf-probe.py — the provenance behind internal/importer/adf_attrs.go.

Jira Cloud sends `description` as an Atlassian Document Format tree. walkADF read `text` nodes and
NOTHING ELSE, so every node that keeps its content in `attrs` — a link, a mention, an emoji, an
attachment — contributed nothing and reported nothing. The prose that survives reads as broken:
"Follow up to   - remove the deprecated stuff" (HHH-20742, verbatim). And `description` is the
product's ONLY full-text index (issue.Store.Search is to_tsvector over title || ' ' || description),
so an issue whose distinguishing content is a link is unfindable by that link.

⚠ THIS MEASURES THE ENDPOINT THE CODE ACTUALLY CALLS, POST /rest/api/3/search/jql, against a real
Jira CLOUD site anonymously — the site #84 found and #87 measured `resolution` on.

⚠ IT READS THE SHIPPED SOURCE rather than a copy (#76's rule): jiraSearchPath, jiraFields, AND the
two pinned tables in adf_attrs.go. If an extraction finds nothing it FAILS rather than probing a
hardcoded string, and if a pinned attribute is not the one the live nodes carry it FAILS too — that
is the control that keeps `inlineCard → attrs.url` a measurement instead of a memory.

⚠ AND IT FAILS ON AN UNPINNED ATTRS-BORNE NODE TYPE. adf_attrs.go is deliberately two pinned tables
rather than one general "report any leaf with attrs" rule, because that rule was measured and is
WRONG (see below). The cost of pinning is that a node type nobody here has seen is silent. THIS
SCRIPT IS WHAT NOTICES ONE — and it is not in CI, so noticing requires running it. Stated as a
limit, not dressed up.

    python3 scripts/w34-jira-api-adf-probe.py

⚠ DELIBERATELY NOT IN CI, for #76's reason: a gate that depends on a third party's uptime is one
people re-run rather than read. CI holds the LOCAL contract — internal/importer/adf_attrs_test.go
and adf_attrs_job_test.go pin the flattening, the per-type reports and the search consumer by hand.
"""

import collections
import json
import os
import pathlib
import re
import sys
import urllib.error
import urllib.request

HOST = "https://hibernate.atlassian.net"
PROJECT = "HHH"
TIMEOUT = 120
# ⚠ THIS NUMBER WAS THE DEFECT, NOT A TUNING KNOB. It was 20 — 2,000 of the project's ~20,550 issues
# — and the script printed "unpinned attrs-borne leaf types: NONE", a sentence about the PROJECT,
# off a census that had read 9.7% of it. `blockCard` occurs ONCE in the first 3,000 and was missed
# for exactly that reason; adf_attrs.go names this script as the only thing that would notice a new
# attrs-borne type, so the bound decided what the whole pinned table could ever contain.
# 260 pages ⇒ 26,000 issues ⇒ the census reaches isLast on this project and the NONE becomes a fact
# about all of it. A full run is minutes, not seconds; this is a hand-run probe and that is the
# trade. Override for a bigger project, and read the "scanned / exhausted" line before believing a
# NONE: the script now refuses to state one as project-wide when it stopped short.
PAGES = int(os.environ.get("PROBE_PAGES", "260"))  # 100 issues per page
IMPORTER = pathlib.Path(__file__).resolve().parent.parent / "internal" / "importer"

# Attributes measured to be EDITOR IDENTITY rather than content: they appear on empty paragraphs,
# rules and empty headings, none of which loses anything. A leaf carrying only these is not a loss.
#
# ⚠ `language` IS THE THIRD, AND IT WAS FOUND BY RAISING PAGES — i.e. by the same bound that hid
# blockCard, in the other direction. Reading the project to its end reports `codeBlock` ×12 as an
# unpinned attrs-borne leaf. MEASURED whole-population (18,917 descriptions) before it was
# classified: all 12 are `{"type":"codeBlock","attrs":{"language":"java"}}` with NO content and NO
# text — an EMPTY code block. There is no code in them, so `language` names the highlighting of
# nothing. That is `localId` on an empty paragraph exactly, and pinning codeBlock into adfAttrText
# would write the word "java" into 12 users' descriptions in place of a value that does not exist.
IDENTITY_ATTRS = {"localId", "level", "language"}

# What Jira's OWN renderer is expected to put in the HTML for each pinned node type, given the
# attribute value adf_attrs.go places.
#
# ⚠ THE mention ENTRY IS NOT DECORATION AND IT IS NOT A LOOSENED CONTROL. The first draft of this
# script asserted "the rendered HTML contains attrs.text" for all three types, and mention FAILED it
# on HHH-20539: the document stores "@Steve Ebersole" and Atlassian renders
# `<a class="user-hover" …>Steve Ebersole</a>` — the "@" is DROPPED, because the HTML renders a chip
# and the sigil is the chip. That is a real difference between the two renderings and it is recorded
# here rather than papered over with a substring match that would have passed on anything.
RENDERED_AS = {
    "inlineCard": lambda v: v,          # the URL is the link text, verbatim
    "emoji": lambda v: v,               # the character, verbatim
    "mention": lambda v: v.lstrip("@"),  # the display name WITHOUT the ADF sigil
    # inlineCard's BLOCK-LEVEL twin: same attribute, same rendering. Measured on HHH-18501, the one
    # issue in 3,000 that carries one — Jira's own HTML for that document contains attrs.url
    # verbatim. It is listed here rather than assumed from the name, which is what makes the pin in
    # adf_attrs.go a measurement.
    "blockCard": lambda v: v,
}

failures = []


def control(name, ok, detail):
    print(f"  control {name:<56} {'PASS' if ok else 'FAIL'}  {detail}")
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
    """Return (search path, fields, adfAttrText, adfNoTextEquivalent) read out of the shipped
    source. Refuses to guess any of the four."""
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
            sys.exit(f"FAILED: jiraFields names {tok!r} and this script cannot resolve it.")

    m = re.search(r"var adfAttrText = map\[string\]string\{(.*?)\n\}", src, re.S)
    if not m:
        sys.exit("FAILED: could not extract adfAttrText. Refusing to probe a copy of the table.")
    attr_text = dict(re.findall(r'"([^"]+)"\s*:\s*"([^"]+)"', m.group(1)))
    if not attr_text:
        sys.exit("FAILED: adfAttrText extracted EMPTY. A probe that checks nothing passes always.")

    m = re.search(r"var adfNoTextEquivalent = map\[string\]struct\{\}\{(.*?)\n\}", src, re.S)
    if not m:
        sys.exit("FAILED: could not extract adfNoTextEquivalent.")
    no_text = set(re.findall(r'"([^"]+)"\s*:', m.group(1)))
    if not no_text:
        sys.exit("FAILED: adfNoTextEquivalent extracted EMPTY.")
    return path, fields, attr_text, no_text


def walk(node, fn):
    stack = [node]
    while stack:
        n = stack.pop()
        if not isinstance(n, dict):
            continue
        fn(n)
        for c in n.get("content") or []:
            stack.append(c)


def main():
    path, fields, attr_text, no_text = shipped()
    print(f"SHIPPED REQUEST (read from source): POST {path}")
    print(f"SHIPPED fields:              {fields}")
    print(f"SHIPPED adfAttrText:         {attr_text}")
    print(f"SHIPPED adfNoTextEquivalent: {sorted(no_text)}\n")

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
    control("the SHIPPED fields list asks for `description`", "description" in fields,
            f"fields={fields}")

    st, b = post(path, {"jql": jql, "fields": ["summary"], "maxResults": 1})
    keys = sorted(b.get("issues", [{}])[0].get("fields", {}).keys()) if st == 200 else []
    control("fields=[summary] ALONE returns ONLY summary", st == 200 and keys == ["summary"],
            f"HTTP {st} keys={keys}")

    st, b = post(path, {"jql": f"{jql} AND description IS NOT EMPTY",
                        "fields": ["description"], "maxResults": 1})
    doc = b.get("issues", [{}])[0].get("fields", {}).get("description") if st == 200 else None
    control("`description` arrives as an ADF document, not a string",
            isinstance(doc, dict) and doc.get("type") == "doc",
            f"HTTP {st} type={type(doc).__name__}")

    # ⚠ THE CONTROL THAT LICENSES THE WHOLE MAPPING TABLE. `expand: renderedFields` returns
    # ATLASSIAN'S OWN HTML for the same document. If the attribute adf_attrs.go pins is the string
    # Jira itself renders, the mapping is a measurement; if it is not, it is a guess, and this fails.
    print("\n  — each pinned node type checked against Jira's OWN rendering of the same document —")
    for node_type, attr in sorted(attr_text.items()):
        found = None
        token = None
        page = None
        cursor = None
        for _ in range(PAGES):
            body = {"jql": jql, "fields": ["description"], "maxResults": 100}
            if cursor:
                body["nextPageToken"] = cursor
            st, page = post(path, body)
            if st != 200:
                break
            for it in page.get("issues", []):
                hits = []
                walk(it["fields"].get("description") or {},
                     lambda n: hits.append(n) if n.get("type") == node_type else None)
                for h in hits:
                    v = (h.get("attrs") or {}).get(attr)
                    if isinstance(v, str) and v:
                        found, token = it["key"], v
                        break
                if found:
                    break
            if found or page.get("isLast"):
                break
            cursor = page.get("nextPageToken")
        if not found:
            control(f"{node_type}: a live node carrying attrs.{attr}", False,
                    f"no {node_type} with a string attrs.{attr} in {PAGES * 100} issues")
            continue
        st, b = post(path, {"jql": f'key = "{found}"', "fields": ["description"],
                            "expand": "renderedFields", "maxResults": 1})
        html = b.get("issues", [{}])[0].get("renderedFields", {}).get("description", "") if st == 200 else ""
        shape = RENDERED_AS.get(node_type)
        if shape is None:
            control(f"{node_type}: this script has no expectation for it", False,
                    "a pinned type with no measured rendering is a guess — add it to RENDERED_AS")
            continue
        want = shape(token)
        control(f"{node_type}: Jira RENDERS attrs.{attr} as {want[:34]!r}", want in html,
                f"{found} attrs.{attr}={token[:50]!r}")

    if failures:
        print(f"\nCONTROLS FAILED: {failures}. NO NUMBERS REPORTED — a measurement whose controls "
              "answer like its subject is not a measurement.", file=sys.stderr)
        return 1

    print(f"\nTHE CENSUS — {PAGES} pages of {PROJECT}, the shipped request shape")
    types = collections.Counter()
    leaf_identity = collections.Counter()
    unpinned = collections.Counter()
    n_issues = n_desc = n_carrying = n_empty = 0
    cursor = None
    exhausted = False   # did the census reach the END of the project, or stop on PAGES?
    for _ in range(PAGES):
        body = {"jql": jql, "fields": ["description"], "maxResults": 100}
        if cursor:
            body["nextPageToken"] = cursor
        st, page = post(path, body)
        if st != 200:
            sys.exit(f"FAILED: census page returned HTTP {st}")
        for it in page.get("issues", []):
            n_issues += 1
            desc = it["fields"].get("description")
            if not desc:
                continue
            n_desc += 1
            carried = []
            text_len = [0]

            def visit(n):
                ty = n.get("type", "?")
                types[ty] += 1
                if n.get("text"):
                    text_len[0] += len(n["text"])
                if ty in attr_text or ty in no_text:
                    carried.append(ty)
                    return
                if n.get("content") or n.get("text"):
                    return
                attrs = n.get("attrs") or {}
                if not attrs:
                    return
                if set(attrs) <= IDENTITY_ATTRS:
                    leaf_identity[ty] += 1
                    return
                unpinned[ty] += 1  # a leaf with a payload attribute and no pinned rule

            walk(desc, visit)
            if carried:
                n_carrying += 1
            if text_len[0] == 0:
                n_empty += 1
        if page.get("isLast") or not page.get("nextPageToken"):
            exhausted = True
            break
        cursor = page.get("nextPageToken")

    print(f"  issues scanned                                   {n_issues:>7,}")
    # ⚠ THE POPULATION LINE IS NOT BOOKKEEPING. Every negative below is a statement about THESE
    # issues and about no others, and the previous version of this script did not print it: it read
    # 2,000 issues and announced "unpinned attrs-borne leaf types: NONE", which reads as a fact about
    # the project. `blockCard` was one page past that bound.
    print(f"  census reached the end of {PROJECT}                    "
          f"{'YES' if exhausted else 'NO — STOPPED ON PAGES=' + str(PAGES)}")
    print(f"    with a description                             {n_desc:>7,}")
    print(f"    carrying >=1 attrs-borne node                  {n_carrying:>7,}"
          f"  ({100.0 * n_carrying / max(n_desc, 1):.1f}% of descriptions)")
    print(f"    with NO `text` node at all — flatten to \"\"      {n_empty:>7,}")
    print("  attrs-borne node occurrences (the subject of this merge):")
    for ty in sorted(set(attr_text) | no_text):
        print(f"    {ty:<14} {types[ty]:>6,}   {'placed as attrs.' + attr_text[ty] if ty in attr_text else 'REPORTED — no text equivalent'}")

    # ⚠ THE MEASUREMENT THAT REJECTED THE GENERAL RULE. "Report any leaf carrying attrs" needs no
    # table and survives unseen node types — and it fires on these, every one of which is editor
    # identity and loses nothing. The count is the number of warnings that rule manufactures.
    print(f"\n  leaves carrying ONLY {sorted(IDENTITY_ATTRS)} — NOT losses:")
    for ty, c in leaf_identity.most_common():
        print(f"    {ty:<14} {c:>6,}")
    print(f"    {'TOTAL':<14} {sum(leaf_identity.values()):>6,}   ← warnings the general rule invents")

    if unpinned:
        print("\n⚠ UNPINNED ATTRS-BORNE NODE TYPES FOUND — adf_attrs.go is silent about these and "
              "they carry a payload attribute:", file=sys.stderr)
        for ty, c in unpinned.most_common():
            print(f"    {ty:<14} {c:>6,}", file=sys.stderr)
        print("A node type in neither table imports as nothing and reports nothing, which is the "
              "exact defect this merge fixed one type over. Pin it (against Jira's own rendering, "
              "as above) or add it to adfNoTextEquivalent.", file=sys.stderr)
        return 1
    if exhausted:
        print(f"\n  unpinned attrs-borne leaf types: NONE in all {n_issues:,} issues of {PROJECT} — "
              "the census read the project to its end, so every attrs-borne node the shipped "
              "request returns for this project is in one of the two tables.")
    else:
        print(f"\n  unpinned attrs-borne leaf types: NONE IN THE {n_issues:,} ISSUES SCANNED — and "
              f"that is NOT a statement about {PROJECT}. The census stopped on PAGES={PAGES} with "
              "more pages available. THIS IS THE SHAPE THAT HID blockCard: it occurs ONCE in the "
              "first 3,000 issues and the bound was 2,000. Raise PROBE_PAGES until this line says "
              "YES before reading the NONE as a property of the project.", file=sys.stderr)
        return 1

    print("\n⚠ WHAT THIS DOES NOT MEASURE. `date` and `status` (the ADF lozenge) both carry attrs.text "
          "per the ADF spec and occur ZERO times here, so neither is pinned and neither would be "
          "reported — they would land silently, as everything did before this merge. That is the "
          "cost of a pinned table, it is paid on a tenant this environment cannot see, and this "
          "script is the only thing that would notice.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
