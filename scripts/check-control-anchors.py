#!/usr/bin/env python3
"""Every control script's anchors must still resolve in the file they name.

WHY THIS EXISTS, AND WHY THE OBVIOUS VERSION OF IT DOES NOT WORK
===============================================================
A mutate-and-restore control anchors on a literal that was unique WHEN WRITTEN.
The day the code it guards is edited again, the anchor stops resolving and the
control can no longer be APPLIED. It does not go quiet: every one of these
scripts asserts its anchor count before writing and says so loudly — this repo's
scripts print `ANCHOR COUNT n != 1 ... not run` or record a `HARNESS ERROR`. But
NOTHING IN CI RAN THEM, so the loudness reached nobody, and the only signal a
human ever saw was a fraction that reads like a score rather than like an
instrument reporting that it could not run.

W6.43 tried the obvious census — pull string literals out of `.count(`/`.replace(`
first-arguments with `ast` — and it reported ZERO. This one does NOT extract
literals. It EXECUTES each script's module-level definitions in a fresh namespace —
imports, assignments, `def`, `class`, and nothing else — then reads the resulting
VALUES. `if __name__` blocks and bare top-level calls are dropped, so nothing runs
and nothing is written. Because Python does the resolving, f-strings, constant
composition and tuple-building all come out exactly as the script itself sees them.

WHAT IT MEASURED ON THIS REPOSITORY WHEN IT WAS WRITTEN (main 181fdee, W6.50)
=============================================================================
125 scripts in the population — the largest in the estate, against the 80 that
yielded docs' ten dead arms. 33 reached, 223 anchors checked, and

    24 ANCHORS CARRYING 27 CONTROL ARMS ACROSS 10 SCRIPTS CANNOT BE APPLIED,
    THE OLDEST DEAD FOR 176 COMMITS, AND NOTHING IN THIS REPOSITORY COULD SEE IT.

  w34-jira-csv-created-controls.py     5 anchors  5 arms  oldest 176 commits
  w34-jira-csv-resolution-controls.py  4 anchors  5 arms  oldest 173 commits
  w34-burndown-ordering-controls-3d9e.py  4 anchors 4 arms       66 commits
  w34-linear-csv-dates-controls.py     3 anchors  3 arms  oldest 160 commits
  w34-jira-csv-updated-controls.py     2 anchors  2 arms  oldest 176 commits
  w34-api-updated-controls.py          1 anchor   2 arms         173 commits
  w34-burndown-ontrack-controls-3d9e.py 1 anchor  1 arm           66 commits
  w34-jira-api-resolution-controls.py  1 anchor   1 arm          164 commits
  w34-update-allowlist-controls-7c4d.py 1 anchor  1 arm           74 commits  ⚠ NEVER RAN
  w39-summary-empty-population-controls-m5x8.py 2 anchors 3 arms   28 commits  ⚠ see below

⚠ THE MECHANISM IS THE SAME ONE DOCS FOUND AND IT PREDICTS WHERE THE NEXT ONE COMES
FROM: EVERY ONE WAS KILLED BY A FIX TO THE FUNCTION IT GUARDS. A control anchored on
a line INSIDE the seam it watches is killed by precisely the event most likely to
happen next, because a function that just needed one fix tends to need another.

⚠⚠ AND ON THIS REPOSITORY IT WAS NOT ALWAYS THE *NEXT* FIX. IT WAS SOMETIMES THE
SAME COMMIT. `w34-update-allowlist-controls-7c4d.py`'s G1 — the arm that asks
"does anything at all watch the gate?" about the caller-supplied map keys
`internal/issue/store.go` interpolates into an UPDATE's SET list — anchors on

    if _, ok := updatableFields[k]; !ok && k != "completed_at" {

which is the PRE-FIX text of a line that commit `4eb06a2` rewrote. `4eb06a2` is
ALSO THE COMMIT THAT ADDED THE SCRIPT. Measured by resolving the anchor in its
target AT THAT COMMIT: count = 0. THE ARM WAS INERT ON ARRIVAL AND HAS NEVER RUN
ONCE. Every other one of the 22 resolved when it was written and went stale later;
this one was shipped as evidence and never was any. Harness:
~/talyvor-queue/w650-deadatbirth-c3j7.py.

⚠⚠⚠ AND THE GUARDS THEMSELVES ARE NOT KNOWN TO BE BLIND — that is a DIFFERENT
claim and this script cannot make it. W3.64 and W3.65 each ended by measuring that
the gap was the CONTROL and not the guard. Re-pointing an anchor at moved code
yields a control that passes for a NEW REASON unless somebody reads the guard and
re-proves the must-red first, which needs a real Postgres and a `go test` per arm.
That work is filed as W6.55, and G1 is where it should start.
⚠ IT WAS FILED AS W6.51 AND THAT NUMBER NOW BELONGS TO SOMETHING ELSE. Two tabs
appended to the queue in the same hour; the number was taken from the file when this
work was CLAIMED and written ~50 minutes later, and W6.51-W6.54 were filed in that
window. The queue's own advice — take the number at WRITE time — is necessary and not
sufficient, because the read and the write are still two operations over a file with no
lock. Recorded here rather than silently corrected: a pointer that resolves to the WRONG
item reads exactly like a correct one, which is the failure this repository keeps paying
for (see the cross-repo line citations W1.1/#153 found pointing at unrelated constants).

WHAT THIS PROVES AND WHAT IT DOES NOT
=====================================
It proves each control can still be APPLIED. It does NOT prove it still CATCHES:
an arm whose anchor resolves but whose mutation has become inert passes here.
Only running the campaign proves that, and the campaign needs a real Postgres and
a `go test` per arm, which is why it cannot live in this step.

POPULATION BOUNDARY — THE NUMBERS THAT KEEP THIS HONEST
=======================================================
Two detectors, 42 of 125 scripts reached, and every figure below prints on every run.

  ROW detector      33 scripts, 223 anchors. Reads (…, path, old, new, …) rows out of a
                    script's module-level values.
  BARE detector     9 more scripts, 29 constants. For scripts with no such row, checks each
                    anchor-shaped module-level constant against the script's declared targets —
                    but ONLY those an ast pass can show are used in an ANCHOR position.
  unclassified      70 constants have no observed role and are NOT reported. This is deliberate
                    under-reporting: the safe direction for a rule that reddens CI.
  unreached         85 scripts yield neither a row nor a classified constant, and the run prints
                    WHY, because a script with no anchor and a script whose anchor cannot be
                    reached look identical from outside and are opposite findings.

⚠ THE POSITIONAL READING OF A ROW IS WRONG ON THIS REPO AND IT FAILED IN BOTH DIRECTIONS AT
ONCE — see ROW_SCHEMA. Ported unchanged from docs, this census reported 10 unaccounted arms
across two scripts, ALL 10 SPURIOUS, while the 15 REAL anchors in the very same rows went
unread; all 15 resolve. A census that reports a clean bill on anchors it never looked at is
the defect this file exists to refuse, and it shipped that defect on its first run here.

⚠⚠ AND THE COUNT WAS TAKEN OVER THE WRONG BODY TWICE — see SCOPED. Two arms of
w34-api-updated-controls.py were called AMBIGUOUS because the census counted the whole file
where the script counts only after a `scope` marker. Both are unique where the script looks.

⚠⚠⚠⚠ AND THE WORST DEFECT IN THIS FILE WAS NOT AN ANCHOR AT ALL — IT WAS THIS SCRIPT
RUNNING OTHER PEOPLE'S CODE. `module_level_only` keeps `ast.Assign`, AND AN ASSIGNMENT'S
VALUE CAN BE A CALL: `w39-summary-empty-population-controls-m5x8.py:114` is
`ok, failed = run()`, which runs the whole real-Postgres campaign, `subprocess.run(cmd,
cwd=ROOT, ...)` and all. FIVE scripts here spawn a subprocess that way. The docstring that
this file inherited said "nothing in a control script therefore RUNS here, and nothing is
written"; in a repository whose control scripts mutate a tracked Go file and restore it,
a CI step contracted to do neither could have done both.

⚠ HOW IT WAS FOUND, AND IT WAS NOT BY READING: CI REDDED WHERE THE AUTHOR'S MACHINE WAS
GREEN. The first version reported 33 scripts / 225 anchors / 22 not ok locally and
34 / 230 / 24 in CI, because two of the five died on an unset TRACK_TEST_DATABASE_URL
here and loaded there. A census whose COVERAGE depends on the environment reports a
cleaner product on the machine with less of the environment set up — the flattering
direction. The floors had been set from the blinder run.

⚠⚠ CLOSED IN TWO INDEPENDENT LAYERS, because one of them will be wrong eventually:
a STATIC drop of any module-level assignment whose value contains a call that does not
merely compute (transitively — a name bound by a dropped assignment is not defined, and
dropping without that left three scripts raising NameError, which is the same blindness
by another route), and a RUNTIME block that makes subprocess/os.system/shutil/write-mode
`open` raise `CensusSideEffect` for the duration of every read. Controls B1/B2/B2b measure
each layer alone and both together; with BOTH disabled the probe's write lands, which is
what stops B1 from being a probe that never ran.

⚠⚠⚠ WHAT IS NOW DETERMINISTIC, STATED NARROWLY BECAUSE THE BROAD VERSION OF THIS SENTENCE
WAS THE OVER-CLAIM THIS WHOLE ENTRY IS ABOUT. Every input to every rule — scripts reached
(33), anchors checked (223), the not-ok set (24), and both bare-detector figures (9 / 29)
— is identical with the DB env set, unset, and with `go`/`semgrep` off PATH, and CI
reproduces all five. The DIAGNOSTIC TALLY of why the unreached are unreached is NOT: SIX
scripts move between NO-MODULE-LEVEL-ANCHOR and ANCHORS-BUT-NO-ROLE on
TRACK_TEST_DATABASE_URL alone (w310-scoring-tenancy, w34-aicosts-scope,
w34-analytics-index-claims, w34-csv-clobbered-columns, w34-expectquery-behaviour-census,
w34-expectquery-fingerprint-census), because each binds a DSN from the environment and
`None` is not anchor-shaped while a DSN string is. All six stay UNREACHED either way, so
no rule reads a different number — but the tally is a diagnostic, not a measurement, and
saying "deterministic" without that split is how the first version of this file came to
report a coverage figure that depended on the machine.

⚠⚠⚠ TWO FURTHER DETECTORS WERE BUILT AND MEASURED IN DOCS (W6.43c) AND NEITHER SHOULD BE
ADOPTED: a whole-file path harvest (5 more scripts, ZERO real findings, one false positive)
and keyed anchor tables (15 findings, FOURTEEN mis-attributed to the wrong file). The binding
constraint on the unreached is TARGET ATTRIBUTION, not anchor extraction.
"""
import argparse
import ast
import builtins
import contextlib
import hashlib
import io
import os
import re
import subprocess
import sys
import types

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SCRIPTS = os.path.join(REPO, "scripts")

