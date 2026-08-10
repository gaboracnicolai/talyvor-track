#!/usr/bin/env python3
"""w34-linear-csv-updated-controls.py — positive controls for the linear_csv `Updated` merge.

WHAT A CONTROL HAS TO DO HERE. Every guard this change adds passed on its FIRST run once the fix
was in, which is exactly the state that has shipped three unfallible guards in this fleet. So each
mutation below names, IN ADVANCE, the test that must catch it. A mutation caught by a DIFFERENT
test than predicted is reported as a WRONG PREDICTION and KEPT WRONG in this file — the prediction
is the falsifiable claim, not the catch.

THE RUNNER IS ADAPTED FROM scripts/w34-linear-csv-issue-id-controls.py (#99) AND TWO OF ITS
MECHANISMS ARE FIXED HERE RATHER THAN INHERITED. Copying a harness copies its blind spots, so both
are named:

  1. MULTI-EDIT WAS APPLYING ONLY THE LAST EDIT PER FILE. Its apply_control rebuilt each plan from
     the SAVED bytes, so two edits in one file produced two candidate bodies and the second write
     erased the first. Every control in #99 happened to touch one anchor per file, so it never
     fired — but C1 here needs two edits in csv.go, and a half-applied revert would have reported a
     working guard as blind. Edits are now folded per file, in order.
  2. RESTORE RAN AFTER THE TEST CALL, NOT IN A `finally`. An exception between mutate and restore
     left the mutation on disk and skipped the closing sha256 check. Restore is now in a `finally`.

THE LESSONS THE REST OF IT IS BUILT AROUND, each paid for in this repo or its siblings:
  · a build failure is NOT a catch — it proves the file moved, not that the product was wrong,
    so it is scored `BUILD-BROKEN` and the control is void
  · a test that never RAN is not a test that passed — a Go test binary is per PACKAGE, so one
    panicking test takes every later test down with it and absence from the failure list is not
    green. Verdicts read `--- FAIL:` lines out of `go test -v` and print the assertion MESSAGE.
  · every anchor is asserted UNIQUE before ANY write, and every write is verified to have CHANGED
    THE BYTES on disk — a control that silently matched nothing reads exactly like a dead guard
  · files are restored from SAVED BYTES and sha256-compared, never from git
  · NOT CAUGHT must be REACHABLE, or CAUGHT means nothing: C7 is an inverted control whose
    prediction IS "not caught", and if it ever reports CAUGHT this harness is broken

Requires TRACK_TEST_DATABASE_URL and a real Postgres. Run from the repo root.
"""
import hashlib
import os
import re
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CSV_GO = os.path.join(ROOT, "internal/importer/csv.go")
COL_GO = os.path.join(ROOT, "internal/importer/linear_csv_updated.go")
STORE_GO = os.path.join(ROOT, "internal/issue/store.go")

PKG = "./internal/importer/"

# ── the anchors, matched EXACTLY and asserted unique before any write ────────

# ⚠ `UpdatedAt:   updated,` ALONE IS NOT UNIQUE — jiraRowMapper has carried the identical line
# since #85. The anchor is the whole tail of linearRowMapper's literal, which is unique because
# jiraRowMapper's carries DueDate and a different notes list.
MAPPER_TAIL = """			CompletedAt: completed,
			CreatedAt:   created,
			UpdatedAt:   updated,
		},
		notes: append(collectNotes(rawStatus, status, statusOK, statusFallback{}, rawPrio, prio, prioOK),
			concatNotes(createdNotes, completedNotes, updatedNotes)...),
"""
MAPPER_TAIL_REVERTED = """			CompletedAt: completed,
			CreatedAt:   created,
		},
		notes: append(collectNotes(rawStatus, status, statusOK, statusFallback{}, rawPrio, prio, prioOK),
			concatNotes(createdNotes, completedNotes)...),
"""
MAPPER_CALL = "\tupdated, updatedNotes := linearCSVUpdated(ci, row)\n"

CONST_LINE = 'const linearCSVUpdatedColumn = "Updated"'

PARSE_CALL = "\tt, ok := parseLinearCSVTime(raw)\n"

NO_COLUMN_BRANCH = """	if len(ci[strings.ToLower(linearCSVUpdatedColumn)]) == 0 {
		return time.Time{}, []FieldNote{{Field: fieldUpdated, Via: viaNoLinearUpdatedColumn}}
	}
"""

