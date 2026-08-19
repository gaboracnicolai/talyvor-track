#!/usr/bin/env python3
"""w34-linear-description-controls-2q7v.py — positive controls for
internal/importer/linear_csv_description_test.go.

The three tests in that file passed on their FIRST run against pristine code, which is the
state in which a test that asserts nothing is indistinguishable from a test that asserts the
right thing. Each control below names its PREDICTED catcher BEFORE the run and fails the
script if the prediction is wrong in either direction — an unpredicted catcher is as
interesting as a missing one, because it means a test is firing for a reason its name does not
describe.

Scored by SET SUBTRACTION against C0's measured failing set, never by an exit code.
Every mutation is restored in a `finally` and the restore is verified by sha256.
"""

import hashlib
import io
import os
import re
import subprocess
import sys

REPO = "/Users/ng/talyvor-track"
CSV = REPO + "/internal/importer/csv.go"
DSN = "postgres://postgres:postgres@localhost:55442/postgres?sslmode=disable"

NEW = {
    "reaches": "TestLinearCSV_TheDescriptionColumnReachesTheModel",
    "absent": "TestLinearCSV_AnExportWithNoDescriptionColumnImportsAnEmptyDescription",
    "parity": "TestCSVDescription_BothTransportsLandTheSameColumn",
}

LINEAR_READ = (602, 'Description: ci.get(row, "Description"),')
JIRA_READ = (828, 'Description: ci.get(row, "Description"),')

# (id, [(line, old, new), ...], prediction over the THREE NEW TESTS, why)
CONTROLS = [
    ("P1-linear-column-lost",
     [(*LINEAR_READ, 'Description: ci.get(row, "Description"+"_MUTP1"),')],
     {"reaches", "parity"},
     "THE MEASURED GAP ITSELF: the Linear read points at a column no header carries. "
     "`absent` must stay GREEN — it asserts \"\" and \"\" is what a missing column yields, "
     "so a test that fired here would be firing for the wrong reason."),

    ("P2-linear-falls-back-to-title",
     [(*LINEAR_READ, "Description: title,")],
     {"reaches", "absent", "parity"},
     "The plausible WRONG FIX after P1: substitute the title. `reaches` catches it on its "
     "discrimination clause and `absent` catches the fabrication. ⚠ MY FIRST PREDICTION "
     "HERE OMITTED `parity` AND WAS WRONG, AND THE CORRECTION IS THE USEFUL PART: I "
     "reasoned that both mappers would still agree, but only the LINEAR side is mutated, "
     "so Jira still yields `beta-description` while Linear now yields `alpha-title` — they "
     "disagree, and parity is right to fire. The prediction is corrected to the measured "
     "fact rather than the test being loosened to match a wrong guess."),

    ("P3-jira-column-lost",
     [(*JIRA_READ, 'Description: ci.get(row, "Description"+"_MUTP3"),')],
     {"parity"},
     "The OTHER copy of the same one-line read. Only `parity` covers it among the new "
     "tests — that is the whole reason the parity test exists rather than a second "
     "Linear-only assertion."),

    ("P4-BOTH-columns-lost-identically",
     [(*LINEAR_READ, 'Description: ci.get(row, "Description"+"_MUTP4"),'),
      (*JIRA_READ, 'Description: ci.get(row, "Description"+"_MUTP4"),')],
     {"reaches", "parity"},
     "BOTH mappers broken the SAME way, so both return \"\" and the two AGREE. This is the "
     "control that says `parity` pins the VALUE and not merely equality — an agreement-only "
     "test would go green here, which is the classic shape of a cross-check satisfied by "
     "both sides being equally wrong. It must fire."),

    ("P5-VOID-identity",
     [(*LINEAR_READ, 'Description: ci.get(row, "Description"+""),')],
     set(),
     "IDENTITY EDIT. Must be caught by NOTHING. If any test reds here the harness is "
     "reporting that an edit happened, not that a defect was seen."),
]


def sha(p):
    return hashlib.sha256(io.open(p, "rb").read()).hexdigest()


def run():
    p = subprocess.run(["go", "test", "-count=1", "./internal/importer/"], cwd=REPO,
                       capture_output=True, text=True,
                       env={**os.environ, "TRACK_TEST_DATABASE_URL": DSN})
    out = p.stdout + p.stderr
    if "[build failed]" in out or re.search(r"^# github\.com/", out, re.M):
        return None, True
    return {m.group(1) for m in re.finditer(r"^\s*--- FAIL: (\S+)", out, re.M)}, False


def main():
    pristine = io.open(CSV, encoding="utf-8").read()
    p_sha = sha(CSV)
    print(f"csv.go pristine sha256 = {p_sha}\n")

    c0, broken = run()
    if broken:
        raise SystemExit("C0 does not build")
    print(f"C0 failing set: {sorted(c0) if c0 else 'EMPTY'}\n")

    rows, bad = [], False
    try:
        for cid, edits, predicted, why in CONTROLS:
            L = pristine.split("\n")
            for line, old, new in edits:
                if old not in L[line - 1]:
                    raise SystemExit(f"ANCHOR LOST {cid} at csv.go:{line}: {L[line-1]!r}")
                L[line - 1] = L[line - 1].replace(old, new, 1)
            io.open(CSV, "w", encoding="utf-8").write("\n".join(L))

            failed, broken = run()
            io.open(CSV, "w", encoding="utf-8").write(pristine)
            if sha(CSV) != p_sha:
                raise SystemExit(f"RESTORE FAILED after {cid}")
            if broken:
                print(f"{cid}: BROKEN BUILD — not scored")
                bad = True
                continue

            added = failed - c0
            fired = {k for k, name in NEW.items() if name in added}
            ok = fired == predicted
            bad = bad or not ok
            others = sorted(a for a in added if a not in NEW.values())
            print(f"{cid}")
            print(f"    PREDICTED (new tests): {sorted(predicted) or ['none']}")
            print(f"    FIRED     (new tests): {sorted(fired) or ['none']}   "
                  f"{'OK' if ok else '<<< PREDICTION WRONG'}")
            print(f"    other tests also red : {len(others)}"
                  f"{' — ' + ', '.join(others[:4]) if others else ''}")
            print(f"    why: {why}\n")
            rows.append((cid, sorted(predicted), sorted(fired), ok))
    finally:
        io.open(CSV, "w", encoding="utf-8").write(pristine)
        final = sha(CSV)
        print(f"restored csv.go sha256 = {final} "
              f"({'OK' if final == p_sha else 'MISMATCH!!'})")
        if final != p_sha:
            sys.exit(2)

    print("\n" + "=" * 72)
    for cid, pred, fired, ok in rows:
        print(f"{cid:<30} {'OK' if ok else 'PREDICTION WRONG':<18} fired={fired}")
    print("=" * 72)
    print("ALL CONTROLS AS PREDICTED" if not bad else "AT LEAST ONE CONTROL DID NOT BEHAVE")
    sys.exit(1 if bad else 0)


if __name__ == "__main__":
    main()
