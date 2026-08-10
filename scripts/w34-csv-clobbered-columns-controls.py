#!/usr/bin/env python3
"""w34-csv-clobbered-columns-controls.py — the positive-control campaign for the report that a
narrower CSV re-import empties `description` and `labels`.

WHY IT EXISTS: THREE OF THE FIVE TESTS IN csv_clobbered_columns_job_test.go PASSED ON THEIR FIRST
RUN. The premise pin (the write really does empty both columns) and the two refusals (a FIRST import
is not warned about; an export carrying both columns is not reported as carrying neither) were green
before any product line was written, because nothing warned at all. A test that cannot fail is not a
guard, and the only way to tell those apart is to break the product on purpose and watch.

HOW A VERDICT IS READ, and every clause here is a lesson this repository paid for:

  · The whole `internal/importer` package runs with `-v`, and the verdict is the SET OF FAILING TEST
    NAMES plus THE ASSERTION MESSAGE each printed. A list of names cannot tell a real catch from a
    crash, and `go test` without -v prints no PASS lines at all — so absence from a failure list is
    not evidence of green. PASS lines are counted too, and a run whose PASS count collapses is
    reported as such rather than scored.
  · The mutated file is in the SAME package as the guard, which is unavoidable here. So CAUGHT is
    never enough: each control NAMES the test it expects, and a catch by a different test is printed
    as a WRONG PREDICTION rather than quietly counted.
  · Each control states its MUST-STAY-GREEN companions. A mutation that reds everything has proved
    nothing about the one assertion it was written for.
  · The edit is asserted to have APPLIED (the anchor must be present exactly once) and to have
    CHANGED BYTES, before any test runs. A control that silently did not apply reads as a dead guard.
  · Restore happens in a `finally` and the file's sha256 is compared to the pre-run value. A crash
    between mutate and restore would otherwise leave the mutation on disk.
  · A control that stops the package COMPILING scores ERROR, not CAUGHT: a build failure proves the
    edit landed, not that the product was wrong.

    python3 scripts/w34-csv-clobbered-columns-controls.py           # all
    python3 scripts/w34-csv-clobbered-columns-controls.py C4 C9     # a subset
"""
import hashlib
import os
import re
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CSV = os.path.join(ROOT, "internal/importer/csv.go")
SRC = os.path.join(ROOT, "internal/importer/source.go")
CLB = os.path.join(ROOT, "internal/importer/csv_clobbered_columns.go")
STORE = os.path.join(ROOT, "internal/issue/store.go")

DSN = os.environ.get("TRACK_TEST_DATABASE_URL", "")

# The five tests this campaign is about.
PIN = "TestJobRow_JiraCSV_ANarrowerReimportEmptiesTheClobberedColumns"
JIRA_REPORTED = "TestJobRow_JiraCSV_ANarrowerReimportIsReported"
LINEAR_REPORTED = "TestJobRow_LinearCSV_ANarrowerReimportIsReported"
FIRST_IMPORT = "TestJobRow_JiraCSV_AFirstNarrowImportIsNotReported"
WIDE_REIMPORT = "TestJobRow_JiraCSV_AWideReimportIsNotReported"