UNPARSEABLE_REPORT = """	if !ok {
		return time.Time{}, []FieldNote{{Field: fieldUpdated, Value: raw, Via: viaUnparseableDate}}
	}
"""

# ⚠ THE UPSERT ARM, NOT THE CREATE ARM. The gate text is byte-identical in both, so the anchor
# carries the comment line that precedes only the upsert copy. linear_csv rows carry `ID` since
# #99, so they route through UpsertByIdentifier — which is the arm that has to deliver the value.
STORE_UPSERT_GATE = """	// RE-import still stamps NOW() on every row it touches. Left in the queue with numbers.
	var updatedAt *time.Time
	if !issue.UpdatedAt.IsZero() && issue.CreatorID == model.ImporterCreatorID {
"""
STORE_UPSERT_GATE_DISARMED = """	// RE-import still stamps NOW() on every row it touches. Left in the queue with numbers.
	var updatedAt *time.Time
	if false && !issue.UpdatedAt.IsZero() && issue.CreatorID == model.ImporterCreatorID {
"""

# ── the guards this change adds, by name ────────────────────────────────────
G_JOB_COLUMN = "TestJobRow_LinearCSV_ImportedIssueKeepsTheDateLinearLastUpdatedIt"
G_JOB_ORDER = "TestJobRow_LinearCSV_AStaleImportDoesNotOutrankTodaysWork"
G_PARSER = "TestLinearCSVUpdated_Rule1_ParsesWithTheLinearListAndPinsNoLayoutOfItsOwn"
G_WIRED = "TestLinearCSVUpdated_Rule1_TheMapperIsWiredIntoLinearRowMapper"
G_BYTES = "TestLinearCSVUpdated_Rule2_TheMeasuredBytes"
G_REFUSED = "TestLinearCSVUpdated_Rule2_TheMeasuredShapeTheLayoutsRefuse"
G_SPELLING = "TestLinearCSVUpdated_TheColumnSpellingIsPinned"
G_OUTCOMES = "TestLinearCSVUpdated_TheFourOutcomes"
G_ABSENCES = "TestLinearCSVUpdated_TheTwoAbsencesRenderDifferently"

ALL_MINE = {G_JOB_COLUMN, G_JOB_ORDER, G_PARSER, G_WIRED, G_BYTES, G_REFUSED,
            G_SPELLING, G_OUTCOMES, G_ABSENCES}


class Edit:
    def __init__(self, path, old, new):
        self.path, self.old, self.new = path, old, new