# Source extensions a row's first element must end in to be TREATED AS A TARGET
# PATH AT ALL. Shape, not existence — see TARGET-MISSING below.
SOURCE_SUFFIXES = (".go", ".ts", ".tsx", ".js", ".jsx", ".sql", ".yaml", ".yml",
                   ".mod", ".sum", ".py", ".sh", ".json", ".css", ".html", ".md")

# ── FLOORS, ONE PER DETECTOR STAGE ───────────────────────────────────────────
# ⚠ TWO FLOORS, NOT ONE OVER THE UNION. A single floor on "anchors checked" is
# satisfied by one stage alone: blind the module-level exec so it reaches half
# the scripts, and a table that grew elsewhere holds the total up while coverage
# silently halved. talyvor-suite's W6.41a shipped exactly that trap.
#
# ⚠⚠ EVERY VALUE BELOW WAS MEASURED ON THIS REPOSITORY AT `181fdee`, NOT CARRIED
# OVER FROM talyvor-docs. Docs' own file records that copying a floor from a
# different population is how you get one that reds for no reason, or — one digit
# the other way — one that can never fire. Track's population is 125 scripts to
# docs' 80, and its bare-constant detector reaches 9 scripts where docs' reaches 16.
SCRIPTS_FLOOR = 33
ANCHORS_FLOOR = 223
BARE_SCRIPTS_FLOOR = 9
BARE_ANCHORS_FLOOR = 29