CONTROLS = [
    dict(
        id="C1", file=SRC,
        why="the gate REMOVED — report on the header alone, which is the fix a reviewer writes first",
        old="\t\tnotes := row.Notes\n\t\tif overwroteExisting {\n\t\t\tnotes = concatNotes(row.Notes, row.NotesIfUpdated)\n\t\t}",
        new="\t\tnotes := concatNotes(row.Notes, row.NotesIfUpdated)\n\t\t_ = overwroteExisting",
        expect=[FIRST_IMPORT],
        green=[JIRA_REPORTED, LINEAR_REPORTED, PIN],
    ),
    dict(
        id="C2", file=SRC,
        why="the gate INVERTED — report on an INSERT instead of an overwrite",
        old="\t\t\toverwroteExisting = !inserted",
        new="\t\t\toverwroteExisting = inserted",
        expect=[JIRA_REPORTED, LINEAR_REPORTED, FIRST_IMPORT],
        green=[PIN, WIDE_REIMPORT],
    ),
    dict(
        id="C3", file=CSV,
        why="columnIndex.has is CONSTANT TRUE — every export looks like it carries every column",
        old="func (ci columnIndex) has(key string) bool {\n\treturn len(ci[strings.ToLower(key)]) > 0\n}",
        new="func (ci columnIndex) has(key string) bool {\n\t_ = key\n\treturn true\n}",
        expect=[JIRA_REPORTED, LINEAR_REPORTED],
        green=[PIN, FIRST_IMPORT, WIDE_REIMPORT],
    ),
    dict(
        id="C4", file=CSV,
        why="columnIndex.has is CONSTANT FALSE — every export looks like it carries nothing",
        old="func (ci columnIndex) has(key string) bool {\n\treturn len(ci[strings.ToLower(key)]) > 0\n}",
        new="func (ci columnIndex) has(key string) bool {\n\t_ = key\n\treturn false\n}",
        expect=[WIDE_REIMPORT],
        green=[PIN, JIRA_REPORTED, LINEAR_REPORTED, FIRST_IMPORT],
    ),
    dict(
        id="C5", file=CLB,
        why="the Labels LITERAL misspelled — the mapper's authority over which column it reads is the string. "
            "⚠ PREDICTION CORRECTED TO THE MEASURED SET, AND THE MISS IS THE POINT: I predicted "
            f"{WIDE_REIMPORT} (a wide export would look like it lacks the column). It stays GREEN, and "
            "VACUOUSLY — the constant is rendered INTO the sentence, so a misspelling changes the emitted "
            "line to `no \"Label\" column` and the refusal's needle (`no \"Labels\" column`) simply stops "
            "matching. A negative assertion whose needle is the literal under test cannot see that literal "
            "move. The positive tests are what earn this one.",
        old='\tclobberedLabelsColumn      = "Labels"',
        new='\tclobberedLabelsColumn      = "Label"',
        expect=[JIRA_REPORTED, LINEAR_REPORTED],
        green=[PIN, FIRST_IMPORT, WIDE_REIMPORT],
    ),
    dict(
        id="C6", file=CSV,
        why="the LINEAR half of the seam dropped — one transport fixed, the other not",
        old="\t\t// Same two columns, same spellings, same report — see the jira twin above.\n\t\tonUpdate: csvClobberedColumnNotes(ci),",
        new="\t\t// Same two columns, same spellings, same report — see the jira twin above.\n\t\tonUpdate: nil,",
        expect=[LINEAR_REPORTED],
        green=[PIN, JIRA_REPORTED, FIRST_IMPORT, WIDE_REIMPORT],
    ),
    dict(
        id="C7", file=CSV,
        why="the two render branches COLLAPSED — the Labels note renders the Description sentence",
        old='\tcase n.Via == viaNoLabelsColumn:',
        new='\tcase n.Via == viaNoLabelsColumn && false:',
        expect=[JIRA_REPORTED, LINEAR_REPORTED],
        green=[PIN, FIRST_IMPORT, WIDE_REIMPORT],
    ),
    dict(
        id="C8", file=CSV,
        why="the Description sentence stops naming the WRITE — it reports the export and not the data",
        old='\t\treturn fmt.Sprintf("no %q column in this export — %d issue(s) already in Track were "+\n\t\t\t"re-imported and had their description overwritten with an empty value; a re-import "+',
        new='\t\treturn fmt.Sprintf("no %q column in this export — %d issue(s) already in Track were "+\n\t\t\t"re-imported; a re-import "+',
        expect=[JIRA_REPORTED],
        green=[PIN, LINEAR_REPORTED, FIRST_IMPORT, WIDE_REIMPORT],
    ),
    dict(
        id="C9", file=STORE,
        why="THE PREMISE PIN'S OWN CONTROL — the conflict arm PRESERVES description instead of clobbering it. "
            "This is the fix the queue entry asks somebody to decide on, applied as a mutation: without it the "
            "pin passed on its first run and could be measuring nothing.",
        old="        description = EXCLUDED.description,   -- CLOBBER",
        new="        description = COALESCE(NULLIF(EXCLUDED.description, ''), issues.description),",
        expect=[PIN],
        green=[JIRA_REPORTED, LINEAR_REPORTED, FIRST_IMPORT, WIDE_REIMPORT],
    ),
    dict(
        id="C10", file=CLB,
        why="SHIPPED DOCUMENTED-INERT. `title` is the THIRD clobbered column and is deliberately absent from "
            "the list; adding it must change NOTHING, because errEmptyTitle refuses a titleless row before it "
            "can reach the write. A green sweep here is the evidence for a stated limit, not a failure.",
        old="\tif !ci.has(clobberedLabelsColumn) {",
        new="\tif !ci.has(\"Summary\") {\n\t\tout = append(out, FieldNote{Field: \"title\", Via: viaNoLabelsColumn})\n\t}\n\tif !ci.has(clobberedLabelsColumn) {",
        expect=[],
        green=[PIN, JIRA_REPORTED, LINEAR_REPORTED, FIRST_IMPORT, WIDE_REIMPORT],
    ),
    dict(
        id="C11", file=CSV,
        why="the note carries a PER-ROW value — the report becomes one line per ISSUE instead of one per "
            "COLUMN, which is the unbounded shape #80 built the warning bound to end. C1 showed that bound "
            "is per-KIND, so a note whose Value varies per row multiplies kinds and slips straight past it. "
            "⚠ IT HAS TO BE MUTATED AT THE CALL SITE: csvClobberedColumnNotes is handed the HEADER and never "
            "the row, so inside that function a per-row value is not merely wrong, it is unreachable — the "
            "boundedness is by construction, and this control is what turns that sentence into a run.",
        old="\t\t// The columns this export does not carry that a RE-import would empty. Reported only if\n"
            "\t\t// this row overwrote an issue that already existed — see csv_clobbered_columns.go.\n"
            "\t\tonUpdate: csvClobberedColumnNotes(ci),",
        new="\t\tonUpdate: func() []FieldNote {\n"
            "\t\t\tns := csvClobberedColumnNotes(ci)\n"
            "\t\t\tfor i := range ns {\n"
            "\t\t\t\tns[i].Value = ci.get(row, jiraCSVIssueKeyColumn)\n"
            "\t\t\t}\n"
            "\t\t\treturn ns\n"
            "\t\t}(),",
        expect=[JIRA_REPORTED],
        green=[PIN, LINEAR_REPORTED, FIRST_IMPORT, WIDE_REIMPORT],
    ),
]


