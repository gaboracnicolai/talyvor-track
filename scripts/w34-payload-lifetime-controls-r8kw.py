#!/usr/bin/env python3
"""w34-payload-lifetime-controls-r8kw.py — positive controls for payload_lifetime_test.go.

WHY THIS EXISTS. All four tests in internal/importer/payload_lifetime_test.go PASSED ON THEIR FIRST
RUN. A test that has never been seen to fail is not evidence; three sessions in this queue shipped
guards that could not fail and every one was caught only by a control. So each assertion here is
required to go RED for a mutation that names it FIRST, and one mutation is required to leave them all
GREEN so that "CAUGHT" is not a catch-all.

DISCIPLINE
  · one mutation at a time, restored in a `finally`, sha256-verified identical afterwards
  · the predicted catcher is written down BEFORE the run and the run is scored against it
  · the harness REFUSES to score if the tree is dirty at the start, if a mutation changes zero
    bytes (a control that edits nothing reports NOT CAUGHT for a defect it never introduced),
    or if TRACK_TEST_DATABASE_URL is unset — rather than scoring itself green over skipped tests
  · C4 mutates the TEST rather than the product on purpose: an absence-census that cannot see
    anything must REFUSE, not pass, and that is the one property no product mutation can show.

Usage:  TRACK_TEST_DATABASE_URL=... python3 scripts/w34-payload-lifetime-controls-r8kw.py
"""

import hashlib
import os
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

TESTFILE = "internal/importer/payload_lifetime_test.go"
JOBS = "internal/importer/jobs.go"
CORE = "migrations/0001_core.sql"
IMPORTJOBS = "migrations/0020_import_jobs.sql"
HARNESS = "internal/testutil/harness.go"
CSV = "internal/importer/csv.go"

T1 = "TestMeasured_TheUploadedPayloadOutlivesTheFinishedImport"
T2 = "TestMeasured_NothingInProductionEverDeletesAnImportJobOrItsPayload"
T3 = "TestMeasured_TheOnlyCascadeThatCouldReachThePayloadCannotRun"
T4 = "TestMeasured_TheWorkspaceChildTablesThatRefuseADelete"
ALL = [T1, T2, T3, T4]

# The FINISH body both SQL controls splice into, so the two differ ONLY in whether the statement is
# a single literal a text scan can read.
FINISH_ANCHOR = """	if err != nil {
		return fmt.Errorf("importer: finish job: %w", err)
	}
	return nil"""


def sh(*args, **kw):
    return subprocess.run(args, cwd=ROOT, capture_output=True, text=True, **kw)


def sha(path):
    with open(os.path.join(ROOT, path), "rb") as f:
        return hashlib.sha256(f.read()).hexdigest()


def read(path):
    with open(os.path.join(ROOT, path), encoding="utf-8") as f:
        return f.read()


def write(path, s):
    with open(os.path.join(ROOT, path), "w", encoding="utf-8") as f:
        f.write(s)


def run_tests(names):
    """Run the named tests. Returns (ok, output). ok=True means every one PASSED."""
    pattern = "^(" + "|".join(names) + ")$"
    r = sh("go", "test", "-count=1", "-run", pattern, "./internal/importer/")
    return r.returncode == 0, r.stdout + r.stderr


def which_red(output):
    """The set of our tests named on a FAIL line."""
    red = set()
    for line in output.splitlines():
        if line.strip().startswith("--- FAIL:"):
            for t in ALL:
                if t in line:
                    red.add(t)
    return red


def control(label, description, files, mutate, predicted, extra_check=None):
    """Apply `mutate` (path -> new text per file), run every test, score against `predicted`."""
    print(f"\n=== {label} — {description}")
    print(f"    PREDICTED CAUGHT BY: {', '.join(sorted(predicted)) if predicted else 'NOTHING (must stay green)'}")
    before = {p: (read(p), sha(p)) for p in files}
    try:
        for p in files:
            new = mutate(p, before[p][0])
            if new == before[p][0]:
                print(f"    REFUSING: mutation of {p} changed zero bytes")
                return False
            write(p, new)
        ok, out = run_tests(ALL)
        red = which_red(out)
        if not ok and not red:
            # A build failure or a panic is not a caught mutation.
            print("    REFUSING: the run failed without naming any of these tests (build error?)")
            print("    " + "\n    ".join(out.strip().splitlines()[-12:]))
            return False
        verdict = red == predicted
        print(f"    ACTUAL   CAUGHT BY: {', '.join(sorted(red)) if red else 'NOTHING'}")
        if extra_check is not None:
            if not extra_check():
                print("    REFUSING: the mutation's own reality check failed")
                return False
        print(f"    {'AS PREDICTED' if verdict else '*** MISPREDICTED ***'}")
        return verdict
    finally:
        for p in files:
            write(p, before[p][0])
            if sha(p) != before[p][1]:
                print(f"    *** RESTORE FAILED for {p} — TREE IS DIRTY ***")


