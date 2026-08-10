#!/usr/bin/env python3
"""w34-jira-csv-corpus-probe.py — what do REAL Jira CSV exports, from many unrelated instances,
actually serialise their dates as, and are they the shape internal/importer/jira_csv_dates.go pins?

WHY THIS EXISTS. jiraCSVTimeLayouts holds ONE layout, `"2/Jan/2006 3:04 PM"`, pinned from ONE
instance (jira.atlassian.com, Server/DC) by scripts/w34-jira-csv-export-probe.py. That file states
its own limit in prose:

    "THE FORMAT IS A PER-INSTANCE PREFERENCE. Jira renders CSV dates with the instance's
     look-and-feel date format, so another tenant's export may not be this shape at all."

A stated limit is not a measurement. This probe measures it: real Jira CSV exports committed to
PUBLIC GitHub repositories, read as BYTES, from unrelated instances — the instrument
scripts/w34-linear-csv-export-probe.py invented for the Linear half and that nobody had pointed at
the Jira half.

PROVENANCE, STATED PLAINLY AND NOT BORROWED. w34-jira-csv-export-probe.py drives a real Jira's own
export endpoint and reads first-hand what that server emits; this reads exports OTHER PEOPLE'S
instances produced and committed, which is SECOND-HAND BYTES. What makes it evidence is agreement —
or disagreement — ACROSS INSTANCES THAT HAVE NEVER MET, plus the negative controls below.

⚠ IT READS RAW BYTES, NEVER `utf-8-sig`. The sibling probe decodes with `utf-8-sig`, which silently
eats a UTF-8 BOM; Go's strings.TrimSpace does not treat U+FEFF as whitespace, so a BOM would make
buildIndex key the first column "﻿summary" and jiraRowMapper's title lookup would miss on every
row of the file. A lenient decoder cannot see the byte that breaks the product.

⚠ IT DOES NOT DISCARD RAGGED ROWS. #99/#100/#101 all counted behind `len(row) != len(hdr)` and the
73 rows they skipped were exactly the defect #102 found. Every data row is counted here.

⚠ THE PARSE VERDICT IS NOT DECIDED HERE. Python's strptime is more tolerant than Go's time.Parse
about zero-padding, so a Python "it parses" would be a fact about Python. This probe writes the
distinct raw values to a file; internal/importer's own parseJiraCSVTime is what answers, through
TestJiraCSVCorpus_* in the package. What this file prints under PARSE is Python's opinion, labelled
as such, and is a SEPARATE LINE from the shape census.

NEGATIVE CONTROLS RUN FIRST, because a search that quietly returns nothing looks exactly like a
search that returns a clean answer:
  N1  a fabricated column-name set            must find 0 files
  N2  a fabricated repository                 must refuse
  N3  a fabricated path in a real repository  must refuse

Requires `gh` authenticated. Read-only: GET only, no writes to any repository.

    python3 scripts/w34-jira-csv-corpus-probe.py
"""
import base64
import collections
import csv as csvmod
import io
import json
import re
import subprocess
import sys
import urllib.parse

# Jointly distinctive of a Jira export and present in NO Linear export.
JIRA_MARKERS = '"Issue key" "Issue id" "Project key"'
FABRICATED_MARKERS = '"Zorbulax key" "Zorbulax id" "Project key"'

# The columns internal/importer/csv.go + jira_csv_*.go look up, exactly as spelled there.
READ_COLUMNS = ["Summary", "Issue key", "Status", "Priority", "Description",
                "Labels", "Resolution", "Created", "Updated", "Resolved", "Due Date"]
DATE_COLUMNS = ["Created", "Updated", "Resolved", "Due Date"]

VALUES_OUT = "/tmp/w34-jira-corpus-date-values.json"


def gh(args):
    return subprocess.run(["gh"] + args, capture_output=True, text=True)


def search_count(q):
    r = gh(["api", "-X", "GET", "search/code", "-f", f"q={q}", "--jq", ".total_count"])
    if r.returncode != 0:
        return None
    return int(r.stdout.strip())


def search_items(q, pages=4):
    out = []
    for page in range(1, pages + 1):
        r = gh(["api", "-X", "GET", "search/code", "-f", f"q={q}",
                "-f", "per_page=100", "-f", f"page={page}",
                "--jq", '.items[] | "\(.repository.full_name)\t\(.path)"'])
        if r.returncode != 0:
            break
        lines = [l for l in r.stdout.splitlines() if l.strip()]
        out.extend(lines)
        if len(lines) < 100:
            break
    return out


CACHE = "/tmp/w34-jira-corpus"


