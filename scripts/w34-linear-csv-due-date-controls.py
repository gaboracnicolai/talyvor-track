#!/usr/bin/env python3
"""w34-linear-csv-due-date-controls.py — the positive controls behind linear_csv_due_date.go.

EVERY GUARD IN THIS MERGE PASSED ON ITS FIRST RUN AFTER THE FIX, AND TWO OF THEM PASSED BEFORE IT
TOO. A guard that has never been observed failing is not known to be a guard. Each control below
mutates the SHIPPED SOURCE in one place, names the ONE test that must catch it, and names the tests
that must STAY GREEN — because a mutation two guards catch justifies neither of them, and a
must-stay-green that goes red means the control was broader than its description.

⚠ THE VERDICT IS READ FROM THE FAILING TEST NAMES AND THEIR MESSAGES, NOT FROM THE EXIT CODE.
A crash, a compile error and a real catch are the same non-zero exit. A control whose Go build
fails is reported BROKEN, never CAUGHT — a compile error proves the identifier moved, not that the
product was wrong.

⚠ RESTORE HAPPENS IN A `finally` AND IS VERIFIED BY SHA256. A crash between mutate and restore
would otherwise leave a mutated importer on disk, and the closing check is exactly the thing that
would not run.

Run:  TRACK_TEST_DATABASE_URL=... python3 scripts/w34-linear-csv-due-date-controls.py
"""
import hashlib
import io
import os
import subprocess
import sys

CSV = "internal/importer/csv.go"
DUE = "internal/importer/linear_csv_due_date.go"

# Every test this merge added or changed. The runner executes ALL of them for every control, so a
# must-stay-green is observed rather than assumed.
ALL_TESTS = (
    "TestLinearCSVDueDate_RealExportCellsLandOnTheIssue|"
    "TestLinearCSVDueDate_TheLeakedHeaderRowIsRefusedAndReported|"
    "TestLinearCSVDueDate_AbsentIsNotReported|"
    "TestLinearCSVDueDate_ANeighbouringDateColumnIsNotRead|"
    "TestLinearCSVDueDate_Rule1_TheMapperIsWiredIntoLinearRowMapper|"
    "TestLinearCSVMapper_ReadsTheDocumentedDatesAndInventsNoOthers|"
    "TestJobRow_LinearCSV_ImportedIssueKeepsTheDateLinearSaidItWasDue|"
    "TestJobRow_LinearCSV_ARepeatedImportDoesNotMoveTheDueDate"
)

WIRE_CALL = """	due, dueNotes := linearCSVDueDate(ci.get(row, linearCSVDueDateColumn))
"""
WIRE_FIELD = """			DueDate:     due,
"""
WIRE_NOTES = "concatNotes(createdNotes, completedNotes, updatedNotes, dueNotes)..."

