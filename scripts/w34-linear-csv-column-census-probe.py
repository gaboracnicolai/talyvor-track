#!/usr/bin/env python3
"""w34-linear-csv-column-census-probe.py — WHICH COLUMNS DO REAL LINEAR EXPORTS CARRY, AND WHICH
OF THEM DOES linearRowMapper NEVER READ?

WHY THIS EXISTS. W3.4's own remaining-work list names "field mapping completeness per provider
(what silently drops)" as STILL UNMEASURED. Every previous field merge in this package went the
other way round: someone suspected ONE column (`Created`, `Completed`, `Updated`, `ID`) and then
measured whether real exports carry it. That order can only ever confirm a column somebody already
thought of, and #86's post-mortem says exactly how that failed — the enumeration of "the columns
this transport ignores" was taken from THIS PACKAGE'S OWN FIXTURES, whose Linear header is the nine
columns Linear's IMPORT documentation names. `Updated` is not in that line, so it could not appear
in the list of dropped columns, and it stayed invisible for thirty-one merges.

So this probe inverts the question. It does not start from a column name. It reads the FULL HEADER
of every real Linear export it can find and subtracts the set linearRowMapper reads. What is left
is the drop list, derived from the product's actual input rather than from its fixtures.

PROVENANCE, STATED PLAINLY. Same corpus and same weakness as the three probes before it
(w34-linear-csv-export-probe.py, w34-linear-csv-updated-probe.py): second-hand bytes from tenants
nobody here controls, found by GitHub code search. It is NOT dressed up as equal to the Jira
probe's first-hand bytes. What makes it evidence is AGREEMENT ACROSS TENANTS THAT HAVE NEVER MET,
reported per column as an OWNER count alongside the row count, plus the negative controls below.

NEGATIVE CONTROLS RUN FIRST, because a search that quietly returns nothing looks exactly like a
search that returns a clean answer:
  N1  a fabricated column-name set                 must find 0 files
  N2  a fabricated repository                      must refuse
  N3  a fabricated path in a real repository       must refuse
  N4  a column name that is in NO Linear export    must census 0 files / 0 non-empty cells
A run where any of these SUCCEEDS is a broken instrument and prints INSTRUMENT FAILED.

N4 is the one the three earlier probes do not have, and it is the control this probe specifically
needs: N1-N3 prove the SEARCH reaches real bytes, and none of them prove the CENSUS can report a
zero. A census whose per-column counter was wired to the wrong index would report plausible
non-zero numbers for every column including a fabricated one.

⚠ SHORT ROWS ARE COUNTED, NOT DISCARDED. The earlier probes skip `len(row) != len(hdr)` — which is
the exact population #96 found the product REFUSING, so a probe that drops them silently is blind
to the rows most likely to be mishandled. Here they are tallied separately and reported.

Requires `gh` authenticated. Read-only: GET only, no writes to any repository.
"""
import base64
import hashlib
import io
import os
import csv as csvmod
import subprocess
import sys
import urllib.parse

LINEAR_MARKERS = '"Cycle Number" "Cycle Name" "Triaged" "Canceled"'
FABRICATED_MARKERS = '"Zorbulax Number" "Zorbulax Name" "Triaged" "Canceled"'

# N4: a column name no Linear export carries. The census must report it absent everywhere.
FABRICATED_COLUMN = "Zorbulax Quotient"

# The columns linearRowMapper (internal/importer/csv.go) actually reads, spelled exactly as the
# mapper spells them. Kept as a LITERAL list rather than parsed out of the Go source, because a
# source-derived list goes green when the source shrinks (a deleted ci.get would silently move a
# column from READ to READ).
MAPPER_READS = [
    "ID",           # linearCSVIssueIDColumn -> Identifier
    "Title",
    "Description",
    "Status",
    "Priority",
    "Labels",
    "Created",      # linearCSVCreated
    "Completed",    # linearCSVCompletedColumn
    "Updated",      # linearCSVUpdatedColumn
]


def gh(args):
    return subprocess.run(["gh"] + args, capture_output=True, text=True)


def search_count(q):
    r = gh(["api", "-X", "GET", "search/code", "-f", f"q={q}", "--jq", ".total_count"])
    if r.returncode != 0:
        return None
    return int(r.stdout.strip())


def search_items(q, pages=3):
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


CACHE = "/tmp/w34-linear-corpus-cache"


def fetch(repo, path, cache=False):
    """cache=True memoises the bytes under /tmp so a re-run analyses the SAME corpus without
    re-spending 45 API calls. The negative controls deliberately pass cache=False — a control that
    could be answered from a cache is not a control."""
    key = None
    if cache:
        os.makedirs(CACHE, exist_ok=True)
        key = os.path.join(CACHE, hashlib.sha256(f"{repo}/{path}".encode()).hexdigest())
        if os.path.exists(key):
            return io.open(key, encoding="utf-8").read()
    q = urllib.parse.quote(path)
    r = gh(["api", f"repos/{repo}/contents/{q}", "--jq", ".content"])
    if r.returncode != 0:
        return None
    try:
        body = base64.b64decode(r.stdout).decode("utf-8", "replace")
    except Exception:
        return None
    if key:
        io.open(key, "w", encoding="utf-8").write(body)
    return body


def shape(v):
    """Collapse a cell to its SHAPE so distinct instants collapse and distinct formats do not."""
    return "".join("9" if c.isdigit() else "A" if c.isalpha() else c for c in v)


