#!/usr/bin/env python3
"""Positive controls for the ADF attrs-borne nodes on the JIRA API TRANSPORT (W3.4, after #87).

WHAT THIS PROVES AND WHAT IT DOES NOT. Eight of the ten test functions were RED before the fix, and
that is necessary and not sufficient: it shows the guards can fail on the ORIGINAL defect, not that
they still fail on each INDIVIDUAL half of it. Each control below removes exactly ONE half and names
the test that must speak.

⚠ TWO OF THE TEN PASSED ON THEIR FIRST RUN AND ARE THE REASON THIS FILE EXISTS.
TestJiraAPI_EmptyStructuralNodesAreNotReportedAsLost and TestJiraAPI_APlainStringDescriptionStillImports
are must-stay-green companions by construction — they cannot red on the defect, only on the FIX
going too far. C5 is the mutation that earns the first: it installs the general rule
("report any leaf carrying attrs") that adf_attrs.go rejects, which is measured to invent 109
warnings over 2,000 real issues.

⚠ EVERY CONTROL CARRIES A MUST-STAY-GREEN COMPANION. Without one, "the target went red" is equally
consistent with a mutation that broke the build or reddened everything. BOTH RED IS `SUSPECT`,
NEVER `CAUGHT`.

⚠ THE RUNNER AND ITS VERDICT LOGIC ARE #86/#87's, CARRIED OVER UNCHANGED because they were paid for
by RUNNING them: an ambiguous anchor that silently matched twice (hence `scope`), a mutation that did
not compile being scored as CAUGHT (hence the BUILD state), and a `-run` pattern matching nothing
exiting 0 (hence the NOMATCH state). The CONTROLS are this merge's own.

⚠ THE BASELINE GATE IS LOAD-BEARING. Without TRACK_TEST_DATABASE_URL every job control would SKIP,
`go test` would exit 0, and this script would report a clean sweep of controls that never ran.

    TRACK_TEST_DATABASE_URL=... python3 scripts/w34-jira-api-adf-controls.py
"""
import hashlib
import os
import pathlib
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent

IMP = "./internal/importer/"

URL_TEXT = "TestJiraAPI_AnInlineCardsURLReachesTheDescription"
ONLY_LINK = "TestJiraAPI_ADescriptionThatIsOnlyALinkIsNotEmpty"
MENTION = "TestJiraAPI_AMentionAndAnEmojiKeepTheirText"
ATTACHMENT = "TestJiraAPI_AnAttachmentInTheDescriptionIsReported"
THREE = "TestJiraAPI_ThreeAttachmentsOnOneIssueAreOneNote"
TWO_TYPES = "TestJiraAPI_TwoDifferentDroppedTypesAreTwoNotes"
STRUCTURAL = "TestJiraAPI_EmptyStructuralNodesAreNotReportedAsLost"
MISSING_ATTR = "TestJiraAPI_ANodeWhosePinnedAttributeIsMissingIsReported"
PLAIN = "TestJiraAPI_APlainStringDescriptionStillImports"
WARNING = "TestJiraAPI_TheDroppedNodeWarningNamesTheNodeAndTheLoss"
JOB = "TestJobRow_JiraAPI_ALinkedURLIsInThePostgresSearchIndex"
OLD_ADF = "TestADFToText"

