#!/usr/bin/env python3
"""w34-csv-column-reach-controls-2q7v.py — is every CSV column the Jira/Linear mappers READ
actually pinned by an assertion, or only read?

THE QUESTION. A column name is the JOIN KEY between an export file and Track's model: the
mapper asks `ci.get(row, "Summary")` and `columnIndex.get` returns "" — never an error — when
the header has no such column. That is deliberate and documented ("Returns "" if the column
doesn't exist OR THE ROW IS TOO SHORT"), and it is exactly what makes a misspelling silent: a
column that stops being read looks identical to a column the provider left blank. So the
honest question is not "is the spelling right today" but "would anything go red if it were
wrong". That is knowable by mutation and not by reading.

THE MUTATION IS AT THE CALL SITE, NEVER AT THE CONSTANT, AND THAT IS THE LOAD-BEARING CHOICE.
Nine of these columns are named by a package constant (jiraCSVDueDateColumn, ...) and the
tests build their fixture headers from THOSE SAME CONSTANTS. Mutating the constant moves the
production lookup and the fixture header together, so the row still matches, the test still
passes, and the site scores NOT CAUGHT for a reason that has nothing to do with coverage —
a false negative that would have put unpinned columns on a list they do not belong on.
Appending the suffix at the READ SITE moves only the lookup, which is the real defect's shape.

SCORING IS SET SUBTRACTION AGAINST C0's MEASURED FAIL SET, never an exit code and never a
test's name. NOT CAUGHT verdicts from the fast sweep (./internal/importer/) are RE-RUN against
the whole repository before being reported, because a guard for a mapper may live in another
package — internal/mcp and the analytics window guard both already do exactly that.

CONTROLS ON THE INSTRUMENT:
  · VOID — appending "" at a read site (a no-op concatenation). MUST be NOT CAUGHT; if it is
    scored CAUGHT the harness is reporting that an edit happened, not that a defect was seen.
  · LIVE — a site the corpus work already proved load-bearing ("Summary" on the Jira path;
    csv_bom.go's header says a BOM gluing itself to that one column imports NOTHING from 66
    of 304 real exports). MUST be CAUGHT; if it is not, the suite is not running.
  · BUILD — a mutation that fails to compile is BROKEN, never CAUGHT.
"""

import hashlib
import io
import os
import re
import subprocess
import sys

REPO = "/Users/ng/talyvor-track"
PKG = "internal/importer"
DSN = "postgres://postgres:postgres@localhost:55442/postgres?sslmode=disable"
SUF = '+"_MUT2Q7V"'

