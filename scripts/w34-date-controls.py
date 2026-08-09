#!/usr/bin/env python3
"""Positive controls for W3.4's date-fields merge.

Each control MUTATES production code, asserts a NAMED guard goes red, asserts a NAMED guard
stays GREEN (so no control passes by breaking everything), and restores the file sha256-identical.

The anchor count is asserted BEFORE the write. #71's lesson: a substitution that matches nothing
edits zero bytes and is byte-indistinguishable from a guard that works.

Not committed as a CI job — it mutates the tree. Run by hand; output pasted into the PR.
"""
import hashlib
import pathlib
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
JIRA = ROOT / "internal/importer/jira.go"
STORE = ROOT / "internal/issue/store.go"

DB = "postgres://postgres:postgres@localhost:55434/postgres?sslmode=disable"

WIRE = "TestJiraRequest_AsksForTheDateFields"
LAND = "TestJiraSource_RecordsDueDateAndCompletedAt"
FORMATS = "TestJiraDateParse_TheMeasuredWireFormats"
NOTDONE = "TestJiraSource_ResolutionDateNotRecordedUnlessDone"
UNPARSE = "TestJiraSource_UnparseableDateIsReportedNotDropped"
JOB = "TestJobRow_JiraAPI_DatesLandInPostgres"
REIMPORT = "TestJobRow_JiraAPI_ReimportDoesNotMoveTheDateColumns"
ABSENT = "TestJiraSource_AbsentDatesStayNil"

# (name, file, old, new, must-go-red, must-stay-green)
CONTROLS = [
    ("C1 drop duedate from the wire", JIRA,
     '"labels", "duedate", "resolutiondate"}',
     '"labels", "resolutiondate"}',
     WIRE, NOTDONE),

    ("C2 drop resolutiondate from the wire", JIRA,
     '"labels", "duedate", "resolutiondate"}',
     '"labels", "duedate"}',
     WIRE, UNPARSE),

    ("C3 THE TRAP: use time.RFC3339 alone", JIRA,
     '''var jiraTimeLayouts = []string{
	"2006-01-02T15:04:05.000-0700", // measured: resolutiondate
	time.RFC3339,                   // tolerated: the same instant with a colon in the offset, or Z
	"2006-01-02",                   // measured: duedate
}''',
     'var jiraTimeLayouts = []string{time.RFC3339}',
     FORMATS, ABSENT),

    ("C4 drop the date-only layout", JIRA,
     '\t"2006-01-02",                   // measured: duedate\n',
     '',
     FORMATS, ABSENT),

    ("C5 record a completion time regardless of status", JIRA,
     '\tif status != model.StatusDone {\n\t\treturn nil, []FieldNote{{Field: fieldResolutionDate, Value: raw, Mapped: string(status), Via: viaStatusNotDone}}\n\t}\n',
     '',
     NOTDONE, LAND),

    ("C6 refuse the completion time but say nothing", JIRA,
     'return nil, []FieldNote{{Field: fieldResolutionDate, Value: raw, Mapped: string(status), Via: viaStatusNotDone}}',
     'return nil, nil',
     NOTDONE, LAND),

    ("C7 drop an unparseable date silently", JIRA,
     '\t\treturn nil, []FieldNote{{Field: fieldDueDate, Value: raw, Via: viaUnparseableDate}}',
     '\t\treturn nil, nil',
     UNPARSE, LAND),

    # ⚠ C8/C8b BLIND EACH READ BEHAVIOURALLY, NOT BY DELETION. Deleting the two struct fields
    # produces "declared and not used" — a BUILD failure reds every test in the package, including
    # the one that must stay green, and proves nothing about any guard. A control that cannot
    # distinguish "the guard caught it" from "nothing compiled" is not a control.
    ("C8 blind the due-date read (it never runs)", JIRA,
     'func jiraDueDate(raw string) (*time.Time, []FieldNote) {\n\tif strings.TrimSpace(raw) == "" {',
     'func jiraDueDate(raw string) (*time.Time, []FieldNote) {\n\tif true {',
     LAND, NOTDONE),

    ("C8b blind the resolution-date read (it never runs)", JIRA,
     'func jiraCompletedAt(raw string, status model.IssueStatus) (*time.Time, []FieldNote) {\n\tif strings.TrimSpace(raw) == "" {',
     'func jiraCompletedAt(raw string, status model.IssueStatus) (*time.Time, []FieldNote) {\n\tif true {',
     LAND, ABSENT),

    # ⚠ THE ONE THAT PROVES THE JOB TEST IS NOT THE UNIT TEST TWICE: revert ONLY the SQL half.
    # The mapper still produces a perfect CompletedAt; the column never receives it.
    ("C9 revert the store half (completed_at out of the INSERT)", STORE,
     '         due_date, completed_at, lens_feature, labels, sort_order)\n    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)',
     '         due_date, lens_feature, labels, sort_order)\n    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)',
     JOB, LAND),

    # ⚠ C10/C11 CONTROL A TEST THAT PASSED ON ITS FIRST RUN. TestJobRow_..._ReimportDoesNotMove...
    # pins behaviour this merge deliberately did NOT change, so it was green from the start and had
    # to be shown capable of failing. Each adds one column to the upsert's CLOBBER set; the JOB test
    # must stay green, because a fresh INSERT never reaches DO UPDATE and the control is specific.
    ("C10 clobber due_date on re-import", STORE,
     '        title       = EXCLUDED.title,        -- CLOBBER: provider is source of truth',
     '        due_date    = EXCLUDED.due_date,\n        title       = EXCLUDED.title,        -- CLOBBER: provider is source of truth',
     REIMPORT, JOB),

    ("C11 clobber completed_at on re-import", STORE,
     '        title       = EXCLUDED.title,        -- CLOBBER: provider is source of truth',
     '        completed_at = EXCLUDED.completed_at,\n        title       = EXCLUDED.title,        -- CLOBBER: provider is source of truth',
     REIMPORT, JOB),
]