# ── ROWS WHOSE (old, new) PAIR IS NOT DIRECTLY AFTER THE PATH ────────────────
# ⚠ THIS MAP EXISTS BECAUSE THE DEFAULT POSITIONAL READING IS WRONG ON THIS REPO
# AND IT FAILS IN BOTH DIRECTIONS AT ONCE: it reports a false alarm on the column
# it mistakes for an anchor, AND it never checks the real anchor at all. MEASURED
# at `181fdee`: without these two entries the census reports 10 unaccounted arms
# across these two scripts, all 10 spurious, while the 15 REAL anchors in the same
# rows go unread — and all 15 resolve. A census that reports a clean bill on
# anchors it never looked at is the defect this file exists to refuse.
#
# ⚠⚠ THE FIX IS A DECLARATION, NOT A HEURISTIC, AND THAT IS DELIBERATE. The
# tempting rule is "try every column and keep the reading whose `old` resolves" —
# which is the same shape as "a literal that resolves 0 times was probably never
# an anchor", the rule this file's NOT_ANCHORS block already refuses because it
# defines away every true finding. A human read each row and wrote down where its
# anchor is; R8 fails if a listed script stops producing rows, so the map cannot
# rot into decoration.
#
# Keyed script -> (columns past the path where `old` sits, why).
ROW_SCHEMA = {
    # (label, path, GO-TEST-PACKAGE-PATH, anchor, must_red, must_green). Column 2 is
    # the `./internal/<pkg>/` argument this script hands to `go test`, so it is
    # path-ish enough to survive path_shaped() (it has no source suffix) and sits
    # exactly where an anchor would. Read at 181fdee, SITES.
    "w34-inert-ownergate-census-7k2p.py": (2, "col 2 is the go-test package path; the anchor is col 3"),
    # (id, target, PREDICTION, anchor, replacement, note). Column 2 is the arm's
    # predicted verdict — the literal strings CAUGHT / NOT CAUGHT (void) /
    # CAUGHT-ELSEWHERE / CAUGHT ([R-PREMISE-*]). Read at 181fdee, CONTROLS.
    "w34-report-ordering-controls-8f3d.py": (2, "col 2 is the predicted verdict; the anchor is col 3"),
}

# ── ANCHORS THE OWNING SCRIPT COUNTS IN A SCOPED BODY, NOT THE WHOLE FILE ────
# ⚠ WITHOUT THIS THE CENSUS USES A DENOMINATOR THE SCRIPT DOES NOT USE, and the
# direction it fails in is the flattering one for the census and the alarming one
# for the reader: it calls an arm AMBIGUOUS that its own script resolves uniquely.
# `w34-api-updated-controls.py` splits its target at a `scope` marker
# (`src.find(scope)`, then `body.count(anchor)`) and only mutates after it. MEASURED
# at 181fdee: both anchors below occur 2x in internal/issue/store.go and exactly 1x
# in the scoped body, so both arms apply and the census said they did not.
#
# ⚠⚠ A MISSING MARKER IS A FAILURE, NOT A PASS. The verdict SCOPE-MARKER-MISSING is
# reported like any other non-ok row, because the script itself prints
# "scope marker not found — not run" and that is an arm that cannot be applied.
#
# Keyed (script, sha256(anchor)[:12]) -> (marker, why).
SCOPED = {
    ("w34-api-updated-controls.py", "fd16aee09a61"):
        ("func (s *Store) UpsertByIdentifier", "C3: the script scopes this arm to UpsertByIdentifier"),
    ("w34-api-updated-controls.py", "e15a47616b3e"):
        ("func (s *Store) UpsertByIdentifier", "C6: the script scopes this arm to UpsertByIdentifier"),
}

# ── ROWS A HUMAN HAS READ AND CLASSIFIED AS NOT ANCHORS ──────────────────────
# ⚠ THIS LIST EXISTS BECAUSE THE OBVIOUS ALTERNATIVE IS A RULE THAT CANNOT FAIL.
# The tempting way to drop a false positive is "a literal that resolves 0 times
# was probably never an anchor" — which defines away every true finding this
# script exists for. So a non-anchor row is named here, by content hash, with
# the reason a human found by reading it. R3 fails if an entry stops appearing,
# so the list cannot quietly rot into decoration.
#
# Keyed (script, sha256(anchor)[:12]).
NOT_ANCHORS = {
    # EMPTY ON THIS REPOSITORY, AND THAT IS A RESULT RATHER THAN A LIST NOBODY WROTE.
    # Docs carries three entries here. Track's twelve candidates were all read, and
    # every one turned out to be a COLUMN the detector mistook for an anchor (10) or a
    # count taken over the wrong body (2) — defects in the reading, not rows that
    # merely look like anchors. Both are now declared in ROW_SCHEMA and SCOPED, where
    # the fix restores the real anchor to the census instead of excusing a false one.
    # ⚠ R3 is therefore VACUOUS here and control N3 says so out loud rather than
    # letting an empty list read as a checked one.
}

