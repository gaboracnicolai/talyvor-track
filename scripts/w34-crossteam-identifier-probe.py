#!/usr/bin/env python3
"""w34-crossteam-identifier-probe.py — the EXTRACT half of the project-key namespace numbers.

THE QUESTION: `issues.identifier` is UNIQUE per (workspace_id, identifier) and carries no team in
it, so two imports into two teams of one workspace collide whenever they carry the same provider
key. How collision-prone is that namespace in real data? This probe writes out the PROJECT KEY of
every `Issue key` cell in every cached export and stops.

⚠ THIS FILE DECIDES NOTHING ABOUT TRACK, and that is #101's lesson applied. Whether two keys
"collide" is a question about the product's UNIQUE constraint and its routing, so the SHIPPED code
answers it next door — TestJiraCSVCorpus_ProjectKeyNamespace in internal/importer. Python's opinion
about what Track considers one identifier would be a fact about Python.

IT DOES NOT RE-FETCH THE BYTES. Populate the cache with scripts/w34-jira-csv-corpus-probe.py (#103),
which owns the network half and the provenance argument for it. Refusing on a cold cache is the
point: a probe that silently re-downloads measures a corpus that changed under it.

⚠⚠ THE UNIT IS THE FILE, AND THE FILE IS NOT THE INSTANCE. Two exports carrying key `TEST` may be
two unrelated Jira sites or the same project exported twice by one repository, and the cache is
keyed by sha256(repo TAB path) with the (repo, path) pair NOT stored — so the bytes on disk cannot
tell them apart. `--owners` re-runs #103's SEARCH (metadata only, no byte re-download) to attribute
each cached blob to a repository OWNER and reports the pairs that are owner-distinct; without it
every number below is per FILE and says so. A count carries the shape of the query that made it.

⚠ RAW BYTES, NEVER `utf-8-sig` — #103's whole finding was a BOM the sibling probe's codec ate. The
BOM is stripped HERE, explicitly, as a FILE prefix, because csv_bom.go strips it the same way.

NEGATIVE CONTROLS RUN FIRST, because a census that quietly reads nothing looks exactly like a clean
one:
  N1  a fabricated column name       must appear in 0 files
  N2  the selection predicate        must find 305 Jira exports among the cached blobs
  N3  an INVERTED predicate          `Summary` must be ABSENT from exactly 2 of them — the live
                                     instrument check made by inverting the question rather than by
                                     planting a fixture
  N4  a fabricated project key       must appear in 0 cells

    python3 scripts/w34-crossteam-identifier-probe.py [--owners]
"""
import collections
import csv as csvmod
import glob
import hashlib
import io
import json
import os
import statistics
import subprocess
import sys

CACHE = "/tmp/w34-jira-corpus"
KEYS_OUT = "/tmp/w34-crossteam-project-keys.json"

KEY_COL = "issue key"              # lowercased ONLY to select files and locate the column
FABRICATED_COL = "zorbulax key"
FABRICATED_PROJECT_KEY = "ZORBULAX"

JIRA_MARKERS = '"Issue key" "Issue id" "Project key"'


def rows_of(raw):
    """RAW BYTES in, rows out. The BOM is a FILE prefix and is removed as one."""
    s = raw.decode("utf-8", "replace")
    if s and s[0] == "\ufeff":
        s = s[1:]
    return list(csvmod.reader(io.StringIO(s)))


def project_key(cell):
    """PROJ-123 -> PROJ. Jira's key is <project>-<number> and the number is the last hyphen group;
    a cell that is not that shape yields None and is counted apart rather than guessed at."""
    cell = cell.strip()
    if "-" not in cell:
        return None
    head, _, tail = cell.rpartition("-")
    if not head or not tail.isdigit():
        return None
    return head


def gh_owner_map():
    """Attribute cached blobs to repository owners by RE-RUNNING #103's search for the FILE LIST
    only. No bytes are re-downloaded: the cache key is sha256(repo TAB path)[:32], so a search hit
    that names a blob already on disk attributes it. Blobs the search no longer returns stay
    unattributed and are reported as such."""
    out = {}
    for page in range(1, 5):
        r = subprocess.run(
            ["gh", "api", "-X", "GET", "search/code", "-f", f"q={JIRA_MARKERS} extension:csv",
             "-f", "per_page=100", "-f", f"page={page}",
             "--jq", '.items[] | "\\(.repository.full_name)\t\\(.path)"'],
            capture_output=True, text=True)
        if r.returncode != 0:
            break
        lines = [l for l in r.stdout.splitlines() if l.strip()]
        for line in lines:
            repo, _, path = line.partition("\t")
            key = hashlib.sha256(f"{repo}\t{path}".encode()).hexdigest()[:32]
            out[key] = repo.split("/")[0]
        if len(lines) < 100:
            break
    return out


