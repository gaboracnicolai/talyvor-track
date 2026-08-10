#!/usr/bin/env python3
"""w34-jira-csv-issue-key-controls.py — positive controls for "a Jira CSV export names every
issue and the mapper never asked".

    TRACK_TEST_DATABASE_URL=postgres://... python3 scripts/w34-jira-csv-issue-key-controls.py

WHAT A CONTROL IS HERE. Each entry names ONE mutation, the test that MUST red because of it, and a
set of tests that MUST STAY GREEN under it. Both halves are checked. A control whose predicted
catcher stays green is a guard that cannot see the thing it exists for; a control that reddens the
must-stay-green set is a mutation that broke something other than the behaviour under test, and its
catch says nothing about the guard.

THE HARNESS RULES, EACH LEARNED THE HARD WAY IN THIS REPO:

  · ANCHORS ARE ASSERTED UNIQUE BEFORE ANY WRITE, and every edit is verified to have CHANGED THE
    BYTES ON DISK. A control that silently matched nothing scores NOT CAUGHT and reads as a dead
    guard (W3.4/#71's first two controls did exactly that).
  · FILES ARE RESTORED FROM SAVED BYTES and sha256-compared afterwards, per file, on every exit
    path.
  · `go test` IS RUN WITH -v. Without it there are no `--- PASS:` lines at all, so "absent from the
    failure list" means nothing — the #97 harness reported all six controls UNREADABLE on exactly
    this. A test that never ran is its own verdict (NEVER_RAN), never "green".
  · A BUILD FAILURE IS SCORED EXPLICITLY AND IS NOT A CATCH. A compile error proves the code moved,
    not that the product was wrong.
  · ONE CONTROL IS INVERTED (C7): a different spelling of the same predicate, which must leave
    EVERYTHING green. Without it a harness that reddens under any edit would score 100%.

⚠ TWO THINGS THIS CAMPAIGN DOES NOT CLAIM, STATED RATHER THAN LEFT TO BE ASSUMED:

  · C5's FIRST PREDICTION WAS WRONG and the corrected one is recorded in its own entry below. The
    correction is the transferable part: T_ABSENT and T_NEIGH assert one property on two keyless
    fixtures, one a strict subset of the other, so C5 justifies NEITHER individually.
  · C2 CLASSIFIES SEVEN OF THE NINE TESTS. T_DUP and T_REFUSE were left unpredicted for it, so this
    run says nothing about them under that mutation. That is a gap in the campaign, not a green.
"""

import hashlib
import os
import re
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CSV_GO = os.path.join(ROOT, "internal/importer/csv.go")
KEY_GO = os.path.join(ROOT, "internal/importer/jira_csv_issue_key.go")
SOURCE_GO = os.path.join(ROOT, "internal/importer/source.go")

# The shipped line the fix added, and the routing predicate it feeds.
FIX_LINE = "\t\t\tIdentifier:  ci.get(row, jiraCSVIssueKeyColumn),\n"
ROUTE_PREDICATE = 'if issueModel.Identifier != "" && imp.upserter != nil {'
KEY_CONST = 'const jiraCSVIssueKeyColumn = "Issue key"'

# Test names, spelled once.
T_SPELL = "TestJiraCSVIssueKey_TheMeasuredSpelling"
T_REACH = "TestJiraCSVIssueKey_ReachesTheModel"
T_ABSENT = "TestJiraCSVIssueKey_AbsentColumnStillImports"
T_NEIGH = "TestJiraCSVIssueKey_ANeighbouringKeyColumnIsNotRead"
T_KEEPS = "TestJobRow_JiraCSV_TheIssueKeepsTheKeyJiraGaveIt"
T_DUP = "TestJobRow_JiraCSV_ReimportingTheSameExportDoesNotDuplicate"
T_UPD = "TestJobRow_JiraCSV_AReimportUpdatesTheRowItAlreadyWrote"
T_KEYLESS = "TestJobRow_JiraCSV_AKeylessExportStillImports"
T_REFUSE = "TestJobRow_JiraCSV_ARowAHumanOwnsIsRefusedNotOverwritten"