# ── ARMS KNOWN DEAD, EACH WITH WHAT MOVED AND WHEN ───────────────────────────
# ⚠ THIS LIST MAY ONLY SHRINK. R2 fails on an entry whose anchor resolves again,
# so a repair cannot land without deleting its line; R1 fails on any dead arm
# NOT listed, so the next one reddens the build on the commit that kills it.
#
# ⚠⚠ THESE ARE NOT BUGS TO BE PATCHED BY RE-POINTING THE ANCHOR. Re-pointing an
# anchor at moved code yields a control that passes for a NEW REASON unless
# somebody reads the guard and re-proves the must-red first, which needs a real
# Postgres and a `go test` per arm. That work is filed separately as W6.55 (filed as
# W6.51; renumbered by a queue race — see the header).
#
# ⚠⚠⚠ EVERY "KILLED BY" BELOW WAS VERIFIED BY COUNTING THE ANCHOR AT BOTH THE
# COMMIT AND ITS PARENT, never by reading `git log -S`'s output — `-S` reports a
# commit where the occurrence COUNT changed, which is a weaker claim than
# "present here, absent there" and fires on a commit that removed one of two.
# Harness: ~/talyvor-queue/w650-when-died-c3j7.py.
#
# Keyed (script, sha256(anchor)[:12]).
KNOWN_DEAD = {
    ("w34-api-updated-controls.py", "a8f4fd1aed4b"):
        "INERT 9746f13ae 2026-08-10: importer: the Jira API transport imported abandoned work as delivered (#87) (count 1 -> 0, 173 commits ago)",
    ("w34-burndown-ontrack-controls-3d9e.py", "e561e94b166a"):
        "INERT 622bc9948 2026-08-20: analytics+cycle: the burndown day boundary is exclusive, so the final second o (count 1 -> 0, 66 commits ago)",
    ("w34-burndown-ordering-controls-3d9e.py", "0223285a895f"):
        "INERT 622bc9948 2026-08-20: analytics+cycle: the burndown day boundary is exclusive, so the final second o (count 1 -> 0, 66 commits ago)",
    ("w34-burndown-ordering-controls-3d9e.py", "415fc5e3b440"):
        "INERT 622bc9948 2026-08-20: analytics+cycle: the burndown day boundary is exclusive, so the final second o (count 1 -> 0, 66 commits ago)",
    ("w34-burndown-ordering-controls-3d9e.py", "636305778d0c"):
        "INERT 622bc9948 2026-08-20: analytics+cycle: the burndown day boundary is exclusive, so the final second o (count 1 -> 0, 66 commits ago)",
    ("w34-burndown-ordering-controls-3d9e.py", "87a175df7d7b"):
        "INERT 622bc9948 2026-08-20: analytics+cycle: the burndown day boundary is exclusive, so the final second o (count 1 -> 0, 66 commits ago)",
    ("w34-jira-api-resolution-controls.py", "3730e8370c79"):
        "INERT 8b1f39f16 2026-08-10: importer: the resolution that means delivered was reported as unreadable (W3.4 (count 1 -> 0, 164 commits ago)",
    ("w34-jira-csv-created-controls.py", "343b9d8e912a"):
        "INERT b532e563e 2026-08-13: fix(issues): the roadmap's per-milestone counters were structurally zero — no  (count 1 -> 0, 129 commits ago)",
    ("w34-jira-csv-created-controls.py", "579d7f29e367"):
        "INERT b532e563e 2026-08-13: fix(issues): the roadmap's per-milestone counters were structurally zero — no  (count 2 -> 0, 129 commits ago)",
    ("w34-jira-csv-created-controls.py", "d10a0a0d6496"):
        "AMBIGUOUS d3aaacace 2026-08-10: importer: the API transports recorded every imported issue as opened today (#8 (count 1 -> 2, 176 commits ago)",
    ("w34-jira-csv-created-controls.py", "f03f537b7aed"):
        "INERT 666ce7a2a 2026-08-10: importer: a Linear CSV import recorded every issue as opened at import time an (count 1 -> 0, 171 commits ago)",
    ("w34-jira-csv-created-controls.py", "f8536dce1452"):
        "AMBIGUOUS 666ce7a2a 2026-08-10: importer: a Linear CSV import recorded every issue as opened at import time an (count 1 -> 2, 171 commits ago)",
    ("w34-jira-csv-resolution-controls.py", "10f648f2cd09"):
        "INERT 9746f13ae 2026-08-10: importer: the Jira API transport imported abandoned work as delivered (#87) (count 1 -> 0, 173 commits ago)",
    ("w34-jira-csv-resolution-controls.py", "7fa5167790c5"):
        "INERT 8b1f39f16 2026-08-10: importer: the resolution that means delivered was reported as unreadable (W3.4 (count 1 -> 0, 164 commits ago)",
    ("w34-jira-csv-resolution-controls.py", "aa1498223573"):
        "INERT 8b1f39f16 2026-08-10: importer: the resolution that means delivered was reported as unreadable (W3.4 (count 1 -> 0, 164 commits ago)",
    ("w34-jira-csv-resolution-controls.py", "efa68e8d71bb"):
        "INERT 9746f13ae 2026-08-10: importer: the Jira API transport imported abandoned work as delivered (#87) (count 1 -> 0, 173 commits ago)",
    ("w34-jira-csv-updated-controls.py", "9ea4968b975c"):
        "AMBIGUOUS 313ce8094 2026-08-10: importer: the API transports recorded every imported issue as touched at the m (count 1 -> 2, 174 commits ago)",
    ("w34-jira-csv-updated-controls.py", "ed65f6be282a"):
        "AMBIGUOUS 85b3e1721 2026-08-10: importer: a Linear CSV export says when Linear last touched the issue and the  (count 1 -> 2, 160 commits ago)",
    ("w34-linear-csv-dates-controls.py", "1bf26b711aac"):
        "INERT d67f98052 2026-08-11: importer: `Due Date` is in 45 of 45 real Linear exports and this was the one t (count 1 -> 0, 151 commits ago)",
    ("w34-linear-csv-dates-controls.py", "245da2393c29"):
        "INERT 85b3e1721 2026-08-10: importer: a Linear CSV export says when Linear last touched the issue and the  (count 1 -> 0, 160 commits ago)",
    ("w34-linear-csv-dates-controls.py", "9fb64b447745"):
        "INERT 9e7156eab 2026-08-10: importer: a quarter of real Linear CSV exports lose Created and Updated to a d (count 1 -> 0, 152 commits ago)",
    ("w34-update-allowlist-controls-7c4d.py", "b19f92b0eae7"):
        "INERT — ⚠ NEVER APPLICABLE. G1, the arm that asks 'does anything at all watch the gate?' "
        "on internal/issue/store.go. Killed by 4eb06a27a 2026-08-19 'issue: a completion time is "
        "recorded only on a row that is done' — WHICH IS THE COMMIT THAT ADDED THIS SCRIPT. The "
        "anchor is the PRE-FIX text of a line the same commit rewrote, so the arm was inert on "
        "arrival and has never run once in 74 commits. count at birth = 0.",
    # ⚠ THESE TWO WERE INVISIBLE TO THE FIRST VERSION OF THIS GUARD ON THE MACHINE THAT
    # WROTE IT AND VISIBLE IN CI — the census used to spawn subprocesses while reading
    # w39's module level, and whether that script loaded at all depended on an env var.
    # CI's red is what found it. Both anchors are in internal/scoring/store.go.
    ("w39-summary-empty-population-controls-m5x8.py", "1a0376880446"):
        "INERT d677e876a 2026-08-27: the scoring summary counted issues and scores from different populations (count 1 -> 0, 28 commits ago)",
    ("w39-summary-empty-population-controls-m5x8.py", "34e1eec4a154"):
        "INERT d677e876a 2026-08-27: the scoring summary counted issues and scores from different populations (count 1 -> 0, 28 commits ago)",
}


class CensusSideEffect(RuntimeError):
    """A control script tried to DO something while the census was reading it."""


# Callables that mutate the tree, spawn a process, or touch the network. Blocked
# for the duration of every exec, so an attempt becomes a NAMED failure instead of
# a side effect nobody sees.
_BLOCKED = [
    ("subprocess", "run"), ("subprocess", "Popen"), ("subprocess", "call"),
    ("subprocess", "check_call"), ("subprocess", "check_output"), ("subprocess", "getoutput"),
    ("os", "system"), ("os", "popen"), ("os", "remove"), ("os", "unlink"),
    ("os", "rename"), ("os", "replace"), ("os", "rmdir"), ("os", "makedirs"),
    ("shutil", "copy"), ("shutil", "copy2"), ("shutil", "copyfile"),
    ("shutil", "move"), ("shutil", "rmtree"),
    ("pathlib.Path", "write_text"), ("pathlib.Path", "write_bytes"),
    ("pathlib.Path", "unlink"), ("pathlib.Path", "rename"), ("pathlib.Path", "mkdir"),
]


