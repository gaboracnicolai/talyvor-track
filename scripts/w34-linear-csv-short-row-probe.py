#!/usr/bin/env python3
"""w34-linear-csv-short-row-probe.py — how many REAL Linear CSV rows are narrower than their header,
and are the columns they DO supply still aligned?

WHY THIS EXISTS. csvSource.Next refused any row narrower than the header:

    if len(row) < s.expectedCols { return ...Err: "row N: expected X columns, got Y" }, true

That is a whole-header width test in front of a mapper that reads twelve columns, all of them in the
first two thirds of every measured header — and columnIndex.get's own doc comment says it "Returns
"" if the column doesn't exist OR THE ROW IS TOO SHORT". This probe asks how often a real export is
in that state and, crucially, whether refusing it was right.

⚠⚠ THE ROWS THIS MEASURES ARE THE ROWS EVERY EARLIER PROBE ON THIS ITEM DISCARDED. #99, #100 and
#101 all count behind `if len(row) != len(hdr): continue`, which is why "3,026 data rows" appears in
three merges when the corpus holds 3,099. The 73 the product refuses are exactly the 73 no
measurement of the product had ever looked at — #89's shape, and the reason this file exists as a
separate probe rather than a flag on an old one.

⚠ ALIGNMENT IS THE LOAD-BEARING MEASUREMENT, NOT THE COUNT. A short row could be a MISALIGNED row,
and importing a misaligned row would write garbage — refusing it would then be correct. So every
short row is checked field by field, at its header index, against the shapes the Go mapper accepts:
the ID key shape, a non-empty Title, mapLinearStatus's vocabulary, mapLinearPriority's vocabulary,
and whether `Created` holds a DATE AT ALL. A run where those do NOT come back clean is a run that
says the refusal was right, and this probe is written so that answer is reportable rather than
unreachable.

⚠ "IS IT A DATE" AND "DOES A PINNED LAYOUT PARSE IT" ARE REPORTED AS TWO SEPARATE LINES, because on
this corpus they have DIFFERENT ANSWERS — 73/73 and 0/73. Only the first bears on whether refusing
the ROW was right; the second is #101's already-decided question about linearCSVTimeLayouts. Rolling
them into one line is how a true measurement acquires a false label.

⚠ THE VOCABULARIES ARE TRANSCRIBED BY HAND from internal/importer/csv.go and
internal/importer/linear_csv_dates.go, not parsed out of them. A classifier that derives its
vocabulary from the code it is judging agrees with that code for every possible value, including a
wrong one.

⚠ THE DECODER IS NAMED. Raw bytes are read once and checked for a UTF-8 BOM before any decoding;
`utf-8-sig` would silently eat that byte while Go's strings.TrimSpace does not treat U+FEFF as
whitespace. This probe decodes with plain "utf-8" so a BOM stays visible, and prints the count.

NEGATIVE CONTROLS RUN FIRST, because a search that quietly returns nothing looks exactly like a
search that returns a clean answer:
  N1  a fabricated column-name set                 must find 0 files
  N2  a fabricated repository                      must refuse
  N3  a fabricated path in a real repository       must refuse
A run where any of these SUCCEEDS is a broken instrument and prints INSTRUMENT FAILED.

PROVENANCE: second-hand bytes. Nobody here has a Linear tenant; these are exports other people's
tenants produced and committed to public GitHub repositories. What makes them evidence is agreement
across tenants that have never met, which is why every count below is reported PER OWNER.

Requires `gh` authenticated. Read-only: GET only, no writes to any repository.
"""
import base64
import io
import csv as csvmod
import re
import subprocess
import sys
import urllib.parse
from collections import Counter, defaultdict

LINEAR_MARKERS = '"Cycle Number" "Cycle Name" "Triaged" "Canceled"'
FABRICATED_MARKERS = '"Zorbulax Number" "Zorbulax Name" "Triaged" "Canceled"'

# HAND-TRANSCRIBED, 2026-08-10:
#   mapLinearStatus / mapLinearPriority   internal/importer/csv.go
#   linearCSVTimeLayouts                  internal/importer/linear_csv_dates.go
MAP_LINEAR_STATUS = {"backlog", "todo", "to do", "in progress", "in_progress",
                     "in review", "in_review", "done", "completed", "cancelled", "canceled"}
MAP_LINEAR_PRIORITY = {"urgent", "high", "medium", "low", "", "none", "no priority"}
KEY_RE = re.compile(r"^[A-Za-z0-9]+-\d+$")
# "2006-01-02" and time.RFC3339 — the two pinned layouts, as regexes rather than strptime so the
# pinned-ness is visible in one line.
PINNED_DATE_RE = re.compile(r"^\d{4}-\d{2}-\d{2}$|^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$")
# JavaScript's Date.toString — "Tue May 14 2024 08:53:33 GMT+0000 (GMT)". NOT a pinned layout;
# #101 measured it at 746 of 2,947 real `Updated` cells (25.3%, six unrelated owners) and left
# linearCSVTimeLayouts alone on purpose, so it arrives as a reported warning rather than a guess.
DATE_TOSTRING_RE = re.compile(r"^[A-Z][a-z]{2} [A-Z][a-z]{2} \d{2} \d{4} \d{2}:\d{2}:\d{2} GMT[+-]\d{4}")