# (id, file, anchor, replacement, must_red, must_stay_green, package, note, scope)
CONTROLS = [
    ("C1", "internal/importer/adf_attrs.go",
     '\t"inlineCard": "url",\n', "",
     [URL_TEXT, ONLY_LINK, JOB], [PLAIN, ATTACHMENT, STRUCTURAL], IMP,
     "THE DEFECT ITSELF, one node type at a time: with inlineCard unpinned a link contributes "
     "nothing, exactly as it did before this merge — 753 of them across 1,828 real descriptions.",
     None),

    ("C2", "internal/importer/adf_attrs.go",
     '"inlineCard": "url",', '"inlineCard": "href",',
     [URL_TEXT, JOB], [ATTACHMENT, STRUCTURAL], IMP,
     "A PLAUSIBLE WRONG ATTRIBUTE. `href` is what anybody who has written HTML reaches for and it "
     "is not what ADF sends. This is why the tests name the attribute's VALUE rather than merely "
     "checking the node type is handled — and it is the one mutation the probe ALSO catches "
     "(RENDERED_AS), which matters because the probe is not in CI and this is.",
     None),

    ("C3", "internal/importer/adf_attrs.go",
     '"mention":    "text",', '"mention":    "id",',
     [MENTION], [URL_TEXT, ATTACHMENT], IMP,
     "The account id instead of the name. attrs.id is RIGHT THERE and is the wrong string — an "
     "operator would read `557058:aafa2e9a-…` in their description. Both halves of the mention "
     "assertion exist for this: the name must arrive AND the id must not.",
     None),

    ("C4", "internal/importer/adf_attrs.go",
     '\t"media":       {},\n', "",
     [ATTACHMENT, THREE, JOB], [STRUCTURAL, URL_TEXT], IMP,
     "Unpin the attachment. An attachment has no text equivalent, so the ONLY honest thing this "
     "importer can do about it is say so — this removes the saying.",
     None),

    ("C5", "internal/importer/jira.go",
     "\tif _, ok := adfNoTextEquivalent[n.Type]; ok {\n\t\tdropped.add(n.Type)\n\t}\n",
     "\tif _, placed := adfAttrText[n.Type]; !placed &&\n"
     "\t\tn.Text == \"\" && len(n.Content) == 0 && len(n.Attrs) > 0 {\n\t\tdropped.add(n.Type)\n\t}\n",
     [STRUCTURAL], [ATTACHMENT, URL_TEXT, TWO_TYPES], IMP,
     "⚠ THE CONTROL THAT EARNS A TEST THAT PASSED ON ITS FIRST RUN. It REPLACES the "
     "adfNoTextEquivalent table with the general rule adf_attrs.go rejects — 'report any leaf "
     "carrying attrs that we did not place' — which needs no table and survives unseen node types, "
     "and which fires on empty paragraphs, rules and empty headings carrying attrs.localId: 109 "
     "invented warnings over 2,000 real issues. Nothing else in this package can see it.\n"
     "     ⚠ THE FIRST DRAFT SCORED SUSPECT AND THAT WAS THE CONTROL'S FAULT, NOT THE CODE'S. It "
     "ADDED the general rule alongside the table instead of replacing it, so it also reported every "
     "inlineCard the mapper had just PLACED — the URL companion went red and the verdict became "
     "ambiguous. A control has to isolate the predicate the target governs, and this one now does: "
     "attachments are still reported, links are still placed, and only the invented warnings move.",
     None),

    ("C6", "internal/importer/adf_attrs.go",
     "\tif d.seen[nodeType] {\n\t\treturn\n\t}\n", "",
     [THREE], [ATTACHMENT, TWO_TYPES], IMP,
     "Drop the per-description dedup. The pipeline COUNTS notes, so three attachments on ONE issue "
     "become a warning that says '3 issue(s)' about one issue — a wrong number in the one channel "
     "an operator reads to size the loss.",
     None),

    ("C7", "internal/importer/adf_attrs.go",
     "\tfor _, nodeType := range d.order {\n"
     "\t\tout = append(out, FieldNote{Field: fieldDescription, Value: nodeType, Via: viaADFNodeDropped})\n"
     "\t}\n\treturn out\n",
     "\tfor _, nodeType := range d.order {\n"
     "\t\tout = append(out, FieldNote{Field: fieldDescription, Value: nodeType, Via: viaADFNodeDropped})\n"
     "\t}\n\treturn nil\n",
     [ATTACHMENT, THREE, JOB], [URL_TEXT, ONLY_LINK, STRUCTURAL], IMP,
     "Kill the REPORT channel while leaving the TEXT mapping intact. This is what separates the two "
     "halves of the merge: a fix that places links but silently swallows attachments would pass "
     "every assertion about description text.",
     None),

    ("C8", "internal/importer/csv.go",
     "\tcase n.Via == viaADFNodeDropped:", '\tcase n.Via == "adf-node-dropped-unreachable":',
     [WARNING, JOB], [ATTACHMENT, URL_TEXT], IMP,
     "⚠ THIS CONTROL CORRECTED THE MERGE RATHER THAN THE CODE. Making the branch unreachable drops "
     "the line into render's DEFAULT, which emits `unrecognised description \"media\" on 1 issue(s) "
     "— imported as \"\"`. The job test's first draft asserted only that the words `\"media\"` and "
     "`1 issue(s)` appeared, and BOTH are in that sentence — it stayed GREEN on a mutation that "
     "removes the only line an operator can act on. It now asserts the sentence. #87's lesson (2), "
     "one field over.",
     None),

    ("C9", "internal/importer/jira.go",
     "append(updatedNotes, descriptionNotes...)", "append(updatedNotes, descriptionNotes[:0]...)",
     [ATTACHMENT, THREE, JOB], [URL_TEXT, STRUCTURAL], IMP,
     "The WIRING. adfToText can be perfect and mapJiraIssues can still drop its notes on the floor — "
     "the mapper builds five other note slices in the same expression and one more is easy to lose.\n"
     "     ⚠ THE FIRST DRAFT WAS `append(updatedNotes, []FieldNote{}...)`, WHICH LEFT "
     "descriptionNotes DECLARED AND NOT USED — a BUILD FAILURE, which the runner correctly refuses "
     "to score as CAUGHT. `[:0]` keeps the variable live and drops every note, which is the "
     "mutation this control was for.",
     None),

    ("C10", "internal/importer/jira.go",
     "\t\tif s := adfAttrString(n.Attrs, attr); s != \"\" {\n\t\t\tb.WriteString(s)\n"
     "\t\t} else {\n\t\t\tdropped.add(n.Type)\n\t\t}\n",
     "\t\tif s := adfAttrString(n.Attrs, attr); s != \"\" {\n\t\t\tb.WriteString(s)\n\t\t}\n",
     [MISSING_ATTR], [URL_TEXT, ATTACHMENT, STRUCTURAL], IMP,
     "A PINNED NODE TYPE WHOSE ATTRIBUTE IS ABSENT. Without the else arm, a pinned type that stops "
     "carrying its attribute — the exact shape C2 mutates into — imports as nothing AND reports "
     "nothing, which is the defect this merge fixed, hiding inside the fix.",
     None),

    ("C11", "internal/importer/adf_attrs.go",
     "\tvar s string\n\tif json.Unmarshal(raw, &s) != nil {\n\t\treturn \"\"\n\t}\n\treturn s\n",
     "\treturn string(raw)\n",
     [URL_TEXT], [ATTACHMENT, STRUCTURAL, OLD_ADF], IMP,
     "⚠ A MUTATION THE FIRST DRAFT OF THE URL TEST COULD NOT SEE. Emitting the raw JSON puts "
     "`\"https://…\"` — quotes and all — into the description, and `strings.Contains(desc, url)` is "
     "TRUE of the quoted form. The assertion is now the whole rendered sentence, which is what Jira "
     "itself produces.",
     None),

    ("C12", "internal/issue/store.go",
     "to_tsvector('english', title || ' ' || description)\n              @@ websearch_to_tsquery('english', $2)",
     "to_tsvector('english', title)\n              @@ websearch_to_tsquery('english', $2)",
     [JOB], [URL_TEXT, ATTACHMENT], IMP,
     "⚠ THE CONTROL ON THE JOB TEST'S OWN INSTRUMENT. PROJ-CONTROL carries the search term as "
     "ORDINARY TEXT precisely so a red on PROJ-LINK alone is the product defect while a red on both "
     "is a broken instrument. Blinding Search's description column must trip the INSTRUMENT branch "
     "and its message, not the product one — an in-test positive control nobody has watched fire is "
     "an in-test positive control nobody knows works.",
     None),
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
    ok, out = run([], IMP)
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