def add_finish_delete(literal: bool):
    def _m(path, src):
        if literal:
            stmt = '`DELETE FROM import_job_payloads WHERE job_id=$1`'
        else:
            # Built at runtime: identical behaviour, invisible to a text scan.
            stmt = '"DELETE FROM import_job_pay" + "loads WHERE job_id=$1"'
        injected = (
            "	if err != nil {\n"
            "		return fmt.Errorf(\"importer: finish job: %w\", err)\n"
            "	}\n"
            "	_, _ = s.pool.Exec(ctx, " + stmt + ", jobID)\n"
            "	return nil"
        )
        assert FINISH_ANCHOR in src, "FINISH_ANCHOR not found in jobs.go"
        return src.replace(FINISH_ANCHOR, injected, 1)
    return _m


def main():
    if not os.environ.get("TRACK_TEST_DATABASE_URL"):
        print("REFUSING: TRACK_TEST_DATABASE_URL is unset. These tests would be measuring nothing.")
        return 2

    dirty = sh("git", "status", "--porcelain").stdout.strip()
    tracked_dirty = [l for l in dirty.splitlines() if not l.startswith("??")]
    if tracked_dirty:
        print("REFUSING: tracked files are already modified — a control cannot attribute its result.")
        print("\n".join(tracked_dirty))
        return 2

    ok, out = run_tests(ALL)
    if not ok:
        print("REFUSING: the four tests are not green on the untouched tree.")
        print(out[-3000:])
        return 2
    print("baseline: all four green on the untouched tree")

    results = []

    # ---- C1: the product deletes the payload, in a statement a text scan CAN read -------------
    results.append(("C1", control(
        "C1", "JobStore.Finish deletes the payload (literal SQL)",
        [JOBS], add_finish_delete(literal=True), {T1, T2})))

    # ---- C2: a production DELETE that never runs — source visible, behaviour unchanged --------
    def c2(path, src):
        return src + (
            "\n// unreachedSweeper is a C2 control artefact: production source naming a deletion that\n"
            "// nothing calls. The census must see it; behaviour must not change.\n"
            "func (s *JobStore) unreachedSweeper(ctx context.Context, jobID string) {\n"
            "	_, _ = s.pool.Exec(ctx, `DELETE FROM import_jobs WHERE id=$1`, jobID)\n"
            "}\n")
    results.append(("C2", control(
        "C2", "a production DELETE FROM import_jobs that nothing calls",
        [JOBS], c2, {T2})))

    # ---- C3: the same deletion, built at runtime — behaviour changes, the census is blind -----
    results.append(("C3", control(
        "C3", "JobStore.Finish deletes the payload with a CONSTRUCTED statement",
        [JOBS], add_finish_delete(literal=False), {T1})))

    # ---- C4: blind the census's matcher — it must REFUSE, never pass -------------------------
    def c4(path, src):
        return src.replace('"DELETE FROM "', '"DELETE FROM ZZZNOSUCHTOKEN "', 1)
    results.append(("C4", control(
        "C4", "the census matcher is blinded — it must REFUSE, not report a clean product",
        [TESTFILE], c4, {T2})))

    # ---- C5: the schema cascades instead of refusing -----------------------------------------
    #
    # ⚠ PREDICTED {T3, T4} AND MEASURED {T4}. THE MISPREDICTION IS RECORDED RATHER THAN QUIETLY
    # RETUNED, because it says something about T3 that reading the test does not: T3's arm A
    # workspace ALSO holds an import job, and import_jobs refuses on its own. So cascading members
    # and teams does NOT make that delete succeed — the payload is still unreachable, which is this
    # file's actual subject. T3 staying green here is correct. C5b below is the control that proves
    # T3 arm A is not simply a test that can never change.
    def c5(path, src):
        # members and teams — the two CreateWithOwner seeds — become cascading.
        out = src.replace(
            "CREATE TABLE IF NOT EXISTS members (\n"
            "    id           TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,\n"
            "    workspace_id TEXT NOT NULL REFERENCES workspaces(id),",
            "CREATE TABLE IF NOT EXISTS members (\n"
            "    id           TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,\n"
            "    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,", 1)
        out = out.replace(
            "CREATE TABLE IF NOT EXISTS teams (\n"
            "    id           TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,\n"
            "    workspace_id TEXT NOT NULL REFERENCES workspaces(id),",
            "CREATE TABLE IF NOT EXISTS teams (\n"
            "    id           TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,\n"
            "    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,", 1)
        return out
    results.append(("C5", control(
        "C5", "members + teams CASCADE (import_jobs still refuses)",
        [CORE], c5, {T4})))

    # ---- C5b: EVERY refusing table on arm A's path cascades — the delete now succeeds ---------
    # This is T3's anti-vacuity control. Without it, "arm A answers 500" is a claim no mutation
    # has ever been able to move, which is the shape this queue keeps finding.
    results.append(("C5b", control(
        "C5b", "members + teams + import_jobs ALL CASCADE — the workspace delete succeeds",
        [CORE, IMPORTJOBS], lambda p, s: c5(p, s) if p == CORE else s.replace(
            "    workspace_id  TEXT NOT NULL REFERENCES workspaces(id),  -- the authorized workspace, captured at creation",
            "    workspace_id  TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,  -- the authorized workspace, captured at creation", 1),
        {T3, T4})))

    # ---- C6: the seed helper starts seeding the way production does --------------------------
    def c6(path, src):
        old = """	ws, err := workspace.NewStore(d.Pool).Create(context.Background(), model.Workspace{
		Name: "Workspace " + tok,
		Slug: "ws-" + tok,
	})"""
        new = """	ws, err := workspace.NewStore(d.Pool).CreateWithOwner(context.Background(), model.Workspace{
		Name: "Workspace " + tok,
		Slug: "ws-" + tok,
	}, "owner-"+tok+"@example.com")"""
        assert old in src, "harness Workspace() anchor not found"
        return src.replace(old, new, 1)
    results.append(("C6", control(
        "C6", "testutil.Workspace seeds like production — arm B's premise must fail",
        [HARNESS], c6, {T3})))

    # ---- C7: MUST STAY GREEN — a real defect elsewhere in the importer -----------------------
    def c7(path, src):
        old = "	return strings.TrimSpace(row[idxs[0]])"
        assert old in src, "csv.go get() anchor not found"
        return src.replace(old, "	return strings.TrimSpace(row[idxs[len(idxs)-1]])", 1)

    def c7_is_real():
        """Prove C7 is a genuine defect: the WHOLE importer package must go red for it.

        ⚠ THE FIRST VERSION OF THIS CHECK RAN `-run TestCSV|TestJiraCSV|TestLinearCSV` AND REPORTED
        GREEN. It was measuring nothing useful: an exit code from a filtered run cannot distinguish
        "no test caught this" from "the filter is wrong", and a `-run` pattern that matches nothing
        exits 0. The check now runs the package unfiltered and REQUIRES a named FAIL line, so an
        empty or mis-filtered population can never read as a clean product.
        """
        r = sh("go", "test", "-count=1", "./internal/importer/")
        out = r.stdout + r.stderr
        fails = [l for l in out.splitlines() if l.strip().startswith("--- FAIL:")]
        if not fails:
            print("    reality check: the importer package is GREEN for this mutation — it is not a "
                  "defect this package can see, so it cannot serve as a must-stay-green control")
            return False
        print(f"    reality check: the importer package is RED for this mutation ({fails[0].strip()})")
        return True

    results.append(("C7", control(
        "C7", "MUST STAY GREEN: columnIndex.get names the LAST occurrence (a real CSV defect)",
        [CSV], c7, set(), extra_check=c7_is_real)))

    print("\n================ RESULT ================")
    for name, ok in results:
        print(f"  {name}: {'AS PREDICTED' if ok else 'MISPREDICTED'}")
    passed = sum(1 for _, ok in results if ok)
    print(f"  {passed}/{len(results)} as predicted")

    final = sh("git", "status", "--porcelain").stdout.strip()
    final_tracked = [l for l in final.splitlines() if not l.startswith("??")]
    if final_tracked:
        print("*** TREE IS DIRTY AFTER THE RUN ***")
        print("\n".join(final_tracked))
        return 1
    print("  tree clean after the run (tracked files restored)")
    return 0 if passed == len(results) else 1


if __name__ == "__main__":
    sys.exit(main())
