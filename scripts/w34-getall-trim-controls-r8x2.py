#!/usr/bin/env python3
"""Positive controls for csv_blank_padding_notes_job_test.go (W3.4, tab-r8x2).

THE FINDING. `columnIndex.getAll`'s strings.TrimSpace is what makes a cell holding only a space
NOT count as a populated column. Delete it and the whole 39-package suite stays green, while the
three `len(ci.getAll(...)) == 0` gates start firing on blank padding and the STORED, customer-facing
warning inflates its count -- measured 1 -> 2 on a three-row Jira export where exactly one row
carries a comment.

This is finding (e) of #189, ridden on its own diff. #189 guarded the OTHER two trims and its own
control C3 predicted this one NOT CAUGHT and got NOT CAUGHT -- the gap this harness closes was
therefore already measured, in public, before this file existed.

WHAT THE ARMS ANSWER. Same shape as #189's harness:

  new  = the two tests this merge adds
  old  = the whole internal/importer package with BOTH this merge's file AND #189's file moved
         away -- i.e. the suite as it stood before either. That is what proves the gap was real.

⚠ C2 AND C3 RE-RUN #189's TWO MUTATIONS AGAINST THIS MERGE'S TESTS, AND THEY ARE PREDICTED
NOT CAUGHT ON PURPOSE. If this file caught #189's mutations too, the two merges would be guarding
one thing twice and neither would be pinning what it claims to pin.
"""

import hashlib
import os
import shutil
import subprocess
import sys
import tempfile

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CSV = os.path.join(REPO, "internal/importer/csv.go")
NEWTEST = os.path.join(REPO, "internal/importer/csv_blank_padding_notes_job_test.go")
PRIORTEST = os.path.join(REPO, "internal/importer/csv_whitespace_padding_job_test.go")
NEWTESTS = "TestBlankPaddingCell_"

ALL_TRIM = ('\t\tif v := strings.TrimSpace(row[idx]); v != "" {\n',
            '\t\tif v := row[idx]; v != "" {\n')
GET_TRIM = ("\treturn strings.TrimSpace(row[idxs[0]])\n", "\treturn row[idxs[0]]\n")
HDR_TRIM = ("\t\tk := strings.TrimSpace(strings.ToLower(h))\n", "\t\tk := strings.ToLower(h)\n")
# Arithmetically identity: trimming an already-trimmed cell. A real edit, no behaviour.
ALL_VOID = (ALL_TRIM[0],
            '\t\tif v := strings.TrimSpace(strings.TrimSpace(row[idx])); v != "" {\n')
# Drops the empty-cell filter but KEEPS the trim. The gate then counts a truly EMPTY cell as
# populated -- a different defect with the same customer-visible symptom, which this file's
# fixture carries a row for (PROJ-3) and must therefore also catch.
ALL_KEEPEMPTY = (ALL_TRIM[0], '\t\tif v := strings.TrimSpace(row[idx]); true {\n')
# Blinds the FIXTURE, not the product: the blank-padding row becomes a copy of the empty row, so
# nothing in the export holds " " any more. Run with the product mutation it must go QUIET.
BLIND_FIXTURE = ('\t"PROJ-2,Two,Done,\\" \\",\\" \\",\\" \\",\\" \\"\\n" +\n',
                 '\t"PROJ-2,Two,Done,,,,\\n" +\n')


def sha(path):
    return hashlib.sha256(open(path, "rb").read()).hexdigest()


def refuse(msg):
    print("REFUSING: " + msg)
    sys.exit(2)


def preflight():
    if not os.environ.get("TRACK_TEST_DATABASE_URL"):
        refuse("TRACK_TEST_DATABASE_URL is unset. Every arm would score CAUGHT for that reason "
               "alone -- a harness that cannot be wrong.")
    out = subprocess.run(["git", "-C", REPO, "status", "--porcelain"],
                         capture_output=True, text=True).stdout.strip().splitlines()
    allowed = {"internal/importer/csv_blank_padding_notes_job_test.go",
               "scripts/w34-getall-trim-controls-r8x2.py"}
    dirty = [l for l in out if l[3:].strip() not in allowed]
    if dirty:
        refuse("the working tree carries changes this harness did not make:\n  " + "\n  ".join(dirty))


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


