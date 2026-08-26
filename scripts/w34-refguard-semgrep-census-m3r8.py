#!/usr/bin/env python3
"""DOES SEMGREP SEE A REF GUARD WHOSE ANSWER IS THROWN AWAY? — the census, semgrep only.

#183 measured this with `go test` in the loop and recorded the answer as a NAMED NEXT STEP:
`.semgrep/cross-object-tenancy.yml`'s four `pattern-not-inside` arms exempt a function that CALLS
a ref guard, which asserts the CALL IS PRESENT and nothing about its ANSWER. Semgrep caught
**0 of 12** with every guard inert.

⚠ THIS HARNESS DELIBERATELY DOES NOT RUN `go test`, AND THAT IS A PROCESS FIX RATHER THAN A
SHORTCUT. #183's record: "the first run of this census WAS KILLED BY A COMMAND TIMEOUT AND ITS
`finally` NEVER RAN, LEAVING internal/importer/jobs.go WITH AN INERT GUARD IN THE WORKING TREE. A
sha256-verified restore inside a `finally` DOES NOT SURVIVE SIGTERM." The `go test ./...` calls are
what made that run long enough to be killed. The subject here is semgrep alone, each scan is
seconds, and the exposure shrinks with the runtime. It still checks the tree before AND after.

⚠ AND IT ASSERTS THE MUTATION LANDED. A census that edits a line the anchor no longer matches
reports NOT CAUGHT for a mutation that never happened — indistinguishable from a rule that cannot
see a real one.

Usage: python3 scripts/w34-refguard-semgrep-census-m3r8.py
"""

import hashlib
import os
import subprocess
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SEMGREP = ["semgrep", "scan", "--config", ".semgrep/", "--error", "--metrics=off",
           "--quiet", "internal/", "cmd/"]

# The twelve sites #183 measured: the five it found UNHELD by any test, plus the seven that were
# already held — kept so this harness carries its own population rather than a convenient subset.
SITES = [
    ("internal/notification/store.go", 64, "notification"),
    ("internal/cycle/store.go", 145, "cycle"),
    ("internal/label/store.go", 65, "label"),
    ("internal/milestone/store.go", 78, "milestone"),
    ("internal/project/store.go", 58, "project"),
    ("internal/scoring/store.go", 215, "scoring"),
    ("internal/featureboard/store.go", 333, "featureboard"),
    ("internal/guest/store.go", 251, "guest"),
    ("internal/template/store.go", 388, "template"),
    ("internal/customfield/store.go", 167, "customfield"),
    ("internal/dependency/store.go", 264, "dependency"),
    ("internal/importer/jobs.go", 67, "importer"),
]


def read(p):
    return open(os.path.join(REPO, p), encoding="utf-8").read()


def write(p, t):
    open(os.path.join(REPO, p), "w", encoding="utf-8").write(t)


def sha(p):
    return hashlib.sha256(open(os.path.join(REPO, p), "rb").read()).hexdigest()


def semgrep_findings():
    """Non-zero exit means at least one ERROR finding; --error makes that the signal."""
    p = subprocess.run(SEMGREP, cwd=REPO, capture_output=True, text=True)
    return p.returncode != 0, p.stdout + p.stderr


def _porcelain():
    p = subprocess.run(["git", "status", "--porcelain"], cwd=REPO, capture_output=True, text=True)
    return [l for l in p.stdout.split("\n") if l.strip()]


SITE_PATHS = {path for path, _, _ in SITES}


def dirty_sites():
    """Modifications to the twelve files this harness MUTATES. These are disqualifying.

    ⚠ THE SCOPE IS THE MUTATED FILES, NOT THE WHOLE TREE, AND BOTH NARROWINGS WERE MISTAKES THIS
    FUNCTION MADE FIRST. Version one refused because the harness itself was untracked. Version two
    refused because `.semgrep/cross-object-tenancy.yml` was edited — which is the very change this
    census exists to measure, so the guard blocked its own subject.

    What the check is FOR is #183's incident: a run killed by a tool timeout whose `finally` never
    executed, leaving a production file with an inert guard in the working tree. That is a claim
    about THESE files. A guard that also refuses on unrelated edits teaches its reader to delete
    it, and a deleted guard catches nothing at all.
    """
    return [l for l in _porcelain()
            if not l.startswith("??") and any(sp in l for sp in SITE_PATHS)]


def other_modifications():
    """Everything else that is modified — REPORTED, never a refusal, so a reader can see what
    else was in the tree when these numbers were taken."""
    return [l for l in _porcelain()
            if not l.startswith("??") and not any(sp in l for sp in SITE_PATHS)]


