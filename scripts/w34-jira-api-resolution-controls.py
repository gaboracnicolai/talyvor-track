#!/usr/bin/env python3
"""Positive controls for `resolution` on the JIRA API TRANSPORT (W3.4, the merge after #86).

WHAT THIS PROVES AND WHAT IT DOES NOT.  Eight test functions were RED before the fix.  That is
necessary and not sufficient: it shows the guards can fail on the ORIGINAL defect, not that they
still fail on each INDIVIDUAL half of it.  Each control below removes exactly ONE half and names
the test that must speak.

⚠ EVERY CONTROL CARRIES A MUST-STAY-GREEN COMPANION.  Without one, "the target went red" is
equally consistent with a mutation that broke the build or reddened everything.  BOTH RED IS
`SUSPECT`, NEVER `CAUGHT`.

⚠ THE RUNNER AND ITS VERDICT LOGIC ARE #86's, CARRIED OVER UNCHANGED because #86 paid for three
defects in them by RUNNING it: an ambiguous anchor that silently matched twice (hence `scope`), a
mutation that did not compile being scored as CAUGHT (hence the BUILD state), and a `-run` pattern
matching nothing exiting 0 (hence the NOMATCH state).  The CONTROLS are this merge's own.

⚠ THE BASELINE GATE IS LOAD-BEARING.  Without TRACK_TEST_DATABASE_URL every job control would SKIP,
`go test` would exit 0, and this script would report a clean sweep of controls that never ran.

    TRACK_TEST_DATABASE_URL=... python3 scripts/w34-jira-api-resolution-controls.py
"""
import hashlib
import os
import pathlib
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent

