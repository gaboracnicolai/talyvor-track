#!/usr/bin/env python3
"""w34-jira-csv-updated-controls.py — the positive-control campaign for the `Updated` merge.

Every guard added by this merge is mutated here and must be OBSERVED RED. A guard that has never
been seen to fail is a guard nobody has tested.

Each control:
  1. ASSERTS ITS ANCHOR IS EXACTLY-ONCE before touching the file. A str.replace whose anchor is
     missing silently does nothing, and the harness would then report a working guard as blind —
     the failure mode that cost an earlier session three good controls.
  2. records the file's sha256 BEFORE, applies the mutation, runs ONE named test,
  3. restores from the saved bytes and RE-CHECKS THE SHA. A control that does not restore exactly
     poisons every control after it.

⚠ THE LAST CONTROL IS A MUST-STAY-GREEN, and it is not decoration. Without it, "every mutation
turned something red" is equally consistent with a harness that reds on ANY edit — including one
that changes no behaviour. C10 edits real bytes in the same file and must leave everything green.

    TRACK_TEST_DATABASE_URL=... python3 scripts/w34-jira-csv-updated-controls.py
"""

import hashlib
import os
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

CSV_GO = "internal/importer/csv.go"
UPD_GO = "internal/importer/jira_csv_updated.go"
STORE_GO = "internal/issue/store.go"
SEARCH_TEST = "TestJobRow_JiraCSV_AStaleImportDoesNotOutrankTodaysWork"
COL_TEST = "TestJobRow_JiraCSV_ImportedIssueKeepsTheDateJiraLastUpdatedIt"

# (name, file, old, new, package, test regex, expectation)
CONTROLS = [
    ("C1  mapper result dropped on the floor", CSV_GO,
     "\t\t\tUpdatedAt:   updated,\n", "",
     "./internal/importer/", COL_TEST, "red"),

    ("C2  updated_at removed from the INSERT column list", STORE_GO,
     "due_date, completed_at, lens_feature, labels, sort_order, created_at, updated_at)",
     "due_date, completed_at, lens_feature, labels, sort_order, created_at)",
     "./internal/importer/", COL_TEST, "red"),

    ("C3  the measured column spelling changed", UPD_GO,
     'const jiraCSVUpdatedColumn = "Updated"',
     'const jiraCSVUpdatedColumn = "Updated At"',
     "./internal/importer/", "TestJiraCSVUpdated_TheColumnSpellingIsPinned", "red"),

    ("C4  the spelling change is caught END TO END too", UPD_GO,
     'const jiraCSVUpdatedColumn = "Updated"',
     'const jiraCSVUpdatedColumn = "Updated At"',
     "./internal/importer/", COL_TEST, "red"),

    ("C5  mapper always answers the zero time", UPD_GO,
     "\tt, ok := parseJiraCSVTime(raw)\n\tif !ok {",
     "\tt, ok := time.Time{}, false\n\tif !ok {",
     "./internal/importer/", COL_TEST, "red"),

    ("C6  the shared parser is no longer called (the FLOOR half of rule 1)", UPD_GO,
     "\tt, ok := parseJiraCSVTime(raw)\n\tif !ok {",
     "\tt, ok := time.Time{}, false\n\tif !ok {",
     "./internal/importer/", "TestJiraCSVUpdated_Rule1_PinsNoLayoutOfItsOwn", "red"),

    ("C7  a second date layout is pinned locally (the ABSENCE half of rule 1)", UPD_GO,
     'const fieldUpdated = "last-updated time"',
     'const fieldUpdated = "last-updated time"\n\nconst sneakyLayout = "2/Jan/2006 3:04 PM"',
     "./internal/importer/", "TestJiraCSVUpdated_Rule1_PinsNoLayoutOfItsOwn", "red"),

    ("C8  the two absences collapse into one sentence", CSV_GO,
     '\tcase n.Via == viaNoUpdatedValue:\n\t\treturn fmt.Sprintf("empty %s on %d issue(s) — recorded as last updated at import time", n.Field, count)',
     '\tcase n.Via == viaNoUpdatedValue:\n\t\treturn fmt.Sprintf("no %q column in this export — %d issue(s) recorded as last updated at "+\n\t\t\t"import time, so they sort above current work and every one reads as just updated",\n\t\t\tjiraCSVUpdatedColumn, count)',
     "./internal/importer/", "TestJiraCSVUpdated_TheTwoAbsencesRenderDifferently", "red"),

    # ⚠ C9 mutates THE CONSUMER, not the importer: it is the only control that proves the second
    # job test reads the product's real ordering rather than re-asserting the column it just wrote.
    # REVERSING the sort is the mutation that actually changes the answer — see C9b for the one
    # that does not, and for why that is a fact about the fixture rather than about the guard.
    ("C9  the consumer stops ordering by recency (sort reversed)", STORE_GO,
     "          AND to_tsvector('english', title || ' ' || description)\n              @@ websearch_to_tsquery('english', $2)\n        ORDER BY updated_at DESC",
     "          AND to_tsvector('english', title || ' ' || description)\n              @@ websearch_to_tsquery('english', $2)\n        ORDER BY updated_at ASC",
     "./internal/importer/", SEARCH_TEST, "red"),

    # ⚠ C9b IS EXPECTED TO STAY GREEN AND THAT IS THE POINT — it RECORDS A LIMIT rather than
    # claiming a pass. Swapping the sort key updated_at → created_at changes real bytes and does
    # NOT change this test's answer, because a stale imported issue is older than today's work on
    # BOTH axes (measured: imported created 300d ago / updated 200d ago, native both "now"). So
    # this test proves the list is RECENCY-ordered and, with C1/C2/C4/C5, that updated_at carries
    # the provider's value — but it does NOT on its own discriminate created_at from updated_at as
    # the sort key, and no realistic fixture can, since Created ≤ Updated for every real issue.
    # Written down here so the next session reads a measured limit instead of re-deriving it, and
    # so a green C9b is never mistaken for a caught mutation.
    ("C9b DOCUMENTED-INERT: sort key swapped to created_at (must NOT change the answer)", STORE_GO,
     "          AND to_tsvector('english', title || ' ' || description)\n              @@ websearch_to_tsquery('english', $2)\n        ORDER BY updated_at DESC",
     "          AND to_tsvector('english', title || ' ' || description)\n              @@ websearch_to_tsquery('english', $2)\n        ORDER BY created_at DESC",
     "./internal/importer/", SEARCH_TEST, "green"),

    # ⚠ MUST STAY GREEN. Real bytes, in the same file every other control edits, no behaviour.
    ("C10 MUST-STAY-GREEN: a comment reworded, nothing else", UPD_GO,
     "// jira_csv_updated.go — the column that says WHEN THE ISSUE WAS LAST TOUCHED, and why its absence\n// reorders the product's main screen.",
     "// jira_csv_updated.go — the column recording when the provider last changed the issue, and why\n// leaving it unread reorders the screen the team works from.",
     "./internal/importer/", COL_TEST, "green"),
]


