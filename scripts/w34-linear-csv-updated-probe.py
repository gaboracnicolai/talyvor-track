#!/usr/bin/env python3
"""w34-linear-csv-updated-probe.py — what is in a REAL Linear CSV export's DATE columns, and is
`Updated` one of them?

WHY THIS EXISTS. Two separate claims in internal/importer are checked here against the same bytes:

  1. linearRowMapper reads Created and Completed and NOT `Updated`, while the other three
     transports all read it (jira_csv → jiraCSVUpdated, jira_api/linear_api → apiUpdated). #89's
     own note enumerated "the two other columns this transport ignores" as ID and Estimate and did
     not name Updated at all. Is `Updated` actually in the header a real tenant exports?

  2. linear_csv_dates.go pins linearCSVTimeLayouts BY HAND and says so loudly: "nothing reachable
     from here emits a Linear CSV export", so the SERIALISATION was never measured. #99 then found
     a transport nobody had listed — real exports OTHER tenants committed to public repositories.
     That makes the serialisation measurable, which means the pinned layouts are now falsifiable.
     If a real export's cells match no pinned layout, #89's merged fix is INERT on real bytes and
     every date arrives as a warning.

PROVENANCE, STATED PLAINLY. Same corpus and same weakness as
scripts/w34-linear-csv-export-probe.py: second-hand bytes from tenants nobody here controls. What
makes it evidence is AGREEMENT ACROSS TENANTS THAT HAVE NEVER MET, and the negative controls below.

NEGATIVE CONTROLS RUN FIRST, because a search that quietly returns nothing looks exactly like a
search that returns a clean answer:
  N1  a fabricated column-name set                 must find 0 files
  N2  a fabricated repository                      must refuse
  N3  a fabricated path in a real repository       must refuse
A run where any of these SUCCEEDS is a broken instrument and prints INSTRUMENT FAILED.

It also measures the `Status` column against mapLinearStatus's vocabulary. That section reports a
CLEAN result and is kept so nobody re-derives it: 2,985 of 3,026 real cells (98.6%) are recognised.
The worry it was written to test — that Linear teams rename workflow states freely, so a CSV import
would classify a large fraction as backlog — is NOT supported by this corpus. The 41 that miss are
`Duplicate` (27, four unrelated owners — Linear's own `duplicate` state, which mapLinearStateType
deliberately refuses to classify and reports), a header row leaked into the data (6), `Blocked` (6),
`QA` (1) and `Pending Release` (1). A measured zero is worth as much as a measured defect when the
alternative is a fifth session re-deriving it.

It writes every distinct raw date cell it saw to /tmp/w34-linear-csv-date-cells.txt.

Requires `gh` authenticated. Read-only: GET only, no writes to any repository.
"""
import base64
import io
import csv as csvmod
import subprocess
import sys
import urllib.parse

LINEAR_MARKERS = '"Cycle Number" "Cycle Name" "Triaged" "Canceled"'
FABRICATED_MARKERS = '"Zorbulax Number" "Zorbulax Name" "Triaged" "Canceled"'

# The columns linear_csv_dates.go names, plus the one this probe exists to ask about.
DATE_COLUMNS = ["Created", "Updated", "Started", "Triaged", "Completed", "Canceled",
                "Archived", "Due Date"]

CELLS_OUT = "/tmp/w34-linear-csv-date-cells.txt"


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