def main():
    want_owners = "--owners" in sys.argv
    blobs = sorted(f for f in glob.glob(os.path.join(CACHE, "*")) if os.path.isfile(f))
    if not blobs:
        print(f"cold cache at {CACHE} — this probe does not fetch. Populate it with "
              f"scripts/w34-jira-csv-corpus-probe.py first, which owns the network half.")
        return 2

    fabricated_col_hits = 0
    fabricated_key_hits = 0
    no_summary = 0
    per_file = {}            # blob basename -> sorted list of project keys in it
    unshaped_cells = 0
    total_cells = 0

    for f in blobs:
        try:
            rows = rows_of(open(f, "rb").read())
        except Exception:
            continue
        if not rows:
            continue
        header = [c.strip() for c in rows[0]]
        low = [h.lower() for h in header]
        if FABRICATED_COL in low:
            fabricated_col_hits += 1
        if KEY_COL not in low:
            continue
        if "summary" not in low:
            no_summary += 1
        idx = low.index(KEY_COL)
        keys = set()
        for row in rows[1:]:
            if idx >= len(row):
                continue
            cell = row[idx].strip()
            if not cell:
                continue
            total_cells += 1
            k = project_key(cell)
            if k is None:
                unshaped_cells += 1
                continue
            if k.upper() == FABRICATED_PROJECT_KEY:
                fabricated_key_hits += 1
            keys.add(k)
        per_file[os.path.basename(f)] = sorted(keys)

    print("== NEGATIVE CONTROLS ==")
    ok = True
    print(f"  N1  {FABRICATED_COL!r} in {fabricated_col_hits} files            "
          f"{'ok' if fabricated_col_hits == 0 else 'FAILED — the census is matching noise'}")
    print(f"  N2  {len(per_file)} Jira exports among {len(blobs)} cached blobs   "
          f"{'ok' if len(per_file) == 305 else 'DIFFERENT CORPUS — every number below is about a different set'}")
    print(f"  N3  `Summary` ABSENT from {no_summary} of them              "
          f"{'ok' if no_summary == 2 else 'the inverted predicate moved — re-read before trusting anything below'}")
    print(f"  N4  project key {FABRICATED_PROJECT_KEY!r} in {fabricated_key_hits} cells        "
          f"{'ok' if fabricated_key_hits == 0 else 'FAILED — the extractor is inventing keys'}")
    if fabricated_col_hits or fabricated_key_hits or not per_file:
        ok = False
    if not ok:
        return 2

    keys_to_files = collections.defaultdict(set)
    for blob, keys in per_file.items():
        for k in keys:
            keys_to_files[k].add(blob)
    lengths = sorted(len(k) for k in keys_to_files)
    shared = {k: sorted(v) for k, v in keys_to_files.items() if len(v) >= 2}

    print("\n== EXTRACT (per FILE — see the header on why that is not per instance) ==")
    print(f"  exports read                     {len(per_file)}")
    print(f"  `Issue key` cells                {total_cells} ({unshaped_cells} not <project>-<int>)")
    print(f"  distinct project keys            {len(keys_to_files)}")
    print(f"  project-key length  min/median/max  {lengths[0]}/{statistics.median(lengths):g}/{lengths[-1]}")
    print(f"  keys carried by >= 2 exports     {len(shared)} "
          f"({100.0 * len(shared) / max(1, len(keys_to_files)):.1f}%)")
    top = sorted(shared.items(), key=lambda kv: -len(kv[1]))[:10]
    for k, files in top:
        print(f"      {k!r:14} in {len(files)} exports")

    owners = {}
    if want_owners:
        owners = gh_owner_map()
        attributed = {b: owners[b] for b in per_file if b in owners}
        owner_shared = {}
        for k, files in keys_to_files.items():
            os_ = {attributed[b] for b in files if b in attributed}
            if len(os_) >= 2:
                owner_shared[k] = sorted(os_)
        print("\n== OWNER ATTRIBUTION (search re-run, metadata only — no bytes re-downloaded) ==")
        print(f"  cached exports attributed to an owner   {len(attributed)} of {len(per_file)}")
        print(f"  keys carried by >= 2 DISTINCT OWNERS    {len(owner_shared)}")
        for k, os_ in sorted(owner_shared.items(), key=lambda kv: -len(kv[1]))[:10]:
            print(f"      {k!r:14} {len(os_)} owners: {', '.join(os_[:4])}")

    with open(KEYS_OUT, "w") as f:
        json.dump({"per_file": per_file, "owners": owners}, f)
    print(f"\n  {KEYS_OUT}")
    print("\nNow run the half that decides anything — it asks the product's own routing, not Python:")
    print("  go test ./internal/importer/ -run TestJiraCSVCorpus_ProjectKeyNamespace -v")
    return 0


if __name__ == "__main__":
    sys.exit(main())
