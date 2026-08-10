#!/usr/bin/env python3
"""w34-csv-clobbered-columns-probe.py — the EXTRACT half of the clobbered-column numbers.

THE QUESTION: how many real Jira exports carry no column for a field the re-import conflict arm
CLOBBERS? An export that carries no `Description` or `Labels` column tells the write path "this
issue has no description" when it said nothing about descriptions at all, and a re-import empties
the stored value. csv_clobbered_columns.go quotes the answer in its header; this file and
TestJiraCSVCorpus_ClobberedColumnPresence in internal/importer are what make it re-runnable.

⚠ THIS FILE DECIDES NOTHING, AND THAT IS #101's LESSON APPLIED. It writes out the RAW HEADER of
every cached export and stops. Whether a header "carries" a column is a question about
`columnIndex.has` — strings.ToLower is Unicode-aware and Python's str.lower() is not the same
function — so the SHIPPED code answers it next door. A probe that transcribes the predicate it is
judging agrees with that predicate for every possible value, including a wrong one.

IT DOES NOT RE-FETCH. Populate the cache with scripts/w34-jira-csv-corpus-probe.py (#103), which
owns the network half and the provenance argument for it. Refusing on a cold cache is the point: a
probe that silently re-downloads measures a corpus that changed under it.

⚠ RAW BYTES, NEVER `utf-8-sig` — #103's whole finding was a BOM the sibling probe's codec ate. The
BOM is stripped HERE, explicitly, as a FILE PREFIX, because csv_bom.go strips it the same way and a
header census that did not would report 66 files as having no `Summary` column.

⚠ THE FILE-SELECTION PREDICATE IS CASE-INSENSITIVE, WHICH IS NOT WHAT #104's PROBE DID, AND THE
DIFFERENCE IS ONE FILE. That probe selects Jira exports on a case-SENSITIVE `Issue key` and reports
304; buildIndex lowercases, so the product would also read a file headed `Issue Key` — 305. The
extra file is a six-column event log (`Timestamp,Event Type,Issue Key,…`) with no `Summary`, so
every row of it is refused by errEmptyTitle either way. Both numbers are true of their own question
and this one is stated rather than inherited.

NEGATIVE CONTROLS RUN FIRST, because a census that quietly reads nothing looks exactly like a clean
one:
  N1  a fabricated column name    must appear in 0 files
  N2  the selection predicate     must find 305 Jira exports among the cached blobs
  N3  an INVERTED predicate       `Summary` must be ABSENT from exactly 2 of them — a live-instrument
                                  check made by inverting the question, not by planting a fixture

    python3 scripts/w34-csv-clobbered-columns-probe.py
"""
import csv as csvmod
import glob
import io
import json
import os
import sys

CACHE = "/tmp/w34-jira-corpus"
HEADERS_OUT = "/tmp/w34-clobbered-column-headers.json"

KEY_COL = "issue key"          # lowercased here ONLY to select files; the census re-asks with the product's own predicate
FABRICATED_COL = "zorbulax description"


def rows_of(raw):
    """RAW BYTES in, rows out. The BOM is a FILE prefix and is removed as one — see the header."""
    s = raw.decode("utf-8")
    if s and s[0] == "﻿":
        s = s[1:]
    return list(csvmod.reader(io.StringIO(s)))


def main():
    blobs = sorted(f for f in glob.glob(os.path.join(CACHE, "*")) if os.path.isfile(f))
    if not blobs:
        print(f"cold cache at {CACHE} — this probe does not fetch. Populate it with "
              f"scripts/w34-jira-csv-corpus-probe.py first, which owns the network half.")
        return 2

    headers, fabricated_hits, no_summary = {}, 0, 0
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
            fabricated_hits += 1
        if KEY_COL not in low:
            continue
        headers[os.path.basename(f)] = header
        if "summary" not in low:
            no_summary += 1

    print("== NEGATIVE CONTROLS ==")
    print(f"  N1  {FABRICATED_COL!r} in {fabricated_hits} files          "
          f"{'ok' if fabricated_hits == 0 else 'FAILED — the census is matching noise'}")
    print(f"  N2  {len(headers)} Jira exports among {len(blobs)} cached blobs  "
          f"{'ok' if len(headers) == 305 else 'DIFFERENT CORPUS — every number below is about a different set'}")
    print(f"  N3  `Summary` ABSENT from {no_summary} of them            "
          f"{'ok' if no_summary == 2 else 'the inverted predicate moved — re-read before trusting anything below'}")
    if fabricated_hits or not headers:
        return 2

    with open(HEADERS_OUT, "w") as f:
        json.dump(headers, f)

    print("\n== EXTRACT ==")
    print(f"  export headers written: {len(headers)}")
    print(f"  {HEADERS_OUT}")
    print("\nNow run the half that decides anything — it asks columnIndex.has, not Python:")
    print("  go test ./internal/importer/ -run TestJiraCSVCorpus_ClobberedColumnPresence -v")
    return 0


if __name__ == "__main__":
    sys.exit(main())