CONTROLS = [
    dict(
        name="C1  revert the fix — linearRowMapper stops reading Updated",
        why="THE RED-FIRST RUN, re-run as a control. Two edits in ONE file (the call and the "
            "struct/notes tail), which is exactly the case #99's harness would have half-applied. "
            "Deleting only the assignment would leave `updated` declared-and-unused and score "
            "BUILD-BROKEN, which is a fact about Go and not about the product.",
        edits=[
            Edit(CSV_GO, MAPPER_TAIL, MAPPER_TAIL_REVERTED),
            Edit(CSV_GO, MAPPER_CALL, ""),
        ],
        predict={G_JOB_COLUMN, G_JOB_ORDER, G_WIRED},
        expect_caught=True,
    ),
    dict(
        name="C2  parse the Linear cell with the JIRA layout list",
        why="#75's overclaim as a mutation: a Linear export parsed with layouts pinned from a real "
            "JIRA's bytes. It is the control that justifies the Rule-1 absence assertion existing "
            "at all — without it, 'does not call parseJiraCSVTime' is a sentence nobody has "
            "watched fail. G_REFUSED must STAY GREEN: it calls parseLinearCSVTime directly, so a "
            "change at the CALL SITE must not move it. "
            "⚠⚠ THE PREDICTION BELOW IS WRONG AND IS KEPT WRONG, AND IT IS THE TRANSFERABLE HALF "
            "OF THIS RUN. G_BYTES was predicted to catch this and did NOT: it calls "
            "parseLinearCSVTime DIRECTLY, so it is a fact about the PARSER and is structurally "
            "blind to which parser the MAPPER reaches for. The same reasoning applies to G_REFUSED, "
            "which is why that one is listed as must-stay-green rather than as a catcher — the two "
            "assertions look alike and only one of them was reasoned about correctly. What can see "
            "the substitution is G_PARSER (it reads the shipped source) and G_OUTCOMES (it goes "
            "THROUGH the mapper). A guard that calls the helper cannot police the call site.",
        edits=[Edit(COL_GO, PARSE_CALL, "\tt, ok := parseJiraCSVTime(raw)\n")],
        predict={G_PARSER, G_BYTES, G_OUTCOMES, G_JOB_COLUMN, G_JOB_ORDER},  # G_BYTES: WRONG, kept
        must_stay_green={G_REFUSED, G_SPELLING, G_ABSENCES},
        expect_caught=True,
    ),
    dict(
        name="C3  the measured column spelling is changed",
        why="A rename that sends every real export down the no-column branch. The failure is "
            "SILENT in the product — updated_at is DEFAULT NOW(), so the rows still look "
            "populated — which is why the spelling is pinned as a hand-transcribed literal rather "
            "than derived from the source.",
        edits=[Edit(COL_GO, CONST_LINE, 'const linearCSVUpdatedColumn = "Last Updated"')],
        predict={G_SPELLING, G_OUTCOMES, G_JOB_COLUMN, G_JOB_ORDER},
        must_stay_green={G_PARSER, G_BYTES, G_REFUSED},
        expect_caught=True,
    ),
    dict(
        name="C4  the no-column branch is deleted (a missing header reports as an empty cell)",
        why="THE CONTROL THAT JUSTIFIES KEEPING TWO Via CONSTANTS. With this branch gone an export "
            "with NO Updated column reports 'empty last-updated time' — an operator is told their "
            "rows had blank cells when in fact Track never looked. Nothing else changes: the job "
            "fixture carries the column, so both end-to-end tests MUST stay green, and if they go "
            "red this control is measuring something other than what it claims.",
        edits=[Edit(COL_GO, NO_COLUMN_BRANCH, "")],
        predict={G_OUTCOMES},
        must_stay_green={G_JOB_COLUMN, G_JOB_ORDER, G_SPELLING, G_BYTES},
        expect_caught=True,
    ),
    dict(
        name="C5  an unparseable value is silently dropped instead of reported",
        why="THE FAIL-SAFE IS THE LOAD-BEARING PART OF A HAND-PINNED LAYOUT LIST — 25.3% of the "
            "measured corpus lands on this branch. Silently dropping is the difference between a "
            "tenant learning on its first import and a tenant receiving a column of import-instant "
            "timestamps that reads as a working import.",
        edits=[Edit(COL_GO, UNPARSEABLE_REPORT, "\tif !ok {\n\t\treturn time.Time{}, nil\n\t}\n")],
        predict={G_OUTCOMES},
        must_stay_green={G_JOB_COLUMN, G_JOB_ORDER, G_REFUSED, G_BYTES},
        expect_caught=True,
    ),
    dict(
        name="C6  the mapper keeps reading it and the STORE stops delivering it",
        why="THE CONTROL THE TWO JOB TESTS EXIST FOR. Every source-level and unit-level assertion "
            "in this change stays true — the column is read, parsed, wired and reported — while "
            "the value never reaches the database. A change set held only by mapper tests would be "
            "green here and the product would be exactly as broken as it was before the merge. "
            "The gate is disarmed on the UPSERT arm, the one a keyed Linear CSV row takes.",
        edits=[Edit(STORE_GO, STORE_UPSERT_GATE, STORE_UPSERT_GATE_DISARMED)],
        predict={G_JOB_COLUMN, G_JOB_ORDER},
        must_stay_green={G_PARSER, G_WIRED, G_BYTES, G_REFUSED, G_SPELLING, G_OUTCOMES, G_ABSENCES},
        expect_caught=True,
    ),
    dict(
        name="C8  a spurious note on the SUCCESS path (the guard this merge edited must still fire)",
        why="THIS MERGE EDITED SOMEBODY ELSE'S GUARD AND THIS IS THE CONTROL THAT SAYS IT STILL "
            "WORKS. TestLinearCSV_AFullyReadableRowAddsNoWarning (#89) went red on the baseline "
            "because its fixture is the nine-column IMPORT header, which carries no `Updated`; its "
            "header was replaced with one that gives the mapper every column it reads. Editing a "
            "test to make your own change pass is how a guard gets blinded, so: a mapper that "
            "reports something on a row where NOTHING failed must still turn it red. If this "
            "control comes back NOT CAUGHT, that test was blinded rather than corrected.",
        edits=[Edit(COL_GO, "\treturn t, nil\n",
                    '\treturn t, []FieldNote{{Field: fieldUpdated, Value: raw, Via: viaUnparseableDate}}\n')],
        predict={G_OUTCOMES},
        must_stay_green={G_JOB_COLUMN, G_JOB_ORDER, G_SPELLING, G_BYTES, G_REFUSED},
        also_expect_red={"TestLinearCSV_AFullyReadableRowAddsNoWarning"},
        expect_caught=True,
    ),
    dict(
        name="C7  INVERTED — a redundant .UTC() on the mapper's return",
        why="PREDICTED NOT CAUGHT, AND THIS IS THE ROW THAT MAKES EVERY `CAUGHT` ABOVE MEAN "
            "SOMETHING. parseLinearCSVTime already returns t.UTC(), so this edit changes real "
            "bytes on disk and cannot change one observable value. A harness that reports CAUGHT "
            "for every mutation it is given has measured nothing. ⚠ #99's C7 lesson is why the "
            "edit is a NO-OP AT THE CALL SITE rather than a spelling change: 'behaviourally inert' "
            "is a claim about the PRODUCT and does not transfer to a guard whose job is to be a "
            "fact about bytes — a constant's spelling is pinned on purpose, a variable's journey "
            "to UTC is not.",
        edits=[Edit(COL_GO, "\treturn t, nil\n", "\treturn t.UTC(), nil\n")],
        predict=set(),
        expect_caught=False,
    ),
]


