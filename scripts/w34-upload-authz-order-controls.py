#!/usr/bin/env python3
"""Positive controls for the import endpoint's authorize-before-read order (W3.4, after #89).

THE FINDING. handler.run called r.ParseMultipartForm fourteen lines above
authz.AuthorizeWorkspace, so a caller with a valid gateway identity and NO membership in the
target workspace had their ENTIRE upload read, buffered and (past maxUploadBytes) spilled to a
temp file before the 403. MEASURED on 666ce7a on the production middleware stack against real
Postgres: 41,943,379 bytes of a 40 MiB body read, then "not a member of this workspace".

TestImporter_NonMemberUploadIsNotRead was RED before the fix, at the assertion it exists for
(upload_authz_order_test.go, "server read N bytes … of a NON-MEMBER's upload"). That is
necessary and not sufficient: red-on-the-original-defect says nothing about whether each
INDIVIDUAL assertion in the test is live. Each control below removes exactly one thing and
names the assertion that must speak.

⚠ THE GUARD'S OWN POSITIVE CONTROL IS INSIDE IT, AND C3 IS WHAT MAKES THAT A FACT.
"0 bytes read" is ALSO what a test with an empty fixture reports, so the interesting assertion
could pass while measuring nothing. The test therefore sends a byte-identical upload as a
MEMBER and requires the server to read all of it and land the one real row. C3 empties the
fixture and requires the member half — and only the member half — to red. Without C3 that half
is a claim; with it, it is a run.

⚠ EVERY CONTROL CARRIES A MUST-STAY-GREEN COMPANION — the three pre-existing importer authz
tests. Without one, "the target went red" is equally consistent with a mutation that broke the
build or reddened everything. BOTH RED IS `SUSPECT`, NEVER `CAUGHT`.

⚠ C1's MUST-GREEN LIST IS THE CLAIM THAT THIS GUARD IS NOT REDUNDANT. Restoring the defect
leaves TestImporter_MemberOfA_CannotImportIntoB, TestImporter_NoMembership_403 and
TestImporter_TeamFromOtherWorkspace_RejectedByStore all GREEN — the whole pre-existing authz
suite cannot see it, because the status code is 403 either way and only the byte count differs.
If any of them reds here, this guard was not needed.

⚠ C4 IS EXPECTED TO CATCH NOTHING AND IS SHIPPED SAYING SO. Deleting the explicit
ParseMultipartForm call leaves FormFile to parse implicitly with net/http's 32 MiB default, so
the in-memory buffer silently halves and every test in this repo stays green. That is a real
limit of this suite recorded as a run rather than as a sentence.

⚠ THE MUST-RED OUTPUT IS READ BY ASSERTION, NOT BY EXIT CODE. A CAUGHT verdict can name a test
that never reached the assertion the control exists for — an earlier t.Fatalf is enough to skip
it. This runner prints the file:line of the first failing assertion for every must-red target.

⚠ THE RUNNER AND ITS VERDICT LOGIC ARE #86/#87/#88/#89's, CARRIED OVER UNCHANGED because they
were paid for by RUNNING them: an ambiguous anchor that silently matched twice, a mutation that
did not compile being scored as CAUGHT (hence the BUILD state), and a `-run` pattern matching
nothing exiting 0 (hence NOMATCH). The CONTROLS are this merge's own.

⚠ THE BASELINE GATE IS LOAD-BEARING. Without TRACK_TEST_DATABASE_URL every control here would
SKIP, `go test` would exit 0, and this script would report a clean sweep of controls that never
ran.

    TRACK_TEST_DATABASE_URL=... python3 scripts/w34-upload-authz-order-controls.py
"""
import hashlib
import os
import pathlib
import re
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent

IMP = "./internal/importer/"

HANDLER = "internal/importer/handler.go"
TEST = "internal/importer/upload_authz_order_test.go"

ORDER = "TestImporter_NonMemberUploadIsNotRead"
MEMBER_AB = "TestImporter_MemberOfA_CannotImportIntoB"
NO_MEMBERSHIP = "TestImporter_NoMembership_403"
OTHER_TEAM = "TestImporter_TeamFromOtherWorkspace_RejectedByStore"

AUTHZ_SUITE = [MEMBER_AB, NO_MEMBERSHIP, OTHER_TEAM]

