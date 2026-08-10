#!/usr/bin/env python3
"""w34-linear-csv-priority-probe.py — what does a REAL Linear CSV export put in `Priority`?

WHY THIS EXISTS. #101 measured the `Status` vocabulary of this transport whole-population and
found it clean (2,985 of 3,026 cells recognised). It measured the column BESIDE the one that
`linearRowMapper` reads in the same expression:

    rawStatus, rawPrio := ci.get(row, "Status"), ci.get(row, "Priority")
    status, statusOK := mapLinearStatus(rawStatus)
    prio,   prioOK   := mapLinearPriority(rawPrio)

`mapLinearPriority` knows SEVEN spellings and answers everything else with
(model.PriorityNone, false) — imported as "no priority", reported as a warning. Nobody has ever
asked what a real export actually writes there. This probe asks.

PROVENANCE, STATED PLAINLY, BECAUSE IT IS THE SAME SECOND-HAND CORPUS #99/#100/#101 USED AND MUST
NOT BE DRESSED UP AS FIRST-HAND. Nobody here has a Linear tenant. These are exports OTHER PEOPLE'S
tenants produced and committed to PUBLIC GitHub repositories. What makes them evidence rather than
folklore is AGREEMENT ACROSS TENANTS THAT HAVE NEVER MET, plus the negative controls below. Every
count is reported PER OWNER and PER HEADER WIDTH for exactly that reason: N cells is ONE
measurement if they share a toolchain and a fact about the provider if unrelated tenants emit it.

NEGATIVE CONTROLS RUN FIRST, because a search that quietly returns nothing looks exactly like a
search that returns a clean answer:
  N1  a fabricated column-name set                 must find 0 files
  N2  a fabricated repository                      must refuse
  N3  a fabricated path in a real repository       must refuse
A run where any of these SUCCEEDS is a broken instrument and prints INSTRUMENT FAILED.

⚠ THE DECODER IS NAMED, AND THE RAW BYTES ARE READ ONCE. Every file's first three bytes are
checked for a UTF-8 BOM BEFORE any decoding, and the count is printed. `utf-8-sig` — the codec a
probe reaches for by reflex — silently eats that byte; Go's `strings.TrimSpace` does not treat
U+FEFF as whitespace, so the product and a utf-8-sig probe would disagree about what the first
header cell is called. This probe decodes with plain "utf-8" so a BOM stays visible, and reports
the count either way. (The same class of hole as reading a column census off documentation.)

⚠ THE CLASSIFIER'S VOCABULARY IS TRANSCRIBED BY HAND FROM csv.go's mapLinearPriority, NOT PARSED
OUT OF IT. A classifier that derives its vocabulary from the code it is judging agrees with that
code for every possible value, including a wrong one. If mapLinearPriority changes, this list is
supposed to go stale and be updated by a human who looked at both.

Requires `gh` authenticated. Read-only: GET only, no writes to any repository.
"""
import base64
import io
import csv as csvmod
import subprocess
import sys
import urllib.parse
from collections import Counter, defaultdict

LINEAR_MARKERS = '"Cycle Number" "Cycle Name" "Triaged" "Canceled"'
FABRICATED_MARKERS = '"Zorbulax Number" "Zorbulax Name" "Triaged" "Canceled"'

# HAND-TRANSCRIBED from internal/importer/csv.go mapLinearPriority, 2026-08-10. Each entry is a
# case label of that switch, already lowercased/trimmed the way the Go does.
MAP_LINEAR_PRIORITY = {
    "urgent": 1,
    "high": 2,
    "medium": 3,
    "low": 4,
    "": 0,
    "none": 0,
    "no priority": 0,
}


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


def fetch_bytes(repo, path):
    """RAW bytes. No decoding here on purpose — the BOM check needs them intact."""
    q = urllib.parse.quote(path)
    r = gh(["api", f"repos/{repo}/contents/{q}", "--jq", ".content"])
    if r.returncode != 0:
        return None
    try:
        return base64.b64decode(r.stdout)
    except Exception:
        return None