ALL_TESTS = [T_SPELL, T_REACH, T_ABSENT, T_NEIGH, T_KEEPS, T_DUP, T_UPD, T_KEYLESS, T_REFUSE]
RUN_RE = "|".join(ALL_TESTS)


def sha(path):
    with open(path, "rb") as f:
        return hashlib.sha256(f.read()).hexdigest()


def read(path):
    with open(path, "rb") as f:
        return f.read()


def write(path, data):
    with open(path, "wb") as f:
        f.write(data)


def edit(path, old, new, label):
    """Assert the anchor is UNIQUE, apply it, and assert the bytes moved. Returns nothing; raises
    on any failure, because a control that did not apply must never be scored."""
    body = read(path).decode()
    n = body.count(old)
    if n != 1:
        raise RuntimeError(f"{label}: anchor occurs {n} times in {os.path.basename(path)}, want exactly 1:\n  {old!r}")
    before = sha(path)
    write(path, body.replace(old, new, 1).encode())
    if sha(path) == before:
        raise RuntimeError(f"{label}: the write did not change {os.path.basename(path)} on disk")


def run_tests():
    """Run the package with -v. Returns (built, per-test verdict dict, per-test message)."""
    p = subprocess.run(
        ["go", "test", "-count=1", "-v", "-run", f"^({RUN_RE})$", "./internal/importer/"],
        cwd=ROOT, capture_output=True, text=True,
    )
    out = p.stdout + p.stderr
    # A build failure is its own outcome and is NOT a catch.
    if "build failed" in out or "cannot use" in out or "undefined:" in out or "syntax error" in out:
        return False, {}, out
    verdict, msg = {}, {}
    for name in ALL_TESTS:
        if re.search(rf"^--- FAIL: {name} ", out, re.M):
            verdict[name] = "FAIL"
        elif re.search(rf"^--- PASS: {name} ", out, re.M):
            verdict[name] = "PASS"
        else:
            verdict[name] = "NEVER_RAN"
    # The first assertion line printed under each failing test — the verdict is read from the
    # MESSAGE, never from the test's presence in a list.
    for name in ALL_TESTS:
        m = re.search(rf"=== RUN   {name}\n((?:    .*\n)+?)--- (?:FAIL|PASS): {name} ", out)
        if m:
            first = m.group(1).strip().splitlines()[0].strip()
            msg[name] = first[:150]
    return True, verdict, msg