CONTROLS = [
    {
        "id": "C1",
        "what": "linearCSVDueDate always returns nil — the column is read and thrown away",
        "file": DUE,
        "edits": [("	return &t, nil\n", "	_ = t\n	return nil, nil\n")],
        # The behaviour guards. The Rule1 SOURCE guard must NOT catch this — the call and the
        # assignment are both still there — and that is the point of listing it as stay-green:
        # a source-shaped guard cannot see a body that stopped meaning anything.
        "must_catch": ["TestLinearCSVDueDate_RealExportCellsLandOnTheIssue",
                       "TestLinearCSVMapper_ReadsTheDocumentedDatesAndInventsNoOthers",
                       "TestJobRow_LinearCSV_ImportedIssueKeepsTheDateLinearSaidItWasDue",
                       "TestJobRow_LinearCSV_ARepeatedImportDoesNotMoveTheDueDate"],
        "must_stay_green": ["TestLinearCSVDueDate_TheLeakedHeaderRowIsRefusedAndReported",
                            "TestLinearCSVDueDate_AbsentIsNotReported",
                            "TestLinearCSVDueDate_ANeighbouringDateColumnIsNotRead",
                            "TestLinearCSVDueDate_Rule1_TheMapperIsWiredIntoLinearRowMapper"],
    },
    {
        "id": "C2",
        "what": "a REFUSED value is silently nil'd instead of reported",
        "file": DUE,
        "edits": [("		return nil, []FieldNote{{Field: fieldDueDate, Value: raw, Via: viaUnparseableDate}}\n",
                   "		return nil, nil\n")],
        "must_catch": ["TestLinearCSVDueDate_TheLeakedHeaderRowIsRefusedAndReported"],
        "must_stay_green": ["TestLinearCSVDueDate_RealExportCellsLandOnTheIssue",
                            "TestLinearCSVDueDate_AbsentIsNotReported",
                            "TestLinearCSVDueDate_ANeighbouringDateColumnIsNotRead",
                            "TestLinearCSVDueDate_Rule1_TheMapperIsWiredIntoLinearRowMapper",
                            "TestLinearCSVMapper_ReadsTheDocumentedDatesAndInventsNoOthers",
                            "TestJobRow_LinearCSV_ImportedIssueKeepsTheDateLinearSaidItWasDue",
                            "TestJobRow_LinearCSV_ARepeatedImportDoesNotMoveTheDueDate"],
    },
    {
        "id": "C3",
        "what": "an ABSENT nullable due date is reported as degradation",
        # ⚠ THE CONTROL THIS CAMPAIGN EXISTS FOR. TestLinearCSVDueDate_AbsentIsNotReported passed
        # BEFORE the fix and after it — it never went red once, so nothing has ever shown it can.
        "file": DUE,
        "edits": [("	if strings.TrimSpace(raw) == \"\" {\n		return nil, nil\n	}\n",
                   "	if strings.TrimSpace(raw) == \"\" {\n		return nil, []FieldNote{{Field: fieldDueDate, Via: viaUnparseableDate}}\n	}\n")],
        "must_catch": ["TestLinearCSVDueDate_AbsentIsNotReported"],
        "must_stay_green": ["TestLinearCSVDueDate_RealExportCellsLandOnTheIssue",
                            "TestLinearCSVDueDate_TheLeakedHeaderRowIsRefusedAndReported",
                            "TestLinearCSVDueDate_Rule1_TheMapperIsWiredIntoLinearRowMapper",
                            "TestLinearCSVMapper_ReadsTheDocumentedDatesAndInventsNoOthers",
                            "TestJobRow_LinearCSV_ImportedIssueKeepsTheDateLinearSaidItWasDue",
                            "TestJobRow_LinearCSV_ARepeatedImportDoesNotMoveTheDueDate"],
    },
    {
        "id": "C4",
        "what": "the mapper falls back to the NEIGHBOURING `Cycle Start` column when Due Date is absent",
        # Scoped as a FALLBACK rather than a rename on purpose. Renaming the column constant would
        # blind the real-cells test too (its header has no Cycle Start), and #82's campaign already
        # recorded that shape: the positive half's t.Fatalf fires first and the catch is scored for
        # the wrong reason. A fallback leaves every other fixture untouched, so exactly one guard
        # can see it.
        "file": CSV,
        "edits": [(WIRE_CALL,
                   "	dueRaw := ci.get(row, linearCSVDueDateColumn)\n"
                   "	if strings.TrimSpace(dueRaw) == \"\" {\n"
                   "		dueRaw = ci.get(row, \"Cycle Start\")\n"
                   "	}\n"
                   "	due, dueNotes := linearCSVDueDate(dueRaw)\n")],
        "must_catch": ["TestLinearCSVDueDate_ANeighbouringDateColumnIsNotRead"],
        "must_stay_green": ["TestLinearCSVDueDate_RealExportCellsLandOnTheIssue",
                            "TestLinearCSVDueDate_TheLeakedHeaderRowIsRefusedAndReported",
                            "TestLinearCSVDueDate_AbsentIsNotReported",
                            "TestLinearCSVDueDate_Rule1_TheMapperIsWiredIntoLinearRowMapper",
                            "TestLinearCSVMapper_ReadsTheDocumentedDatesAndInventsNoOthers",
                            "TestJobRow_LinearCSV_ImportedIssueKeepsTheDateLinearSaidItWasDue",
                            "TestJobRow_LinearCSV_ARepeatedImportDoesNotMoveTheDueDate"],
    },
    {
        "id": "C5",
        "what": "the whole wiring is reverted out of linearRowMapper (the pre-merge state)",
        # Three edits in ONE file. Each replacement count is asserted, because a control that
        # applies half of itself reports a working guard as blind.
        "file": CSV,
        # ⚠ WIRE_FIELD IS NOT UNIQUE IN csv.go AND THE ANCHOR ASSERTION IS WHAT SAID SO:
        # jiraRowMapper's mappedIssue literal is BYTE-IDENTICAL in that four-line window
        # (`Labels:` / `DueDate:` / `CompletedAt:` / `CreatedAt:`). A replace(count=1) would have
        # silently mutated the JIRA mapper instead and scored a catch for the wrong transport. It
        # is anchored positionally instead: after the (unique) linear wiring call.
        "edits": [(WIRE_CALL, ""),
                  (WIRE_NOTES, "concatNotes(createdNotes, completedNotes, updatedNotes)...")],
        "edit_after": [("linearCSVUpdated(ci, row)", WIRE_FIELD, "")],
        "must_catch": ["TestLinearCSVDueDate_Rule1_TheMapperIsWiredIntoLinearRowMapper",
                       "TestLinearCSVDueDate_RealExportCellsLandOnTheIssue",
                       "TestLinearCSVMapper_ReadsTheDocumentedDatesAndInventsNoOthers",
                       "TestJobRow_LinearCSV_ImportedIssueKeepsTheDateLinearSaidItWasDue",
                       "TestJobRow_LinearCSV_ARepeatedImportDoesNotMoveTheDueDate"],
        "must_stay_green": ["TestLinearCSVDueDate_AbsentIsNotReported",
                            "TestLinearCSVDueDate_ANeighbouringDateColumnIsNotRead"],
    },
]