@contextlib.contextmanager
def _no_side_effects():
    """⚠ THIS IS NOT BELT-AND-BRACES. IT CLOSES A HOLE THAT WAS OPEN AND FIRING.

    `module_level_only` keeps `ast.Assign`, and AN ASSIGNMENT'S VALUE CAN BE A CALL.
    `w39-summary-empty-population-controls-m5x8.py:114` is `ok, failed = run()` — a
    module-level assignment that runs the ENTIRE real-Postgres control campaign,
    `subprocess.run(cmd, cwd=ROOT, ...)` and all. MEASURED, not feared: reading that
    one script raised from inside `subprocess.Popen`, and the only reason it did not
    proceed was that TRACK_TEST_DATABASE_URL was unset on the machine that read it.
    CI HAS THAT VARIABLE SET. In a repository whose control scripts mutate a tracked
    Go file and restore it, a census step contracted to do neither could have done
    both — and the docstring that said "nothing in a control script therefore RUNS
    here, and nothing is written" was simply wrong.

    Blocking is better than a safe-call allowlist here because the census NEEDS
    calls: every path constant in these scripts is `os.path.join(...)`. Enumerating
    the safe set is open-ended; enumerating the dangerous set is not, and anything
    missed still lands in a bucket the run prints rather than passing silently.
    """
    import shutil as _shutil, pathlib as _pathlib
    mods = {"subprocess": subprocess, "os": os, "shutil": _shutil,
            "pathlib.Path": _pathlib.Path}
    saved, saved_open = [], builtins.open

    def blocked(label):
        def f(*_a, **_k):
            raise CensusSideEffect("blocked at census read: %s" % label)
        return f

    def guarded_open(file, mode="r", *a, **k):
        if any(c in mode for c in "wxa+"):
            raise CensusSideEffect("blocked at census read: open(%r, %r)" % (file, mode))
        return saved_open(file, mode, *a, **k)

    for modname, attr in _BLOCKED:
        obj = mods[modname]
        if hasattr(obj, attr):
            saved.append((obj, attr, getattr(obj, attr)))
            setattr(obj, attr, blocked("%s.%s" % (modname, attr)))
    builtins.open = guarded_open
    try:
        yield
    finally:
        builtins.open = saved_open
        for obj, attr, orig in saved:
            setattr(obj, attr, orig)


# Dotted prefixes and bare names whose calls only COMPUTE. A module-level assignment
# whose value contains any other call is DROPPED — see module_level_only.
_SAFE_CALL_PREFIXES = ("os.path.", "os.environ.get", "os.getenv", "os.sep", "re.compile",
                       "re.escape", "pathlib.Path", "Path", "textwrap.dedent",
                       "json.loads", "collections.")
_SAFE_CALL_NAMES = {
    "str", "int", "float", "bool", "bytes", "list", "tuple", "set", "frozenset", "dict",
    "sorted", "reversed", "len", "range", "enumerate", "zip", "map", "filter", "sum",
    "min", "max", "abs", "round", "repr", "chr", "ord", "any", "all", "type",
}
_SAFE_METHODS = {
    "join", "split", "rsplit", "splitlines", "strip", "lstrip", "rstrip", "replace",
    "format", "lower", "upper", "title", "encode", "decode", "startswith", "endswith",
    "count", "index", "find", "get", "keys", "values", "items", "copy", "dedent",
    "read_text", "read_bytes", "hexdigest", "group", "groups", "findall", "sub", "match",
    "search", "expanduser", "resolve", "as_posix", "removeprefix", "removesuffix",
}


def _dotted(node):
    """`os.path.join` -> "os.path.join"; `f` -> "f"; anything else -> None."""
    parts = []
    while isinstance(node, ast.Attribute):
        parts.append(node.attr)
        node = node.value
    if isinstance(node, ast.Name):
        parts.append(node.id)
        return ".".join(reversed(parts))
    return ".".join(reversed(parts)) if parts else None


def _assigned_names(node):
    """Every bare name an assignment binds, including tuple targets."""
    out = set()
    targets = node.targets if isinstance(node, ast.Assign) else [node.target]
    for t in targets:
        for sub in ast.walk(t):
            if isinstance(sub, ast.Name):
                out.add(sub.id)
    return out


def _calls_only_compute(node):
    """True if every Call inside this expression is one that merely computes a value."""
    for sub in ast.walk(node):
        if not isinstance(sub, ast.Call):
            continue
        name = _dotted(sub.func)
        if name is None:
            return False
        if name in _SAFE_CALL_NAMES:
            continue
        if any(name.startswith(pre) for pre in _SAFE_CALL_PREFIXES):
            continue
        # a method call: judge by the method name, since the receiver is usually a
        # literal or a value this pass cannot type
        if isinstance(sub.func, ast.Attribute) and sub.func.attr in _SAFE_METHODS:
            continue
        return False
    return True


def module_level_only(src, path):
    """A code object holding ONLY module-level definitions.

    Everything that is not an import, an assignment, a `def` or a `class` is
    dropped — including `if __name__ == "__main__"` and any bare top-level call.

    ⚠ THAT IS NOT THE SAME AS SIDE-EFFECT-FREE, AND THIS DOCSTRING USED TO CLAIM IT
    WAS. An `ast.Assign` is kept and its VALUE may be a call, so `x = run()` at module
    level runs `run()`. See `_no_side_effects`, which is what actually makes the read
    safe; this function only decides what is READ.
    """
    tree = ast.parse(src, filename=path)
    keep, dropped = [], set()
    for n in tree.body:
        if isinstance(n, (ast.Import, ast.ImportFrom, ast.FunctionDef,
                          ast.AsyncFunctionDef, ast.ClassDef)):
            keep.append(n)
        elif isinstance(n, (ast.Assign, ast.AnnAssign)):
            # ⚠ DROPPING MUST BE TRANSITIVE OR IT TRADES A SIDE EFFECT FOR A NameError.
            # MEASURED: dropping `ok, failed = run()` alone left three scripts raising
            # `NameError: name 'before' is not defined` on a LATER assignment that reads
            # it — and a script that raises is a script whose anchors go unchecked, which
            # is the same blindness by a different route. A name defined by a dropped
            # assignment is not defined at all, so anything reading it goes too.
            if n.value is not None and any(
                    isinstance(sub, ast.Name) and sub.id in dropped
                    for sub in ast.walk(n.value)):
                dropped.update(_assigned_names(n))
                continue
            # ⚠ AN ASSIGNMENT IS NOT INERT. `ok, failed = run()` runs the campaign.
            # Dropping the whole script over one such line would lose its anchor table
            # as well, which is why the unsafe ASSIGNMENT is dropped and the rest of
            # the module is still read. `_no_side_effects` is the backstop for anything
            # this static pass judges wrongly; the two floors catch it judging too
            # widely, because over-dropping shows up as coverage falling.
            if n.value is not None and not _calls_only_compute(n.value):
                dropped.update(_assigned_names(n))
                continue
            keep.append(n)
    tree.body = keep
    return compile(tree, path, "exec")


