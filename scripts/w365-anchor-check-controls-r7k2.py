#!/usr/bin/env python3
"""W3.65 — controls for `JobPin` and for `w634-toolchain-pin-controls.py --check-anchors`.

Both are new and both PASSED ON THEIR FIRST RUN, which this repo's standing rule says to suspect.

⚠ B7 IS THE ONE THAT MATTERS AND IT GUARDS A BLIND SPOT THE CAMPAIGN ITSELF HAS.
TestCIGoVersionPinsAreAtLeastTheToolchainFloor reds if ANY pin in ci.yaml is below the floor. So a
JobPin that resolved to the WRONG JOB would still make its control report CAUGHT — the campaign
cannot tell which job was mutated, and a per-job control that silently targets one job three times
would look exactly like three per-job controls. B7 asserts the targeting directly, by diffing the
file: mutating job X must change X's pin and leave the other two byte-identical.

⚠ B4 IS INVERTED: an untouched tree must stay GREEN. A check that reds on correct work gets
relaxed until it reds on nothing.

⚠ B5 DEFENDS THE CI STEP'S PREMISE — `--check-anchors` is in CI only because it mutates nothing and
runs no `go test`.

Mutates tracked files, restores them in a `finally` with sha256 compared, and installs a signal
handler — a `finally` does not run on SIGTERM (scripts/check-restore-signal-handlers.py).
"""
import hashlib
import importlib.util
import os
import pathlib
import re
import signal
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
CI = ROOT / ".github/workflows/ci.yaml"
CAMPAIGN = ROOT / "scripts/w634-toolchain-pin-controls.py"


def restore_on_signal(snapshot: dict) -> None:
    """Put every snapshotted file back, then die of the signal we were sent."""

    def handler(signum, _frame):
        for path, blob in snapshot.items():
            try:
                path.write_bytes(blob)
            except OSError:
                pass
        sys.stderr.write("\n!! signal %d — restored %d mutated file(s) before exiting\n"
                         % (signum, len(snapshot)))
        signal.signal(signum, signal.SIG_DFL)
        os.kill(os.getpid(), signum)

    for s in (signal.SIGTERM, signal.SIGINT, signal.SIGHUP):
        signal.signal(s, handler)


def sha(p: pathlib.Path) -> str:
    return hashlib.sha256(p.read_bytes()).hexdigest()


def check() -> tuple[bool, str]:
    r = subprocess.run([sys.executable, str(CAMPAIGN), "--check-anchors"],
                       cwd=ROOT, capture_output=True, text=True)
    return r.returncode == 0, r.stdout + r.stderr


def load_jobpin():
    """Import JobPin from the campaign WITHOUT running it.

    The campaign ends in `sys.exit(...)`, so it cannot simply be imported. Reading the source and
    exec'ing only up to the CONTROLS table would be fragile; instead the class is re-parsed from
    the file by slicing it out, which fails loudly if the class is renamed rather than silently
    testing nothing.
    """
    src = CAMPAIGN.read_text()
    start = src.index("class JobPin:")
    end = src.index("\ndef anchored(", start)
    ns: dict = {"re": re}
    exec(compile(src[start:end], str(CAMPAIGN), "exec"), ns)
    return ns["JobPin"]


PIN_RE = re.compile(r'(?m)^\s*go-version:\s*"([^"]+)"$')


def pins_by_job(text, jobs, JobPin):
    """The version string each named job pins, so a mutation can be attributed to one job."""
    out = {}
    for j in jobs:
        a, b = JobPin(j, "x")._span(text)
        found = PIN_RE.findall(text[a:b])
        out[j] = found[0] if len(found) == 1 else "?%d" % len(found)
    return out


