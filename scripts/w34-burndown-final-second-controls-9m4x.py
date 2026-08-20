#!/usr/bin/env python3
"""
W3.4 / tab-9m4x — positive controls for the burndown day boundary.

THE DEFECT.  `endOfDay` returned 23:59:59.000000000 and both consumers compared with `<=`, so the
half-open second [23:59:59.000000001, 23:59:59.999999999] fell OUTSIDE the day.  On the cycle's
LAST day that is terminal: the read's own bound is the same instant, so the row is never fetched
and no later day exists to absorb it.  A cycle that closed all of its work reported issues still
remaining and drew the red "Off track" badge.  The rule was written TWICE — internal/analytics
(what the frontend draws) and internal/cycle (a mounted route) — and both copies had it.

THE GUARDS PASSED ON THEIR FIRST RUN AFTER THE FIX, so everything that makes them guards is here.
Every control names its PREDICTED verdict and its PREDICTED catching tag BEFORE running.  Where
the measurement disagrees the prediction is corrected to the measurement and said so.

MEMBERSHIP IS SET SUBTRACTION AGAINST A MEASURED C0, never an exit code and never a test's name.
A mutation that fails to compile scores VOID: a build error is not a caught mutation.
"""

import hashlib
import os
import re
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
ENGINE = os.path.join(ROOT, "internal", "analytics", "engine.go")
CYCLE = os.path.join(ROOT, "internal", "cycle", "store.go")
TESTFILE = os.path.join(ROOT, "internal", "analytics", "burndown_final_second_realpg_test.go")
CLOSURE = ["./internal/analytics/", "./internal/cycle/", "./internal/importer/",
           "./internal/mcp/", "./cmd/track/"]

CAUGHT, NOT_CAUGHT, VOID = "CAUGHT", "NOT CAUGHT", "VOID(build)"

# The shipped helper, and the pre-fix one it replaced. Body only — the doc comments differ between
# the two files, so anchoring on the body keeps one control text valid for both ports.
NEW_BODY = ("\treturn time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, "
            "day.Location()).AddDate(0, 0, 1)")
OLD_BODY = "\treturn time.Date(day.Year(), day.Month(), day.Day(), 23, 59, 59, 0, day.Location())"

# An arithmetically identical spelling: Go's time.Date normalises an out-of-range day, so
# time.Date(y, m, d+1, ...) IS time.Date(y, m, d, ...).AddDate(0, 0, 1), DST included.
VOID_BODY = ("\treturn time.Date(day.Year(), day.Month(), day.Day()+1, 0, 0, 0, 0, "
             "day.Location())")

FIXTURE_SUBSECOND = "\tlateOnDay0 = lastSecondOf(0).Add(500 * time.Millisecond)\n" \
                    "\tlateOnFinalDay = lastSecondOf(span).Add(500 * time.Millisecond)"
FIXTURE_WHOLE = "\tlateOnDay0 = lastSecondOf(0)\n\tlateOnFinalDay = lastSecondOf(span)"

# (id, description, [(file, anchor, replacement)], PREDICTED verdict, PREDICTED catchers)
CONTROLS = [
    ("B1", "THE DEFECT ITSELF, analytics port only — helper back to inclusive 23:59:59",
     [(ENGINE, NEW_BODY, OLD_BODY)], CAUGHT,
     "F-FINAL, F-INTERIOR, F-ONTRACK, P-VALUE(analytics), P-PARITY"),

    ("B2", "THE DEFECT IN THE OTHER PORT ONLY — internal/cycle back to inclusive 23:59:59",
     [(CYCLE, NEW_BODY, OLD_BODY)], CAUGHT,
     "P-VALUE(cycle) and P-PARITY ONLY; every F-* assertion stays GREEN because the analytics "
     "port is untouched. This is the control that justifies the parity test existing at all."),

    ("B3", "THE DEFECT IN BOTH PORTS AT ONCE — they are wrong and they AGREE",
     [(ENGINE, NEW_BODY, OLD_BODY), (CYCLE, NEW_BODY, OLD_BODY)], CAUGHT,
     "F-* and BOTH P-VALUE halves; **P-PARITY STAYS GREEN**. #170's lesson reproduced: an "
     "agreement-only cross-check reports health for a uniformly wrong product, which is why "
     "P-VALUE is asserted before P-PARITY."),

    ("B4", "the analytics SQL bound alone reverted to `<=`, helper left exclusive",
     [(ENGINE, "AND completed_at IS NOT NULL AND completed_at < $2",
       "AND completed_at IS NOT NULL AND completed_at <= $2")], NOT_CAUGHT,
     "nothing — this admits exactly ONE extra instant (midnight of the day after the window) and "
     "no fixture in the repository stores a completion at exactly 00:00:00.000000. A DOCUMENTED "
     "BLINDNESS, recorded rather than papered over: these guards pin the lost final second, not "
     "every off-by-one-instant the two comparisons admit."),

    ("B5", "VOID CONTROL: the helper rewritten as an arithmetically identical expression",
     [(ENGINE, NEW_BODY, VOID_BODY)], NOT_CAUGHT,
     "nothing. Go normalises an out-of-range day, so behaviour is identical by construction. A "
     "red here would mean something matches engine.go's TEXT rather than its answers."),

    ("B6", "THE FIXTURE GOES UNIFORM: both completions moved onto the whole second",
     [(TESTFILE, FIXTURE_SUBSECOND, FIXTURE_WHOLE)], CAUGHT,
     "F-PREMISE ALONE. With both rows exactly ON the boundary, `<= 23:59:59` and `< next-midnight` "
     "are the same predicate and every other assertion passes VACUOUSLY. This is the control that "
     "justifies F-PREMISE — without it the file could go quietly inert."),
]


