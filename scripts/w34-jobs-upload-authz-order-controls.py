#!/usr/bin/env python3
"""Positive controls for the ASYNC import endpoint's authorize-before-read order (W3.4, after #90).

THE FINDING. `6d5824b` (#90) hoisted authz above the parse in handler.run — the SYNC routes
POST /v1/import/{linear,jira}. It left the OTHER multipart handler in this repo untouched, and
there is exactly one other: JobHandler.create (job_handler.go), POST /v1/import/jobs, the T8
Build B ASYNC surface — the endpoint that exists so a bulk import can outlive the inline 30s
timeout, i.e. the one the large uploads are meant to use.

`grep -rn 'ParseMultipartForm|FormFile' --include='*.go'` over this repo returns TWO non-test
call sites: handler.go (fixed at #90) and job_handler.go (not). MEASURED on 6d5824b on the
production middleware stack against real Postgres: a caller with a valid gateway identity and
NO membership in the target workspace POSTed 4 MiB to /v1/import/jobs and the server read all
4,194,830 bytes before answering 403 FORBIDDEN.

TestJobHandler_NonMemberUploadIsNotRead was RED before the fix, at the assertion it exists for
("server read N bytes … of a NON-MEMBER's upload"). That is necessary and not sufficient:
red-on-the-original-defect says nothing about whether each INDIVIDUAL assertion in the test is
live. Each control below removes exactly one thing and NAMES THE ASSERTION THAT MUST SPEAK,
predicted before the run.

⚠ THE GUARD'S OWN POSITIVE CONTROL IS INSIDE IT, AND C3 IS WHAT MAKES THAT A FACT. "0 bytes
read" is ALSO what a test with an empty fixture reports. The test therefore sends a
byte-identical upload as a MEMBER and requires the server to read all of it, return 202 and
PERSIST a payload row of at least that size. C3 empties the fixture and requires the member
half — and only the member half — to red.

⚠ EVERY CONTROL CARRIES A MUST-STAY-GREEN COMPANION: the four pre-existing tests on this async
surface (TestAPIEnqueue_Tenancy, TestJobCreate_CrossWorkspaceTeam_400, TestJobStatus_TenancyScoped,
TestJob_PayloadAtomicityAndCascade). Without one, "the target went red" is equally consistent
with a mutation that broke the build or reddened everything. BOTH RED IS `SUSPECT`, NEVER `CAUGHT`.

⚠ C1's MUST-GREEN LIST IS THE CLAIM THAT THIS GUARD IS NOT REDUNDANT. Restoring the defect
leaves all four green — the status code is 403 with or without it and only the byte count
differs, so nothing already in this package could see it. If any of them reds here, this guard
was not needed.

⚠ C3 ALSO REDS TestImporter_NonMemberUploadIsNotRead (#90's guard) AND THAT IS EXPECTED, NOT A
COMPANION FAILURE: importUpload is ONE fixture shared by both files, so emptying it removes the
payload from both tests. It is deliberately absent from C3's must-green list, and its absence is
a stated fact about the coupling rather than a gap.

⚠ C5 IS EXPECTED TO CATCH NOTHING AND IS SHIPPED SAYING SO. It confirms, on this second
handler, the limit #90's C4 recorded on the first: delete the explicit ParseMultipartForm and
FormFile parses implicitly at net/http's 32 MiB default, the heap buffer silently changes, and
NOTHING in talyvor-track notices. The ordering property still holds (the implicit parse is
below the authz check), so a green sweep is the evidence for that limit, not a failure of it.

⚠ THE MUST-RED OUTPUT IS READ BY ASSERTION, NOT BY EXIT CODE. A CAUGHT verdict can name a test
that never reached the assertion the control exists for — an earlier t.Fatalf is enough to skip
it. This runner prints the file:line of the first failing assertion for every must-red target.

⚠ THE RUNNER AND ITS VERDICT LOGIC ARE #86/#87/#88/#89/#90's, CARRIED OVER UNCHANGED because
they were paid for by RUNNING them: an ambiguous anchor that silently matched twice, a mutation
that did not compile being scored as CAUGHT (hence the BUILD state), and a `-run` pattern
matching nothing exiting 0 (hence NOMATCH). The CONTROLS are this merge's own.

⚠ THE ANCHORS ARE COMPOSED FOR A REASON. The authz block in create() is BYTE-IDENTICAL to the
one in createAPI(), so an anchor of the authz lines alone matches TWICE and the runner would
refuse it. Every ordering anchor below spans params→authz→parse, which occurs once.

⚠ THE BASELINE GATE IS LOAD-BEARING. Without TRACK_TEST_DATABASE_URL every control here would
SKIP, `go test` would exit 0, and this script would report a clean sweep of controls that never
ran.

    TRACK_TEST_DATABASE_URL=... python3 scripts/w34-jobs-upload-authz-order-controls.py
"""
import hashlib
import os
import pathlib
import re
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent

