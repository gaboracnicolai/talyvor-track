#!/usr/bin/env python3
"""Positive controls for csv_whitespace_padding_job_test.go (W3.4, tab-r8x2).

THE FINDING THIS HARNESS EXISTS FOR. tab-r8kw ended #188 by handing on a measurement it did not
ride: `columnIndex.get` can stop trimming whitespace and `go test ./internal/importer/` stays
`ok`. Re-measured here on 2cc81e9 and extended -- the SIBLING trim in `buildIndex` is silent too.
Both are load-bearing for what lands in the issues table.

WHAT EACH ARM ANSWERS. Every arm mutates ONE line of internal/importer/csv.go, runs two
populations, and compares the result against a PREDICTION written before the run:

  new  = the four tests this merge adds
  old  = the whole internal/importer package with the new file MOVED AWAY -- i.e. the suite
         exactly as it stood on main. This is what proves the gap was real rather than asserted.

REFUSALS (a control that runs on the wrong tree reports about the wrong tree):
  * a working tree carrying modifications other than the expected new files
  * a missing TRACK_TEST_DATABASE_URL -- the real-Postgres tests would then fail for that
    reason and every arm would score CAUGHT
  * a mutation that changed no bytes, or produced a zero-byte file
  * a post-run sha256 that does not match the pre-run one

C3 AND C4 ARE THE ARMS THAT CAN EMBARRASS ME, and they are here on purpose. A harness whose every
arm is predicted CAUGHT has not been tested against the possibility that it reacts to ANY edit.
"""

import hashlib
import os
import shutil
import subprocess
import sys
import tempfile

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CSV = os.path.join(REPO, "internal/importer/csv.go")
NEWTEST = os.path.join(REPO, "internal/importer/csv_whitespace_padding_job_test.go")
NEWTESTS = "TestPaddedCells_|TestPaddedIdentifier_|TestWhitespaceOnlyTitle_|TestPaddedHeader_"

# The three trims, each quoted with the exact bytes on main so a drifted file REFUSES rather than
# silently mutating nothing.
GET_TRIM = ("\treturn strings.TrimSpace(row[idxs[0]])\n", "\treturn row[idxs[0]]\n")
HDR_TRIM = ("\t\tk := strings.TrimSpace(strings.ToLower(h))\n", "\t\tk := strings.ToLower(h)\n")
ALL_TRIM = ('\t\tif v := strings.TrimSpace(row[idx]); v != "" {\n',
            '\t\tif v := row[idx]; v != "" {\n')
# Arithmetically identity: trimming an already-trimmed string. A real edit with no behaviour.
GET_VOID = (GET_TRIM[0], "\treturn strings.TrimSpace(strings.TrimSpace(row[idxs[0]]))\n")

# C5 blinds the FIXTURE rather than the product: the padded export becomes a copy of the clean one,
# so TestPaddedCells_ compares clean against clean. Run together with C1's product mutation, it
# must go quiet -- which is what proves the fixture, not luck, is what catches C1.
BLIND_FIXTURE = (
    'const wsPaddedCellsLinearExport = "ID,Team,Title,Description,Status,Priority\\n" +\n'
    '\t"\\"  AWA-27  \\",\\"  Awaqi  \\",\\"  Issue one  \\",\\"  body one  \\",\\"  Todo  \\",\\"  High  \\"\\n" +\n'
    '\t"\\"  SAN-617  \\",\\"  Sanjiovani  \\",\\"  Issue two  \\",\\"  body two  \\",\\"  Done  \\",\\"  Low  \\"\\n"',
    'const wsPaddedCellsLinearExport = wsCleanLinearExport',
)


def sha(path):
    return hashlib.sha256(open(path, "rb").read()).hexdigest()


def refuse(msg):
    print("REFUSING: " + msg)
    sys.exit(2)


def preflight():
    if not os.environ.get("TRACK_TEST_DATABASE_URL"):
        refuse("TRACK_TEST_DATABASE_URL is unset. The real-Postgres tests would fail for that "
               "reason and EVERY arm would score CAUGHT -- a harness that cannot be wrong.")
    out = subprocess.run(["git", "-C", REPO, "status", "--porcelain"],
                         capture_output=True, text=True).stdout.strip().splitlines()
    allowed = {"internal/importer/csv_whitespace_padding_job_test.go",
               "scripts/w34-column-index-trim-controls-r8x2.py"}
    dirty = [l for l in out if l[3:].strip() not in allowed]
    if dirty:
        refuse("the working tree carries changes this harness did not make, so a red could be "
               "theirs rather than the mutation's:\n  " + "\n  ".join(dirty))