def sha256(p):
    with open(p, "rb") as fh:
        return hashlib.sha256(fh.read()).hexdigest()


def run():
    env = dict(os.environ)
    env.setdefault("TRACK_TEST_DATABASE_URL",
                   "postgres://postgres:postgres@localhost:55934/postgres?sslmode=disable")
    proc = subprocess.run(["go", "test", "-timeout", "600s", "-race", "-count=1"] + CLOSURE,
                          cwd=ROOT, env=env, capture_output=True, text=True)
    raw = proc.stdout + proc.stderr
    fails = set(re.findall(r"^\s*--- FAIL: (\S+)", raw, re.M))
    # Which named assertions actually FIRED. A failure's own log lines are printed too and several
    # of these assertions NAME another tag in their prose, so a bare findall over the output would
    # credit tags that never fired — #161 shipped exactly that bug. Read only the tag a failure
    # LINE OPENS WITH, after the `file.go:NN:` prefix.
    tags = set(re.findall(r"^\s*\S+\.go:\d+:\s*\[([A-Z-]+)\]", raw, re.M))
    return fails, tags, ("[build failed]" in raw or "build failed" in raw), raw


def main():
    originals = {p: open(p).read() for p in (ENGINE, CYCLE, TESTFILE)}
    shas = {p: sha256(p) for p in originals}

    print("C0 — the shipped tree. Every guard must be GREEN and the baseline failing set measured.")
    f0, t0, b0, raw0 = run()
    if b0:
        print("C0 DOES NOT BUILD:\n" + raw0[-2500:])
        return 2
    print(f"   C0 failing set: {sorted(f0) if f0 else 'EMPTY'}   tags fired: {sorted(t0) or 'none'}")

    rows, wrong = [], []
    try:
        for cid, desc, edits, predicted, catchers in CONTROLS:
            for path, anchor, repl in edits:
                src = open(path).read()
                n = src.count(anchor)
                if n != 1:
                    raise SystemExit(f"{cid}: anchor matched {n}x in {os.path.basename(path)}, "
                                     f"not 1 — re-anchor before trusting any result")
                open(path, "w").write(src.replace(anchor, repl))

            fails, tags, build_failed, raw = run()
            new = fails - f0
            verdict = VOID if build_failed else (CAUGHT if new else NOT_CAUGHT)

            print(f"\n{cid} — {desc}")
            print(f"    predicted {predicted:11}  measured {verdict}")
            print(f"    predicted catchers: {catchers}")
            if new:
                print(f"    reds: {sorted(new)}")
                print(f"    tags that FIRED: {sorted(tags - t0) or 'none'}")
            if verdict != predicted:
                wrong.append(cid)
            rows.append((cid, predicted, verdict, sorted(tags - t0), desc))

            for path in {p for p, _, _ in edits}:
                open(path, "w").write(originals[path])
    finally:
        for path, src in originals.items():
            open(path, "w").write(src)
        bad = [os.path.basename(p) for p in originals if sha256(p) != shas[p]]
        print("\nrestore: " + ("ALL FILES byte-identical to pristine (sha256 verified)"
                               if not bad else f"NOT RESTORED: {bad} — STOP"))
        if bad:
            return 3

    print("\n" + "=" * 92)
    for cid, predicted, verdict, tags, desc in rows:
        mark = "  " if verdict == predicted else "<-"
        print(f"{mark} {cid} {verdict:12} (predicted {predicted:11}) fired={','.join(tags) or '-':38} {desc[:40]}")
    print("=" * 92)
    if wrong:
        print(f"PREDICTIONS WRONG: {', '.join(wrong)} — recorded, not tuned away")
        return 1
    print("every prediction held")
    return 0


if __name__ == "__main__":
    sys.exit(main())
