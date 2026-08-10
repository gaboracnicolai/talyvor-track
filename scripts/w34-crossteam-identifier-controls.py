#!/usr/bin/env python3
"""w34-crossteam-identifier-controls.py — the positive-control harness for the cross-team
identifier refusal.

WHAT A CONTROL IS HERE: one mutation of the SHIPPED code, a PREDICTION of which test names must go
red written BEFORE the run, and a must-stay-green companion for every one of them. A guard that
cannot fail is not a guard, and an exit code cannot tell a caught mutation from a compile error —
so every verdict below is read from the set of failing test NAMES and from the assertion MESSAGE
that fired, never from `go test`'s status alone.

⚠ THE RESTORE IS IN A `finally` AND IS CHECKED BY sha256. A crash between mutate and restore would
otherwise leave a mutated predicate on disk, and the closing check would never run.

⚠ THE TARGET EXCLUDES NOTHING AND THAT IS DELIBERATE — both packages run, because a mutation of
internal/issue that only its own package can see would score CAUGHT while saying nothing about
whether the importer's job-level guards work.

    TRACK_TEST_DATABASE_URL=... python3 scripts/w34-crossteam-identifier-controls.py
"""
import hashlib
import os
import re
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
STORE = os.path.join(ROOT, "internal/issue/store.go")
SOURCE = os.path.join(ROOT, "internal/importer/source.go")
RUNNER = os.path.join(ROOT, "internal/importer/runner.go")
CSV = os.path.join(ROOT, "internal/importer/csv.go")

PKGS = ["./internal/importer/", "./internal/issue/"]

