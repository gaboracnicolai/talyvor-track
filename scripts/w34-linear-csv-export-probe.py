#!/usr/bin/env python3
"""w34-linear-csv-export-probe.py — what does a REAL Linear CSV export's header look like?

WHY THIS EXISTS. W3.4 carried, for thirty merges, the stop reason "Linear's export header cannot
be measured from this environment" — #98 declined to give `linear_csv` the same provider-key fix it
gave `jira_csv` on exactly that ground, and it was right to refuse to GUESS. This probe measures it
instead: real Linear CSV exports committed to PUBLIC GitHub repositories, read as bytes, from
several unrelated tenants.

PROVENANCE, STATED PLAINLY, BECAUSE IT IS WEAKER THAN THE JIRA PROBE'S AND MUST NOT BE DRESSED UP
AS EQUAL. scripts/w34-jira-csv-export-probe.py drives a REAL Jira instance's OWN export endpoint and
reads what that server emits. Nobody here has a Linear tenant, so this reads exports OTHER PEOPLE'S
tenants produced and committed. That is second-hand bytes. What makes it evidence rather than
folklore is AGREEMENT ACROSS UNRELATED TENANTS plus the negative controls below: the same header,
in the same order, from tenants that have never met.

NEGATIVE CONTROLS RUN FIRST, because a search that quietly returns nothing looks exactly like a
search that returns a clean answer:
  N1  a fabricated column-name set                 must find 0 files
  N2  a fabricated repository                      must refuse
  N3  a fabricated path in a real repository       must refuse
A run where any of these SUCCEEDS is a broken instrument and prints INSTRUMENT FAILED.

Requires `gh` authenticated. Read-only: GET only, no writes to any repository.
"""
import base64
import io
import csv as csvmod
import re
import subprocess
import sys
import urllib.parse

# Columns chosen because they are jointly distinctive of Linear and appear in NO Jira export:
# Linear's cycle vocabulary plus its US spelling of "Canceled".
LINEAR_MARKERS = '"Cycle Number" "Cycle Name" "Triaged" "Canceled"'
FABRICATED_MARKERS = '"Zorbulax Number" "Zorbulax Name" "Triaged" "Canceled"'


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


def fetch(repo, path):
    q = urllib.parse.quote(path)
    r = gh(["api", f"repos/{repo}/contents/{q}", "--jq", ".content"])
    if r.returncode != 0:
        return None
    try:
        return base64.b64decode(r.stdout).decode("utf-8", "replace")
    except Exception:
        return None


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
    total = search_count(q)
    items = search_items(q)
    print(f"\n== POPULATION ==\n  query total_count={total}, fetched {len(items)} file references")

    key_re = re.compile(r"^[A-Za-z0-9]+-\d+$")
    uuid_re = re.compile(r"^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$")

    files = 0
    first_col = {}
    id_present = 0
    uuid_present = 0
    id_col_index = {}
    rows_total = 0
    rows_id_nonempty = 0
    rows_id_keyshaped = 0
    rows_uuid_shaped = 0
    teams = set()
    header_shapes = {}
    samples = []

    for line in items:
        repo, path = line.split("\t", 1)
        body = fetch(repo, path)
        if body is None:
            continue
        rdr = csvmod.reader(io.StringIO(body))
        try:
            hdr = next(rdr)
        except StopIteration:
            continue
        if "Cycle Number" not in hdr:
            continue  # search matched prose, not a header
        files += 1
        first_col[hdr[0]] = first_col.get(hdr[0], 0) + 1
        header_shapes[tuple(hdr)] = header_shapes.get(tuple(hdr), 0) + 1
        if "ID" in hdr:
            id_present += 1
            id_col_index[hdr.index("ID")] = id_col_index.get(hdr.index("ID"), 0) + 1
        if "UUID" in hdr:
            uuid_present += 1
        i_id = hdr.index("ID") if "ID" in hdr else None
        i_uu = hdr.index("UUID") if "UUID" in hdr else None
        i_team = hdr.index("Team") if "Team" in hdr else None
        shown = 0
        for row in rdr:
            if not row or len(row) != len(hdr):
                continue
            rows_total += 1
            if i_id is not None:
                v = row[i_id].strip()
                if v:
                    rows_id_nonempty += 1
                if key_re.match(v):
                    rows_id_keyshaped += 1
                    if i_team is not None:
                        teams.add(row[i_team].strip())
                    if shown < 1:
                        samples.append((repo, v, row[i_uu].strip() if i_uu is not None else "-"))
                        shown += 1
            if i_uu is not None and uuid_re.match(row[i_uu].strip()):
                rows_uuid_shaped += 1

    print(f"  {files} files parsed as Linear exports")
    print(f"  first column     : {first_col}")
    print(f"  'ID' in header   : {id_present}/{files} files, at index {id_col_index}")
    print(f"  'UUID' in header : {uuid_present}/{files} files")
    print(f"  distinct header shapes: {len(header_shapes)} (column counts: "
          f"{sorted({len(h) for h in header_shapes})})")
    print(f"\n== THE `ID` COLUMN'S CONTENT ==")
    print(f"  {rows_total} data rows")
    print(f"  ID non-empty              : {rows_id_nonempty}/{rows_total}")
    print(f"  ID matches ^PREFIX-<int>$ : {rows_id_keyshaped}/{rows_total}")
    print(f"  UUID matches a real uuid  : {rows_uuid_shaped}")
    print(f"  distinct team prefixes seen: {len(teams)} {sorted(teams)[:12]}")
    print("\n  sample (repo, ID, UUID):")
    for s in samples[:12]:
        print(f"    {s[0]:<42} {s[1]:<12} {s[2]}")

    if header_shapes:
        canonical = sorted(header_shapes.items(), key=lambda kv: -kv[1])[0][0]
        print(f"\n== MOST COMMON HEADER ({header_shapes[canonical]} files) ==\n  {list(canonical)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