IMP = "./internal/importer/"

HANDLER = "internal/importer/job_handler.go"
TEST = "internal/importer/jobs_upload_authz_order_test.go"
FIXTURE = "internal/importer/upload_authz_order_test.go"  # importUpload lives here, shared

ORDER = "TestJobHandler_NonMemberUploadIsNotRead"
API_TENANCY = "TestAPIEnqueue_Tenancy"
CROSS_TEAM = "TestJobCreate_CrossWorkspaceTeam_400"
STATUS_TENANCY = "TestJobStatus_TenancyScoped"
PAYLOAD_ATOMIC = "TestJob_PayloadAtomicityAndCascade"

ASYNC_SUITE = [API_TENANCY, CROSS_TEAM, STATUS_TENANCY, PAYLOAD_ATOMIC]

# The post-fix ordering, verbatim. The long prose block this merge added above PARAMS is NOT
# part of any anchor: a later edit to it would otherwise turn every control into
# "ANCHOR 0 != 1 — NOT RUN" (the pointerAudit lesson, in a different repo's shape).
PARAMS = (
    '\tworkspaceID := r.URL.Query().Get("workspace_id")\n'
    '\tteamID := r.URL.Query().Get("team_id")\n'
    '\tsourceType := r.URL.Query().Get("source_type")\n'
    '\tif workspaceID == "" || teamID == "" || sourceType == "" {\n'
    '\t\twriteErr(w, http.StatusBadRequest, "BAD_PARAMS", "workspace_id, team_id, source_type are required (query string)")\n'
    "\t\treturn\n"
    "\t}\n"
    "\tif !validSourceTypes[sourceType] {\n"
    '\t\twriteErr(w, http.StatusBadRequest, "BAD_SOURCE_TYPE", "source_type must be linear_csv or jira_csv")\n'
    "\t\treturn\n"
    "\t}\n"
)
COMMENT = (
    "\t// SAME tenancy gate as the sync path: authorize the caller-supplied workspace against memberships; the\n"
    "\t// workspace persisted below is the server-resolved membership row (m.WorkspaceID), never the query alone.\n"
)
AUTHZ = (
    "\tm, ok := authz.AuthorizeWorkspace(r.Context(), workspaceID)\n"
    "\tif !ok {\n"
    '\t\twriteErr(w, http.StatusForbidden, "FORBIDDEN", "not a member of this workspace")\n'
    "\t\treturn\n"
    "\t}\n"
)
PARSE = (
    "\tif err := r.ParseMultipartForm(jobMaxUploadBytes); err != nil {\n"
    '\t\twriteErr(w, http.StatusBadRequest, "BAD_UPLOAD", err.Error())\n'
    "\t\treturn\n"
    "\t}\n"
)