def sha(path):
    return hashlib.sha256(open(path, "rb").read()).hexdigest()


def run_test(pkg, regex):
    p = subprocess.run(
        ["go", "test", pkg, "-run", regex, "-count=1"],
        cwd=ROOT, capture_output=True, text=True,
    )
    return p.returncode == 0, (p.stdout + p.stderr)


def main():
    if not os.environ.get("TRACK_TEST_DATABASE_URL"):
        sys.exit("TRACK_TEST_DATABASE_URL is unset — the job-row controls would SKIP and every "
                 "verdict below would be a lie. Refusing to run.")

    print("── BASELINE: every named test must be GREEN before any mutation ──")
    for name, _f, _o, _n, pkg, regex, _exp in CONTROLS:
        ok, out = run_test(pkg, regex)
        if not ok:
            print(out[-1500:])
            sys.exit(f"BASELINE RED for {regex} — a control campaign on a red tree measures nothing")
    print(f"   {len(CONTROLS)} baselines green\n")

    caught = 0
    verdicts = []
    for name, fname, old, new, pkg, regex, expect in CONTROLS:
        path = os.path.join(ROOT, fname)
        original = open(path, "rb").read()
        before = sha(path)
        text = original.decode()

        n = text.count(old)
        if n != 1:
            verdicts.append((name, f"ANCHOR NOT EXACTLY-ONCE (count={n}) — NOT A VERDICT"))
            print(f"  {name}\n      ⚠ anchor count = {n}; refusing to mutate. This is a broken "
                  f"control, not a blind guard.")
            continue

        open(path, "w").write(text.replace(old, new))
        try:
            ok, out = run_test(pkg, regex)
        finally:
            open(path, "wb").write(original)
            if sha(path) != before:
                sys.exit(f"RESTORE FAILED for {fname} — stopping before the next control runs")

        if expect == "red":
            verdict = "CAUGHT (went red)" if not ok else "⚠ NOT CAUGHT — the guard stayed green"
            caught += 0 if ok else 1
        else:
            verdict = "STAYED GREEN (correct)" if ok else "⚠ RED ON A NO-OP EDIT — the harness reds on any change"
            caught += 1 if ok else 0
        verdicts.append((name, verdict))
        print(f"  {name}\n      {verdict}")

    print(f"\n── {caught}/{len(CONTROLS)} controls behaved as specified ──")
    bad = [v for v in verdicts if "⚠" in v[1]]
    if bad:
        print("\nNOT A CLEAN CAMPAIGN:")
        for n, v in bad:
            print(f"  {n}: {v}")
        sys.exit(1)


if __name__ == "__main__":
    main()