def path_shaped(v):
    """Does this string CLAIM to be a source path? Shape only — never existence.

    ⚠ EXISTENCE IS DELIBERATELY NOT PART OF THIS TEST. Keying on os.path.isfile
    makes a row whose target file was DELETED fail the test, vanish from the
    census, and take its anchors with it — the detector goes quiet exactly when
    something big moved. A missing target is a FINDING (TARGET-MISSING), not a
    reason to stop looking.
    """
    if not (isinstance(v, str) and v and "\n" not in v and len(v) < 400):
        return False
    if not v.endswith(SOURCE_SUFFIXES):
        return False
    # A repo-root target is a BARE filename with no separator — `README.md`,
    # `go.mod`, `docker-compose.yaml`. Requiring a "/" dropped nine real rows in
    # w31-setup-claims-controls.py alone. A prose string that happens to end in a
    # source suffix is excluded instead by having whitespace in it.
    return "/" in v or not any(ch.isspace() for ch in v)


def anchor_sha(s):
    return hashlib.sha256(s.encode("utf-8")).hexdigest()[:12]


def harvest(obj, out, depth=0, seen=None, off=1, declared=False):
    """Find (..., path, old, new, ...) rows anywhere inside a module-level value.

    `off` is how many columns past the path the (old, new) pair starts — 1 by
    default. See ROW_SCHEMA for why it is not always 1 in this repository.

    `declared` is set when ROW_SCHEMA names this script. The `new`-must-be-a-string
    test below exists ONLY to disambiguate a row nobody has read; a declared row has
    been read, and requiring it drops real anchors. MEASURED: with the test kept,
    w34-inert-ownergate-census-7k2p.py's rows are `(label, path, pkg, anchor, True,
    True)` — `new` is a bool, every row is rejected, the script falls out of the
    census entirely and its five live anchors go unchecked. The declaration is the
    disambiguation; asking for it twice loses the rows it was written to recover.
    """
    if seen is None:
        seen = set()
    if depth > 8 or id(obj) in seen:
        return
    seen.add(id(obj))
    if isinstance(obj, dict):
        for v in obj.values():
            harvest(v, out, depth + 1, seen, off, declared)
        return
    if not isinstance(obj, (list, tuple)):
        return
    # The path is NOT always at index 0 — w635 puts the control NAME first and
    # the target second, and keying on index 0 left the one script whose drift
    # created this item unreachable by its own census.
    for i in range(len(obj) - 1 - off):
        if not path_shaped(obj[i]):
            continue
        old, new = obj[i + off], obj[i + off + 1]
        # ⚠ REJECT (path, path, path) ROWS. Without this the census read w635's
        # own tuple of target FILES as an anchor and called it INERT — while
        # that script's independent --check-anchors says all 8 apply. Found by
        # the positive control, not by reading.
        if not (isinstance(old, str) and old and not path_shaped(old)):
            continue
        if not declared and not (isinstance(new, str) and not path_shaped(new)):
            continue
        want = None
        j = i + off + 2
        if len(obj) > j and isinstance(obj[j], int) and not isinstance(obj[j], bool):
            want = obj[j]
        out.append({"path": obj[i], "old": old, "want": want})
        return
    for v in obj:
        harvest(v, out, depth + 1, seen, off, declared)


# ── DETECTOR 2: ANCHORS THAT ARE BARE MODULE-LEVEL CONSTANTS ─────────────────
# 52 of 80 scripts have no (…, path, old, new, …) row for the detector above. They are NOT
# anchorless: they keep the anchor in a standalone module-level constant and bind it to a FILE
# inside a function, so there is no row to read.
#
# ⚠ THE OBVIOUS VERSION OF THIS IS UNUSABLE AND THAT WAS MEASURED, NOT FEARED. Checking every
# anchor-shaped constant against the script's declared targets produced 49 findings over those
# 52 scripts, of which 46 were noise: DSNs, docker image tags, and above all REPLACEMENT values,
# which by construction must NOT occur in the target. W6.43 predicted exactly this — "a control's
# `new` value is a literal that must NOT be present, and it is indistinguishable from a stale
# `old` that is ALSO not present".
#
# ⚠⚠ IT IS INDISTINGUISHABLE BY LITERAL. IT IS NOT INDISTINGUISHABLE BY IDENTIFIER. The scripts
# reference these constants by NAME, so an ast pass can see which ARGUMENT POSITION each name is
# used in — `x.replace(NAME, …)` is an anchor, `x.replace(…, NAME)` is a replacement. Resolving
# one hop of the script's OWN helper functions (`mutate(path, old, new)`, `sub(old, new)`,
# `patch(path, old, new)`) took the 49 down to 3 candidates, and ALL THREE were real: they are
# the W6.43b findings in w31-bodypage-attribution-controls.py and w31-changelog-delete-controls.py.
#
# ⚠⚠⚠ ONLY `anchor`-ROLE CONSTANTS ARE REPORTED, WHICH MEANS THIS DETECTOR DELIBERATELY
# UNDER-REPORTS. A constant used through two hops of indirection, or never passed to a string
# operation this pass understands, has NO observed role and is NOT reported — it is counted and
# printed instead. Under-reporting is the safe direction for a rule that reddens CI; the number
# it cannot classify is the honest part and is on every run.
ANCHOR_METHODS = {"count", "index", "find", "rfind", "startswith", "endswith", "split"}


def _direct_roles(tree):
    roles = {}

    def add(node, role):
        for n in ast.walk(node):
            if isinstance(n, ast.Name):
                roles.setdefault(n.id, set()).add(role)

    for n in ast.walk(tree):
        if isinstance(n, ast.Call) and isinstance(n.func, ast.Attribute):
            if n.func.attr == "replace":
                if len(n.args) >= 1:
                    add(n.args[0], "anchor")
                if len(n.args) >= 2:
                    add(n.args[1], "replacement")
            elif n.func.attr in ANCHOR_METHODS and n.args:
                add(n.args[0], "anchor")
        if isinstance(n, ast.Compare) and isinstance(n.left, ast.Name):
            for op in n.ops:
                if isinstance(op, (ast.In, ast.NotIn)):
                    roles.setdefault(n.left.id, set()).add("anchor")
    return roles