def fetch_bytes(repo, path, cache=True):
    """RAW BYTES. Not str, and never through utf-8-sig — the BOM question needs the byte.

    Cached on disk so a re-run measures the SAME bytes rather than whatever the repositories hold
    today; a corpus that changes under the instrument makes two runs incomparable.
    """
    import hashlib
    import os
    key = hashlib.sha256(f"{repo}\t{path}".encode()).hexdigest()[:32]
    fp = os.path.join(CACHE, key)
    if cache and os.path.exists(fp):
        with open(fp, "rb") as f:
            return f.read()
    q = urllib.parse.quote(path)
    r = gh(["api", f"repos/{repo}/contents/{q}", "--jq", ".content"])
    if r.returncode != 0:
        return None
    try:
        raw = base64.b64decode(r.stdout)
    except Exception:
        return None
    if cache:
        os.makedirs(CACHE, exist_ok=True)
        with open(fp, "wb") as f:
            f.write(raw)
    return raw


def shape_of(v):
    """Collapse a date cell to its SHAPE so unrelated instants group: digits→9, letters→A."""
    return re.sub(r"[A-Za-z]", "A", re.sub(r"\d", "9", v))


def python_parses(v, fmt):
    """Python's opinion about ONE layout, labelled as Python's. The Go answer comes from the
    package's own test.

    ⚠ IT TAKES THE LAYOUT AS AN ARGUMENT ON PURPOSE. An earlier draft tried the pinned
    four-digit-year layout AND a two-digit-year one and reported a single accepted/total ratio —
    which is a true number under the name of a predicate that is not the product's. #102 shipped
    the same mistake in an ad-hoc check and caught it before the merge comment; this is that lesson
    written into the function signature.
    """
    import datetime
    try:
        datetime.datetime.strptime(v, fmt)
        return True
    except ValueError:
        return False


