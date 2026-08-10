#!/usr/bin/env python3
"""w34-jira-csv-bom-controls.py — positive controls for the UTF-8 BOM fix (csv_bom.go).

A guard that has never been watched fail is a guard nobody knows still works. Each control below
MUTATES the product, names the test that MUST go red BEFORE the run, and names the tests that MUST
STAY GREEN. The verdict is read from the set of FAILING TEST NAMES **and the assertion message**,
never from an exit code: two of these controls are caught by the same test name and differ only in
which sentence fired, and an exit code cannot tell them apart.

  C1  revert the fix                          the whole finding
  C2  strip the BOM from EVERY header cell    over-correction: a mid-header U+FEFF is not a BOM
  C3  strip the BOM from every CELL VALUE     over-correction: rewriting a customer's text
  C4  discard three bytes unconditionally     the floor: 238 of 304 files carry no BOM
  C5  fix only the Jira path, not the seam    the Linear guard + the SYNC/ASYNC seam split
  C6  route every row through Create          the routing/duplication half, apart from the job row
  C7  strip on header[0] instead of the bytes INVERTED — behaviourally identical, must NOT be caught
  C8  refuse a file shorter than a BOM        the short-input boundary

⚠ EVERY ANCHOR IS COUNTED BEFORE ANY BYTE IS WRITTEN (#71's lesson: a substitution that matches
nothing is byte-indistinguishable from a working guard), all of a control's edits are applied to ONE
in-memory copy (#99's lesson: rebuilding each edit from the saved bytes silently keeps only the
last), and the restore runs in a `finally` with a sha256 comparison (#102's lesson: a crash between
mutate and restore leaves the mutation on disk).

    TRACK_TEST_DATABASE_URL=... python3 scripts/w34-jira-csv-bom-controls.py
"""
import hashlib
import os
import re
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SRC = os.path.join(ROOT, "internal/importer/source.go")
BOM = os.path.join(ROOT, "internal/importer/csv_bom.go")
CSV = os.path.join(ROOT, "internal/importer/csv.go")

# Every test this merge added. A control's verdict is read against this whole set, so a mutation
# that reddens something unpredicted is visible rather than absorbed.
MINE = [
    "TestCSVBOM_TheLanguageFactsThisRestsOn",
    "TestJiraCSV_ABOMdExportImportsItsRows",
    "TestJiraCSV_AnExportWithNoBOMImportsExactlyAsBefore",
    "TestLinearCSV_ABOMdExportStillReadsTheRoutingKey",
    "TestCSVBOM_OnlyTheFILEStartIsStripped",
    "TestCSVBOM_AFileThatIsOnlyABOMIsNotAnError",
    "TestJobRow_JiraCSV_ABOMdExportIsNotReportedAsAFailedImport",
    "TestJobRow_JiraCSV_ABOMdExportLandsItsRowsUnderTheProviderKey",
    "TestJobRow_JiraCSV_ReimportingABOMdExportDoesNotDuplicate",
]

FIXED_CALL = "rd := csv.NewReader(skipUTF8BOM(r))"