def main():
    before = dirty_sites()
    if before:
        print("REFUSING TO RUN — one of the twelve mutated files is already modified, so a "
              "restore cannot be verified\n" + "\n".join(before))
        return 2
    others = other_modifications()
    if others:
        print("note: other files are modified (reported, not disqualifying):\n  "
              + "\n  ".join(others) + "\n")

    clean_fired, out = semgrep_findings()
    if clean_fired:
        print("FATAL: semgrep already fires on the CLEAN tree, so every 'CAUGHT' below would be "
              "that pre-existing finding rather than the mutation:\n" + out[-2000:])
        return 1
    print("clean tree: semgrep 0 findings\n")

    caught = 0
    measured = 0
    for path, line, pkg in SITES:
        base, bsha = read(path), sha(path)
        lines = base.split("\n")
        idx = line - 1
        if idx >= len(lines) or "err != nil" not in lines[idx]:
            print(f"✗ {pkg:<14} ANCHOR MOVED at {path}:{line} — {lines[idx].strip()[:60] if idx < len(lines) else '<past EOF>'}\n"
                  f"   This site probes NOTHING and is NOT counted.")
            continue
        mutated = lines[:]
        mutated[idx] = lines[idx].replace("err != nil", "err != nil && false", 1)
        try:
            write(path, "\n".join(mutated))
            # The mutation must actually be on disk: a census that edits nothing reports NOT
            # CAUGHT for a defect it never introduced.
            if "err != nil && false" not in read(path):
                print(f"✗ {pkg:<14} MUTATION DID NOT LAND — not counted")
                continue
            measured += 1
            fired, _ = semgrep_findings()
            print(f"{'✓' if fired else '✗'} {pkg:<14} semgrep={'CAUGHT' if fired else 'NOT CAUGHT'}")
            caught += 1 if fired else 0
        finally:
            write(path, base)
            assert sha(path) == bsha, f"RESTORE FAILED {path}"

    # ── THE TWO SITES SEMGREP DOES NOT CATCH, DEMONSTRATED RATHER THAN EXCUSED ──────────────
    #
    # A census that reports "10 of 12" and stops leaves a reader to guess whether the other two
    # are a rule gap, a broken probe or something out of scope. They are two different things and
    # both are measurable, so both are measured here every run.
    print("\n── why the other two are not caught ──")

    # (1) template/store.go:388 is in ApplyTemplate, which performs a LOOKUP and no INSERT. This
    #     rule is `cross-object-INSERT-requires-tenancy-guard`; a function with no INSERT is
    #     outside its population by construction, not a hole in it. The control is template's
    #     OTHER guard — Create's team_id, which does sit beside an INSERT — and it MUST be caught,
    #     otherwise "out of population" would be indistinguishable from "this package is blind".
    base, bsha = read("internal/template/store.go"), sha("internal/template/store.go")
    lines = base.split("\n")
    if "err != nil" in lines[165]:
        try:
            m = lines[:]
            m[165] = lines[165].replace("err != nil", "err != nil && false", 1)
            write("internal/template/store.go", "\n".join(m))
            fired, _ = semgrep_findings()
            print(f"{'✓' if fired else '✗'} template  Create/team_id (a guard that DOES sit beside an "
                  f"INSERT) = {'CAUGHT' if fired else 'NOT CAUGHT'}")
            print("   → so template:388 is out of this rule's population (ApplyTemplate has no "
                  "INSERT), not a gap in it.")
        finally:
            write("internal/template/store.go", base)
            assert sha("internal/template/store.go") == bsha, "RESTORE FAILED template"

    # (2) notification/store.go guards TWO refs in one function (members at :64, issues at :68).
    #     `pattern-not-inside` is per-FUNCTION, not per-REFERENCE, so EITHER intact guard exempts
    #     the whole function and one inert guard hides behind its sibling. Shown in both
    #     directions — either alone NOT CAUGHT, both together CAUGHT — because "not caught" on its
    #     own is also what a broken probe looks like.
    base, bsha = read("internal/notification/store.go"), sha("internal/notification/store.go")
    lines = base.split("\n")
    try:
        both = lines[:]
        both[63] = lines[63].replace("err != nil", "err != nil && false", 1)
        both[67] = lines[67].replace("err != nil", "err != nil && false", 1)
        write("internal/notification/store.go", "\n".join(both))
        fired, _ = semgrep_findings()
        print(f"{'✓' if fired else '✗'} notification  BOTH guards inert = "
              f"{'CAUGHT' if fired else 'NOT CAUGHT'}")
        print("   → either one alone is NOT caught: the exemption is per-FUNCTION, so an intact "
              "sibling guard releases an inert one.")
        print("   → and BOTH of notification's guards ARE held by `go test ./internal/notification/` "
              "(measured, each direction). Semgrep is blind here; the repository is not.")
    finally:
        write("internal/notification/store.go", base)
        assert sha("internal/notification/store.go") == bsha, "RESTORE FAILED notification"

    after = dirty_sites()
    print("\n" + "=" * 62)
    print(f"sites measured: {measured}   semgrep CAUGHT: {caught} of {measured}")
    print(f"the twelve mutated files after: {'ALL RESTORED' if not after else 'DIRTY — ' + str(after)}")
    print("=" * 62)
    return 0 if not after else 1


sys.exit(main())
