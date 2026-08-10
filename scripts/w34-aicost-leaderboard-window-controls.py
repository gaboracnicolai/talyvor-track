#!/usr/bin/env python3
"""Positive controls for the ai-cost leaderboard window (W3.4, tab-7b52).

Every control names the test AND the assertion line it expects to speak BEFORE it runs, and the
verdict is read from the PRINTED ASSERTION MESSAGE, never from a test name and never from a bare
exit code.

Rules this script follows, each of them a lesson a previous run on this item paid for:

  * the anchor is asserted UNIQUE before every write — a substitution matching nothing edits zero
    bytes and is byte-indistinguishable from a guard that works (#71);
  * files are restored from SAVED BYTES, never `git checkout` — the tree carries the uncommitted
    fix — and sha256 is compared after every restore;
  * a BUILD failure is detected explicitly and can never score as a catch. `go test` announces a
    compile error with a `# <package>` header and a parse error in a _test.go with
    `[setup failed]`; both are matched, because the tab-4b8c run scored a control CAUGHT on a
    package that never compiled (its detector looked only for "build failed");
  * the run target is THE WHOLE REPO, so "which tests spoke" is a measurement rather than an
    assumption, and a control caught only because it was pointed at its own package cannot happen.
"""

import hashlib
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
ENGINE = ROOT / "internal" / "analytics" / "engine.go"
GUARD = ROOT / "internal" / "analytics" / "aicost_window_test.go"
DB = "postgres://postgres:postgres@localhost:55472/postgres?sslmode=disable"

# The shipped predicate, as one contiguous move.
FIXED_PREDICATE = """        WHERE workspace_id = $1 AND ai_cost_usd > 0
          AND updated_at > NOW() - (INTERVAL '1 day' * $2::int)
        ORDER BY ai_cost_usd DESC LIMIT 10`,
		workspaceID, days,"""

# The fixture's second ageing statement — the one that moves issues.updated_at.
FIXTURE_AGES_ISSUE = """	if _, err := d.Pool.Exec(ctx,
		`UPDATE issues SET updated_at = NOW() - $2::interval WHERE id = $1`, iss.ID, age); err != nil {
		t.Fatalf("age issue row: %v", err)
	}"""


def sha(p: Path) -> str:
    return hashlib.sha256(p.read_bytes()).hexdigest()


def run_suite() -> tuple[int, str]:
    r = subprocess.run(
        ["go", "test", "./...", "-count=1"],
        cwd=ROOT, capture_output=True, text=True,
        env={**__import__("os").environ, "TRACK_TEST_DATABASE_URL": DB},
    )
    return r.returncode, r.stdout + r.stderr


BUILD_BROKEN = re.compile(r"^# |\[setup failed\]|\[build failed\]|build failed", re.M)
ASSERTION = re.compile(r"^\s+(\w+_test\.go:\d+): (.*)$", re.M)


def verdict(out: str) -> tuple[list[str], list[str]]:
    """Return (assertion lines, failing test names) read out of the printed output."""
    lines = [f"{m.group(1)}: {m.group(2)[:160]}" for m in ASSERTION.finditer(out)]
    tests = re.findall(r"^--- FAIL: (\S+)", out, re.M)
    return lines, tests


class Control:
    def __init__(self, key, title, path, old, new, predicted, must_stay_green=False):
        self.key, self.title, self.path = key, title, path
        self.old, self.new = old, new
        self.predicted = predicted            # substring expected in an assertion line
        self.must_stay_green = must_stay_green