# ⚠ THESE TWO REGEXES ANSWER TWO DIFFERENT QUESTIONS AND CONFLATING THEM IS A MISTAKE THIS PROBE
# ALREADY MADE ONCE. "Is the cell at the Created index a DATE at all" is the ALIGNMENT question — a
# misaligned row would hold an email address or a label there. "Does a pinned layout PARSE it" is a
# separate question about linearCSVTimeLayouts, already decided by #101. An earlier draft measured
# the first and wrote the number down under the second's name, which would have put "Created matches
# a pinned layout 73/73" into a merge comment when the real figure for that predicate is 0 of 73.


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
    rows_total = 0
    rows_exact = 0
    rows_long = 0
    short_delta = Counter()
    short_owners = set()
    missing_tail = Counter()
    per_file_short = []
    owners = set()
    # alignment tallies over the short rows
    al = Counter()
    al_total = 0

    for line in items:
        repo, path = line.split("\t", 1)
        raw = fetch_bytes(repo, path)
        if raw is None:
            continue
        if raw[:3] == b"\xef\xbb\xbf":
            bom_files += 1
        rdr = csvmod.reader(io.StringIO(raw.decode("utf-8", "replace")))
        try:
            hdr = next(rdr)
        except StopIteration:
            continue
        if "Cycle Number" not in hdr:
            continue  # search matched prose, not a header
        files += 1
        owner = repo.split("/", 1)[0]
        owners.add(owner)

        idx = {c: hdr.index(c) for c in ("ID", "Title", "Status", "Priority", "Created") if c in hdr}
        n_rows = n_short = 0
        for row in rdr:
            if not row:
                continue
            rows_total += 1
            n_rows += 1
            d = len(row) - len(hdr)
            if d > 0:
                rows_long += 1
                continue
            if d == 0:
                rows_exact += 1
                continue
            n_short += 1
            short_delta[d] += 1
            short_owners.add(owner)
            missing_tail[tuple(hdr[len(row):])] += 1
            al_total += 1
            # ⚠ EVERY CHECK IS AT THE HEADER INDEX, which is the whole question: if the row were
            # misaligned rather than truncated, these are the columns that would go wrong first.
            if "ID" in idx and KEY_RE.match(row[idx["ID"]].strip()):
                al["id"] += 1
            if "Title" in idx and row[idx["Title"]].strip():
                al["title"] += 1
            if "Status" in idx and row[idx["Status"]].strip().lower() in MAP_LINEAR_STATUS:
                al["status"] += 1
            if "Priority" in idx and row[idx["Priority"]].strip().lower() in MAP_LINEAR_PRIORITY:
                al["priority"] += 1
            if "Created" in idx:
                created = row[idx["Created"]].strip()
                if PINNED_DATE_RE.match(created):
                    al["created_pinned"] += 1
                if PINNED_DATE_RE.match(created) or DATE_TOSTRING_RE.match(created):
                    al["created_dateshaped"] += 1
        if n_short:
            per_file_short.append((repo, path, n_rows, n_short))

    print(f"  {files} files parsed as Linear exports, {len(owners)} distinct owners")
    print(f"  ⚠ UTF-8 BOM present in : {bom_files}/{files} files (plain-utf-8 decode, so it would show)")

    print(f"\n== ROW WIDTH AGAINST THE HEADER ==")
    print(f"  {rows_total} data rows")
    print(f"    exactly the header width : {rows_exact}")
    print(f"    WIDER than the header    : {rows_long}   (tolerated today — extra cells are unread)")
    n_short_total = sum(short_delta.values())
    pct = (100.0 * n_short_total / rows_total) if rows_total else 0.0
    print(f"    NARROWER than the header : {n_short_total} ({pct:.1f}%)  deltas={dict(short_delta)}")
    print(f"    from {len(short_owners)} owner(s): {sorted(short_owners)}")

    print(f"\n== WHICH HEADER(S) THE TRUNCATION REMOVED ==")
    for k, v in missing_tail.most_common():
        print(f"  {v:>4} rows  missing {list(k)}")

    print(f"\n== THE FILES THAT CARRY THEM ==")
    print("  ⚠ THE PROPORTION THAT MATTERS IS PER FILE, NOT PER CORPUS: an import is one file.")
    for repo, path, n_rows, n_short in per_file_short:
        print(f"  {n_short}/{n_rows} rows short ({100.0*n_short/n_rows:.0f}%)  {repo}/{path}")

    print(f"\n== ALIGNMENT OF THE SHORT ROWS (every column the Go mapper reads, at its header index) ==")
    if al_total == 0:
        print("  no short rows in this run — nothing to check")
    else:
        for k, label in (("id", "ID matches ^PREFIX-<int>$      "),
                         ("title", "Title non-empty                "),
                         ("status", "Status in mapLinearStatus      "),
                         ("priority", "Priority in mapLinearPriority  "),
                         ("created_dateshaped", "Created is DATE-SHAPED (alignment)  "),
                         ("created_pinned", "Created parses under a PINNED layout")):
            print(f"  {label} : {al[k]}/{al_total}")
        print("\n  ⚠ READ THIS BEFORE READING THE COUNT ABOVE. If these are clean, the rows are")
        print("    TRUNCATED, not misaligned, and refusing them threw away good data. If they are")
        print("    NOT clean, the rows are misaligned and refusing them was right — that answer is")
        print("    reachable from this probe, which is why the check is here rather than assumed.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