def apply_patch(path, old, new):
    src = open(path).read()
    n = src.count(old)
    if n != 1:
        refuse("the anchor for %s occurs %d times, not once -- the file has drifted and this "
               "mutation would not be the one described." % (os.path.basename(path), n))
    patched = src.replace(old, new, 1)
    if patched == src:
        refuse("mutation changed no bytes")
    open(path, "w").write(patched)
    if os.path.getsize(path) == 0:
        refuse("mutation produced a zero-byte file")


def run(cmd):
    return subprocess.run(cmd, cwd=REPO, capture_output=True, text=True).returncode


def measure(kind, run_filter=None):
    """CAUGHT == the population goes red."""
    if kind == "new":
        return run(["go", "test", "-timeout", "300s", "-count=1", "-run",
                    run_filter or NEWTESTS, "./internal/importer/"]) != 0
    stash = os.path.join(tempfile.gettempdir(), "w34-r8x2-newtest.go")
    shutil.move(NEWTEST, stash)
    try:
        return run(["go", "test", "-timeout", "600s", "-count=1", "./internal/importer/"]) != 0
    finally:
        shutil.move(stash, NEWTEST)


# (id, description, [(file, old, new)], predicted_new, predicted_old, run_filter)
#
# ⚠ C5 IS SCOPED TO ONE TEST AND ITS FIRST DRAFT WAS NOT, WHICH IS WHY IT MISPREDICTED.
# Unscoped it ran all four new tests, and TestWhitespaceOnlyTitle_ catches C1's product mutation
# from its OWN fixture — so blinding the padded-cells fixture could never make the population go
# quiet. The arm was measuring "does anything in this file catch C1", which is C1's question, not
# C5's. Scoped to TestPaddedCells_ it asks the question it was written for.
ARMS = [
    ("C1", "get: the CELL trim removed -- padded identifier/title/description reach the DB",
     [(CSV, *GET_TRIM)], True, False, None),
    ("C2", "buildIndex: the HEADER trim removed -- a TRAILING-padded or quoted header (`Title `, "
           "`Title\\t`, `\" Title \"`) loses every title and the whole file is rejected. NOT "
           "`ID, Title`: rd.TrimLeadingSpace has already removed that, and the first draft of the "
           "fixture used it and could not fail. This arm is what caught that.",
     [(CSV, *HDR_TRIM)], True, False, None),
    ("C3", "getAll: its trim removed -- MEASURED to be invisible to what lands (splitLabels "
           "re-trims and drops empties); only three len(getAll)==0 note sites move. This merge "
           "does NOT claim it, so the new tests must stay QUIET.",
     [(CSV, *ALL_TRIM)], False, False, None),
    ("C4", "VOID: get trims TWICE -- a real edit that is arithmetically identity. Nothing may move.",
     [(CSV, *GET_VOID)], False, False, None),
    ("C5", "C1's product mutation AND the padded fixture blinded to the clean one. The new tests "
           "must go QUIET, which is what proves the FIXTURE is what catches C1.",
     [(CSV, *GET_TRIM), (NEWTEST, *BLIND_FIXTURE)], False, None, "TestPaddedCells_"),
    ("C5b", "C1's product mutation, fixture INTACT, same single test. The mirror of C5: this one "
             "must be CAUGHT, which is what makes C5's silence mean something.",
     [(CSV, *GET_TRIM)], True, None, "TestPaddedCells_"),
    ("C6", "MUST STAY GREEN: no mutation at all.", [], False, False, None),
]


def main():
    preflight()
    originals = {p: (sha(p), open(p).read()) for p in (CSV, NEWTEST)}
    results = []
    try:
        for cid, desc, patches, want_new, want_old, run_filter in ARMS:
            for path, old, new in patches:
                apply_patch(path, old, new)
            got_new = measure("new", run_filter)
            got_old = measure("old") if want_old is not None else None
            for path in (CSV, NEWTEST):
                open(path, "w").write(originals[path][1])
            ok = (got_new == want_new) and (want_old is None or got_old == want_old)
            results.append((cid, desc, want_new, got_new, want_old, got_old, ok))
            print("%s %-4s new: predicted %-9s got %-9s | old: predicted %-9s got %-9s"
                  % ("PASS" if ok else "MISPREDICTED", cid,
                     "CAUGHT" if want_new else "NOT",
                     "CAUGHT" if got_new else "NOT",
                     "n/a" if want_old is None else ("CAUGHT" if want_old else "NOT"),
                     "n/a" if got_old is None else ("CAUGHT" if got_old else "NOT")))
            print("     " + desc)
    finally:
        for path, (digest, body) in originals.items():
            open(path, "w").write(body)
            if sha(path) != digest:
                refuse("RESTORE FAILED for %s -- sha256 does not match the pre-run one. The tree "
                       "is not as this harness found it." % path)
        print("restored: both files sha256-verified against their pre-run digests")

    good = sum(1 for r in results if r[6])
    print("\n%d/%d as predicted" % (good, len(results)))
    return 0 if good == len(results) else 1


if __name__ == "__main__":
    sys.exit(main())