CONTROLS = [
    Control(
        "C1", "revert the fix — main's state, the leaderboard takes the workspace id alone",
        ENGINE, FIXED_PREDICATE,
        """        WHERE workspace_id = $1 AND ai_cost_usd > 0
        ORDER BY ai_cost_usd DESC LIMIT 10`,
		workspaceID,""",
        "aicost_window_test.go:213",
    ),
    Control(
        "C2", "window on the WRONG COLUMN (created_at, the row's birth, not its last touch)",
        ENGINE,
        "          AND updated_at > NOW() - (INTERVAL '1 day' * $2::int)\n        ORDER BY ai_cost_usd DESC LIMIT 10",
        "          AND created_at > NOW() - (INTERVAL '1 day' * $2::int)\n        ORDER BY ai_cost_usd DESC LIMIT 10",
        "aicost_window_test.go:213",
    ),
    Control(
        "C3", "NARROW rather than window — the predicate can never match, leaderboard always empty",
        ENGINE,
        "          AND updated_at > NOW() - (INTERVAL '1 day' * $2::int)\n        ORDER BY ai_cost_usd DESC LIMIT 10",
        "          AND updated_at > NOW() + (INTERVAL '1 day' * $2::int)\n        ORDER BY ai_cost_usd DESC LIMIT 10",
        "aicost_window_test.go:255",
    ),
    Control(
        "C4", "blind the FIXTURE — age the ledger row but leave issues.updated_at at NOW()",
        GUARD, FIXTURE_AGES_ISSUE, "\t_ = age",
        "PREMISE 2",
    ),
    Control(
        "C5", "INVERTED — same predicate spelled with make_interval; MUST STAY GREEN",
        ENGINE,
        "          AND updated_at > NOW() - (INTERVAL '1 day' * $2::int)\n        ORDER BY ai_cost_usd DESC LIMIT 10",
        "          AND updated_at > NOW() - make_interval(days => $2::int)\n        ORDER BY ai_cost_usd DESC LIMIT 10",
        "",
        must_stay_green=True,
    ),
]


def main() -> int:
    saved = {p: p.read_bytes() for p in (ENGINE, GUARD)}
    sums = {p: sha(p) for p in (ENGINE, GUARD)}

    print("BASELINE — the fixed tree, whole repo")
    rc, out = run_suite()
    if rc != 0:
        print("  the baseline is NOT green; every verdict below would be unreadable")
        print(out[-3000:])
        return 1
    print("  green\n")

    results = []
    for c in CONTROLS:
        text = c.path.read_text()
        n = text.count(c.old)
        if n != 1:
            print(f"{c.key}: ANCHOR COUNT {n}, want 1 — NOT APPLIED, no verdict")
            results.append((c.key, "NOT APPLIED"))
            continue
        c.path.write_text(text.replace(c.old, c.new))
        try:
            rc, out = run_suite()
            lines, tests = verdict(out)
            broken = bool(BUILD_BROKEN.search(out))
            print(f"{c.key} — {c.title}")
            print(f"   predicted catcher: {c.predicted or '(none — must stay green)'}")
            if broken:
                print("   BUILD BROKEN — this control compiled nothing and scores NOTHING")
                results.append((c.key, "BUILD BROKEN"))
            elif c.must_stay_green:
                ok = rc == 0
                print(f"   exit={rc} failing tests={tests or '[]'}")
                print(f"   {'AS SPECIFIED (stayed green)' if ok else 'NOT AS SPECIFIED — it reds'}")
                results.append((c.key, "GREEN AS SPECIFIED" if ok else "NOT AS SPECIFIED"))
            else:
                hit = [ln for ln in lines if c.predicted in ln]
                for ln in lines:
                    print(f"   spoke: {ln}")
                print(f"   failing tests: {tests}")
                ok = rc != 0 and bool(hit)
                print(f"   {'CAUGHT by the PREDICTED assertion' if ok else 'NOT CAUGHT AS PREDICTED'}")
                results.append((c.key, "CAUGHT" if ok else "NOT AS PREDICTED"))
        finally:
            c.path.write_bytes(saved[c.path])
            after = sha(c.path)
            if after != sums[c.path]:
                print(f"   ⚠ RESTORE MISMATCH on {c.path.name}: {after} != {sums[c.path]}")
                return 2
        print()

    print("SUMMARY")
    for k, v in results:
        print(f"  {k}: {v}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