def sha(b):
    return hashlib.sha256(b).hexdigest()


def read(path):
    with open(path, "rb") as f:
        return f.read()


def run_tests():
    """Returns (build_ok, failing_test_names, message_lines)."""
    p = subprocess.run(
        ["go", "test", "-timeout", "300s", "-count=1", "-v", PKG],
        cwd=ROOT, capture_output=True, text=True, env=dict(os.environ))
    out = p.stdout + p.stderr
    # A build/vet failure is NOT a catch. Detect it before reading any verdict.
    if ("[build failed]" in out or "cannot use" in out or "undefined:" in out
            or "syntax error" in out or "declared and not used" in out):
        return False, set(), [l for l in out.splitlines() if l.strip()][:14]
    failing = set(re.findall(r"^\s*--- FAIL: (\S+)", out, re.M))
    ran = set(re.findall(r"^=== RUN\s+(\S+)", out, re.M))
    msgs = [l.rstrip() for l in out.splitlines()
            if re.match(r"^\s{4,}\S+_test\.go:\d+:", l)]
    # ⚠ A subtest failure prints as `--- FAIL: Parent/sub`. Fold to the parent so a prediction
    # naming the top-level test is comparable with what actually failed.
    failing = {f.split("/")[0] for f in failing}
    return True, failing, (msgs[:10] if msgs else [f"(no assertion messages; {len(ran)} tests ran)"])


def apply_control(ctrl, saved):
    """Assert EVERY anchor unique BEFORE any write, then write. Returns None or an error string.

    ⚠ EDITS ARE FOLDED PER FILE, IN ORDER. Rebuilding each plan from the SAVED bytes — what #99's
    harness did — makes the last edit in a file erase the earlier ones.
    """
    bodies = {p: b.decode() for p, b in saved.items()}
    for e in ctrl["edits"]:
        n = bodies[e.path].count(e.old)
        if n != 1:
            return f"ANCHOR NOT UNIQUE in {os.path.basename(e.path)}: {n} occurrences"
        bodies[e.path] = bodies[e.path].replace(e.old, e.new, 1)
    touched = [e.path for e in ctrl["edits"]]
    for path in dict.fromkeys(touched):
        with open(path, "w") as f:
            f.write(bodies[path])
        if read(path) == saved[path]:
            return f"WRITE CHANGED NOTHING in {os.path.basename(path)}"
    return None


def restore(saved):
    bad = []
    for path, body in saved.items():
        with open(path, "wb") as f:
            f.write(body)
        if sha(read(path)) != sha(body):
            bad.append(path)
    return bad