def _roles(tree):
    """name -> observed roles, resolving ONE hop of locally-defined helper.

    One hop, deliberately: a constant reached through two is left with no observed
    role and therefore unreported, which is the same choice talyvor-suite's
    `_writing_functions` made and for the same reason — guess less, count what
    you could not decide.
    """
    roles = {k: set(v) for k, v in _direct_roles(tree).items()}
    sigs = {}
    for fn in [n for n in ast.walk(tree) if isinstance(n, (ast.FunctionDef, ast.AsyncFunctionDef))]:
        inner = _direct_roles(fn)
        m = {}
        for i, a in enumerate(fn.args.args):
            r = inner.get(a.arg, set())
            if len(r) == 1:
                m[i] = next(iter(r))
        if m:
            sigs[fn.name] = m
    for n in ast.walk(tree):
        if isinstance(n, ast.Call) and isinstance(n.func, ast.Name) and n.func.id in sigs:
            for i, arg in enumerate(n.args):
                if i in sigs[n.func.id]:
                    for q in ast.walk(arg):
                        if isinstance(q, ast.Name):
                            roles.setdefault(q.id, set()).add(sigs[n.func.id][i])
    return roles


def _anchor_shaped(v):
    if not isinstance(v, str) or path_shaped(v) or not (12 <= len(v) <= 4000):
        return False
    return any(c in v for c in "(){}=;:") or "\n" in v


def bare_census(name, ns, src):
    """Findings, anchors-considered, and the count this pass could NOT classify."""
    paths, cands = [], []
    for k, v in list(ns.items()):
        if k.startswith("__"):
            continue
        if path_shaped(v):
            paths.append(v)
        elif _anchor_shaped(v):
            cands.append((k, v))
    targets = []
    for pv in paths:
        t = pv if os.path.isabs(pv) else os.path.join(REPO, pv)
        if os.path.isfile(t):
            targets.append((os.path.relpath(t, REPO), open(t, encoding="utf-8").read()))
    if not targets or not cands:
        return [], 0, 0
    roles = _roles(ast.parse(src))
    rows, considered, unclassified = [], 0, 0
    for k, av in cands:
        if "anchor" not in roles.get(k, set()):
            unclassified += 1
            continue
        considered += 1
        total = sum(tx.count(av) for _, tx in targets)
        if total == 0:
            rows.append({"script": name, "path": " | ".join(t for t, _ in targets),
                         "want": None, "old": av, "sha": anchor_sha(av),
                         "count": 0, "verdict": "INERT", "bare": True})
    return rows, considered, unclassified


def why_unreached(src, ns):
    """A per-script REASON, because two opposite outcomes look identical from outside.

    A script with NO anchor at all and a script whose anchor cannot be REACHED are
    both "unreached", and they are opposite findings: the first is honest (there is
    nothing to check), the second is a blind spot. A bucket count cannot tell them
    apart, so this names which one each script is and a human can check the answer.
    """
    tree = ast.parse(src)
    mutates = any(isinstance(n, ast.Call) and isinstance(n.func, ast.Attribute)
                  and n.func.attr in ("replace", "write_text", "write_bytes", "write")
                  for n in ast.walk(tree))
    if not mutates:
        return "NOT-A-MUTATOR — no replace and no write anywhere; not a control script"
    paths = sum(1 for k, v in list(ns.items()) if not k.startswith("__") and path_shaped(v))
    anchors = sum(1 for k, v in list(ns.items()) if not k.startswith("__") and _anchor_shaped(v))
    if paths == 0:
        return "NO-MODULE-LEVEL-TARGET — the file it edits is built inside a function"
    if anchors == 0:
        return "NO-MODULE-LEVEL-ANCHOR — the anchor text is built inside a function"
    return "ANCHORS-BUT-NO-ROLE — %d constant(s) whose role is 2+ hops away" % anchors


