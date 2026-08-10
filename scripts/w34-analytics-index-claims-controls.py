#!/usr/bin/env python3
"""
Positive controls for internal/analytics/index_claims_realpg_test.go.

THE GUARD PASSED ON ITS FIRST RUN, which is the state in which a guard is least trustworthy: it
reports a measurement, and a measurement that cannot come out any other way is decoration. These
controls establish that each of its five load-bearing parts can fail, and that the two most
dangerous failure modes — a fixture that guarantees its answer, and a stats instrument that is
simply dead — are REPORTED rather than silently read as "unused".

The verdict is the SET of assertion tags that fired, predicted before the run. A build failure or
a panic is scored as such and never as a catch.

Restores are `cp` from bytes saved before the run, in a `finally`, sha256-compared at the end.

Usage:  TRACK_TEST_DATABASE_URL=... python3 scripts/w34-analytics-index-claims-controls.py
"""

import hashlib
import os
import pathlib
import re
import shutil
import subprocess
import sys
import tempfile

REPO = pathlib.Path(__file__).resolve().parent.parent
DSN = os.environ.get(
    "TRACK_TEST_DATABASE_URL",
    "postgres://postgres:postgres@localhost:55434/postgres?sslmode=disable",
)
GUARD = "internal/analytics/index_claims_realpg_test.go"
FILES = [GUARD]
PKG = "./internal/analytics/"
RUN = "TestAnalyticsIndexes"

TAG_RE = re.compile(r"\[(A-[A-Z-]+(?:/[a-z_]+)?)\]")
BUILD_RE = re.compile(r"^\S+\.go:\d+:\d+: |\[build failed\]|build failed", re.M)
PANIC_RE = re.compile(r"^panic: |^\[signal SIG", re.M)


def sh(args):
    env = dict(os.environ, TRACK_TEST_DATABASE_URL=DSN)
    return subprocess.run(args, cwd=REPO, capture_output=True, text=True, env=env)


def sha256(p):
    return hashlib.sha256((REPO / p).read_bytes()).hexdigest()


def run_guard():
    r = sh(["go", "test", "-timeout", "900s", "-count=1", "-run", RUN, "-v", PKG])
    out = r.stdout + r.stderr
    if BUILD_RE.search(out):
        return set(), "BUILD", out
    if PANIC_RE.search(out):
        return set(), "PANIC", out
    # Only tags on FAILING lines count. The guard logs [A-REACHABLE] and [A-INSTRUMENT] on the
    # HAPPY path too, so scraping every tag would score a clean run as five catches.
    failing = set()
    if "--- FAIL" in out or "\nFAIL" in out:
        for line in out.splitlines():
            if "_test.go:" in line and ("[A-" in line):
                failing |= set(TAG_RE.findall(line))
    return failing, None, out


def mutate(path, old, new, count=1):
    p = REPO / path
    s = p.read_text()
    if s.count(old) != count:
        raise SystemExit(
            f"ANCHOR MISS in {path}: wanted {count} of {old!r}, found {s.count(old)}. "
            "A control that matched no bytes scores NOT CAUGHT for free."
        )
    p.write_text(s.replace(old, new, count))