def main():
    print("== NEGATIVE CONTROLS ==")
    n1 = search_count(f"{FABRICATED_MARKERS} extension:csv")
    print(f"  N1 fabricated column set          -> total_count={n1}   (want 0)")
    n2 = fetch_bytes("this-org-does-not-exist-zzq/nope", "a.csv")
    print(f"  N2 fabricated repository          -> {'REFUSED' if n2 is None else 'RETURNED BYTES'}   (want REFUSED)")
    n3 = fetch_bytes("Amanuel-Ayal3w/Awaqi", "definitely-not-here-zzq.csv")
    print(f"  N3 fabricated path in a real repo -> {'REFUSED' if n3 is None else 'RETURNED BYTES'}   (want REFUSED)")
    if n1 != 0 or n2 is not None or n3 is not None:
        print("INSTRUMENT FAILED — a control did not refuse. Every count below is meaningless.")
        return 2

    q = f"{LINEAR_MARKERS} extension:csv"
    total = search_count(q)
    items = search_items(q)
    print(f"\n== POPULATION ==\n  query total_count={total}, fetched {len(items)} file references")

    files = 0
    bom_files = 0
    files_with_priority = 0
    priority_col_index = Counter()
    rows_total = 0
    cells_nonempty = 0
    values = Counter()
    value_owners = defaultdict(set)
    value_widths = defaultdict(set)
    unrec_owners = set()
    header_widths = Counter()
    owners = set()
    per_owner_rows = Counter()

    for line in items:
        repo, path = line.split("\t", 1)
        raw = fetch_bytes(repo, path)
        if raw is None:
            continue
        has_bom = raw[:3] == b"\xef\xbb\xbf"
        body = raw.decode("utf-8", "replace")
        rdr = csvmod.reader(io.StringIO(body))
        try:
            hdr = next(rdr)
        except StopIteration:
            continue
        if "Cycle Number" not in hdr:
            continue  # search matched prose, not a header
        files += 1
        if has_bom:
            bom_files += 1
        owner = repo.split("/", 1)[0]
        owners.add(owner)
        header_widths[len(hdr)] += 1

        i_prio = hdr.index("Priority") if "Priority" in hdr else None
        if i_prio is not None:
            files_with_priority += 1
            priority_col_index[i_prio] += 1

        for row in rdr:
            if not row or len(row) != len(hdr):
                continue
            rows_total += 1
            per_owner_rows[owner] += 1
            if i_prio is None:
                continue
            v = row[i_prio].strip()
            if v:
                cells_nonempty += 1
            key = v.lower()
            values[key] += 1
            value_owners[key].add(owner)
            value_widths[key].add(len(hdr))
            if key not in MAP_LINEAR_PRIORITY:
                unrec_owners.add(owner)

    print(f"  {files} files parsed as Linear exports, {len(owners)} distinct owners")
    print(f"  header widths          : {dict(sorted(header_widths.items()))}")
    print(f"  ⚠ UTF-8 BOM present in : {bom_files}/{files} files   (instrument control: this probe")
    print(f"                            decodes with plain utf-8, so a BOM would stay visible)")
    print(f"  'Priority' in header   : {files_with_priority}/{files} files, at index {dict(priority_col_index)}")

    print(f"\n== THE `Priority` COLUMN'S CONTENT ==")
    print(f"  {rows_total} data rows across {len(owners)} owners")
    recognised = sum(c for v, c in values.items() if v in MAP_LINEAR_PRIORITY)
    unrecognised = sum(c for v, c in values.items() if v not in MAP_LINEAR_PRIORITY)
    tot = recognised + unrecognised
    print(f"  Priority cells (incl. empty) : {tot}")
    print(f"  non-empty                    : {cells_nonempty}")
    if tot:
        print(f"  RECOGNISED by mapLinearPriority : {recognised}/{tot} ({100.0*recognised/tot:.1f}%)")
        print(f"  UNRECOGNISED                    : {unrecognised}/{tot} ({100.0*unrecognised/tot:.1f}%)")

    print(f"\n  every distinct value, verbatim (lowercased as the Go does), with provenance:")
    for v, c in values.most_common():
        mark = "  " if v in MAP_LINEAR_PRIORITY else "⚠ "
        shown = repr(v)
        print(f"    {mark}{shown:<20} {c:>6}  owners={len(value_owners[v]):<3} widths={sorted(value_widths[v])}")

    if unrecognised:
        print(f"\n  ⚠ UNRECOGNISED values come from {len(unrec_owners)} owner(s): {sorted(unrec_owners)}")
        print(f"    Every one of those rows imports as PriorityNone and is REPORTED as a warning.")
    else:
        print(f"\n  ZERO unrecognised values. mapLinearPriority covers this corpus completely.")

    print(f"\n  rows per owner: {dict(per_owner_rows.most_common())}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
