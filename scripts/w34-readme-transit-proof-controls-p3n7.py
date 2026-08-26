#!/usr/bin/env python3
"""Positive controls for TestREADMECurlExamplesCarryTheGatewayTransitProof.

WHY THIS EXISTS
    The guard it controls is a documentation guard, and documentation guards are the easiest
    kind to ship inert: point the reader at a file, find nothing, report green. This queue has
    shipped that shape three times. So every arm of the guard is broken here, one at a time,
    and each break must produce the verdict PREDICTED BEFORE THE RUN — including the two
    breaks whose correct answer is REFUSE rather than red.

WHAT EACH CONTROL PROVES
    C1  a recipe missing the transit proof                       -> RED (names x-gateway-auth)
    C2  a recipe missing the identity header                     -> RED (names x-user-email)
    C3  gwExempt renamed in cmd/track/main.go                    -> REFUSE, not green
    C4  gwExempt's body emptied of HasPrefix calls               -> REFUSE, not green
    C5  both recipes deleted from the README                     -> REFUSE, not green
        (the empty-population arm: "found no recipes" must never read as "all recipes correct")
    C6  an EXEMPT-path curl with no headers, alongside the real recipes -> GREEN
        (the exemption arm is load-bearing: this guard is not "every curl needs headers")
    C7  a recipe whose line-continuation backslash is missing    -> RED
        (the join is load-bearing; a recipe that does not parse as one command is broken
         in a shell too, so red is the honest verdict rather than a parser convenience)

DISCIPLINE
    - Refuses to score if the guard is not GREEN on the untouched tree.
    - Refuses to score if either target file is already modified — a control campaign run over
      somebody else's edit measures their tree, not the guard.
    - Every mutation asserts it CHANGED THE BYTES before running the test. A mutation whose
      anchor has drifted patches nothing and would score its defect NOT-CAUGHT; that is the
      failure this repository has recorded more than once, so it stops instead.
    - Every file is restored in a `finally` and verified sha256-identical at the end.
"""
from __future__ import annotations

import hashlib
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
README = ROOT / "README.md"
MAIN = ROOT / "cmd" / "track" / "main.go"
TEST = "TestREADMECurlExamplesCarryTheGatewayTransitProof"


def sha(p: Path) -> str:
    return hashlib.sha256(p.read_bytes()).hexdigest()


def run_guard() -> tuple[int, str]:
    r = subprocess.run(
        ["go", "test", "-count=1", "-run", TEST, "./internal/gatewayauth/"],
        cwd=ROOT, capture_output=True, text=True,
    )
    return r.returncode, r.stdout + r.stderr


def verdict(rc: int, out: str) -> str:
    """GREEN / RED / REFUSE — REFUSE is a red that says the guard could not form an opinion."""
    if rc == 0:
        return "GREEN"
    return "REFUSE" if "REFUSE:" in out else "RED"


def mutate(path: Path, fn) -> str:
    """Apply fn to the file's text. Returns the original text. Refuses on a no-op."""
    before = path.read_text(encoding="utf-8")
    after = fn(before)
    if after == before:
        raise SystemExit(
            f"REFUSE: the mutation of {path.name} changed nothing — its anchor has drifted. "
            "Scoring now would report NOT-CAUGHT for a defect that was never introduced."
        )
    path.write_text(after, encoding="utf-8")
    return before


CONTROLS = []


def control(name, want, needle=""):
    def deco(fn):
        CONTROLS.append((name, want, needle, fn))
        return fn
    return deco


@control("C1 recipe missing the transit proof", "RED", "x-gateway-auth")
def c1():
    return README, lambda s: s.replace('  -H "X-Gateway-Auth: $GATEWAY_AUTH_SECRET" \\\n', "", 1)


@control("C2 recipe missing the identity header", "RED", "x-user-email")
def c2():
    # the SECOND occurrence (the Jira recipe), so C1 and C2 do not hide behind each other
    def f(s):
        first = s.find('  -H "X-User-Email: you@example.com" \\\n')
        second = s.find('  -H "X-User-Email: you@example.com" \\\n', first + 1)
        if second < 0:
            return s
        return s[:second] + s[second:].replace('  -H "X-User-Email: you@example.com" \\\n', "", 1)
    return README, f


@control("C3 gwExempt renamed", "REFUSE", "cannot find")
def c3():
    return MAIN, lambda s: s.replace("gwExempt := func(", "gwExemptRenamed := func(", 1)


@control("C4 gwExempt body emptied of prefixes", "REFUSE", "ZERO strings.HasPrefix")
def c4():
    def f(s):
        start = s.index("gwExempt := func(")
        end = start + s[start:].index("\n\t}")
        body = s[start:end]
        return s[:start] + re.sub(r'strings\.HasPrefix\(p,\s*"[^"]+"\)', "false", body) + s[end:]
    return MAIN, f


@control("C5 both recipes deleted (empty population)", "REFUSE", "no curl example")
def c5():
    return README, lambda s: re.sub(r'(?m)^curl -X POST "http://localhost:3000/v1/import/.*?\n```',
                                    "```", s, flags=re.S)


@control("C6 an exempt-path curl with no headers", "GREEN")
def c6():
    return README, lambda s: s.replace(
        "## Migrate from Linear",
        '```bash\ncurl "http://localhost:3000/v1/public/boards/abc"\n```\n\n## Migrate from Linear', 1)


@control("C7 broken line continuation", "RED", "x-gateway-auth")
def c7():
    return README, lambda s: s.replace(
        'curl -X POST "http://localhost:3000/v1/import/linear?workspace_id=WS&team_id=TEAM" \\\n',
        'curl -X POST "http://localhost:3000/v1/import/linear?workspace_id=WS&team_id=TEAM"\n', 1)


def main() -> int:
    dirty = subprocess.run(["git", "status", "--porcelain", "--", str(README), str(MAIN)],
                           cwd=ROOT, capture_output=True, text=True).stdout.strip()
    if dirty:
        print("REFUSE: README.md or cmd/track/main.go is already modified:\n" + dirty)
        return 3

    baseline = {p: sha(p) for p in (README, MAIN)}

    rc, out = run_guard()
    if verdict(rc, out) != "GREEN":
        print("REFUSE: the guard is not GREEN on the untouched tree — nothing below would mean "
              f"anything.\n{out}")
        return 3
    print(f"clean tree: {TEST} GREEN\n")

    failures = 0
    for name, want, needle, fn in CONTROLS:
        path, mutation = fn()
        original = None
        try:
            original = mutate(path, mutation)
            rc, out = run_guard()
            got = verdict(rc, out)
            ok = got == want and (not needle or needle in out)
            failures += 0 if ok else 1
            detail = "" if ok else f"   <-- expected {want}" + (f" naming {needle!r}" if needle else "")
            print(f"  [{'ok ' if ok else 'BAD'}] {name:<48} -> {got}{detail}")
            if not ok:
                print("      " + "\n      ".join(out.strip().splitlines()[:8]))
        finally:
            if original is not None:
                path.write_text(original, encoding="utf-8")

    for p, want in baseline.items():
        if sha(p) != want:
            print(f"\nBAD: {p.name} was NOT restored (sha256 differs)")
            failures += 1

    rc, out = run_guard()
    if verdict(rc, out) != "GREEN":
        print(f"\nBAD: the guard is not GREEN again after restore:\n{out}")
        failures += 1

    print(f"\n{len(CONTROLS) - failures} of {len(CONTROLS)} controls behaved as predicted; "
          "tree restored and sha256-verified.")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