def main():
    if not os.environ.get("TRACK_TEST_DATABASE_URL"):
        print("TRACK_TEST_DATABASE_URL unset — the job controls need real Postgres. Refusing to run.")
        return 2

    saved = {p: read(p) for p in (CSV_GO, COL_GO, STORE_GO)}
    print("SAVED BYTES:")
    for p, b in saved.items():
        print(f"  {os.path.basename(p):<28} {len(b):>7} bytes  sha256 {sha(b)[:16]}")

    print("\nBASELINE (no mutation) — must be GREEN, or every verdict below is meaningless")
    ok, failing, msgs = run_tests()
    if not ok or failing:
        print(f"  BASELINE BROKEN: build_ok={ok} failing={sorted(failing)}")
        for m in msgs:
            print("   ", m)
        restore(saved)
        return 2
    print("  baseline green")

    results = []
    for ctrl in CONTROLS:
        print("\n" + "=" * 78)
        print(ctrl["name"])
        print("  WHY: " + ctrl["why"])
        print(f"  PREDICTED CATCHER(S): {sorted(ctrl['predict']) or 'NONE (inverted control)'}")

        err = apply_control(ctrl, saved)
        if err:
            print(f"  CONTROL VOID — {err}")
            results.append((ctrl["name"], "VOID"))
            restore(saved)
            continue

        # ⚠ RESTORE IN A `finally`. An exception here used to leave the mutation on disk and skip
        # the closing sha256 check entirely.
        try:
            ok, failing, msgs = run_tests()
        finally:
            restore_bad = restore(saved)
        if restore_bad:
            print(f"  ⚠ RESTORE FAILED for {restore_bad} — STOPPING")
            return 2

        if not ok:
            print("  BUILD-BROKEN — scored as NOT a catch. The edit proved the file moved, not "
                  "that the product was wrong.")
            for m in msgs:
                print("   ", m)
            results.append((ctrl["name"], "VOID (build)"))
            continue

        mine = failing & ALL_MINE
        others = sorted(failing - ALL_MINE)
        print(f"  OBSERVED FAIL (this change's guards): {sorted(mine) or 'NONE'}")
        if others:
            print(f"  CONTEXT — other tests also red: {others[:6]}{' …' if len(others) > 6 else ''}")
        for m in msgs[:4]:
            print("   ", m)

        if not ctrl["expect_caught"]:
            verdict = "NOT CAUGHT (as predicted)" if not failing else f"UNEXPECTED CATCH {sorted(failing)}"
        elif not mine:
            verdict = "NOT CAUGHT — THE GUARD IS BLIND"
        elif mine == ctrl["predict"]:
            verdict = "CAUGHT as predicted"
        else:
            verdict = (f"WRONG PREDICTION — extra {sorted(mine - ctrl['predict'])}, "
                       f"missing {sorted(ctrl['predict'] - mine)}")

        # A control may name a test OUTSIDE this change's own guard set that must also go red —
        # the only way to prove an edited pre-existing guard still speaks.
        if ctrl.get("also_expect_red"):
            silent = ctrl["also_expect_red"] - failing
            print(f"  ALSO-EXPECT-RED: {'fired' if not silent else 'SILENT — ' + str(sorted(silent))}")
            if silent:
                verdict += f" | EDITED GUARD DID NOT FIRE {sorted(silent)}"

        if ctrl.get("must_stay_green"):
            leaked = ctrl["must_stay_green"] & failing
            print(f"  MUST-STAY-GREEN: {'held' if not leaked else 'VIOLATED by ' + str(sorted(leaked))}")
            if leaked:
                verdict += f" | MUST-STAY-GREEN VIOLATED {sorted(leaked)}"

        print(f"  VERDICT: {verdict}")
        results.append((ctrl["name"], verdict))

    print("\n" + "=" * 78)
    print("SUMMARY")
    for name, verdict in results:
        print(f"  {name.split()[0]:<4} {verdict}")

    print("\nFINAL TREE CHECK (restored from saved bytes, sha256):")
    clean = True
    for p, b in saved.items():
        now = sha(read(p))
        same = now == sha(b)
        clean = clean and same
        print(f"  {os.path.basename(p):<28} {'IDENTICAL' if same else 'DRIFTED'}  {now[:16]}")
    return 0 if clean else 2


if __name__ == "__main__":
    sys.exit(main())