CONTROLS = [
    # (label, description, [(path, old, new)...], must_red, must_stay_green)
    (
        "C1",
        "revert the fix — the Identifier line deleted from jiraRowMapper (main's shape)",
        [(CSV_GO, FIX_LINE, "")],
        [T_REACH, T_KEEPS, T_DUP, T_UPD, T_REFUSE],
        [T_SPELL, T_ABSENT, T_NEIGH, T_KEYLESS],
    ),
    (
        "C2",
        "the key is read but TRUNCATED to its project prefix — a plausible 'normalise it' edit that "
        "keeps a non-empty identifier, so the routing still changes but the NAME is wrong",
        [(CSV_GO, "Identifier:  ci.get(row, jiraCSVIssueKeyColumn),",
          'Identifier:  strings.SplitN(ci.get(row, jiraCSVIssueKeyColumn), "-", 2)[0],')],
        [T_REACH, T_KEEPS, T_UPD],
        [T_SPELL, T_ABSENT, T_KEYLESS],
    ),
    (
        "C3",
        "THE CONTROL THE JOB TESTS EXIST FOR — the mapper still reads the key, but run()'s routing "
        "predicate is disarmed, so every CSV row takes Create anyway. Every SOURCE-level assertion "
        "passes while the product duplicates the backlog.",
        [(SOURCE_GO, ROUTE_PREDICATE, 'if false && issueModel.Identifier != "" && imp.upserter != nil {')],
        [T_KEEPS, T_DUP, T_UPD, T_REFUSE],
        [T_SPELL, T_REACH, T_ABSENT, T_NEIGH, T_KEYLESS],
    ),
    (
        "C4",
        "a 'be generous' fallback to the NEIGHBOURING column — Issue id (the numeric surrogate) "
        "when Issue key is absent",
        [(CSV_GO, "Identifier:  ci.get(row, jiraCSVIssueKeyColumn),",
          'Identifier:  firstNonEmptyKey(ci.get(row, jiraCSVIssueKeyColumn), ci.get(row, "Issue id")),')],
        [T_NEIGH],
        [T_SPELL, T_REACH, T_ABSENT, T_KEEPS, T_DUP, T_UPD, T_KEYLESS],
    ),
    (
        "C5",
        "a key INVENTED when the column is absent — the row would land on a fabricated provider "
        "identifier instead of a Track-derived one.\n"
        "      ⚠ MY FIRST PREDICTION HERE WAS WRONG AND IS KEPT WRONG IN THIS FILE. I predicted\n"
        "      T_ABSENT ALONE and listed T_NEIGH as must-stay-green; the run scored CAUGHT BUT\n"
        "      UNCLEAN because T_NEIGH's fixture ALSO carries no `Issue key`, so an invented key\n"
        "      reddens it too. The guard was fine; the claim about which assertion speaks was not.\n"
        "      ⚠ AND THE CORRECTED PREDICTION IS THE FINDING: T_ABSENT AND T_NEIGH ASSERT THE SAME\n"
        "      POSTCONDITION (Identifier == \"\") ON TWO KEYLESS FIXTURES, AND T_ABSENT'S IS A STRICT\n"
        "      SUBSET OF T_NEIGH'S. No mutation can red T_ABSENT without redding T_NEIGH, so C5\n"
        "      JUSTIFIES NEITHER CATCHER INDIVIDUALLY. C4 — which reds T_NEIGH ALONE, because only\n"
        "      it plants a tempting neighbour to be read — is what justifies the pair. T_ABSENT is\n"
        "      kept as the plain statement of the property, not as independently-earned coverage.",
        [(CSV_GO, "Identifier:  ci.get(row, jiraCSVIssueKeyColumn),",
          'Identifier:  firstNonEmptyKey(ci.get(row, jiraCSVIssueKeyColumn), "IMPORTED-"+title),')],
        [T_ABSENT, T_NEIGH],
        [T_SPELL, T_REACH, T_KEEPS, T_DUP, T_UPD, T_KEYLESS],
    ),
    (
        "C6",
        "THE DELETION CONTROL FOR THE PINNED LITERAL — the constant is re-spelled and the mapper "
        "compensates with an inline literal, so BEHAVIOUR IS UNCHANGED and only the pinned spelling "
        "moved. A source-derived assertion cannot see this at all.",
        [(KEY_GO, KEY_CONST, 'const jiraCSVIssueKeyColumn = "Issue Key (renamed)"'),
         (CSV_GO, "Identifier:  ci.get(row, jiraCSVIssueKeyColumn),",
          'Identifier:  ci.get(row, "Issue key"),')],
        [T_SPELL],
        [T_REACH, T_ABSENT, T_NEIGH, T_KEEPS, T_DUP, T_UPD, T_KEYLESS, T_REFUSE],
    ),
    (
        "C7",
        "INVERTED — the same predicate spelled differently (the lookup hoisted into a local). "
        "EVERYTHING must stay green; without this a harness that reds under any edit scores 100%.",
        [(CSV_GO, "Identifier:  ci.get(row, jiraCSVIssueKeyColumn),",
          "Identifier:  jiraCSVKeyOf(ci, row),")],
        [],
        ALL_TESTS,
    ),
    (
        "C8",
        "the routing predicate widened — a KEYLESS row sent to the upsert too, landing it on an "
        "empty provider identifier. The source-level absent-column test cannot see this.",
        [(SOURCE_GO, ROUTE_PREDICATE, "if imp.upserter != nil {")],
        [T_KEYLESS],
        [T_SPELL, T_REACH, T_ABSENT, T_NEIGH, T_KEEPS, T_DUP, T_UPD],
    ),
]