def sha(p):
    return hashlib.sha256(open(p, "rb").read()).hexdigest()


def run_package():
    """Run the whole importer package with -v. Returns (failed_names, passed_names, msgs, build_ok)."""
    env = dict(os.environ)
    if DSN:
        env["TRACK_TEST_DATABASE_URL"] = DSN
    p = subprocess.run(
        ["go", "test", "./internal/importer/", "-count=1", "-v"],
        cwd=ROOT, capture_output=True, text=True, env=env)
    out = p.stdout + p.stderr
    if "build failed" in out or "[build failed]" in out or re.search(r"^# github", out, re.M):
        return set(), set(), out[:1200], False
    failed = set(re.findall(r"^--- FAIL: (\S+)", out, re.M))
    passed = set(re.findall(r"^--- PASS: (\S+)", out, re.M))
    # The assertion lines a failing test printed — a name alone cannot tell a catch from a crash.
    msgs = []
    cur = None
    for line in out.splitlines():
        m = re.match(r"^=== RUN\s+(\S+)", line)
        if m:
            cur = m.group(1)
        elif re.match(r"^\s+\S+_test\.go:\d+:", line) and cur in failed:
            msgs.append(f"      [{cur}] {line.strip()[:190]}")
    return failed, passed, "\n".join(msgs[:8]), True


def main():
    wanted = set(a.upper() for a in sys.argv[1:])
    if not DSN:
        print("TRACK_TEST_DATABASE_URL is unset — the guard is a real-Postgres job test and would SKIP/FAIL.\n"
              "A control campaign against a suite that never ran is the thing this file exists to prevent.")
        return 2

    print("== BASELINE (unmutated) ==")
    base_fail, base_pass, base_msg, ok = run_package()
    if not ok or base_fail:
        print(f"  baseline is NOT clean — failing: {sorted(base_fail)}\n{base_msg}")
        return 2
    print(f"  {len(base_pass)} test functions PASS, 0 fail\n")

    results = []
    for c in CONTROLS:
        if wanted and c["id"] not in wanted:
            continue
        path = c["file"]
        before = open(path, encoding="utf-8").read()
        before_sha = sha(path)
        n = before.count(c["old"])
        print(f"== {c['id']} — {c['why']}")
        print(f"   file: {os.path.relpath(path, ROOT)}   anchor occurrences: {n}")
        if n != 1:
            print(f"   ERROR — the anchor matches {n} times, so this control never applied. NOT A RESULT.\n")
            results.append((c["id"], "ERROR-ANCHOR", ""))
            continue
        try:
            open(path, "w", encoding="utf-8").write(before.replace(c["old"], c["new"], 1))
            if sha(path) == before_sha:
                print("   ERROR — the file's bytes did not change.\n")
                results.append((c["id"], "ERROR-NOCHANGE", ""))
                continue
            failed, passed, msgs, built = run_package()
        finally:
            open(path, "w", encoding="utf-8").write(before)
            assert sha(path) == before_sha, f"RESTORE FAILED for {path}"

        if not built:
            print(f"   ERROR — the package stopped compiling. A build error is not a caught mutation.\n{msgs}\n")
            results.append((c["id"], "ERROR-BUILD", ""))
            continue
        if len(passed) + len(failed) < len(base_pass) - 2:
            print(f"   ERROR — only {len(passed)+len(failed)} tests ran against a baseline of {len(base_pass)}; "
                  f"the binary probably died. NOT A RESULT.\n")
            results.append((c["id"], "ERROR-TRUNCATED", ""))
            continue

        exp, green = set(c["expect"]), set(c["green"])
        broke_green = green & failed
        if not exp:
            verdict = "INERT-AS-PREDICTED" if not failed else "UNEXPECTED-CATCH"
        elif failed == exp:
            verdict = "CAUGHT-AS-PREDICTED"
        elif exp <= failed:
            verdict = "CAUGHT-PLUS-EXTRA"
        elif exp & failed:
            verdict = "CAUGHT-PARTIAL"
        elif failed:
            verdict = "WRONG-PREDICTION"
        else:
            verdict = "NOT-CAUGHT"
        print(f"   predicted red : {sorted(exp) if exp else '(none — inert)'}")
        print(f"   actually red  : {sorted(failed) if failed else '(none)'}")
        print(f"   must-stay-green breached: {sorted(broke_green) if broke_green else 'no'}")
        if msgs:
            print("   assertion lines:")
            print(msgs)
        print(f"   VERDICT: {verdict}\n")
        results.append((c["id"], verdict, ",".join(sorted(failed))))

    print("== SUMMARY ==")
    for cid, v, f in results:
        print(f"  {cid:4s} {v:22s} {f}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