# The post-fix ordering, verbatim. Comments are stripped from the anchors so a later prose edit
# does not silently turn a control into "ANCHOR 0 != 1 — NOT RUN"; the code lines below are
# contiguous in the source apart from the comment block C1/C2 span, which is included because
# the mutation has to move code ACROSS it.
PARAMS = (
    '\tworkspaceID := r.URL.Query().Get("workspace_id")\n'
    '\tteamID := r.URL.Query().Get("team_id")\n'
    '\tif workspaceID == "" || teamID == "" {\n'
    '\t\twriteErr(w, http.StatusBadRequest, "BAD_PARAMS", "workspace_id and team_id are required (query string)")\n'
    "\t\treturn\n"
    "\t}\n"
)
AUTHZ = (
    "\tm, ok := authz.AuthorizeWorkspace(r.Context(), workspaceID)\n"
    "\tif !ok {\n"
    '\t\twriteErr(w, http.StatusForbidden, "FORBIDDEN", "not a member of this workspace")\n'
    "\t\treturn\n"
    "\t}\n"
)
PARSE = (
    "\tif err := r.ParseMultipartForm(maxUploadBytes); err != nil {\n"
    '\t\twriteErr(w, http.StatusBadRequest, "BAD_UPLOAD", err.Error())\n'
    "\t\treturn\n"
    "\t}\n"
)
COMMENT = (
    "\t// This is a flat /v1/import/* route (no path {wsID}), so T10 resolved the caller's\n"
    "\t// memberships but authorized no single workspace. Authorize the caller-supplied\n"
    "\t// workspace_id against those memberships — not a member → 403. The workspace then comes\n"
    "\t// from the membership row (server-resolved), never trusted from the query alone.\n"
)

# (id, file, anchor, replacement, must_red, must_stay_green, package, note, scope)
CONTROLS = [
    ("C1", HANDLER,
     PARAMS + COMMENT + AUTHZ + PARSE,
     PARSE + PARAMS + COMMENT + AUTHZ,
     [ORDER], AUTHZ_SUITE, IMP,
     "THE DEFECT ITSELF — the exact pre-merge order, restored as one contiguous move so the "
     "harness cannot apply half of it. PREDICTED CATCHER, stated before the run: ORDER reds at "
     "the `cb.n != 0` assertion (\"server read N bytes … of a NON-MEMBER's upload\"), NOT at the "
     "403 check and NOT at either member-half assertion — the status code is 403 with or "
     "without this mutation, which is precisely why the pre-existing authz suite must stay "
     "green. Read the red@file:line below against that claim.",
     None),

    ("C2", HANDLER,
     PARAMS + COMMENT + AUTHZ + PARSE,
     PARAMS + PARSE + COMMENT + AUTHZ,
     [ORDER], AUTHZ_SUITE, IMP,
     "THE HALF-FIX: hoist the cheap query-parameter check above the parse but leave the "
     "MEMBERSHIP check below it. This is the shape a reviewer would accept as 'the ordering is "
     "fixed' — and it is not: a non-member still supplies workspace_id and team_id, so the "
     "parse still runs and the whole body is still read before the 403. It earns the claim that "
     "AuthorizeWorkspace specifically, not merely 'something cheap', had to move.",
     None),

    ("C3", TEST,
     "\tfor written := 0; written < uploadPayload; written += len(filler) {\n",
     "\tfor written := 0; written < 0; written += len(filler) { // CONTROL\n",
     [ORDER], AUTHZ_SUITE, IMP,
     "⚠ THE CONTROL THAT EARNS THE MEMBER HALF OF THE GUARD. Empty the filler so the upload is "
     "a bare one-row CSV. The non-member assertion (`cb.n != 0`) then PASSES — vacuously, "
     "because there was nothing to read — and the only thing that can notice is the member "
     "half. PREDICTED CATCHER: ORDER reds at `okCB.n < uploadPayload` (\"the fixture is not "
     "producing the payload, so the non-member's zero above proves nothing\"), not at the "
     "non-member assertion. This is the difference between a guard that measures refusal and a "
     "guard that measures an empty pipe.",
     None),

    ("C4", HANDLER,
     PARSE,
     "",
     [], [ORDER] + AUTHZ_SUITE, IMP,
     "⚠ SHIPPED AS A DOCUMENTED-INERT CONTROL: it is EXPECTED to catch nothing, and the verdict "
     "to read is STAYED GREEN. Delete the explicit ParseMultipartForm call and r.FormFile "
     "parses implicitly with net/http's 32 MiB defaultMaxMemory — the ordering property this "
     "merge fixes still holds (the implicit parse is still below the authz check), so ORDER is "
     "correctly green, but maxUploadBytes becomes dead and the in-memory buffer silently halves "
     "with NOTHING in this repo noticing. That is a real limit of this suite recorded as a run "
     "rather than as a sentence: no test in talyvor-track pins how much of an upload is held in "
     "heap. A green sweep here is the evidence for that claim, not a failure of it.",
     None),
]


def sha(path):
    return hashlib.sha256((ROOT / path).read_bytes()).hexdigest()


ASSERTION = re.compile(r"^\s+(\w+_test\.go:\d+):", re.M)


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


def first_assertion(out):
    """The file:line of the first failing assertion — so a CAUGHT verdict names the sentence that
    spoke, rather than merely reporting that the test exited non-zero."""
    m = ASSERTION.search(out)
    return m.group(1) if m else "no assertion line"


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
                passed, o = run([t], pkg)
                if passed is None:
                    red_detail.append(f"{t}=BUILD/NOMATCH")
                    red_ok = False
                elif passed:
                    red_detail.append(f"{t}=STILL GREEN")
                    red_ok = False
                else:
                    red_detail.append(f"{t}=red@{first_assertion(o)}")

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