# ─── the controls ───────────────────────────────────────────────────────────
# Each: (id, file, old, new, prediction, must_stay_green, note)
CONTROLS = [
    ("C1", STORE,
     "        AND issues.team_id = $2\n",
     "",
     ["TestJobRow_AnImportIntoAnotherTeamDoesNotReportRowsThatTeamNeverGot",
      "TestJobRow_AnImportIntoAnotherTeamCountsTheRowsAsRefused",
      "TestJobRow_AnImportIntoAnotherTeamSaysWHICHTeamHasTheIssue",
      "TestJobRow_ACollidingKeyFromAnotherProviderDoesNotOverwriteTheOtherTeamsIssues",
      "TestJobRow_TheRefusalMessageNamesNoOtherWorkspacesTeam"],
     ["TestJobRow_AReimportIntoTheSameTeamStillUpdates",
      "TestJobRow_TheNativeCollisionRefusalKeepsItsOwnSentence"],
     "THE FIX REVERTED. This is the defect itself, back."),

    ("C2", STORE,
     "      WHERE issues.creator_id = '` + model.ImporterCreatorID + `'",
     "      WHERE issues.team_id IS NOT NULL",
     ["TestJobRow_TheNativeCollisionRefusalKeepsItsOwnSentence",
      "TestImport_DoesNotClobberANativeIssueSharingTheProviderKey",
      "TestJobRow_JiraCSV_ARowAHumanOwnsIsRefusedNotOverwritten",
      "TestJobRow_LinearCSV_ARowAHumanOwnsIsRefusedNotOverwritten"],
     ["TestJobRow_AReimportIntoTheSameTeamStillUpdates",
      "TestJobRow_ACollidingKeyFromAnotherProviderDoesNotOverwriteTheOtherTeamsIssues",
      "TestJobRow_AHumanIssueInAnotherTeamIsStillTheNativeRefusal"],
     "THE TEAM PREDICATE REPLACING #71's RATHER THAN JOINING IT. The import may now overwrite a "
     "human's issue in its own team. "
     "⚠ ONE PREDICTION WAS WRONG AND IS CORRECTED TO THE MEASURED SET RATHER THAN THE RUN TO THE "
     "PREDICTION: I named AHumanIssueInAnotherTeamIsStillTheNativeRefusal as a catcher and it "
     "stays GREEN — CORRECTLY. A human's issue in ANOTHER team is refused by EITHER predicate, so "
     "that case cannot distinguish them and is a must-stay-green here, not a catcher. Only a "
     "human's issue in the SAME team is protected by creator_id alone."),

    ("C3", SOURCE,
     "\t\t\t\tcase errors.Is(err, model.ErrIdentifierOwnedByAnotherTeam):\n"
     "\t\t\t\t\tout.Refused++\n"
     "\t\t\t\t\tout.refusedOtherTeam++\n",
     "\t\t\t\tcase errors.Is(err, model.ErrIdentifierOwnedByAnotherTeam):\n"
     "\t\t\t\t\tout.Skipped++\n"
     "\t\t\t\t\tout.refusedOtherTeam++\n",
     ["TestJobRow_AnImportIntoAnotherTeamCountsTheRowsAsRefused"],
     ["TestJobRow_AnImportIntoAnotherTeamDoesNotReportRowsThatTeamNeverGot",
      "TestJobRow_AReimportIntoTheSameTeamStillUpdates"],
     "A REFUSAL COUNTED AS A FAILURE — dcfbaa3's defect re-created for the second refusal."),

    ("C4", STORE,
     '\t\t\t"issue: %q is already imported into another team of this workspace (%s); this import will not move it or overwrite it: %w",\n'
     "\t\t\tidentifier, holdingTeamIdentifier, ErrIdentifierOwnedByAnotherTeam)",
     '\t\t\t"issue: %q already exists in this workspace and was not created by an import; refusing to overwrite it (%s): %w",\n'
     "\t\t\tidentifier, holdingTeamIdentifier, ErrIdentifierNotImportOwned)",
     ["TestJobRow_AnImportIntoAnotherTeamSaysWHICHTeamHasTheIssue"],
     ["TestJobRow_AnImportIntoAnotherTeamCountsTheRowsAsRefused",
      "TestJobRow_TheNativeCollisionRefusalKeepsItsOwnSentence"],
     "THE CROSS-TEAM REFUSAL WEARING #71's SENTENCE. Note the counter does NOT move — both "
     "sentinels are Refused — so only the sentence assertions can see this."),

    ("C5", RUNNER,
     "\tif native := out.Refused - out.refusedOtherTeam; native > 0 {",
     "\tif native := out.Refused; native > 0 {",
     ["TestJobRow_AnImportIntoAnotherTeamSaysWHICHTeamHasTheIssue"],
     ["TestJobRow_TheNativeCollisionRefusalKeepsItsOwnSentence",
      "TestJobRow_AllRowsRefused"],
     "THE SUMMARY SPLIT UNDONE: a cross-team refusal also renders the native sentence, so the "
     "operator is told both things and one of them is false."),

    ("C6", STORE,
     "\tcase creatorID != model.ImporterCreatorID:",
     "\tcase holdingTeamID == teamID && creatorID != model.ImporterCreatorID:",
     ["TestJobRow_AHumanIssueInAnotherTeamIsStillTheNativeRefusal"],
     ["TestJobRow_TheNativeCollisionRefusalKeepsItsOwnSentence",
      "TestJobRow_AnImportIntoAnotherTeamSaysWHICHTeamHasTheIssue"],
     "THE DIAGNOSIS ORDER. A human's issue in ANOTHER team now falls through to the cross-team "
     "branch. Every test written before this control shared a team between the human and the "
     "import, so the whole set was blind to it."),

    ("C7", STORE,
     "          WHERE i.workspace_id = $1 AND i.identifier = $2`,",
     "          WHERE $1 <> '' AND i.identifier = $2`,",
     ["TestJobRow_TheRefusalMessageNamesNoOtherWorkspacesTeam"],
     ["TestJobRow_AnImportIntoAnotherTeamSaysWHICHTeamHasTheIssue",
      "TestJobRow_TheNativeCollisionRefusalKeepsItsOwnSentence"],
     "THE DIAGNOSTIC READ UNSCOPED. `identifier` is unique per WORKSPACE, so an unscoped read "
     "can answer with another tenant's row and name its team in this workspace's job row."),

    ("C8", CSV,
     "\t\t\tIdentifier:  ci.get(row, jiraCSVIssueKeyColumn),",
     "\t\t\tIdentifier:  namespacedForControl(ci.get(row, jiraCSVIssueKeyColumn)),",
     ["TestJiraCSVCorpus_ProjectKeyNamespace"],
     ["TestJobRow_AnImportIntoAnotherTeamSaysWHICHTeamHasTheIssue",
      "TestJobRow_AReimportIntoTheSameTeamStillUpdates",
      "TestJobRow_JiraCSV_ANarrowerReimportEmptiesTheClobberedColumns"],
     "THE MUTATION ONLY THE CORPUS CENSUS CAN SEE, AND IT HAD TO BE MANUFACTURED — twice. It "
     "namespaces the provider key ONLY when the key CONTAINS A DIGIT: zero fixture keys in this "
     "repository do and 49 of the corpus's 172 do. The first draft keyed on length >= 8 and "
     "JRASERVER is nine characters long, so it reddened six job-level tests and isolated "
     "nothing. Every job-level guard stays green now; the census is the only thing in the "
     "repository that reads a REAL project key."),
]