# (id, file, 1-based line, exact old substring, new substring, what the column feeds)
M = [
    # ── Linear CSV mapper ──
    ("L-title", "csv.go", 549, 'ci.get(row, "Title")', 'ci.get(row, "Title"' + SUF + ')', "Linear: issue title"),
    ("L-status", "csv.go", 553, 'ci.get(row, "Status")', 'ci.get(row, "Status"' + SUF + ')', "Linear: status"),
    ("L-priority", "csv.go", 553, 'ci.get(row, "Priority")', 'ci.get(row, "Priority"' + SUF + ')', "Linear: priority"),
    ("L-completed", "csv.go", 564, "ci.get(row, linearCSVCompletedColumn)", "ci.get(row, linearCSVCompletedColumn" + SUF + ")", "Linear: completion time"),
    ("L-duedate", "csv.go", 582, "ci.get(row, linearCSVDueDateColumn)", "ci.get(row, linearCSVDueDateColumn" + SUF + ")", "Linear: due date"),
    ("L-issueid", "csv.go", 600, "ci.get(row, linearCSVIssueIDColumn)", "ci.get(row, linearCSVIssueIDColumn" + SUF + ")", "Linear: Identifier (re-import key)"),
    ("L-description", "csv.go", 602, 'ci.get(row, "Description")', 'ci.get(row, "Description"' + SUF + ')', "Linear: description"),
    ("L-labels", "csv.go", 605, 'ci.getAll(row, "Labels")', 'ci.getAll(row, "Labels"' + SUF + ')', "Linear: labels"),
    # ── Jira CSV mapper ──
    ("J-summary", "csv.go", 766, 'ci.get(row, "Summary")', 'ci.get(row, "Summary"' + SUF + ')', "Jira: title (primary)"),
    ("J-title-fallback", "csv.go", 770, 'ci.get(row, "Title")', 'ci.get(row, "Title"' + SUF + ')', "Jira: title (fallback)"),
    ("J-status", "csv.go", 775, 'ci.get(row, "Status")', 'ci.get(row, "Status"' + SUF + ')', "Jira: status"),
    ("J-priority", "csv.go", 775, 'ci.get(row, "Priority")', 'ci.get(row, "Priority"' + SUF + ')', "Jira: priority"),
    ("J-resolution", "csv.go", 791, "ci.get(row, jiraCSVResolutionColumn)", "ci.get(row, jiraCSVResolutionColumn" + SUF + ")", "Jira: Resolution -> status"),
    ("J-duedate", "csv.go", 795, "ci.get(row, jiraCSVDueDateColumn)", "ci.get(row, jiraCSVDueDateColumn" + SUF + ")", "Jira: due date"),
    ("J-resolved", "csv.go", 796, "ci.get(row, jiraCSVResolvedColumn)", "ci.get(row, jiraCSVResolvedColumn" + SUF + ")", "Jira: resolution date"),
    ("J-issuekey", "csv.go", 826, "ci.get(row, jiraCSVIssueKeyColumn)", "ci.get(row, jiraCSVIssueKeyColumn" + SUF + ")", "Jira: Identifier (re-import key)"),
    ("J-description", "csv.go", 828, 'ci.get(row, "Description")', 'ci.get(row, "Description"' + SUF + ')', "Jira: description"),
    ("J-labels", "csv.go", 831, 'ci.getAll(row, "Labels")', 'ci.getAll(row, "Labels"' + SUF + ')', "Jira: labels"),
    # ── single-column readers in their own files ──
    ("J-updated", "jira_csv_updated.go", 90, "ci.get(row, jiraCSVUpdatedColumn)", "ci.get(row, jiraCSVUpdatedColumn" + SUF + ")", "Jira: updated_at"),
    ("J-created", "jira_csv_created.go", 88, "ci.get(row, jiraCSVCreatedColumn)", "ci.get(row, jiraCSVCreatedColumn" + SUF + ")", "Jira: created_at"),
    ("J-statuscat", "jira_csv_status_category.go", 92, "ci.get(row, jiraCSVStatusCategoryColumn)", "ci.get(row, jiraCSVStatusCategoryColumn" + SUF + ")", "Jira: Status Category"),
    ("L-created", "linear_csv_dates.go", 151, "ci.get(row, linearCSVCreatedColumn)", "ci.get(row, linearCSVCreatedColumn" + SUF + ")", "Linear: created_at"),
    ("L-updated", "linear_csv_updated.go", 108, "ci.get(row, linearCSVUpdatedColumn)", "ci.get(row, linearCSVUpdatedColumn" + SUF + ")", "Linear: updated_at"),
    ("C-desc-present", "csv_clobbered_columns.go", 73, "ci.has(clobberedDescriptionColumn)", "ci.has(clobberedDescriptionColumn" + SUF + ")", "clobber report: Description present?"),
    ("C-labels-present", "csv_clobbered_columns.go", 76, "ci.has(clobberedLabelsColumn)", "ci.has(clobberedLabelsColumn" + SUF + ")", "clobber report: Labels present?"),
    # ── instrument controls ──
    ("VOID-noop", "csv.go", 549, 'ci.get(row, "Title")', 'ci.get(row, "Title"+"")', "identity concat — must be NOT CAUGHT"),
]

LIVE_CONTROL = "J-summary"   # must be CAUGHT or the suite is not running
VOID_CONTROL = "VOID-noop"   # must be NOT CAUGHT or the harness reports edits, not defects


def sha(p):
    return hashlib.sha256(io.open(p, "rb").read()).hexdigest()