CONTROLS = [
    dict(
        name="C1  revert the fix",
        edits=[(SRC, FIXED_CALL, "rd := csv.NewReader(r)")],
        predict={"TestJiraCSV_ABOMdExportImportsItsRows",
                 "TestLinearCSV_ABOMdExportStillReadsTheRoutingKey",
                 "TestJobRow_JiraCSV_ABOMdExportIsNotReportedAsAFailedImport",
                 "TestJobRow_JiraCSV_ABOMdExportLandsItsRowsUnderTheProviderKey",
                 "TestJobRow_JiraCSV_ReimportingABOMdExportDoesNotDuplicate"},
        note="an UNDER-specific control: it reddens five guards at once, so its CAUGHT says less "
             "about any one of them than the verdict suggests. C2/C3/C5/C6/C8 are the narrow ones.",
    ),
    dict(
        name="C2  strip the BOM from EVERY header cell",
        edits=[(CSV,
                'k := strings.TrimSpace(strings.ToLower(h))',
                'k := strings.TrimPrefix(strings.TrimSpace(strings.ToLower(h)), "\\uFEFF")')],
        predict={"TestCSVBOM_OnlyTheFILEStartIsStripped"},
        expect_msg="Priority = ",
        note="the over-correction the refusal exists for: a U+FEFF in a LATER header cell is ZERO "
             "WIDTH NO-BREAK SPACE, not a byte-order mark, and reading that column means inventing "
             "a name the export did not use.",
    ),
    dict(
        name="C3  strip the BOM from every CELL VALUE",
        edits=[(CSV,
                "	return strings.TrimSpace(row[idxs[0]])",
                '	return strings.TrimPrefix(strings.TrimSpace(row[idxs[0]]), "\\uFEFF")')],
        predict={"TestCSVBOM_OnlyTheFILEStartIsStripped"},
        expect_msg="Title = ",
        note="SAME TEST NAME AS C2 AND A DIFFERENT SENTENCE. This is why the verdict reads the "
             "message: a name-only verdict would score two distinct over-corrections identically "
             "and neither assertion would be justified by it.",
    ),
    dict(
        name="C4  discard three bytes unconditionally",
        edits=[(BOM,
                "if b, err := br.Peek(len(utf8BOM)); err == nil && bytes.Equal(b, utf8BOM) {",
                "if b, err := br.Peek(len(utf8BOM)); err == nil || bytes.Equal(b, utf8BOM) {")],
        predict={"TestJiraCSV_AnExportWithNoBOMImportsExactlyAsBefore",
                 "TestCSVBOM_OnlyTheFILEStartIsStripped"},
        note="the floor. 238 of the 304 measured files carry no BOM; a fix that ate their first "
             "three bytes would trade a fifth of exports for all of them. ⚠ IT IS && => ||, NOT "
             "the deletion I wrote first: deleting the condition left `bytes` unused and the "
             "package stopped COMPILING, which the harness scored ERROR. A control that breaks "
             "the build is not a control.",
    ),
    dict(
        name="C5  fix only the Jira path, not the shared seam",
        edits=[(SRC, FIXED_CALL, "rd := csv.NewReader(r)"),
               (CSV,
                "	src, err := newCSVSource(r, jiraRowMapper)",
                "	src, err := newCSVSource(skipUTF8BOM(r), jiraRowMapper)")],
        predict={"TestLinearCSV_ABOMdExportStillReadsTheRoutingKey",
                 "TestJobRow_JiraCSV_ABOMdExportIsNotReportedAsAFailedImport",
                 "TestJobRow_JiraCSV_ABOMdExportLandsItsRowsUnderTheProviderKey",
                 "TestJobRow_JiraCSV_ReimportingABOMdExportDoesNotDuplicate"},
        note="the enumeration guard — and ⚠ MY FIRST PREDICTION WAS WRONG AND THE REASON IS THE "
             "MOST TRANSFERABLE THING IN THIS FILE. I predicted this control would redden the "
             "LINEAR guard alone, because patching ImportJiraCSV fixes the Jira path. IT DOES "
             "NOT: THERE ARE TWO CALL SITES OF newCSVSource AND THE ASYNC RUNNER IS NOT "
             "ImportJiraCSV. Runner.csvSourceFor calls newCSVSource(bytes.NewReader(payload), "
             "jiraRowMapper) directly, so a fix at the sync entry point leaves the path a real "
             "bulk import uses — the T8 Build B surface that exists precisely so an import can "
             "outlive the inline 30s timeout — completely unfixed. That is #91's finding one "
             "package over (handler.run fixed, JobHandler.create not), and the three job tests "
             "are the only thing in the suite that can see it. The prediction is kept wrong and "
             "the corrected set is what is asserted.",
    ),
    dict(
        name="C6  route every row through Create",
        edits=[(SRC,
                "		if issueModel.Identifier != \"\" && imp.upserter != nil {",
                "		if false && imp.upserter != nil {")],
        predict={"TestJobRow_JiraCSV_ABOMdExportLandsItsRowsUnderTheProviderKey",
                 "TestJobRow_JiraCSV_ReimportingABOMdExportDoesNotDuplicate"},
        note="separates the ROUTING half from the operator-facing half: the job row still reads "
             "succeeded/2, so TestJobRow_..._IsNotReportedAsAFailedImport must STAY GREEN. Without "
             "this control, C1's CAUGHT would be the only evidence for three job tests at once.",
    ),
    dict(
        name="C7  INVERTED — strip on header[0] instead of on the byte stream",
        edits=[(SRC, FIXED_CALL, "rd := csv.NewReader(r)"),
               (SRC,
                "	return &csvSource{\n		rd:           rd,\n		ci:           buildIndex(header),",
                "	header[0] = strings.TrimPrefix(header[0], \"\\uFEFF\")\n"
                "	return &csvSource{\n		rd:           rd,\n		ci:           buildIndex(header),"),
               (SRC, '	"io"\n	"sort"', '	"io"\n	"sort"\n	"strings"')],
        predict=set(),
        inverted=True,
        note="a DIFFERENT implementation with the same behaviour on every measured shape. Predicted "
             "NOT CAUGHT, and kept: it says out loud that these guards pin the BEHAVIOUR and not "
             "the mechanism, so the next person may re-implement skipUTF8BOM without fear. It is "
             "NOT evidence that the guards work.",
    ),
    dict(
        name="C8  refuse a file shorter than a BOM",
        edits=[(SRC, FIXED_CALL,
                "	if _, err := bufio.NewReader(r).Peek(len(utf8BOM)); err != nil {\n"
                "		return nil, fmt.Errorf(\"importer: read header: file shorter than a BOM\")\n"
                "	}\n"
                "	" + FIXED_CALL),
               (SRC, '\t"encoding/csv"', '\t"bufio"\n\t"encoding/csv"')],
        predict={"TestCSVBOM_AFileThatIsOnlyABOMIsNotAnError"},
        note="the short-input boundary. ⚠ IT REDDENS EIGHT OF NINE AND THAT IS THE MUTATION'S "
             "FAULT, NOT THE GUARDS': bufio.NewReader(r).Peek() pulls bytes out of r into a "
             "throwaway buffer, so the stream skipUTF8BOM then reads is already gutted. It catches "
             "the predicted test and it does NOT isolate it, so it justifies "
             "TestCSVBOM_AFileThatIsOnlyABOMIsNotAnError only weakly. NO CONTROL IN THIS SET "
             "REACHES THAT GUARD ALONE — recorded as a limit rather than papered over. "
             "⚠⚠ IT TOOK THREE ATTEMPTS AND "
             "THE FIRST TWO WERE BOTH INERT, WHICH IS THE TRANSFERABLE PART. (a) Returning an empty "
             "reader for a short file produces {0 rows, no error} — exactly what the test asserts, "
             "so a real byte change moved no observable. (b) Dropping the error check and "
             "re-slicing Peek's result to b[:3] does NOT panic: bufio.Peek returns a slice INTO "
             "its own 4096-byte buffer, so the re-slice is inside capacity and yields zero bytes, "
             "which compare unequal to the BOM and take the correct branch BY ACCIDENT. Both "
             "mutations passed the anchor assertion. An anchor assertion proves application, "
             "never meaning, and NOT CAUGHT is ambiguous until you know which of the two it is.",
    ),
]