# (id, file, anchor, replacement, must_red, must_stay_green, package, note, scope)
CONTROLS = [
    # ⚠ C1 AND C1b ARE ONE MUTATION SCORED TWICE, AND C1b IS THE MORE IMPORTANT HALF.
    # C1 asserts the WIRE test speaks when the shipped request stops asking for the field.
    # C1b asserts, on the SAME mutation, that the end-to-end job test CANNOT — the canned httptest
    # server answers ANY request with the same fixture bytes, so no behavioural test in this package
    # can see a `fields` list that stopped asking.  #86 measured that blind spot for two other
    # fields; this re-measures it for this one rather than citing it.  A GREEN C1b IS THE RECORDED
    # LIMIT OF THE FIXTURE, NEVER A CAUGHT MUTATION.
    ("C1", "internal/importer/jira.go",
     "jiraAPIUpdatedField, jiraAPIResolutionField}", "jiraAPIUpdatedField}",
     ["TestJiraRequest_AsksForTheResolutionField"],
     ["TestJiraAPIResolution_AbandonedWorkImportsAsCancelledWithNoCompletionTime"],
     "./internal/importer/",
     "the shipped Jira request stops asking for `resolution` (HTTP 200, key silently absent)", None),

    ("C1b", "internal/importer/jira.go",
     "jiraAPIUpdatedField, jiraAPIResolutionField}", "jiraAPIUpdatedField}",
     [],
     ["TestJobRow_JiraAPI_AbandonedWorkLandsCancelledAndUncountedInPostgres"],
     "./internal/importer/",
     "DOCUMENTED-INERT: the canned server ignores `fields`, so the job test cannot see this", None),

    # ⚠ C2 IS WHY THE LITERAL IN THE WIRE TEST IS HARDCODED (#75's C6).  A test written against
    # jiraAPIResolutionField would compare the constant to itself and stay GREEN on this mutation,
    # which puts a misspelt field name on the wire — answered HTTP 200 with the key simply absent.
    ("C2", "internal/importer/api_resolution.go",
     'jiraAPIResolutionField = "resolution"', 'jiraAPIResolutionField = "resolutions"',
     ["TestJiraRequest_AsksForTheResolutionField"],
     ["TestJiraAPIResolution_AbandonedWorkImportsAsCancelledWithNoCompletionTime"],
     "./internal/importer/",
     "the constant is misspelt — a self-comparing wire test would stay green here", None),

    # ⚠ C3 IS THE CONTROL THAT EARNS THE ORDERING COMMENT IN mapJiraIssues, and it is the one that
    # matters most on THIS transport: UpsertByIdentifier passes CompletedAt through UNGATED (unlike
    # Create, which nils it itself), so the mapper's ordering is the only thing standing between an
    # abandoned issue and a completion time that analytics counts as delivered work.  Running the
    # resolution rule AFTER jiraCompletedAt still produces a `cancelled` row — the status assertions
    # all stay green — and that row still carries its completion time.
    ("C3", "internal/importer/jira.go",
     "\t\tstatus, resolutionNotes := jiraAPIResolution(it.Fields.Resolution, status)\n"
     "\t\tdue, dueNotes := jiraDueDate(it.Fields.DueDate)\n"
     "\t\tcompleted, completedNotes := jiraCompletedAt(it.Fields.ResolutionDate, status)",
     "\t\tdue, dueNotes := jiraDueDate(it.Fields.DueDate)\n"
     "\t\tcompleted, completedNotes := jiraCompletedAt(it.Fields.ResolutionDate, status)\n"
     "\t\tstatus, resolutionNotes := jiraAPIResolution(it.Fields.Resolution, status)",
     ["TestJobRow_JiraAPI_AbandonedWorkLandsCancelledAndUncountedInPostgres",
      "TestJiraAPIResolution_AbandonedWorkImportsAsCancelledWithNoCompletionTime"],
     ["TestJobRow_JiraCSV_AbandonedWorkLandsCancelledAndUndatedInPostgres"],
     "./internal/importer/",
     "THE ORDERING: the rule runs AFTER the completion-time gate, so the cancelled row keeps its date",
     None),

    # ⚠ C4 IS THE CONTROL THAT EARNS THE CATEGORY POSITION.  Running the rule on the status the NAME
    # mapping produced leaves every row that reaches `done` only through statusCategory unexamined —
    # 138 of 3,000 sampled resolved issues on the measured instance.  The name-route tests stay
    # green, which is exactly the failure mode a single abandoned-work test would have missed.
    #
    # ⚠ MY FIRST DRAFT OF C4 SCORED `NOT CAUGHT` AND IT WAS THE MUTATION THAT WAS WRONG, NOT THE
    # GUARD.  It INSERTED a second jiraAPIResolution call before the fallback and left the real one
    # in place, so the rule ran twice: the second call fixed the status the first got wrong, and the
    # only visible effect was a lost warning note — which reddened the COMPANION.  A control that
    # adds a call is not a control that moves one.
    ("C4", "internal/importer/jira.go",
     "\t\tstatus, resolutionNotes := jiraAPIResolution(it.Fields.Resolution, status)",
     "\t\tnameOnly, _ := mapJiraStatus(it.Fields.Status.Name)\n"
     "\t\tmutated, resolutionNotes := jiraAPIResolution(it.Fields.Resolution, nameOnly)\n"
     "\t\tif mutated != nameOnly {\n\t\t\tstatus = mutated\n\t\t}",
     ["TestJiraAPIResolution_TheCategoryRouteOntoDoneIsCoveredToo"],
     ["TestJiraAPIResolution_AbandonedWorkImportsAsCancelledWithNoCompletionTime"],
     "./internal/importer/",
     "THE POSITION: the rule reads the NAME-mapped status, so the category route is unexamined",
     None),

    # ⚠ C5 AND C5b ARE TWO CONTROLS FOR TWO DIFFERENT RULES, AND SEPARATING THEM CHANGED THE CODE.
    # My first draft named one catcher for both and scored `NOT CAUGHT`: jiraAPIResolution opened
    # with its own copy of #82's done-only gate, so blinding EITHER copy left the invariant standing
    # and no single-line mutation could red the test that pins it.  A rule two lines enforce is a
    # rule neither line is answerable for.  The redundant copy was REMOVED — the classification gate
    # now lives only in the shared rule (C5b) and what remains here is reportOnDone, which governs
    # something else: whether an ABSENT field is a loss worth reporting on this row (C5).
    ("C5", "internal/importer/api_resolution.go",
     "func reportOnDone(status model.IssueStatus, n FieldNote) []FieldNote {\n\tif status != model.StatusDone {",
     "func reportOnDone(status model.IssueStatus, n FieldNote) []FieldNote {\n\tif false && status != model.StatusDone {",
     ["TestJiraAPIResolution_AnAbsentFieldIsReportedOnEveryDoneRow"],
     ["TestJiraAPIResolution_AbandonedWorkImportsAsCancelledWithNoCompletionTime"],
     "./internal/importer/",
     "the report gate is blinded — an open row is told its resolution field went missing", None),

    ("C5b", "internal/importer/jira_csv_resolution.go",
     "\tif raw == \"\" || status != model.StatusDone {",
     "\tif raw == \"\" {",
     ["TestJiraAPIResolution_ARowThatDidNotImportAsDoneIsUntouched",
      "TestJiraCSVResolution_OnlyEverActsOnADoneRow"],
     ["TestJiraAPIResolution_AbandonedWorkImportsAsCancelledWithNoCompletionTime"],
     "./internal/importer/",
     "the SHARED rule's done-only gate is blinded — #82's invariant, now load-bearing for two transports",
     None),

    # ⚠ C6 IS THE STRUCTURAL ZERO, AND IT IS THE STATE IN WHICH THE WHOLE RULE IS SILENTLY INERT.
    # A typo or a rename in jiraFields produces exactly this: every abandoned issue reverts to
    # importing as delivered, and without the warning line the database rows and the job report are
    # byte-identical to a correct import.
    ("C6", "internal/importer/api_resolution.go",
     "\tif len(raw) == 0 {\n\t\treturn status, reportOnDone(status, FieldNote{",
     "\tif false && len(raw) == 0 {\n\t\treturn status, reportOnDone(status, FieldNote{",
     ["TestJiraAPIResolution_AnAbsentFieldIsReportedOnEveryDoneRow",
      "TestJobRow_JiraAPI_AResponseWithNoResolutionFieldSaysSoInTheJobRow"],
     ["TestJiraAPIResolution_AbandonedWorkImportsAsCancelledWithNoCompletionTime"],
     "./internal/importer/",
     "an ABSENT `resolution` key stops being reported — the rule goes silently inert", None),

    # ⚠ C7 EARNS THE OTHER TEST THAT PASSED ON ITS FIRST RUN.  Reporting `null` as a structural zero
    # would tell every operator with open issues that their response was broken — the mirror image
    # of C6, and the reason the decoder keeps the raw bytes instead of a *struct.
    ("C7", "internal/importer/api_resolution.go",
     "\tif len(raw) == 0 {", '\tif len(raw) == 0 || string(raw) == "null" {',
     ["TestJiraAPIResolution_ANullResolutionIsNotALoss"],
     ["TestJiraAPIResolution_AbandonedWorkImportsAsCancelledWithNoCompletionTime"],
     "./internal/importer/",
     "an UNRESOLVED issue is reported as a broken response — absent and null conflated", None),

    # ⚠ MY FIRST DRAFT REPLACED THE CONDITION WITH `false`, WHICH LEAVES `err` DECLARED AND UNUSED —
    # a BUILD failure, correctly scored as its own state rather than as a caught mutation. #86 paid
    # for that lesson twice and the harness keeps the scoring; the mutation had to be fixed instead.
    ("C8", "internal/importer/api_resolution.go",
     "\tif err := json.Unmarshal(raw, &res); err != nil {\n\t\t// A shape this decoder cannot read",
     "\tif err := json.Unmarshal(raw, &res); err != nil && false {\n\t\t// A shape this decoder cannot read",
     ["TestJiraAPIResolution_AnUnexpectedShapeIsReportedNotDropped"],
     ["TestJiraAPIResolution_AbandonedWorkImportsAsCancelledWithNoCompletionTime"],
     "./internal/importer/",
     "a `resolution` in a shape the decoder cannot read is dropped in silence", None),

    # ⚠ C9 IS THE ONE THAT PROVES THE RENAME DID NOT BLIND #82's GUARD, and it is the control that
    # WIDENED that guard.  This merge renamed applyJiraCSVResolution → applyJiraResolution because
    # the rule is now shared; a source-derived guard that names a function is exactly the kind that
    # goes quiet when the function moves.  The mutation is #82's own failure mode: the rule growing a
    # vocabulary of its own — with the 7,214 unread resolutions on the measured instance as motive.
    #
    # ⚠ AND IT SCORED `NOT CAUGHT` ON ITS FIRST RUN, CORRECTLY.  The guard inspected `case` clauses
    # only, so an `if raw == "Duplicate"` — the shape a session in a hurry actually writes — walked
    # straight through it. The guard now collects every string literal in the function body. THE
    # CONTROL FOUND A HOLE IN A MERGED GUARD; the mutation was not changed to fit it.
    ("C9", "internal/importer/jira_csv_resolution.go",
     "\tmeaning, _ := mapJiraStatus(raw)",
     '\tif raw == "Duplicate" {\n\t\treturn model.StatusCancelled, nil\n\t}\n\tmeaning, _ := mapJiraStatus(raw)',
     ["TestSourceDerived_TheResolutionRuleOwnsNoVocabulary",
      "TestJiraAPIResolution_UnreadableResolutionIsReportedAndChangesNothing"],
     ["TestJiraAPIResolution_AbandonedWorkImportsAsCancelledWithNoCompletionTime"],
     "./internal/importer/",
     "the shared rule grows its OWN vocabulary — the refusal #82 and #76 both left standing", None),

    # ⚠ C10 IS THE REACHABILITY PROBE FOR C1b.  A green C1b is only the fixture's recorded limit if
    # the job test genuinely EXECUTES this path; otherwise "the job test cannot see it" and "the job
    # test does not run" are the same observation.  This mutates the value the mapper reads — a
    # sibling of what C1 removes from the request — and the job test must catch it immediately.
    ("C10", "internal/importer/jira.go",
     "jiraAPIResolution(it.Fields.Resolution, status)", "jiraAPIResolution(nil, status)",
     ["TestJobRow_JiraAPI_AbandonedWorkLandsCancelledAndUncountedInPostgres"],
     ["TestJobRow_JiraCSV_AbandonedWorkLandsCancelledAndUndatedInPostgres"],
     "./internal/importer/",
     "REACHABILITY PROBE for C1b: the mapper reads nothing, and the job test must see THAT", None),

    # ⚠ C11 MUST STAY GREEN.  Without it, "every mutation went red" is equally consistent with a
    # harness that reds on ANY edit to these files.  #85's C10 / #86's C9, kept.
    ("C11", "internal/importer/api_resolution.go",
     "// api_resolution.go — whether closed work was FINISHED or ABANDONED",
     "// api_resolution.go — whether closed work was finished or abandoned",
     [],
     ["TestJiraAPIResolution_AbandonedWorkImportsAsCancelledWithNoCompletionTime",
      "TestJobRow_JiraAPI_AbandonedWorkLandsCancelledAndUncountedInPostgres"],
     "./internal/importer/",
     "MUST-STAY-GREEN: a comment reworded in the file every other control edits", None),
]