CONTROLS = [
    (
        "C0",
        "MUST STAY GREEN — no mutation. Without it, a control set failing for an environmental "
        "reason scores as six working guards.",
        lambda: None,
        set(),
    ),
    (
        "C1",
        "Claim idx_issues_due IS scanned by the engine. This is the finding itself, inverted.",
        lambda: mutate(GUARD, '"idx_issues_due":       false,', '"idx_issues_due":       true,'),
        {"A-CLAIM/idx_issues_due"},
    ),
    (
        "C2",
        "Claim idx_issues_ai_cost is NOT scanned. The mirror direction — a guard that could only "
        "ever report 'unused' would pass this.",
        lambda: mutate(GUARD, '"idx_issues_ai_cost":   true,', '"idx_issues_ai_cost":   false,'),
        {"A-CLAIM/idx_issues_ai_cost"},
    ),
    (
        "C3",
        "⚠ THE FIXTURE CONTROL. Collapse the fixture to ONE workspace. `workspace_id = $1` then "
        "selects 100% of the table, a seq scan becomes the correct plan for everything, and every "
        "index reads as unused — the exact false positive that made the first version of this "
        "measurement wrong. The guard must NOTICE, via the one index it pins as USED.",
        lambda: mutate(GUARD, "const nWorkspaces, perWorkspace = 200, 300",
                       "const nWorkspaces, perWorkspace = 1, 300"),
        # ⚠ PREDICTION WRONG, KEPT WRONG: I predicted A-CLAIM/idx_issues_ai_cost. The guard does
        # better — with one workspace even the warm-up query of idx_issues_due's OWN declared
        # shape takes a seq scan, so it aborts at A-REACHABLE and refuses to conclude ANYTHING.
        # Refusing to measure beats measuring wrongly.
        {"A-REACHABLE"},
    ),
    (
        "C4",
        "⚠ THE INSTRUMENT CONTROL. Neuter the stats flush so pg_stat is read stale. A dead "
        "instrument reports zero for everything, which is byte-identical to 'no reader'. The "
        "guard must REFUSE rather than record — and it must refuse at the REACHABLE gate, before "
        "any conclusion is drawn.",
        # ⚠ C4's FIRST VERSION WAS INERT AND THAT IS ITSELF A MEASUREMENT, KEPT HERE. It replaced
        # the pg_stat_force_next_flush() call with `SELECT 1` and the guard stayed GREEN — because
        # the force-flush is NOT the load-bearing part. The 300ms sleeps are: Postgres flushes
        # backend stats on its own after PGSTAT_MIN_INTERVAL, so waiting is sufficient and forcing
        # only makes it converge sooner. A control that removes a line nothing depends on scores
        # NOT CAUGHT for free and justifies nothing. The instrument being controlled is the WAIT.
        lambda: mutate(GUARD, "\tfor i := 0; i < 13; i++ {", "\tfor i := 0; i < 0; i++ {"),
        {"A-REACHABLE"},
    ),
    (
        "C5",
        "Stop driving GetAICostTrends and its CSV sibling. 'Unused' must not be able to mean 'I "
        "did not call the method' — dropping the only callers of the one index pinned as USED "
        "must fail, not quietly re-classify it.",
        lambda: mutate(
            GUARD,
            '\t\t{"GetAICostTrends", func() error { _, err := e.GetAICostTrends(ctx, subject.ID, 30); return err }},\n',
            "",
        )
        or mutate(
            GUARD,
            '\t\t{"ExportAICostTrendsCSV", func() error { return e.ExportAICostTrendsCSV(ctx, subject.ID, 30, io.Discard) }},\n',
            "",
        ),
        {"A-CLAIM/idx_issues_ai_cost"},
    ),
    (
        "C6",
        "⚠ THE CENSUS CONTROL. Name a fourth index in declaredIn0009 that the migration does not "
        "create. A source-derived census cannot see an index DISAPPEAR, which is why the list is "
        "pinned; this proves the pin is compared against the database in both directions.",
        lambda: mutate(
            GUARD,
            '\t"idx_issues_due":       "overdue issues per assignee",\n',
            '\t"idx_issues_due":       "overdue issues per assignee",\n'
            '\t"idx_issues_zzz_gone":  "an index 0009 does not create",\n',
        ),
        set(),  # a t.Fatalf with no tag — scored by its own message, see EXPECT_FATAL
    ),
]

# C6 fails via a Fatalf that carries no [A-…] tag on purpose (it aborts before any measurement).
# Scoring it by tag set would read as NOT CAUGHT, so it is scored by its message instead.
EXPECT_FATAL = {"C6": "no longer creates"}


def main():
    saved = tempfile.mkdtemp(prefix="w34-idxclaims-")
    before = {f: sha256(f) for f in FILES}
    for f in FILES:
        shutil.copy2(REPO / f, pathlib.Path(saved) / pathlib.Path(f).name)

    results = []
    try:
        for cid, desc, apply_fn, want in CONTROLS:
            for f in FILES:
                shutil.copy2(pathlib.Path(saved) / pathlib.Path(f).name, REPO / f)
            try:
                apply_fn()
            except SystemExit as e:
                results.append((cid, "ANCHOR-MISS", set(), str(e)))
                continue
            tags, abort, out = run_guard()
            if abort:
                results.append((cid, abort, set(), "scored as such, never as a catch"))
                continue
            if cid in EXPECT_FATAL:
                ok = EXPECT_FATAL[cid] in out and ("--- FAIL" in out or "\nFAIL" in out)
                results.append((cid, "AS PREDICTED" if ok else "MISMATCH (message)", tags,
                                f"wanted a Fatal naming {EXPECT_FATAL[cid]!r}"))
                continue
            ok = tags == want
            results.append((cid, "AS PREDICTED" if ok else "MISMATCH (tag set)", tags,
                            f"want {sorted(want)}"))
    finally:
        for f in FILES:
            shutil.copy2(pathlib.Path(saved) / pathlib.Path(f).name, REPO / f)

    after = {f: sha256(f) for f in FILES}
    restored = all(before[f] == after[f] for f in FILES)

    print("=" * 96)
    print("W3.4 — analytics index-claim guard controls")
    print("=" * 96)
    good = 0
    for cid, verdict, tags, note in results:
        mark = "OK " if verdict == "AS PREDICTED" else "!! "
        if verdict == "AS PREDICTED":
            good += 1
        print(f"{mark}{cid}  {verdict}")
        if verdict != "AS PREDICTED":
            print(f"      got tags: {sorted(tags)}   ({note})")
    print("-" * 96)
    print(f"{good}/{len(CONTROLS)} as predicted;  files restored byte-for-byte: {restored}")
    sys.exit(0 if (good == len(CONTROLS) and restored) else 1)


if __name__ == "__main__":
    main()
