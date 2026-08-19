#!/usr/bin/env python3
"""Positive controls for internal/analytics/default_window_realpg_test.go.

THE QUESTION: can anything in this repository see "the default analytics window" move?

It is written down six times — `defaultWindowDays = 30` in engine.go and five bare `30`s in
`intParam(r, "days", 30)` in handler.go — and the two are reached by two different requests
(`days` omitted vs `days=0`), so they can drift apart into a route that answers one question
two ways.

Every control runs the WHOLE repository (`go test ./...`) against real Postgres. Membership is
decided by SET SUBTRACTION against C0's own measured FAIL set — never by an exit code: this repo
already fails 13 importer tests on a machine holding the empty shared /tmp corpus dirs.

Each mutation is restored in a `finally` and every touched file is sha256-verified afterwards.
Predictions are printed BEFORE the run.

Usage:  TRACK_TEST_DATABASE_URL=... python3 scripts/w34-default-window-controls-5b91.py
"""
import hashlib
import os
import pathlib
import re
import subprocess
import sys

REPO = pathlib.Path(__file__).resolve().parent.parent
ENGINE = REPO / "internal/analytics/engine.go"
HANDLER = REPO / "internal/analytics/handler.go"
GUARD = REPO / "internal/analytics/default_window_realpg_test.go"
NEW = "TestAnalytics_TheDefaultWindowIsOneNumberOnBothPathsIntoIt_RealPG"

CLAMP = """func clampDays(days int) int {
	if days <= 0 {
		return defaultWindowDays
	}
	if days > maxWindowDays {
		return maxWindowDays
	}
	return days
}"""

CONTROLS = [
    ("D1", "THE DEFECT — defaultWindowDays 30 -> 90. Measured NOT CAUGHT by anything on main.",
     [(ENGINE, "\tdefaultWindowDays = 30\n", "\tdefaultWindowDays = 90\n")],
     f"CAUGHT by {NEW} (the days=0 path) and by nothing else"),

    ("D2", "D1 with the new guard file DELETED — the measured blindness on main",
     [(ENGINE, "\tdefaultWindowDays = 30\n", "\tdefaultWindowDays = 90\n"), (GUARD, None, None)],
     "NOT CAUGHT (green): nothing else in this repository can see the default move"),

    ("D3", "THE OTHER SOURCE — the distribution route's own literal 30 -> 90. The engine never "
           "sees this number, so no engine-level test can reach it.",
     [(HANDLER, 'out, err := h.engine.GetDistribution(r.Context(), wsID, groupBy, intParam(r, "days", 30))',
       'out, err := h.engine.GetDistribution(r.Context(), wsID, groupBy, intParam(r, "days", 90))')],
     f"CAUGHT by {NEW} (the omitted-days path AND the tie assertion)"),

    ("D4", "MUST-STAY-GREEN COMPANION — clampDays' `days <= 0` weakened to `days < 0`. A real "
           "defect the pre-existing unit test already owns.",
     [(ENGINE, CLAMP, CLAMP.replace("if days <= 0 {", "if days < 0 {"))],
     "CAUGHT by TestClampDays_BoundsRespected et al — the new file may join, but it is not the "
     "only catcher, so this control does not justify it"),

    ("D5", "VOID — `days <= 0` -> `days < 1`, identical over ints",
     [(ENGINE, CLAMP, CLAMP.replace("if days <= 0 {", "if days < 1 {"))],
     "NOT CAUGHT"),

    ("D6", "ANTI-VACUITY CHECK ON THE GUARD ITSELF — the window predicate dropped from the "
           "distribution query, so `days` stops meaning anything",
     [(ENGINE, "          AND created_at > NOW() - (INTERVAL '1 day' * $2::int)\n        GROUP BY %s",
       "          AND ($2::int IS NOT NULL)\n        GROUP BY %s")],
     f"CAUGHT by {NEW}'s anti-vacuity halves and by the counting guard"),
]


def sha(p):
    return hashlib.sha256(p.read_bytes()).hexdigest() if p.exists() else "ABSENT"


def run(tag):
    r = subprocess.run(["go", "test", "-timeout", "600s", "-count=1", "./..."],
                       cwd=REPO, capture_output=True, text=True, env=os.environ)
    names = set(re.findall(r"^\s*--- FAIL: (\S+)", r.stdout, re.M))
    build = "[build failed]" in r.stdout
    print(f"    [{tag}] rc={r.returncode} fails={len(names)} build_failed={build}", flush=True)
    if build:
        print(r.stdout[-1200:])
    return names, build


def main():
    if not os.environ.get("TRACK_TEST_DATABASE_URL"):
        sys.exit("TRACK_TEST_DATABASE_URL is required — these are real-Postgres controls")
    backups = {p: (sha(p), p.read_text() if p.exists() else None)
               for p in (ENGINE, HANDLER, GUARD)}

    print("C0 — baseline, no mutation, whole repository")
    base, berr = run("C0")
    if berr:
        sys.exit("C0 does not build")
    print(f"    C0 FAIL set = {len(base)} pre-existing (empty shared /tmp corpus dirs)")
    if NEW in base:
        sys.exit("the new guard is RED on an unmutated tree")

    rows = []
    for cid, desc, edits, pred in CONTROLS:
        print(f"\n{cid} — {desc}")
        print(f"    PREDICTION: {pred}")
        try:
            for path, old, new in edits:
                if old is None:
                    path.unlink()
                    continue
                src = path.read_text()
                if src.count(old) != 1:
                    raise AssertionError(f"{cid}: anchor miss in {path.name} ({src.count(old)})")
                path.write_text(src.replace(old, new, 1))
            got, berr2 = run(cid)
            if berr2:
                verdict = "BUILD-FAILED"
            else:
                fresh = sorted(got - base)
                verdict = f"CAUGHT by {fresh}" if fresh else "NOT CAUGHT"
        finally:
            for path, (h, text) in backups.items():
                if text is None:
                    if path.exists():
                        path.unlink()
                else:
                    path.write_text(text)
                assert sha(path) == h, f"{path} NOT RESTORED"
        print(f"    MEASURED:   {verdict}")
        rows.append((cid, pred, verdict))

    print("\n===== SUMMARY =====")
    for cid, pred, verdict in rows:
        print(f"{cid}  predicted: {pred}\n     measured: {verdict}")
    print("\nfiles restored and sha256-verified:")
    for p, (h, _) in backups.items():
        print(f"  {p.relative_to(REPO)}  {sha(p)}  (== {h})")


if __name__ == "__main__":
    main()