def sha(path):
    return hashlib.sha256(io.open(path, "rb").read()).hexdigest()


def run_tests():
    """Returns (build_ok, {test_name: 'PASS'|'FAIL'}, {test_name: message})."""
    r = subprocess.run(
        ["go", "test", "./internal/importer/", "-count=1", "-run", ALL_TESTS, "-v"],
        capture_output=True, text=True)
    out = r.stdout + r.stderr
    if "[build failed]" in out or "cannot use" in out or "undefined:" in out or "declared and not used" in out:
        return False, {}, {"build": out[-1200:]}
    status, msgs, current = {}, {}, None
    for line in out.splitlines():
        s = line.strip()
        if s.startswith("=== RUN"):
            current = s.split()[-1].split("/")[0]
        elif s.startswith("--- FAIL:"):
            status[s.split()[2]] = "FAIL"
        elif s.startswith("--- PASS:"):
            status.setdefault(s.split()[2], "PASS")
        elif current and (".go:" in s) and s.startswith(("linear_", "jira_", "csv")):
            msgs.setdefault(current, s[:240])
    return True, status, msgs


def apply_edits(path, edits, edit_after=()):
    """Every anchor is asserted BEFORE any write. A control that applies half of itself reports a
    working guard as blind, so a multi-edit control must be all-or-nothing."""
    src = io.open(path, encoding="utf-8").read()
    for old, _ in edits:
        n = src.count(old)
        if n != 1:
            raise AssertionError(f"anchor appears {n} times, want exactly 1: {old[:70]!r}")
    for marker, old, _ in edit_after:
        if src.count(marker) != 1:
            raise AssertionError(f"positional marker is not unique: {marker!r}")
        tail = src[src.index(marker):]
        if tail.count(old) < 1:
            raise AssertionError(f"anchor absent after marker: {old[:70]!r}")
    for old, new in edits:
        src = src.replace(old, new, 1)
    for marker, old, new in edit_after:
        i = src.index(marker)
        head, tail = src[:i], src[i:]
        src = head + tail.replace(old, new, 1)
    io.open(path, "w", encoding="utf-8").write(src)


def main():
    if not os.path.exists(CSV):
        print("run me from the repo root")
        return 2
    originals = {p: io.open(p, "rb").read() for p in (CSV, DUE)}
    before = {p: sha(p) for p in originals}

    print("== C0 — THE MUST-STAY-GREEN BASELINE ==")
    ok, status, _ = run_tests()
    if not ok:
        print("   BROKEN — the unmutated tree does not build. Nothing below means anything.")
        return 2
    reds = [t for t, v in status.items() if v == "FAIL"]
    print(f"   {len(status)} tests ran, {len(reds)} red   {'OK' if not reds else 'BROKEN: ' + str(reds)}")
    if reds:
        return 2

    results = []
    for c in CONTROLS:
        applied = False
        try:
            apply_edits(c["file"], c["edits"], c.get("edit_after", ()))
            applied = True
            ok, status, msgs = run_tests()
            if not ok:
                verdict, detail = "BROKEN (build)", msgs.get("build", "")[:300]
            else:
                caught = {t for t, v in status.items() if v == "FAIL"}
                missing_catch = [t for t in c["must_catch"] if t not in caught]
                broke_green = [t for t in c["must_stay_green"] if t in caught]
                never_ran = [t for t in (c["must_catch"] + c["must_stay_green"]) if t not in status]
                if never_ran:
                    verdict, detail = "BROKEN (a named test never ran)", str(never_ran)
                elif missing_catch or broke_green:
                    verdict = "MISPREDICTED"
                    detail = f"expected-catch that stayed green={missing_catch} · stay-green that went red={broke_green}"
                else:
                    verdict = "CAUGHT"
                    detail = " | ".join(msgs.get(t, "(no message captured)") for t in c["must_catch"][:2])
        except AssertionError as e:
            verdict, detail = "BROKEN (control did not apply)", str(e)
        finally:
            for p, b in originals.items():
                io.open(p, "wb").write(b)
        after = sha(c["file"])
        restored = "restored" if after == before[c["file"]] else "⚠ NOT RESTORED"
        results.append((c["id"], verdict, c["what"], detail, restored))
        print(f"\n== {c['id']} — {c['what']} ==")
        print(f"   verdict: {verdict}   ({restored})")
        print(f"   {detail}")

    for p in originals:
        if sha(p) != before[p]:
            print(f"\n⚠⚠ {p} DID NOT RESTORE — fix the tree before committing.")
            return 2
    print("\n== SUMMARY ==   (both files restored byte-identical, sha256 verified)")
    for i, v, w, _, _ in results:
        print(f"  {i}  {v:<14} {w}")
    return 0 if all(v == "CAUGHT" for _, v, _, _, _ in results) else 1


if __name__ == "__main__":
    sys.exit(main())
