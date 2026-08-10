#!/usr/bin/env python3
"""w34-jira-csv-status-category-probe.py — the EXTRACT half of #104's evidence.

#104 merged the Jira CSV `Status Category` read and quoted six numbers in
internal/importer/jira_csv_status_category.go's header. Those numbers were true when they were
written and NOTHING IN THIS REPOSITORY COULD REPRODUCE THEM: the census ran in a scratch file that
was deleted before the merge. A stated number is not a re-runnable measurement — this file and
internal/importer/jira_csv_corpus_census_test.go are the two halves that make it one.

WHAT IT DOES, AND THE ONE THING IT DELIBERATELY DOES NOT DO. It reads the corpus #103 already
FETCHED and CACHED at /tmp/w34-jira-corpus and writes a small extract to /tmp. It does NOT decide
whether a value is recognised — Python's opinion about Track's vocabulary would be a fact about this
file. The package's OWN mapJiraStatus / mapJiraCSVStatusCategory / mapJiraPriority answer that, in
TestJiraCSVCorpus_* next door. #101's lesson, applied: a classifier that transcribes the code it is
judging agrees with that code for every possible value, including a wrong one.

IT DOES NOT RE-FETCH. Refusing on a cold cache is the point: a probe that silently re-downloads
measures a corpus that has changed under it, and two runs stop being comparable. Populate the cache
with scripts/w34-jira-csv-corpus-probe.py (#103), which owns the network half and the provenance
argument for it.

⚠ RAW BYTES, NEVER `utf-8-sig`. #103's whole finding was a BOM the sibling probe's codec ate. The
BOM is stripped HERE, explicitly, once, as a file prefix — because the product strips it too
(csv_bom.go) and a header census that did not would report 66 files as having no `Status` column.

⚠ RAGGED ROWS ARE COUNTED, NOT DISCARDED. #102's finding was in exactly the rows #99/#100/#101
filtered out. A row shorter than its header supplies "" for the columns past its end, which is what
the product's columnIndex.get does, and the two are reported apart by the census next door.

⚠ THE ROW PREDICATE IS STATED BECAUSE IT SETS THE NUMBER: a data row is one with at least one
non-empty cell. That is why this reports 17,657 rows where #103 reported 17,921 — an inherited count
carries its query shape, and both are true of their own question.

NEGATIVE CONTROLS RUN FIRST, because a census that quietly reads nothing looks exactly like a clean
one:
  N1  a fabricated column name          must appear in 0 files
  N2  the file-selection predicate      must find 304 Jira exports among 346 cached blobs
  N3  a column the product DOES read    `Status` must appear in 303 of the 304 (the live-instrument
                                        check: an inverted predicate, not a planted fixture)

    python3 scripts/w34-jira-csv-status-category-probe.py
"""
import collections
import csv as csvmod
import glob
import io
import json
import os
import sys

CACHE = "/tmp/w34-jira-corpus"
TRIPLES_OUT = "/tmp/w34-jira-statuscat-triples.json"
PERFILE_OUT = "/tmp/w34-jira-statuscat-perfile.json"
PRIORITY_OUT = "/tmp/w34-jira-priority-values.json"

# Spelled exactly as internal/importer reads them.
STATUS_COL = "Status"
CATEGORY_COL = "Status Category"
RESOLVED_COL = "Resolved"
PRIORITY_COL = "Priority"
KEY_COL = "Issue key"  # jointly distinctive of a Jira export; present in no Linear one
FABRICATED_COL = "Zorbulax Category"


def rows_of(raw):
    """RAW BYTES in, rows out. The BOM is a FILE prefix and is removed as one — see the header."""
    s = raw.decode("utf-8")
    if s and s[0] == "﻿":
        s = s[1:]
    return list(csvmod.reader(io.StringIO(s)))


def cell(row, idx):
    """What columnIndex.get would answer: "" for a column past a short row's end."""
    return row[idx].strip() if 0 <= idx < len(row) else ""


def index_of(header, name):
    for i, c in enumerate(header):
        if c == name:
            return i
    return -1


def main():
    blobs = sorted(f for f in glob.glob(os.path.join(CACHE, "*")) if os.path.isfile(f))
    if not blobs:
        print(f"cold cache at {CACHE} — this probe does not fetch. Populate it with "
              f"scripts/w34-jira-csv-corpus-probe.py first, which owns the network half.")
        return 2

    jira, fabricated_hits, with_status = [], 0, 0
    for f in blobs:
        try:
            rows = rows_of(open(f, "rb").read())
        except Exception:
            continue
        if not rows:
            continue
        header = [c.strip() for c in rows[0]]
        if FABRICATED_COL in header:
            fabricated_hits += 1
        if KEY_COL not in header:
            continue
        jira.append((os.path.basename(f), header, rows))
        if STATUS_COL in header:
            with_status += 1

    print("== NEGATIVE CONTROLS ==")
    print(f"  N1  {FABRICATED_COL!r} in {fabricated_hits} files                    "
          f"{'ok' if fabricated_hits == 0 else 'FAILED — the census is matching noise'}")
    print(f"  N2  {len(jira)} Jira exports among {len(blobs)} cached blobs        "
          f"{'ok' if len(jira) == 304 else 'DIFFERENT CORPUS — every number below is about a different set'}")
    print(f"  N3  {STATUS_COL!r} present in {with_status} of {len(jira)}                     "
          f"{'ok' if with_status else 'FAILED — the instrument read no headers at all'}")
    if fabricated_hits or not with_status:
        return 2

    triples = collections.Counter()
    perfile, priorities = {}, collections.Counter()
    files_with_category, total_rows = 0, 0
    for name, header, rows in jira:
        si = index_of(header, STATUS_COL)
        ci = index_of(header, CATEGORY_COL)
        ri = index_of(header, RESOLVED_COL)
        pi = index_of(header, PRIORITY_COL)
        if ci >= 0:
            files_with_category += 1
        per = collections.Counter()
        for r in rows[1:]:
            if not any(x.strip() for x in r):
                continue
            total_rows += 1
            status, category = cell(r, si), cell(r, ci)
            triples[(status, category, cell(r, ri) != "")] += 1
            per[(status, category)] += 1
            priorities[cell(r, pi)] += 1
        perfile[name] = {f"{s}\t{c}": n for (s, c), n in per.items()}

    with open(TRIPLES_OUT, "w") as f:
        json.dump({f"{s}\t{c}\t{int(d)}": n for (s, c, d), n in triples.items()}, f)
    with open(PERFILE_OUT, "w") as f:
        json.dump(perfile, f)
    with open(PRIORITY_OUT, "w") as f:
        json.dump(dict(priorities), f)

    print("\n== EXTRACT ==")
    print(f"  Jira CSV exports                     {len(jira)}")
    print(f"    carrying a {CATEGORY_COL!r} column   {files_with_category}")
    print(f"  data rows (>=1 non-empty cell)       {total_rows}")
    print(f"  distinct (status, category, resolved?) {len(triples)}")
    print(f"  distinct raw {PRIORITY_COL} values          {len(priorities)}")
    print(f"\n  written: {TRIPLES_OUT}\n           {PERFILE_OUT}\n           {PRIORITY_OUT}")
    print("\nNow run the half that decides anything:")
    print("  go test ./internal/importer/ -run TestJiraCSVCorpus -v")
    return 0


if __name__ == "__main__":
    sys.exit(main())