def main() -> int:
    JobPin = load_jobpin()
    JOBS = ["lint", "vuln", "test"]
    originals = {p: p.read_bytes() for p in (CI, CAMPAIGN)}
    hashes = {p: sha(p) for p in originals}
    restore_on_signal(originals)

    ok, out = check()
    if not ok:
        print("BASELINE IS NOT GREEN — every verdict below would be unreadable:\n" + out)
        return 2
    print("baseline: GREEN\n")

    ci_src = originals[CI].decode()
    results = []
    try:
        # ── B7: does JobPin target the job it names? ──────────────────────────────────────
        base = pins_by_job(ci_src, JOBS, JobPin)
        if sorted(set(base.values())) != [base["lint"]] or "?" in "".join(base.values()):
            results.append(("B7 JobPin targets the job it names", "CONTROL DEFECT",
                            f"jobs do not each hold exactly one identical pin: {base}"))
            print("  [CONTROL DEFECT] B7")
        else:
            bad = []
            for target in JOBS:
                after = pins_by_job(JobPin(target, "9.99").apply(ci_src), JOBS, JobPin)
                moved = [j for j in JOBS if after[j] != base[j]]
                if moved != [target]:
                    bad.append(f"mutating {target!r} changed {moved}")
            verdict = "OK" if not bad else "!! WRONG JOB"
            results.append(("B7 JobPin targets the job it names", verdict,
                            "each of lint/vuln/test mutated alone, the other two byte-identical"
                            if not bad else "; ".join(bad)))
            print(f"  [{verdict:16s}] B7 JobPin targets the job it names")

        # ── B1..B4: the anchor check must be able to fail ─────────────────────────────────
        CASES = [
            ("B1 a job grows a SECOND go-version pin", CI,
             '          go-version: "1.26.6"\n          cache: false\n',
             '          go-version: "1.26.6"\n          go-version: "1.26.6"\n          cache: false\n',
             True, "the exact way V7 went inert, now scoped so it names the job"),
            ("B2 a pinned job is renamed", CI, "\n  vuln:\n", "\n  vulnerability:\n", True,
             "a job that no longer exists must RAISE, not resolve to something arbitrary"),
            ("B3 VACUITY: the control list shrinks below the floor", CAMPAIGN,
             "ANCHOR_FLOOR = 10", "ANCHOR_FLOOR = 10\nCONTROLS = CONTROLS[:2]", True,
             "a loop over a shrunken list reports clean anchors rather than a missing campaign"),
            ("B4 INVERTED: an untouched tree must stay green", CI,
             "\n  vuln:\n", "\n  vuln:\n", False,
             "a check that reds on correct work gets relaxed until it reds on nothing"),
        ]
        for name, path, find, repl, predict_red, why in CASES:
            src = originals[path].decode()
            if src.count(find) != 1:
                results.append((name, "CONTROL DEFECT",
                                f"needle occurs {src.count(find)}x, want 1 — probe lands nowhere"))
                print(f"  [CONTROL DEFECT] {name}")
                continue
            try:
                path.write_text(src.replace(find, repl, 1))
                passed, _ = check()
            finally:
                path.write_bytes(originals[path])
            red = not passed
            verdict = "OK" if red == predict_red else ("!! BLIND" if not red else "!! REDS ON CORRECT CODE")
            results.append((name, verdict,
                            f"{'RED' if red else 'green'} (predicted {'RED' if predict_red else 'green'}) — {why}"))
            print(f"  [{verdict:16s}] {name}: {'RED' if red else 'green'}")

        # ── B5: the premise the CI step rests on ──────────────────────────────────────────
        pre = {p: sha(p) for p in (CI, CAMPAIGN)}
        check()
        moved = [p.name for p in (CI, CAMPAIGN) if sha(p) != pre[p]]
        verdict = "OK" if not moved else "!! MUTATES THE TREE"
        results.append(("B5 --check-anchors mutates nothing", verdict,
                        ("no watched file changed" if not moved else "CHANGED: " + ",".join(moved))
                        + " — the CI step is only safe because this holds"))
        print(f"  [{verdict:16s}] B5 --check-anchors mutates nothing")

        # ── B6: the CI step this merge adds must not itself look like a pin ───────────────
        r = subprocess.run(["go", "test", "-count=1", "-run",
                            "^TestCIGoVersionPinsAreAtLeastTheToolchainFloor$", "./internal/migrate/"],
                           cwd=ROOT, capture_output=True, text=True)
        verdict = "OK" if r.returncode == 0 else "!! NEW COMMENT PARSES AS A PIN"
        results.append(("B6 the new CI comment is not read as a pin", verdict,
                        "the added step's prose mentions go-version; the guard must still pass"))
        print(f"  [{verdict:16s}] B6 the new CI comment is not read as a pin")
    finally:
        for p, b in originals.items():
            p.write_bytes(b)
        bad = [p.name for p in originals if sha(p) != hashes[p]]
        print("\nrestore: " + ("BYTE-IDENTICAL" if not bad else "MISMATCH " + ",".join(bad)))

    ok, out = check()
    print("post-control baseline: " + ("GREEN" if ok else "RED\n" + out))
    defects = [r for r in results if r[1] != "OK"]
    print(f"\n{len(results)-len(defects)}/{len(results)} controls behaved as predicted")
    for n, v, d in results:
        print(f"  [{v}] {n}: {d}")
    return 1 if defects or not ok else 0


sys.exit(main())