# C8 needs a helper to exist for the package to compile — appended with the mutation and removed
# with it. A control that stops the build scores ERROR, not CAUGHT, and this is what keeps C8 from
# being that.
C8_HELPER = """

func namespacedForControl(k string) string {
	// A PROJECT KEY containing a digit — the project part only, which is everything before the
	// last hyphen. Measured: zero of the Jira-CSV fixture keys in this repository carry one
	// (JRASERVER, JRACLOUD, ENG, PROJ, SCRUM, QUAS, FABRICATED) and 49 of the corpus's 172 do
	// (ENT002001, CP2077, AAOS25 ...).
	//
	// TWO EARLIER DRAFTS ISOLATED NOTHING AND BOTH ARE WORTH THE LINE. The first keyed on
	// LENGTH >= 8, and JRASERVER is nine characters. The second scanned the WHOLE identifier for
	// a digit — and every identifier ends in the issue NUMBER, so it matched all of them; the
	// census reported it as "CAMEL-1" -> "NS-CAMEL-1", which is the whole corpus and every
	// fixture at once.
	i := strings.LastIndex(k, "-")
	if i <= 0 {
		return k
	}
	for _, c := range k[:i] {
		if c >= '0' && c <= '9' {
			return "NS-" + k
		}
	}
	return k
}
"""


def sha(p):
    return hashlib.sha256(open(p, "rb").read()).hexdigest()


def run_tests():
    env = dict(os.environ)
    r = subprocess.run(["go", "test", "-count=1", "-v", "-timeout", "300s"] + PKGS,
                       cwd=ROOT, capture_output=True, text=True, env=env)
    # ⚠ THE ORDER IS GO'S, NOT THE OBVIOUS ONE. `go test -v` prints a test's assertion lines
    # UNDER its `=== RUN` and only then the `--- FAIL:` verdict, so a parser that starts
    # collecting at `--- FAIL:` captures nothing and every verdict reads "no message". A control
    # verdict taken from names alone cannot tell a real catch from a crash — this is what makes
    # the message readable.
    failing, buffered, name = {}, [], None
    for line in r.stdout.splitlines():
        m = re.match(r"^=== RUN\s+(\S+)", line)
        if m:
            name, buffered = m.group(1), []
            continue
        m = re.match(r"^--- FAIL: (\S+)", line)
        if m:
            failing[m.group(1)] = list(buffered) or ["(red, no assertion line printed — a panic or a t.Fatal with no message)"]
            buffered = []
            continue
        if re.match(r"^--- (PASS|SKIP)", line):
            buffered = []
            continue
        if name and line.strip():
            buffered.append(line.strip())
    build_error = "[build failed]" in r.stdout or "cannot use" in r.stderr or r.stderr.strip().startswith("#")
    return failing, build_error, r


def main():
    if not os.environ.get("TRACK_TEST_DATABASE_URL"):
        print("TRACK_TEST_DATABASE_URL is unset — every real-Postgres guard would FAIL rather "
              "than run, and every control would score CAUGHT for the wrong reason.")
        return 2

    print("== BASELINE: the whole target must be green before any mutation ==")
    failing, build_error, r = run_tests()
    if build_error or failing:
        print(f"  NOT GREEN — {sorted(failing)} build_error={build_error}")
        print(r.stdout[-3000:])
        return 2
    print("  green\n")

    only = [a for a in sys.argv[1:] if a.startswith("C")]
    results = []
    for cid, path, old, new, predict, green, note in CONTROLS:
        if only and cid not in only:
            continue
        src = open(path).read()
        before = sha(path)
        if src.count(old) != 1:
            results.append((cid, "ERROR", f"anchor matched {src.count(old)} times, want exactly 1", []))
            print(f"{cid}  ERROR  anchor matched {src.count(old)} times")
            continue
        try:
            mutated = src.replace(old, new)
            if cid == "C8":
                mutated += C8_HELPER
            open(path, "w").write(mutated)
            if sha(path) == before:
                results.append((cid, "ERROR", "the edit changed no bytes", []))
                print(f"{cid}  ERROR  the edit changed no bytes")
                continue
            failing, build_error, r = run_tests()
            if build_error:
                verdict, detail = "ERROR", "the mutation stopped the package compiling"
            else:
                red = set(failing)
                missed = [t for t in predict if t not in red]
                broke = [t for t in green if t in red]
                if missed or broke:
                    verdict = "NOT AS PREDICTED"
                    detail = f"predicted-but-green={missed} must-stay-green-that-red={broke}"
                else:
                    verdict = "CAUGHT"
                    detail = f"{len(red)} red"
                results.append((cid, verdict, detail, sorted(failing)))
                print(f"{cid}  {verdict}  {detail}")
                for t in predict:
                    msg = failing.get(t) or (["(green — the prediction was wrong)"] if t not in failing
                                             else ["(red, no assertion line captured)"])
                    print(f"      {t}\n        {msg[0][:200]}")
                extra = sorted(set(failing) - set(predict))
                if extra:
                    print(f"      also red: {extra}")
                continue
            results.append((cid, verdict, detail, []))
            print(f"{cid}  {verdict}  {detail}")
        finally:
            open(path, "w").write(src)
            assert sha(path) == before, f"{cid}: RESTORE FAILED for {path}"

    print("\n== SUMMARY ==")
    for cid, verdict, detail, _ in results:
        print(f"  {cid}  {verdict}  {detail}")
    bad = [c for c in results if c[1] != "CAUGHT"]
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