def main():
    print("== NEGATIVE CONTROLS ==")
    n1 = search_count(f"{FABRICATED_MARKERS} extension:csv")
    print(f"  N1 fabricated column set          -> total_count={n1}   (want 0)")
    n2 = fetch_bytes("this-org-does-not-exist-zzq/nope", "a.csv", cache=False)
    print(f"  N2 fabricated repository          -> {'REFUSED' if n2 is None else 'RETURNED BYTES'}   (want REFUSED)")
    n3 = fetch_bytes("apache/kafka", "definitely-not-here-zzq.csv", cache=False)
    print(f"  N3 fabricated path in a real repo -> {'REFUSED' if n3 is None else 'RETURNED BYTES'}   (want REFUSED)")
    if n1 != 0 or n2 is not None or n3 is not None:
        print("INSTRUMENT FAILED — a control did not refuse. Every count below is meaningless.")
        return 2

    q = f"{JIRA_MARKERS} extension:csv"
    total = search_count(q)
    items = search_items(q)
    print(f"\n== POPULATION ==\n  query total_count={total}, fetched {len(items)} file references")

    files = 0
    owners = set()
    bom_files = 0
    first_col = collections.Counter()
    col_present = collections.Counter()
    rows_total = 0
    rows_ragged = 0
    # per date column: shape -> [count, set(owners), one verbatim exemplar]
    shapes = {c: {} for c in DATE_COLUMNS}
    nonempty = collections.Counter()
    cells_total = collections.Counter()
    distinct_values = collections.Counter()
    # the product's title lookup, simulated on the header only
    title_readable = 0
    per_file = []

    for line in items:
        repo, path = line.split("\t", 1)
        raw = fetch_bytes(repo, path)
        if raw is None:
            continue
        has_bom = raw.startswith(b"\xef\xbb\xbf")
        # decode WITHOUT stripping the BOM, so the header key is what Go would see
        text = raw.decode("utf-8", "replace")
        rdr = csvmod.reader(io.StringIO(text))
        try:
            hdr = next(rdr)
        except StopIteration:
            continue
        if "Issue key" not in [h.strip() for h in hdr]:
            continue  # the search matched prose, not a header
        files += 1
        owners.add(repo.split("/")[0])
        if has_bom:
            bom_files += 1
        first_col[hdr[0]] += 1
        # Go: buildIndex lowercases+trims BOTH sides; TrimSpace does NOT strip U+FEFF.
        keys = {h.strip().lower() for h in hdr}
        for c in READ_COLUMNS:
            if c.lower() in keys:
                col_present[c] += 1
        if "summary" in keys or "title" in keys:
            title_readable += 1

        idx = {}
        for c in DATE_COLUMNS:
            for i, h in enumerate(hdr):
                if h.strip().lower() == c.lower():
                    idx[c] = i
                    break
        frows = 0
        for row in rdr:
            if not row:
                continue
            rows_total += 1
            frows += 1
            if len(row) != len(hdr):
                rows_ragged += 1
            for c, i in idx.items():
                cells_total[c] += 1
                v = row[i].strip() if i < len(row) else ""
                if not v:
                    continue
                nonempty[c] += 1
                distinct_values[v] += 1
                sh = shape_of(v)
                e = shapes[c].setdefault(sh, [0, set(), v])
                e[0] += 1
                e[1].add(repo.split("/")[0])
        per_file.append((repo, path, frows, has_bom, ("summary" in keys or "title" in keys)))

    print(f"  {files} files parsed as Jira exports, {len(owners)} distinct owners")
    print(f"  {rows_total} data rows ({rows_ragged} ragged — counted, not discarded)")
    print(f"\n== UTF-8 BOM, FROM RAW BYTES ==")
    print(f"  {bom_files}/{files} files begin with EF BB BF")
    print(f"  first column, verbatim (repr shows the BOM if present):")
    for k, v in first_col.most_common(10):
        print(f"    {v:4d}  {k!r}")

    # THE HEADLINE. jiraRowMapper's FIRST act is ci.get(row,"Summary") then ci.get(row,"Title"); an
    # empty result is errEmptyTitle and the row never lands. A header whose only title column is
    # BOM-glued therefore refuses EVERY row of that file — the file imports nothing.
    print(f"\n== WOULD jiraRowMapper FIND A TITLE COLUMN? ==")
    print(f"  header carries a title column Go could read : {title_readable}/{files}")
    dead = [(r, p, n, b) for (r, p, n, b, t) in per_file if not t]
    dead_rows = sum(n for _, _, n, _ in dead)
    dead_bom = sum(1 for _, _, _, b in dead if b)
    print(f"  files where EVERY row would be refused      : {len(dead)}/{files}"
          f"  ({100.0 * len(dead) / files:.1f}%)")
    print(f"    of those, the cause is a BOM on the header: {dead_bom}/{len(dead)}")
    print(f"  data rows in those files                    : {dead_rows}/{rows_total}"
          f"  ({100.0 * dead_rows / rows_total:.1f}%)")
    print(f"  ⚠ THE PROPORTION THAT MATTERS IS PER FILE — an import is ONE file, and in every one")
    print(f"    of these the refusal is 100% of the data.")
    for r, p, n, b in sorted(dead, key=lambda d: -d[2])[:8]:
        print(f"    {n:5d} rows  BOM={b}  {r}/{p}")

    print(f"\n== COLUMN SPELLINGS THE IMPORTER LOOKS UP ==")
    for c in READ_COLUMNS:
        print(f"  {col_present[c]:4d}/{files}  {c!r}")

    print(f"\n== DATE SERIALISATION, PER COLUMN, PER SHAPE ==")
    print(f"  (Go pins exactly one layout: \"2/Jan/2006 3:04 PM\" => shape '99/AAA/9999 9:99 AA')")
    for c in DATE_COLUMNS:
        print(f"\n  {c}: {nonempty[c]}/{cells_total[c]} cells non-empty")
        for sh, (n, own, ex) in sorted(shapes[c].items(), key=lambda kv: -kv[1][0])[:8]:
            print(f"    {n:6d}  {len(own)} owner(s)  {sh!r}   e.g. {ex!r}")

    print(f"\n== PYTHON'S OPINION ONLY — the Go verdict comes from the package's own test ==")
    py_all = sum(distinct_values.values())
    for label, fmt in [("THE PINNED LAYOUT  (4-digit year)", "%d/%b/%Y %I:%M %p"),
                       ("a 2-digit-year twin (NOT pinned) ", "%d/%b/%y %I:%M %p")]:
        n = sum(c for v, c in distinct_values.items() if python_parses(v, fmt))
        print(f"  {n:6d}/{py_all}  ({100.0 * n / py_all:5.1f}%)  {label}  {fmt}")
    print("  ⚠ TWO LINES, NOT ONE RATIO. Only the first is a fact about the product.")

    with open(VALUES_OUT, "w") as f:
        json.dump({"values": distinct_values.most_common(), "files": files,
                   "owners": len(owners), "rows": rows_total, "bom_files": bom_files},
                  f, indent=1)
    print(f"\n  {len(distinct_values)} distinct raw date values written to {VALUES_OUT}")
    print("  -> feed them to internal/importer's own parseJiraCSVTime; Python does not get a vote.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