# C4/C5/C7 need a helper that does not exist on the shipped tree. It is appended to csv.go as part
# of the mutation and removed with it — a control may add scaffolding, provided the scaffolding is
# inert on the shipped tree (it is: nothing calls these unless the mutation does).
HELPERS = """

func firstNonEmptyKey(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func jiraCSVKeyOf(ci columnIndex, row []string) string {
	key := ci.get(row, jiraCSVIssueKeyColumn)
	return key
}
"""


def main():
    if not os.environ.get("TRACK_TEST_DATABASE_URL"):
        print("TRACK_TEST_DATABASE_URL is unset — the job-level controls would SKIP and every")
        print("verdict below would be a zero from an instrument that read nothing. Stopping.")
        return 2

    saved = {p: read(p) for p in (CSV_GO, KEY_GO, SOURCE_GO)}
    shas = {p: sha(p) for p in saved}

    print("BASELINE — the shipped tree must be fully green, or no verdict below is readable.")
    built, verdict, _ = run_tests()
    if not built or any(v != "PASS" for v in verdict.values()):
        print(f"  baseline is not green (built={built}): {verdict}")
        for p, b in saved.items():
            write(p, b)
        return 1
    print(f"  {len(ALL_TESTS)}/{len(ALL_TESTS)} green\n")

    results = []
    try:
        for label, desc, edits, must_red, must_green in CONTROLS:
            print(f"{label}  {desc}")
            try:
                # Helpers first, so an edit that references them compiles.
                if label in ("C4", "C5", "C7"):
                    write(CSV_GO, read(CSV_GO) + HELPERS.encode())
                if label == "C2":
                    body = read(CSV_GO).decode()
                    if '\t"strings"' not in body:
                        raise RuntimeError("C2: csv.go does not import strings")
                for path, old, new in edits:
                    edit(path, old, new, label)
                built, verdict, msg = run_tests()
            except RuntimeError as e:
                print(f"  HARNESS ERROR: {e}\n")
                results.append((label, "HARNESS_ERROR", str(e)))
                for p, b in saved.items():
                    write(p, b)
                continue

            for p, b in saved.items():
                write(p, b)
            for p in saved:
                if sha(p) != shas[p]:
                    raise RuntimeError(f"{label}: {p} not restored byte-identically")

            if not built:
                print("  BUILD FAILED — scored as NOT A CATCH (a compile error proves the code moved)\n")
                results.append((label, "BUILD_FAILED", ""))
                continue

            reds = [t for t in must_red if verdict[t] == "FAIL"]
            missed = [t for t in must_red if verdict[t] != "FAIL"]
            broke = [t for t in must_green if verdict[t] != "PASS"]
            never = [t for t, v in verdict.items() if v == "NEVER_RAN"]

            for t in must_red:
                print(f"    must-red   {t:58s} {verdict[t]:9s} {msg.get(t, '')}")
            for t in must_green:
                print(f"    must-green {t:58s} {verdict[t]:9s} {msg.get(t, '')}")

            if never:
                out = "UNREADABLE (never ran: " + ", ".join(never) + ")"
            elif missed:
                out = "NOT CAUGHT — predicted catcher stayed green: " + ", ".join(missed)
            elif broke:
                out = "CAUGHT BUT UNCLEAN — a must-stay-green test reddened: " + ", ".join(broke)
            elif not must_red and not broke:
                out = "CAUGHT AS PREDICTED (inverted: everything stayed green)"
            else:
                out = "CAUGHT AS PREDICTED by " + ", ".join(reds)
            print(f"  => {out}\n")
            results.append((label, out, ""))
    finally:
        for p, b in saved.items():
            write(p, b)
        bad = [p for p in saved if sha(p) != shas[p]]
        if bad:
            print("⚠ FILES NOT RESTORED: " + ", ".join(bad))
            return 1

    clean = sum(1 for _, o, _ in results if o.startswith("CAUGHT AS PREDICTED"))
    print(f"{clean}/{len(CONTROLS)} controls CAUGHT AS PREDICTED, tree restored byte-identically")
    for label, out, _ in results:
        print(f"  {label}  {out}")
    return 0 if clean == len(CONTROLS) else 1


if __name__ == "__main__":
    sys.exit(main())