def run(scope):
    p = subprocess.run(["go", "test", "-count=1", scope], cwd=REPO,
                       capture_output=True, text=True,
                       env={**os.environ, "TRACK_TEST_DATABASE_URL": DSN})
    out = p.stdout + p.stderr
    broken = ("[build failed]" in out) or bool(re.search(r"^# github\.com/", out, re.M))
    f = set()
    for m in re.finditer(r"^FAIL\s+(\S+)", out, re.M):
        f.add("PKG " + m.group(1))
    for m in re.finditer(r"^\s*--- FAIL: (\S+)", out, re.M):
        f.add("TEST " + m.group(1))
    return f, broken


def main():
    paths = sorted({REPO + "/" + PKG + "/" + f for _, f, *_ in M})
    pristine = {p: io.open(p, encoding="utf-8").read() for p in paths}
    shas = {p: sha(p) for p in paths}
    for p in paths:
        print(f"pristine {os.path.basename(p):<28} {shas[p]}")

    print(f"\nC0 fast scope ./{PKG}/ ...")
    c0f, b = run("./" + PKG + "/")
    if b:
        raise SystemExit("C0 does not build")
    print(f"C0 fast failing set: {sorted(c0f) if c0f else 'EMPTY'}")
    print("C0 full scope ./... ...")
    c0a, b = run("./...")
    if b:
        raise SystemExit("C0 (full) does not build")
    print(f"C0 full failing set: {sorted(c0a) if c0a else 'EMPTY'}")

    results = []
    try:
        for mid, fn, line, old, new, feeds in M:
            path = REPO + "/" + PKG + "/" + fn
            L = pristine[path].split("\n")
            if old not in L[line - 1]:
                raise SystemExit(f"ANCHOR LOST {mid} at {fn}:{line}: {L[line-1]!r}")
            L[line - 1] = L[line - 1].replace(old, new, 1)
            io.open(path, "w", encoding="utf-8").write("\n".join(L))

            failed, broken = run("./" + PKG + "/")
            added, scope = failed - c0f, "importer"
            if not broken and not added:
                # NOT CAUGHT locally -> re-ask the WHOLE repository before reporting it.
                failed_all, broken = run("./...")
                added, scope = failed_all - c0a, "repo"

            io.open(path, "w", encoding="utf-8").write(pristine[path])
            if sha(path) != shas[path]:
                raise SystemExit(f"RESTORE FAILED after {mid}")

            verdict = "BROKEN(build)" if broken else ("CAUGHT" if added else "NOT CAUGHT")
            results.append((mid, feeds, verdict, scope, sorted(added)))
            by = ", ".join(a[5:] for a in sorted(added) if a.startswith("TEST "))[:110] or "-"
            print(f"  {mid:<18} {verdict:<12} [{scope}] {by}")
    finally:
        for p in paths:
            io.open(p, "w", encoding="utf-8").write(pristine[p])
        ok = all(sha(p) == shas[p] for p in paths)
        print(f"\nrestore verified by sha256: {'OK' if ok else 'MISMATCH!!'}")
        if not ok:
            sys.exit(2)

    print("\n" + "=" * 82)
    for mid, feeds, verdict, scope, _ in results:
        print(f"{mid:<18} {feeds:<40} {verdict}")
    print("=" * 82)

    bad = False
    byid = {r[0]: r for r in results}
    if byid[VOID_CONTROL][2] != "NOT CAUGHT":
        print(f"INSTRUMENT INVALID: {VOID_CONTROL} scored {byid[VOID_CONTROL][2]}, want NOT CAUGHT")
        bad = True
    if byid[LIVE_CONTROL][2] != "CAUGHT":
        print(f"INSTRUMENT INVALID: {LIVE_CONTROL} scored {byid[LIVE_CONTROL][2]}, want CAUGHT")
        bad = True
    unpinned = [r for r in results if r[0] != VOID_CONTROL and r[2] == "NOT CAUGHT"]
    if unpinned:
        print(f"\nUNPINNED COLUMN READS ({len(unpinned)} of {len(results)-1}) — the mapper can be "
              f"pointed at a column that does not exist and the WHOLE repository stays green:")
        for mid, feeds, _, _, _ in unpinned:
            print(f"  · {mid:<18} {feeds}")
        bad = True
    else:
        print("Every column read is pinned by at least one assertion.")
    sys.exit(1 if bad else 0)


if __name__ == "__main__":
    main()