# (id, file, anchor, replacement, must_red, must_stay_green, package, note)
CONTROLS = [
    ("C1", HANDLER,
     PARAMS + COMMENT + AUTHZ + PARSE,
     PARSE + PARAMS + COMMENT + AUTHZ,
     [ORDER], ASYNC_SUITE, IMP,
     "THE DEFECT ITSELF — the exact pre-merge order of job_handler.create, restored as ONE "
     "contiguous move so the harness cannot apply half of it. PREDICTED CATCHER, stated before "
     "the run: ORDER reds at the `cb.n != 0` assertion (\"server read N bytes … of a "
     "NON-MEMBER's upload\"), NOT at the 403 check and NOT at any member-half assertion — the "
     "status code is 403 with or without this mutation, which is exactly why all four "
     "pre-existing async tests must stay green. Read the red@file:line below against that claim."),

    ("C2", HANDLER,
     PARAMS + COMMENT + AUTHZ + PARSE,
     PARAMS + PARSE + COMMENT + AUTHZ,
     [ORDER], ASYNC_SUITE, IMP,
     "THE HALF-FIX: hoist the cheap query-parameter and source-type checks above the parse but "
     "leave the MEMBERSHIP check below it. This is the shape a reviewer would accept as 'the "
     "ordering is fixed', and it is not: a non-member still supplies workspace_id, team_id and "
     "a valid source_type, so the parse still runs and the whole body is still read before the "
     "403. It earns the claim that AuthorizeWorkspace specifically, not merely 'something "
     "cheap', had to move. PREDICTED CATCHER: ORDER reds at `cb.n != 0`."),

    ("C3", FIXTURE,
     "\tfor written := 0; written < uploadPayload; written += len(filler) {\n",
     "\tfor written := 0; written < 0; written += len(filler) { // CONTROL\n",
     [ORDER], ASYNC_SUITE, IMP,
     "⚠ THE CONTROL THAT EARNS THE MEMBER HALF OF THE GUARD. Empty the filler so the upload is "
     "a bare one-row CSV. The non-member assertion (`cb.n != 0`) then PASSES — vacuously, "
     "because there was nothing to read — and the only thing that can notice is the member "
     "half. PREDICTED CATCHER: ORDER reds at `okCB.n < uploadPayload` (\"the fixture is not "
     "producing the payload, so the non-member's zero above proves nothing\"), not at the "
     "non-member assertion. ⚠ #90's TestImporter_NonMemberUploadIsNotRead reds here too and is "
     "deliberately NOT in the must-green list: importUpload is one shared fixture."),

    ("C4", HANDLER,
     "\tjobID, err := h.jobs.Create(r.Context(), m.WorkspaceID, teamID, sourceType, payload)\n",
     "\tjobID, err := h.jobs.Create(r.Context(), m.WorkspaceID, teamID, sourceType, []byte{}) // CONTROL\n",
     [ORDER], ASYNC_SUITE, IMP,
     "⚠ THE CONTROL THAT EARNS THE PAYLOAD-LENGTH ASSERTION. Persist an EMPTY payload: the "
     "member half still gets 202, still returns a job_id, still writes exactly one import_jobs "
     "row, and the byte counter still sees the whole body — every other assertion in the test "
     "passes. Only `octet_length(payload)` can tell that the upload did not survive the "
     "handler. It matters because a member half that 202s on a dropped file is a fixture that "
     "cannot vouch for the non-member's zero. PREDICTED CATCHER: ORDER reds at the "
     "`jobPayloadLen(...) < uploadPayload` assertion. ⚠ NOT REDUNDANT, and that is checked "
     "rather than assumed: createReq (jobs_integration_test.go) is used by exactly ONE test and "
     "it expects a 400, so before this merge NO test in this repo posted a valid CSV through "
     "the async handler and looked at what was stored."),

    ("C5", HANDLER,
     PARSE,
     "",
     [], [ORDER] + ASYNC_SUITE, IMP,
     "⚠ SHIPPED AS A DOCUMENTED-INERT CONTROL: it is EXPECTED to catch nothing and the verdict "
     "to read is STAYED GREEN. Delete the explicit ParseMultipartForm and r.FormFile parses "
     "implicitly with net/http's 32 MiB defaultMaxMemory — the ordering property this merge "
     "fixes still holds (the implicit parse is still below the authz check), so ORDER is "
     "correctly green, but jobMaxUploadBytes' role as the heap/disk spill point silently "
     "changes with NOTHING in this repo noticing. #90's C4 recorded exactly this limit for the "
     "SYNC handler; this run is what makes the same statement true of the async one rather than "
     "inherited from it."),
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
    for cid, path, anchor, repl, must_red, must_green, pkg, note in CONTROLS:
        p = ROOT / path
        src = p.read_text()
        n = src.count(anchor)
        if n != 1:
            verdicts[cid] = f"ANCHOR {n} != 1 — NOT RUN"
            print(f"{cid}  ANCHOR COUNT {n} != 1 in {path} — not run")
            continue
        p.write_text(src.replace(anchor, repl, 1))
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