def shape(v):
    """Collapse a cell to its SHAPE so distinct instants collapse and distinct formats do not."""
    out = []
    for ch in v:
        if ch.isdigit():
            out.append("9")
        elif ch.isalpha():
            out.append("A")
        else:
            out.append(ch)
    return "".join(out)


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
    col_in_header = {c: 0 for c in DATE_COLUMNS}
    rows_total = 0
    nonempty = {c: 0 for c in DATE_COLUMNS}
    shapes = {c: {} for c in DATE_COLUMNS}
    shape_owners = {c: {} for c in DATE_COLUMNS}
    shape_widths = {c: {} for c in DATE_COLUMNS}
    samples = {c: [] for c in DATE_COLUMNS}
    all_cells = set()
    status_counts = {}
    status_owners = {}

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
            continue
        files += 1
        idx = {}
        for c in DATE_COLUMNS:
            if c in hdr:
                col_in_header[c] += 1
                idx[c] = hdr.index(c)
        i_status = hdr.index("Status") if "Status" in hdr else None
        for row in rdr:
            if not row or len(row) != len(hdr):
                continue
            rows_total += 1
            if i_status is not None:
                sv = row[i_status].strip()
                if sv:
                    status_counts[sv] = status_counts.get(sv, 0) + 1
                    status_owners.setdefault(sv, set()).add(repo.split("/")[0])
            for c, i in idx.items():
                v = row[i].strip()
                if not v:
                    continue
                nonempty[c] += 1
                s = shape(v)
                shapes[c][s] = shapes[c].get(s, 0) + 1
                shape_owners[c].setdefault(s, set()).add(repo.split("/")[0])
                shape_widths[c].setdefault(s, set()).add(len(hdr))
                all_cells.add(v)
                if len(samples[c]) < 3 and v not in [x[1] for x in samples[c]]:
                    samples[c].append((repo, v))

    print(f"  {files} files parsed as Linear exports · {rows_total} data rows\n")
    print("== IS THE COLUMN IN THE HEADER A REAL TENANT EXPORTS? ==")
    for c in DATE_COLUMNS:
        print(f"  {c:<12} in header: {col_in_header[c]:>3}/{files} files · non-empty cells: {nonempty[c]}")

    print("\n== WHAT DOES THE CELL LOOK LIKE (9=digit, A=letter) ==")
    for c in DATE_COLUMNS:
        if not shapes[c]:
            continue
        print(f"  {c}:")
        for s, n in sorted(shapes[c].items(), key=lambda kv: -kv[1])[:6]:
            print(f"    {n:>6}  {s}")
        for repo, v in samples[c]:
            print(f"           e.g. {v}   ({repo})")

    # ⚠ THE SHAPE COUNT ALONE IS NOT EVIDENCE AND THIS IS THE CONTROL THAT DECIDES IT. A count of
    # 746 cells is one measurement if they all came from one owner's toolchain and a fact about the
    # provider if unrelated tenants emit it. Second-hand bytes only become evidence through
    # AGREEMENT ACROSS TENANTS THAT HAVE NEVER MET, so the shape is reported PER OWNER and per
    # header width (the export has grown over time — 29/30/34 columns).
    print("\n== WHO EMITS EACH SHAPE (the provenance control) ==")
    for c in ["Created", "Updated"]:
        print(f"  {c}:")
        for s in sorted(shape_owners[c], key=lambda k: -shapes[c][k]):
            owners = sorted(shape_owners[c][s])
            widths = sorted(shape_widths[c][s])
            print(f"    {shapes[c][s]:>6}  {s}")
            print(f"            owners={len(owners)} {owners}")
            print(f"            header widths={widths}")

    # ── the Status column, against the vocabulary the mapper actually knows ──
    #
    # mapLinearStatus's case literals, TRANSCRIBED BY HAND from internal/importer/csv.go rather
    # than parsed out of it: a classifier that derives its vocabulary from the code it is judging
    # agrees with that code for every possible value. Anything outside this set imports as
    # model.StatusBacklog and is REPORTED — the question this section answers is HOW OFTEN.
    KNOWN = {"backlog", "todo", "to do", "in progress", "in_progress", "in review", "in_review",
             "done", "completed", "cancelled", "canceled"}
    print("\n== THE `Status` COLUMN vs mapLinearStatus's VOCABULARY ==")
    known_n = sum(n for s, n in status_counts.items() if s.strip().lower() in KNOWN)
    unknown = {s: n for s, n in status_counts.items() if s.strip().lower() not in KNOWN}
    total_s = sum(status_counts.values())
    print(f"  {total_s} non-empty Status cells · recognised {known_n} · UNRECOGNISED {sum(unknown.values())}")
    if total_s:
        print(f"  unrecognised share: {100.0 * sum(unknown.values()) / total_s:.1f}%")
    for s, n in sorted(unknown.items(), key=lambda kv: -kv[1])[:15]:
        print(f"    {n:>5}  {s!r}   owners={sorted(status_owners[s])}")

    with open(CELLS_OUT, "w") as fh:
        for v in sorted(all_cells):
            fh.write(v + "\n")
    print(f"\n  {len(all_cells)} distinct raw date cells written to {CELLS_OUT}")
    print("  -> run TestRealLinearExportDateCellsParse to apply the REAL parseLinearCSVTime to them")
    return 0


if __name__ == "__main__":
    sys.exit(main())