def sha(path):
    return hashlib.sha256((ROOT / path).read_bytes()).hexdigest()


def run(targets, pkg):
    """Return (passed, output). passed is None for BUILD failure or a pattern that matched nothing."""
    cmd = ["go", "test", "-timeout", "300s", "-count=1"]
    if targets:
        cmd += ["-run", "^(" + "|".join(targets) + ")$"]
    cmd.append(pkg)
    p = subprocess.run(cmd, cwd=ROOT, capture_output=True, text=True)
    out = p.stdout + p.stderr
    # ⚠ A BUILD FAILURE IS NOT A CAUGHT MUTATION and must never be scored as one.
    if "build failed" in out or "cannot use" in out or "undefined:" in out or "declared and not used" in out:
        return None, out
    # ⚠ NO TESTS MATCHED IS NOT A PASS. `go test -run` exits 0 when the pattern matches nothing.
    if targets and "no tests to run" in out:
        return None, out
    return p.returncode == 0, out


def main():
    if not os.environ.get("TRACK_TEST_DATABASE_URL"):
        print("REFUSING TO RUN: TRACK_TEST_DATABASE_URL is unset. Every real-Postgres control "
              "would SKIP, go test would exit 0, and this script would report a clean sweep of "
              "controls that never ran.", file=sys.stderr)
        return 3

    files = sorted({c[1] for c in CONTROLS})
    before = {f: sha(f) for f in files}

    print("BASELINE — the suite must be green before any mutation means anything")
    ok, out = run([], "./internal/importer/")
    if not ok:
        print("  BASELINE RED — stopping. A control campaign on a red tree proves nothing.")
        print(out[-3000:])
        return 2
    print("  baseline green\n")

    verdicts = {}
    for cid, path, anchor, repl, must_red, must_green, pkg, note, scope in CONTROLS:
        p = ROOT / path
        src = p.read_text()
        head, body = "", src
        if scope:
            i = src.find(scope)
            if i < 0:
                verdicts[cid] = f"SCOPE MARKER {scope!r} NOT FOUND — NOT RUN"
                print(f"{cid}  scope marker not found in {path} — not run")
                continue
            head, body = src[:i], src[i:]
        n = body.count(anchor)
        if n != 1:
            verdicts[cid] = f"ANCHOR {n} != 1 — NOT RUN"
            print(f"{cid}  ANCHOR COUNT {n} != 1 in {path} — not run")
            continue
        p.write_text(head + body.replace(anchor, repl, 1))
        # ⚠ THE BYTES MUST HAVE CHANGED ON DISK. #83 lost a control whose edit never applied and
        # read the resulting green as a dead guard.
        if sha(path) == before[path]:
            p.write_text(src)
            verdicts[cid] = "EDIT DID NOT CHANGE THE FILE — NOT RUN"
            print(f"{cid}  edit left {path} byte-identical — not run")
            continue
        try:
            red_ok, red_detail = True, []
            for t in must_red:
                passed, _ = run([t], pkg)
                if passed is None:
                    red_detail.append(f"{t}=BUILD/NOMATCH")
                    red_ok = False
                elif passed:
                    red_detail.append(f"{t}=STILL GREEN")
                    red_ok = False
                else:
                    red_detail.append(f"{t}=red")

            green_ok, green_detail = True, []
            for t in must_green:
                passed, _ = run([t], pkg)
                if passed is None:
                    green_detail.append(f"{t}=BUILD/NOMATCH")
                    green_ok = False
                elif passed:
                    green_detail.append(f"{t}=green")
                else:
                    green_detail.append(f"{t}=WENT RED")
                    green_ok = False
        finally:
            p.write_text(src)

        restored = sha(path) == before[path]

        if not must_red and not must_green:
            v = "MEASURED-ONLY"
        elif not must_red:
            v = "STAYED GREEN (as specified)" if green_ok else "COMPANION WENT RED"
        elif red_ok and green_ok:
            v = "CAUGHT"
        elif red_ok and not green_ok:
            v = "SUSPECT — companion also red; a broken build reads like a caught mutation"
        else:
            v = "NOT CAUGHT"
        if not restored:
            v += "  ⚠ TREE NOT RESTORED"
        verdicts[cid] = v
        print(f"{cid}  {v}\n     {note}")
        if red_detail:
            print(f"     must-red   : {'; '.join(red_detail)}")
        if green_detail:
            print(f"     must-green : {'; '.join(green_detail)}")
        print(f"     restored   : {restored}")

    print("\nSUMMARY")
    for cid, v in verdicts.items():
        print(f"  {cid}: {v}")

    bad = [c for c, v in verdicts.items()
           if "NOT RESTORED" in v or v.startswith("NOT CAUGHT") or v.startswith("SUSPECT")
           or v.startswith("ANCHOR") or v.startswith("EDIT DID NOT") or v == "COMPANION WENT RED"]
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