def sha(p):
    with open(p, "rb") as f:
        return hashlib.sha256(f.read()).hexdigest()


def run_tests():
    """-v, because `go test` prints no PASS lines without it and an absence is not a green."""
    r = subprocess.run(
        ["go", "test", "-count=1", "-v", "-run", "|".join(MINE), "./internal/importer/"],
        cwd=ROOT, capture_output=True, text=True)
    out = r.stdout + r.stderr
    if "build failed" in out or "[build failed]" in out:
        return None, out  # ERROR, not CAUGHT — a control that stops the package compiling is void
    failed = set(re.findall(r"^\s*--- FAIL: (\S+)", out, re.M))
    # sub-test names arrive as Parent/Sub; score the parent
    failed = {f.split("/")[0] for f in failed}
    return failed, out


def main():
    if not os.environ.get("TRACK_TEST_DATABASE_URL"):
        print("TRACK_TEST_DATABASE_URL is unset — the job-row controls would SKIP and every "
              "verdict below would be a fact about the harness. Refusing to run.")
        return 2

    print("== BASELINE: every test in the set must be GREEN before any mutation ==")
    failed, out = run_tests()
    if failed is None:
        print("the package does not build; nothing below is readable")
        print(out[-2000:])
        return 2
    if failed:
        print(f"  BASELINE RED: {sorted(failed)} — nothing below is readable")
        return 2
    print(f"  all {len(MINE)} green\n")

    originals = {p: open(p, "rb").read() for p in {SRC, BOM, CSV}}
    hashes = {p: sha(p) for p in originals}
    score = []

    for c in CONTROLS:
        print(f"== {c['name']} ==")
        # ── assert EVERY anchor before writing ANY byte ────────────────────────────
        staged = {}
        ok = True
        for path, old, new in c["edits"]:
            cur = staged.get(path, originals[path].decode("utf-8"))
            n = cur.count(old)
            if n != 1:
                print(f"  ANCHOR FAILED in {os.path.basename(path)}: {n} occurrences of {old[:60]!r}")
                ok = False
                break
            staged[path] = cur.replace(old, new)
        if not ok:
            score.append((c["name"], "VOID (anchor)", ""))
            continue

        try:
            for path, body in staged.items():
                with open(path, "w") as f:
                    f.write(body)
            failed, out = run_tests()
        finally:
            for path, body in originals.items():
                with open(path, "wb") as f:
                    f.write(body)
            for path in originals:
                assert sha(path) == hashes[path], f"RESTORE FAILED for {path}"

        if failed is None:
            print("  ERROR — the control stopped the package COMPILING. Not a catch.")
            score.append((c["name"], "ERROR (build)", ""))
            print(out[-800:])
            continue

        pred, inverted = c["predict"], c.get("inverted", False)
        extra = failed - pred
        missing = pred - failed
        greens = set(MINE) - failed

        msg_ok = True
        if c.get("expect_msg"):
            # THE MESSAGE, not the name: C2 and C3 are caught by the same test.
            body = out[out.find("--- FAIL"):] if "--- FAIL" in out else out
            msg_ok = c["expect_msg"] in out
            print(f"  assertion message contains {c['expect_msg']!r}: {msg_ok}")

        if inverted:
            verdict = "NOT CAUGHT (as predicted)" if not failed else f"CAUGHT — UNPREDICTED {sorted(failed)}"
        elif not failed:
            verdict = "NOT CAUGHT — the guard cannot see this mutation"
        elif missing:
            verdict = f"CAUGHT BY THE WRONG TEST — predicted {sorted(pred)}, missing {sorted(missing)}"
        elif not msg_ok:
            verdict = "CAUGHT BY THE PREDICTED TEST THROUGH THE WRONG ASSERTION"
        elif extra:
            verdict = f"CAUGHT as predicted, PLUS {len(extra)} unpredicted: {sorted(extra)}"
        else:
            verdict = f"CAUGHT, exactly as predicted ({len(failed)})"
        print(f"  red: {sorted(failed) if failed else 'none'}")
        print(f"  still green: {len(greens)}/{len(MINE)}")
        print(f"  => {verdict}")
        print(f"  note: {c['note']}\n")
        score.append((c["name"], verdict, c["note"]))

    print("\n== SUMMARY ==")
    for n, v, _ in score:
        print(f"  {n:52s} {v}")
    for p in originals:
        assert sha(p) == hashes[p]
    print("\nall three product files restored sha256-identical")

    print("\n== POST-RESTORE: the whole set must be green again ==")
    failed, _ = run_tests()
    print(f"  {'GREEN' if not failed else 'RED: ' + str(sorted(failed))}")
    return 0 if not failed else 1


if __name__ == "__main__":
    sys.exit(main())