def census(scripts_dir=SCRIPTS):
    results, unreached = [], []
    bare_stats = [0, 0, 0]  # scripts, anchors considered, constants unclassified
    names = sorted(n for n in os.listdir(scripts_dir) if n.endswith(".py"))
    for name in names:
        path = os.path.join(scripts_dir, name)
        if os.path.samefile(path, os.path.abspath(__file__)):
            continue
        # ⚠ THE NAMESPACE IS A REAL MODULE REGISTERED IN sys.modules, AND THAT IS NOT TIDINESS.
        # `@dataclass` resolves its own field types through `sys.modules[cls.__module__].__dict__`,
        # so exec'ing into a bare dict under a synthetic __name__ raises
        # `AttributeError: 'NoneType' object has no attribute '__dict__'` — and the script lands
        # in the unreached bucket, indistinguishable from one that simply has no anchor table.
        # MEASURED: w31-tenancypredicate-census-5r8k.py was invisible to this guard for exactly
        # that reason, and the guard reported it as unreached rather than as a failure it caused.
        modname = "__anchor_census_%s__" % re.sub(r"\W", "_", name)
        mod = types.ModuleType(modname)
        mod.__file__ = os.path.abspath(path)
        sys.modules[modname] = mod
        ns = mod.__dict__
        try:
            code = module_level_only(open(path, encoding="utf-8").read(), path)
            buf = io.StringIO()
            with contextlib.redirect_stdout(buf), contextlib.redirect_stderr(buf), \
                    _no_side_effects():
                exec(code, ns)
        except Exception as exc:
            sys.modules.pop(modname, None)
            unreached.append((name, "EXEC-FAILED: %s: %s" % (type(exc).__name__, exc)))
            continue
        finally:
            sys.modules.pop(modname, None)
        rows = []
        off = ROW_SCHEMA.get(name, (1, ""))[0]
        for key, val in list(ns.items()):
            if not key.startswith("__"):
                harvest(val, rows, off=off, declared=name in ROW_SCHEMA)
        seen, uniq = set(), []
        for r in rows:
            k = (r["path"], r["old"], r["want"])
            if k not in seen:
                seen.add(k)
                uniq.append(r)
        if not uniq:
            src = open(path, encoding="utf-8").read()
            rows, considered, unclassified = bare_census(name, ns, src)
            if considered:
                bare_stats[0] += 1
                bare_stats[1] += considered
            bare_stats[2] += unclassified
            if rows:
                results.extend(rows)
            if not considered:
                unreached.append((name, why_unreached(src, ns)))
            continue
        for r in uniq:
            target = r["path"] if os.path.isabs(r["path"]) else os.path.join(REPO, r["path"])
            rec = {"script": name, "path": os.path.relpath(target, REPO), "want": r["want"],
                   "old": r["old"], "sha": anchor_sha(r["old"])}
            if not os.path.isfile(target):
                rec.update(count=None, verdict="TARGET-MISSING")
            else:
                text = open(target, encoding="utf-8").read()
                marker = SCOPED.get((name, rec["sha"]), (None, ""))[0]
                if marker is not None:
                    at = text.find(marker)
                    if at < 0:
                        rec.update(count=None, verdict="SCOPE-MARKER-MISSING")
                        results.append(rec)
                        continue
                    text = text[at:]
                    rec["scoped"] = marker
                n = text.count(r["old"])
                rec["count"] = n
                if r["want"] is None:
                    rec["verdict"] = "INERT" if n == 0 else ("ok" if n == 1 else "AMBIGUOUS")
                else:
                    rec["verdict"] = "ok" if n == r["want"] else ("INERT" if n == 0 else "DRIFTED")
            results.append(rec)
    return results, unreached, len(names) - 1, bare_stats  # -1: this file is not a control script


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--ci", action="store_true", help="exit non-zero on any rule violation")
    ap.add_argument("--list", action="store_true", help="print every anchor, not only the failures")
    args = ap.parse_args()

    results, unreached, population, bare = census()
    scripts_reached = len(set(r["script"] for r in results))
    keyed = {(r["script"], r["sha"]): r for r in results}

    bad = [r for r in results if r["verdict"] != "ok"]
    failures = []

    # R1 — a control arm that can no longer be APPLIED and nobody has accounted for.
    unaccounted = [r for r in bad
                   if (r["script"], r["sha"]) not in NOT_ANCHORS
                   and (r["script"], r["sha"]) not in KNOWN_DEAD]
    if unaccounted:
        failures.append("R1: %d control arm(s) can no longer be APPLIED and are not accounted for"
                        % len(unaccounted))

    # R2 — the known-dead list may only SHRINK.
    for key, why in sorted(KNOWN_DEAD.items()):
        row = keyed.get(key)
        if row is None:
            failures.append("R2: KNOWN_DEAD entry %s/%s no longer appears in the census — the "
                            "entry is stale, or the detector narrowed" % key)
        elif row["verdict"] == "ok":
            failures.append("R2: KNOWN_DEAD entry %s/%s now RESOLVES (%s) — the arm was repaired, "
                            "so delete its line; this list may only shrink" % (key[0], key[1], why))

    # R3 — an allowlist entry that has stopped appearing is stale, not satisfied.
    for key in sorted(NOT_ANCHORS):
        if key not in keyed:
            failures.append("R3: NOT_ANCHORS entry %s/%s no longer appears in the census — the "
                            "entry is stale, or the detector narrowed" % key)

    # R8 — a ROW_SCHEMA entry whose script yields no rows is stale, not satisfied.
    # ⚠ WITHOUT THIS THE MAP IS THE ONE THING HERE THAT CAN ROT SILENTLY IN THE
    # FLATTERING DIRECTION: a schema pointing at a column that no longer exists makes
    # the script produce nothing, and a script producing nothing is INVISIBLE to R1.
    # The census would then report a clean bill on a script it stopped reading.
    for script, (off, why) in sorted(ROW_SCHEMA.items()):
        if not any(r["script"] == script for r in results):
            failures.append("R8: ROW_SCHEMA entry %s (off=%d, %s) yields no rows — the script "
                            "changed shape, or the offset is wrong; either way its anchors are "
                            "no longer being checked" % (script, off, why))

    # R9 — a SCOPED entry that has stopped appearing is stale, same argument as R3.
    for key in sorted(SCOPED):
        if key not in keyed:
            failures.append("R9: SCOPED entry %s/%s no longer appears in the census — the entry "
                            "is stale, or the detector narrowed" % key)

    # R4/R5 — one floor per detector stage.
    if scripts_reached < SCRIPTS_FLOOR:
        failures.append("R4: the exec stage reached %d scripts, floor is %d — the detector narrowed"
                        % (scripts_reached, SCRIPTS_FLOOR))
    if len(results) < ANCHORS_FLOOR:
        failures.append("R5: the harvest stage found %d anchors, floor is %d — the detector narrowed"
                        % (len(results), ANCHORS_FLOOR))
    if bare[0] < BARE_SCRIPTS_FLOOR:
        failures.append("R6: the bare-constant detector reached %d scripts, floor is %d — it narrowed"
                        % (bare[0], BARE_SCRIPTS_FLOOR))
    if bare[1] < BARE_ANCHORS_FLOOR:
        failures.append("R7: the bare-constant detector classified %d constants as anchors, floor "
                        "is %d — the role pass narrowed" % (bare[1], BARE_ANCHORS_FLOOR))

    print("control-anchors: %d scripts in population, %d reached, %d NOT reached, "
          "%d anchors checked, %d not ok"
          % (population, scripts_reached, len(unreached), len(results), len(bad)))
    print("control-anchors: bare-constant detector: %d more scripts, %d constants classified as "
          "anchors, %d constants it could NOT classify and therefore did not report"
          % (bare[0], bare[1], bare[2]))
    tally = {}
    for _, why in unreached:
        tally[why.split(" —")[0]] = tally.get(why.split(" —")[0], 0) + 1
    print("control-anchors: why the %d unreached are unreached: %s"
          % (len(unreached), ", ".join("%s=%d" % kv for kv in sorted(tally.items()))))

    if args.list:
        for r in results:
            print("  %-8s %-44s %-34s count=%s want=%s" %
                  (r["verdict"], r["script"], r["path"], r["count"], r["want"]))

    for r in unaccounted:
        print("\n  UNACCOUNTED %s" % r["verdict"])
        print("    script : %s" % r["script"])
        print("    target : %s   count=%s want=%s" % (r["path"], r["count"], r["want"]))
        print("    anchor : %r" % (r["old"][:100],))
        print("    → the control cannot be APPLIED. Read the guard it exercises before "
              "re-pointing it: an anchor moved to new code makes a control that passes for a "
              "NEW REASON. If it is genuinely dead, add it to KNOWN_DEAD with what moved and when.")

    for f in failures:
        print("\ncontrol-anchors: %s" % f)

    if failures:
        print("\ncontrol-anchors: FAILED")
        return 1 if args.ci else 0
    # ⚠ ANCHORS, NOT ARMS. The census dedups by (path, anchor, want), so several arms
    # resting on ONE anchor are one row here — w34-report-ordering's ten arms rest on
    # five anchors. At 181fdee the 22 known-dead anchors carry 24 arms.
    print("control-anchors: ok (%d anchor(s) accounted for as known-dead, %d row(s) as "
          "non-anchors, %d script(s) with a declared row schema, %d scoped count(s))"
          % (len(KNOWN_DEAD), len(NOT_ANCHORS), len(ROW_SCHEMA), len(SCOPED)))
    return 0


if __name__ == "__main__":
    sys.exit(main())