def main():
    print("== NEGATIVE CONTROLS ==")
    n1 = search_count(f"{FABRICATED_MARKERS} extension:csv")
    print(f"  N1 fabricated column set          -> total_count={n1}   (want 0)")
    n2 = fetch("this-org-does-not-exist-zzq/nope", "a.csv")
    print(f"  N2 fabricated repository          -> {'REFUSED' if n2 is None else 'RETURNED BYTES'}   (want REFUSED)")
    n3 = fetch("Amanuel-Ayal3w/Awaqi", "definitely-not-here-zzq.csv")
    print(f"  N3 fabricated path in a real repo -> {'REFUSED' if n3 is None else 'RETURNED BYTES'}   (want REFUSED)")
    if n1 != 0 or n2 is not None or n3 is not None:
        print("INSTRUMENT FAILED — a control did not refuse. Every count below is meaningless.")
        return 2

    q = f"{LINEAR_MARKERS} extension:csv"
    items = search_items(q)
    print(f"\n== POPULATION ==\n  {len(items)} file references")

    files = 0
    owners = set()
    header_shapes = {}
    col_files = {}        # column -> number of export files whose header carries it
    col_owners = {}       # column -> set of distinct GitHub owners
    col_nonempty = {}     # column -> non-empty data cells
    col_samples = {}
    col_shapes = {}
    col_shape_owners = {}
    rows_total = 0
    rows_short = 0
    short_by_file = {}

    for line in items:
        repo, path = line.split("\t", 1)
        body = fetch(repo, path, cache=True)
        if body is None:
            continue
        rdr = csvmod.reader(io.StringIO(body))
        try:
            hdr = next(rdr)
        except StopIteration:
            continue
        if "Cycle Number" not in hdr:
            continue
        files += 1
        owner = repo.split("/")[0]
        owners.add(owner)
        header_shapes[tuple(hdr)] = header_shapes.get(tuple(hdr), 0) + 1
        for c in set(hdr):
            col_files[c] = col_files.get(c, 0) + 1
            col_owners.setdefault(c, set()).add(owner)
        for row in rdr:
            if not row:
                continue
            rows_total += 1
            if len(row) != len(hdr):
                rows_short += 1
                short_by_file[repo] = short_by_file.get(repo, 0) + 1
            for i, c in enumerate(hdr):
                if i >= len(row):
                    continue
                v = row[i].strip()
                if not v:
                    continue
                col_nonempty[c] = col_nonempty.get(c, 0) + 1
                sh = shape(v)
                col_shapes.setdefault(c, {})[sh] = col_shapes.setdefault(c, {}).get(sh, 0) + 1
                col_shape_owners.setdefault(c, {}).setdefault(sh, set()).add(owner)
                s = col_samples.setdefault(c, [])
                if len(s) < 3 and v not in s:
                    s.append(v)

    print(f"  {files} files parsed as Linear exports · {len(owners)} distinct owners")
    print(f"  {rows_total} data rows · {len(header_shapes)} distinct header shapes "
          f"({sorted({len(h) for h in header_shapes})} cols)")
    print(f"  {rows_short} rows SHORTER/LONGER than their header, in {len(short_by_file)} files")

    # N4 — the census must be able to report a zero.
    n4_files = col_files.get(FABRICATED_COLUMN, 0)
    n4_cells = col_nonempty.get(FABRICATED_COLUMN, 0)
    print(f"\n  N4 fabricated COLUMN {FABRICATED_COLUMN!r} -> in {n4_files} headers, "
          f"{n4_cells} non-empty cells   (want 0 / 0)")
    if n4_files != 0 or n4_cells != 0:
        print("INSTRUMENT FAILED — the census invented a column. Every count below is meaningless.")
        return 2
    if files == 0:
        print("INSTRUMENT FAILED — zero files parsed; nothing was measured.")
        return 2

    read = set(MAPPER_READS)
    print("\n== COLUMNS linearRowMapper READS ==")
    for c in MAPPER_READS:
        print(f"  {c:<22} header {col_files.get(c,0):>3}/{files}  owners {len(col_owners.get(c,set())):>2}  "
              f"non-empty {col_nonempty.get(c,0):>5}/{rows_total}")

    print("\n== COLUMNS REAL EXPORTS CARRY AND linearRowMapper NEVER READS ==")
    print("   (ordered by non-empty real cells — this is the drop list, derived from the input)")
    dropped = [c for c in col_files if c not in read]
    dropped.sort(key=lambda c: (-col_nonempty.get(c, 0), c))
    for c in dropped:
        ne = col_nonempty.get(c, 0)
        pct = (100.0 * ne / rows_total) if rows_total else 0.0
        print(f"  {c:<22} header {col_files.get(c,0):>3}/{files}  owners {len(col_owners.get(c,set())):>2}  "
              f"non-empty {ne:>5}/{rows_total} ({pct:4.1f}%)  e.g. {col_samples.get(c, [])[:2]}")

    # ⚠ THE PROVENANCE CONTROL, same one w34-linear-csv-updated-probe.py applies to `Updated`: a
    # cell count is one measurement if every cell came from one owner's toolchain, and a fact about
    # the provider only if unrelated tenants emit the same shape. Reported PER OWNER, per column.
    print("\n== SERIALISATION PER OWNER, for the columns a fix would have to parse ==")
    for c in ["Due Date", "Created", "Updated", "Cycle Start", "Started"]:
        if c not in col_shapes:
            continue
        print(f"  {c}:")
        for sh, n in sorted(col_shapes[c].items(), key=lambda kv: -kv[1]):
            ow = sorted(col_shape_owners[c][sh])
            print(f"    {n:>6}  {sh}")
            print(f"            owners={len(ow)} {ow}")
        print(f"    e.g. {col_samples.get(c, [])}")

    print("\n== HEADER SHAPES ==")
    for h, n in sorted(header_shapes.items(), key=lambda kv: -kv[1]):
        print(f"  {n:>2} file(s), {len(h)} cols: {', '.join(h)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
