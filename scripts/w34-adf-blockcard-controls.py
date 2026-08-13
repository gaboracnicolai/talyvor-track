#!/usr/bin/env python3
"""w34-adf-blockcard-controls-7f6b.py — positive controls for the blockCard pin (W3.4, tab-7f6b).

THE GUARD: internal/importer/adf_attrs_test.go
    TestJiraAPI_ABlockCardsURLReachesTheDescription
    TestJiraAPI_ADescriptionThatIsOnlyABlockCardIsNotEmpty

A guard that passes on the first run is a guard nobody has seen fail. Each control below MUTATES the
shipped source, runs the two tests, and asserts they go RED — and the last two assert the opposite
direction, because a test that reds on everything is as useless as one that reds on nothing.

Every mutation is applied to a COPY-ON-DISK and the file is restored byte-for-byte (sha256 checked)
on every exit path, including a failure.
"""
import hashlib
import pathlib
import re
import subprocess
import sys

# The repo root from THIS FILE's location — never $HOME/talyvor-track, which would make the
# harness work on one machine and silently probe nothing on any other.
REPO = pathlib.Path(__file__).resolve().parent.parent
ADF = REPO / "internal" / "importer" / "adf_attrs.go"
JIRA = REPO / "internal" / "importer" / "jira.go"
TESTS = "TestJiraAPI_ABlockCardsURLReachesTheDescription|TestJiraAPI_ADescriptionThatIsOnlyABlockCardIsNotEmpty"

results = []


def sha(p):
    return hashlib.sha256(p.read_bytes()).hexdigest()


def run_tests():
    """Returns (passed, output). A COMPILE ERROR is NOT a red — it proves the file moved, not that
    the product was wrong — so it is reported as such and fails the control."""
    r = subprocess.run(["go", "test", "./internal/importer/", "-run", TESTS, "-count=1"],
                       cwd=REPO, capture_output=True, text=True)
    out = r.stdout + r.stderr
    compiled = "build failed" not in out and "cannot use" not in out and "[build failed]" not in out
    return r.returncode == 0, out, compiled


def control(name, files_subs, want_pass, why):
    """files_subs: [(path, old, new)]. Applies, runs, restores, records."""
    originals = {}
    try:
        for p, old, new in files_subs:
            originals[p] = (p.read_text(), sha(p))
            src = originals[p][0]
            if old not in src:
                results.append((name, False, f"MUTATION DID NOT APPLY: {old[:60]!r} not in {p.name}"))
                return
            p.write_text(src.replace(old, new, 1))
        passed, out, compiled = run_tests()
        if not compiled:
            results.append((name, False, "did not COMPILE — a compile error is not a behavioural red"))
            return
        ok = (passed == want_pass)
        state = "PASS" if passed else "RED"
        results.append((name, ok, f"tests went {state} (wanted {'PASS' if want_pass else 'RED'}) — {why}"))
    finally:
        for p, (text, digest) in originals.items():
            p.write_text(text)
            assert sha(p) == digest, f"RESTORE FAILED for {p}"


def main():
    print("=== W3.4 blockCard pin — positive controls (tab-7f6b) ===\n")

    # C0 — BASELINE. Unmutated, the guard must PASS. Without this every red below could be a red
    # the guard produces on anything at all.
    control("C0 baseline: shipped source, no mutation", [], True,
            "the guard must be green on the code it ships with")

    # C1 — THE DEFECT ITSELF. Remove the pin and the guard must see it. This is the mutation that
    # reproduces main's behaviour before this merge.
    control("C1 remove the blockCard pin",
            [(ADF, '\t"blockCard":  "url",\n', "")], False,
            "an unpinned blockCard contributes nothing; the guard must catch exactly that")

    # C2 — THE ATTRIBUTE IS NAMED, NOT GUESSED. A pin to the WRONG attribute must red: otherwise the
    # test would pass on any pin at all and `url` would be decoration.
    control("C2 pin blockCard to a wrong attribute",
            [(ADF, '"blockCard":  "url",', '"blockCard":  "href",')], False,
            "the pinned attribute is load-bearing, not the mere presence of an entry")

    # C3 — THE RENDERED VALUE, NOT THE RAW JSON. If the flattener emitted the attribute's raw bytes
    # the URL would still be "contained" in the description; the guard's second assertion exists for
    # that and must fire.
    control("C3 emit the attribute's raw JSON instead of its string",
            [(ADF, "\tvar s string\n\tif json.Unmarshal(raw, &s) != nil {\n\t\treturn \"\"\n\t}\n\treturn s",
              "\tvar s string\n\tif json.Unmarshal(raw, &s) != nil {\n\t\treturn \"\"\n\t}\n\t_ = s\n\treturn string(raw)")],
            False, "a quoted-JSON emission must not satisfy the guard")

    # C4 — VACUITY. Break the walk entirely. A guard that still PASSES when the flattener does
    # nothing is asserting nothing. This is the control that caught three shipped guards elsewhere
    # in this queue.
    control("C4 make walkADF a no-op (vacuity)",
            [(JIRA, "func walkADF(n adfNode, b *strings.Builder, dropped *droppedTypes) {",
              "func walkADF(n adfNode, b *strings.Builder, dropped *droppedTypes) {\n\tif true {\n\t\treturn\n\t}")],
            False, "with no flattening at all the guard must be RED, never green")

    # C5 — THE NOTE ASSERTION IS LOAD-BEARING. A blockCard whose URL now imports must NOT also be
    # reported as lost. Put it in the dropped table as well and the guard must red — otherwise the
    # merge could ship a line that both places the value AND warns it was dropped.
    control("C5 also report blockCard as dropped",
            [(ADF, '\t"media":       {},', '\t"media":       {},\n\t"blockCard":   {},')], False,
            "a value that DID import must not carry a loss note")

    # C6 — THE OTHER DIRECTION, AND THE ONE THAT KEEPS C1-C5 HONEST. A mutation to a node type this
    # guard is NOT about must leave it GREEN. A guard that reds on any edit anywhere is a guard that
    # localises nothing.
    control("C6 mutate an UNRELATED pin (emoji) — guard must stay green",
            [(ADF, '"emoji":      "text",', '"emoji":      "shortName",')], True,
            "the guard is about blockCard and must not answer for emoji")

    print()
    bad = 0
    for name, ok, detail in results:
        print(f"  {'PASS' if ok else 'FAIL'}  {name:<52} {detail}")
        if not ok:
            bad += 1
    print(f"\n{len(results) - bad}/{len(results)} controls behaved as required.")
    if bad:
        print("A CONTROL DID NOT BEHAVE. The guard's evidence is void until this is explained.")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