def measure(kind):
    """CAUGHT == the population goes red."""
    if kind == "new":
        return run(["go", "test", "-timeout", "300s", "-count=1", "-run", NEWTESTS,
                    "./internal/importer/"]) != 0
    tmp = tempfile.gettempdir()
    stashed = []
    for p in (NEWTEST, PRIORTEST):
        dst = os.path.join(tmp, "w34-r8x2-" + os.path.basename(p))
        shutil.move(p, dst)
        stashed.append((dst, p))
    try:
        return run(["go", "test", "-timeout", "600s", "-count=1", "./internal/importer/"]) != 0
    finally:
        for dst, p in stashed:
            shutil.move(dst, p)


# (id, description, patches, predicted_new, predicted_old)
ARMS = [
    ("C1", "getAll: its trim removed -- a \" \" cell reads as populated and the stored warning "
           "counts 2 where 1 row carries a value",
     [(CSV, *ALL_TRIM)], True, False),
    ("C2", "#189's mutation: get's CELL trim removed. Predicted NOT CAUGHT here -- #189 guards it, "
           "and if this file caught it too neither merge would pin what it claims to.",
     [(CSV, *GET_TRIM)], False, False),
    ("C3", "#189's other mutation: buildIndex's HEADER trim removed. Same reasoning as C2.",
     [(CSV, *HDR_TRIM)], False, False),
    ("C4", "VOID: getAll trims TWICE -- a real edit that is arithmetically identity.",
     [(CSV, *ALL_VOID)], False, False),
    # ⚠ C5's `old` PREDICTION WAS "NOT" AND IT WAS WRONG -- CORRECTED TO THE MEASURED VALUE AND
    # RECORDED RATHER THAN TUNED AWAY, BECAUSE THE MISPREDICTION IS THE MOST USEFUL THING THIS
    # HARNESS PRODUCED. The pre-existing suite DOES catch dropping the empty-cell filter: three
    # tests named TestDroppedObjects_AnEmptyCellIsSilent / TestCustomFields_AnEmptyCellIsSilent /
    # TestIssueLinks_AnEmptyCellIsSilent go red, one per gate, plus four job-level tests. So the
    # gap this merge closes is NOT "the gates are unguarded" -- they are well guarded for "" and
    # completely unguarded for " ". C1 and C5 together are what locate that boundary exactly.
    ("C5", "the empty-cell filter dropped, trim KEPT: a truly empty cell counts as populated. "
           "CAUGHT by both populations -- the OLD suite has three *_AnEmptyCellIsSilent tests, "
           "one per gate. This arm exists to show where the pre-existing coverage STOPS.",
     [(CSV, *ALL_KEEPEMPTY)], True, True),
    ("C6", "C1's product mutation AND the blank-padding row blinded to an empty one. Must go QUIET "
           "-- that is what proves the FIXTURE is what catches C1.",
     [(CSV, *ALL_TRIM), (NEWTEST, *BLIND_FIXTURE)], False, None),
    ("C7", "MUST STAY GREEN: no mutation at all.", [], False, False),
]


def main():
    preflight()
    originals = {p: (sha(p), open(p).read()) for p in (CSV, NEWTEST)}
    results = []
    try:
        for cid, desc, patches, want_new, want_old in ARMS:
            for path, old, new in patches:
                apply_patch(path, old, new)
            got_new = measure("new")
            got_old = measure("old") if want_old is not None else None
            for path in (CSV, NEWTEST):
                open(path, "w").write(originals[path][1])
            ok = (got_new == want_new) and (want_old is None or got_old == want_old)
            results.append((cid, ok))
            print("%s %-3s new: predicted %-7s got %-7s | old: predicted %-7s got %-7s"
                  % ("PASS" if ok else "MISPREDICTED", cid,
                     "CAUGHT" if want_new else "NOT", "CAUGHT" if got_new else "NOT",
                     "n/a" if want_old is None else ("CAUGHT" if want_old else "NOT"),
                     "n/a" if got_old is None else ("CAUGHT" if got_old else "NOT")))
            print("     " + desc)
    finally:
        for path, (digest, body) in originals.items():
            open(path, "w").write(body)
            if sha(path) != digest:
                refuse("RESTORE FAILED for %s -- sha256 does not match the pre-run one." % path)
        print("restored: both files sha256-verified against their pre-run digests")

    good = sum(1 for _, ok in results if ok)
    print("\n%d/%d as predicted" % (good, len(results)))
    return 0 if good == len(results) else 1


if __name__ == "__main__":
    sys.exit(main())