def sha(p):
    return hashlib.sha256(p.read_bytes()).hexdigest()


def run(pattern, pkg):
    r = subprocess.run(
        ["go", "test", "-timeout", "300s", "-count=1", "-run", pattern, pkg],
        cwd=ROOT, capture_output=True, text=True,
        env={**__import__("os").environ, "TRACK_TEST_DATABASE_URL": DB},
    )
    return r.returncode == 0, (r.stdout + r.stderr)


def main():
    # C9 patches the store but the guard lives in the importer package; both are run there.
    pkg = "./internal/importer/"
    failures = []

    for name, path, old, new, red_test, green_test in CONTROLS:
        original = path.read_bytes()
        before = sha(path)
        text = original.decode()

        n = text.count(old)
        if n != 1:
            print(f"  {name}: ANCHOR COUNT {n}, want 1 — control NOT applied, would have been a no-op")
            failures.append(f"{name}: anchor {n}")
            continue

        path.write_bytes(text.replace(old, new, 1).encode())
        try:
            if sha(path) == before:
                print(f"  {name}: FILE UNCHANGED after write — control is a no-op")
                failures.append(f"{name}: no-op")
                continue
            red_ok, red_out = run(f"^{red_test}$", pkg)
            green_ok, green_out = run(f"^{green_test}$", pkg)
        finally:
            path.write_bytes(original)
            assert sha(path) == before, f"{name}: RESTORE FAILED for {path}"

        verdict = "PASS" if (not red_ok and green_ok) else "FAIL"
        if verdict == "FAIL":
            failures.append(name)
        print(f"  [{verdict}] {name}")
        print(f"          {red_test} -> {'RED (caught)' if not red_ok else 'GREEN (BLIND!)'}")
        print(f"          {green_test} -> {'green' if green_ok else 'RED (control broke everything)'}")
        if verdict == "FAIL":
            print("          --- red output ---")
            print("\n".join(red_out.splitlines()[:12]))

    print()
    if failures:
        print(f"CONTROLS FAILED: {failures}")
        return 1
    print(f"ALL {len(CONTROLS)} CONTROLS PASSED — every guard caught its mutation, none by breaking the suite")
    return 0


if __name__ == "__main__":
    sys.exit(main())
