#!/usr/bin/env python3
"""w34-via-render-population-controls.py — the positive-control campaign for
TestFieldNoteRender_EveryDeclaredViaHasItsOwnSentence.

WHY IT EXISTS, AND WHY IT IS THE WHOLE PROOF. The guard PASSED ON ITS FIRST RUN: all 34 `via*`
constants already have a case in FieldNote.render, so no red-then-green sequence is available and
"the tests are green" says nothing at all about whether the guard can ever fail. A source-reading
guard has three separate ways to be inert and every one of them looks exactly like a clean package:

  · the reader is blind to a FILE (it would still clear a floor and still assert on what it saw)
  · the reader is blind to a declaration SHAPE (csv.go's grouped `const (…)` block vs the lone
    `const viaX = "…"` beside the code that produces it)
  · the comparison is against the wrong string (identifiers instead of values), in which case every
    via looks like the default arm — or none does, depending on which way it is wrong

So each control below breaks something on purpose and NAMES both the test it expects to red AND a
substring the failure must contain, because every control here reds the SAME test name: without the
message check, "CAUGHT" would only mean "something inside that test fired", which is exactly the
resolution these controls exist to have.

The reading rules are the ones the other w34 controls scripts wrote down and this file follows:

  · the package runs with -v and the verdict is the SET OF FAILING TEST NAMES, not an exit code
  · a mutation that stops the package COMPILING scores ERROR, not CAUGHT (a build failure proves
    the edit landed, not that the guard works)
  · every anchor must appear exactly once and the bytes must actually change before any test runs
  · restore happens in a `finally` and every touched file's sha256 is compared to the pre-run value
  · N1 is a NEGATIVE control: it changes bytes and must leave everything GREEN, which is what says
    the harness is scoring the mutation and not merely the act of editing

    python3 scripts/w34-via-render-population-controls.py          # all
    python3 scripts/w34-via-render-population-controls.py C2 N1    # a subset

No TRACK_TEST_DATABASE_URL: the three tests driven here are pure renderers and read no database.
"""
import hashlib
import os
import re
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CSV = os.path.join(ROOT, "internal/importer/csv.go")
GUARD_FILE = os.path.join(ROOT, "internal/importer/via_render_population_test.go")
NEW_FILE = os.path.join(ROOT, "internal/importer/zz_control_via_3e6a.go")

# The guard this campaign is about, and the two neighbours whose population it widens. All three are
# run every time so a control that reds the wrong one is visible rather than invisible.
GUARD = "TestFieldNoteRender_EveryDeclaredViaHasItsOwnSentence"
SIBLING = "TestFieldNoteRender_EveryCreatedViaHasItsUpdatedTwin"
INSTANCE = "TestFieldNoteRender_TheAPIUpdatedWarningsAreNotTheDefaultSentence"
RUN = "|".join([GUARD, SIBLING, INSTANCE])

# The line every declaration-reading control edits: the file filter inside declaredVias.
FILTER = '\t\tif e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {'

CONTROLS = {
    "C1": dict(
        file=CSV,
        why="disable the arm for a via declared as a LONE `const` in its own file (adf_attrs.go). "
            "If the reader never opens that file this control cannot be felt, so it is the one that "
            "proves the second declaration shape is really reached.",
        anchor="\tcase n.Via == viaADFNodeDropped:",
        replacement='\tcase n.Via == "zz-c1-disabled-3e6a":',
        expect={GUARD},
        expect_msg="viaADFNodeDropped",
    ),
    "C2": dict(
        file=NEW_FILE,
        create='package importer\n\n'
               '// A fabricated via with no arm in FieldNote.render — the exact regression this guard\n'
               '// exists for, in the exact shape it arrives in: a new constant beside new producing code.\n'
               'const viaFabricatedControl3e6a = "fabricated-control-3e6a"\n',
        why="THE REGRESSION ITSELF: a brand-new via constant, in a brand-new file, with no case of "
            "its own. This is what #141 was and what a hand-written list can never see.",
        expect={GUARD},
        expect_msg="viaFabricatedControl3e6a",
    ),
    "C3": dict(
        file=GUARD_FILE,
        why="blind the reader to ONE file (adf_attrs.go). 33 constants still clear the floor, so "
            "nothing else in the guard notices — this is the control that says the two anchors are "
            "load-bearing and not decoration.",
        anchor=FILTER,
        replacement=FILTER[:-2] + ' || n == "adf_attrs.go" {',
        expect={GUARD},
        expect_msg="did not find viaADFNodeDropped",
    ),
    "C3b": dict(
        file=GUARD_FILE,
        why="blind the reader to everything except csv.go — the shape where a reader still sees 25 "
            "of 34 vias and looks entirely healthy. The FLOOR is the only thing that can catch it.",
        anchor=FILTER,
        replacement=FILTER[:-2] + ' || n != "csv.go" {',
        expect={GUARD},
        expect_msg="floor",
    ),
    "C4": dict(
        file=CSV,
        why="disable the arm for a via declared in csv.go's GROUPED const block. C1's twin for the "
            "other declaration shape; together they say the reader is not shape-blind either way.",
        anchor="\tcase n.Via == viaShortRow:",
        replacement='\tcase n.Via == "zz-c4-disabled-3e6a":',
        expect={GUARD},
        expect_msg="viaShortRow",
    ),
    "C5": dict(
        file=os.path.join(ROOT, "internal/importer/linear_csv_dates.go"),
        why="give two constants the SAME string. Both then render a real sentence, so the "
            "falls-through-to-default assertion stays green and only the uniqueness check can see "
            "it — a via whose arm is shadowed by an earlier one is handled by every name-counting "
            "census and unreachable in production.",
        anchor='const viaNoLinearCreatedColumn = "no-Linear-Created-column"',
        replacement='const viaNoLinearCreatedColumn = "no-Created-column"',
        expect={GUARD},
        expect_msg="are both",
        # ⚠ AND THE OTHER ASSERTION MUST STAY QUIET. If the fallthrough check ALSO fired, this
        # control would prove nothing about the uniqueness check — that is the whole claim.
        forbid_msg="has NO case of its own",
    ),
    "C6": dict(
        file=CSV,
        why="disable an arm that IS in the sibling guard's hand-written pair list. Predicts BOTH "
            "tests red: it is what says this guard is strictly wider than the list rather than a "
            "differently-worded copy of it, and that the sibling is still armed.",
        anchor="\tcase n.Via == viaNoCreatedColumn:",
        replacement='\tcase n.Via == "zz-c6-disabled-3e6a":',
        expect={GUARD, SIBLING},
        expect_msg="viaNoCreatedColumn",
    ),
    "N1": dict(
        file=CSV,
        why="NEGATIVE CONTROL: change bytes without changing behaviour. Everything must stay GREEN. "
            "Without this, 'the tests went red' could just mean 'the file was touched'.",
        anchor="\tcase n.Via == viaADFNodeDropped:",
        replacement="\t// n1 negative control: bytes changed, behaviour unchanged\n\tcase n.Via == viaADFNodeDropped:",
        expect=set(),
        expect_msg=None,
    ),
}


def sha(path):
    if not os.path.exists(path):
        return "ABSENT"
    return hashlib.sha256(open(path, "rb").read()).hexdigest()


def go_test():
    """Returns (failing set, pass count, combined output, build error or None)."""
    p = subprocess.run(
        ["go", "test", "-run", RUN, "-count=1", "-v", "./internal/importer/"],
        cwd=ROOT, capture_output=True, text=True,
    )
    out = p.stdout + p.stderr
    if "[build failed]" in out or "build failed" in out or "undefined:" in out:
        return set(), 0, out, out
    failing = set(re.findall(r"^--- FAIL: (\S+)", out, re.M))
    passing = len(re.findall(r"^--- PASS: (\S+)", out, re.M))
    return failing, passing, out, None


def main():
    wanted = [a for a in sys.argv[1:] if a in CONTROLS] or list(CONTROLS)

    if os.path.exists(NEW_FILE):
        sys.exit(f"{NEW_FILE} already exists — C2 would clobber it")

    base_fail, base_pass, _, err = go_test()
    if err:
        sys.exit("baseline does not build:\n" + err[-2000:])
    print(f"baseline: {len(base_fail)} failing, {base_pass} passing")
    if base_fail:
        sys.exit(f"baseline is not green ({sorted(base_fail)}) — controls would be meaningless")
    if base_pass < 3:
        sys.exit(f"baseline ran only {base_pass} tests — the -run filter matched less than the three "
                 f"named tests, so every control below would be scored against nothing")

    scores = {}
    for name in wanted:
        c = CONTROLS[name]
        target = c["file"]
        before = sha(target)
        original = None if "create" in c else open(target, encoding="utf-8").read()
        try:
            if "create" in c:
                open(target, "w", encoding="utf-8").write(c["create"])
            else:
                n = original.count(c["anchor"])
                if n != 1:
                    sys.exit(f"{name}: anchor appears {n} times in {os.path.basename(target)}, want 1")
                mutated = original.replace(c["anchor"], c["replacement"], 1)
                if mutated == original:
                    sys.exit(f"{name}: the edit changed no bytes")
                open(target, "w", encoding="utf-8").write(mutated)
            failing, passing, out, err = go_test()
        finally:
            if "create" in c:
                if os.path.exists(target):
                    os.remove(target)
            else:
                open(target, "w", encoding="utf-8").write(original)
            if sha(target) != before:
                sys.exit(f"{name}: tree NOT restored ({target})")

        if err:
            scores[name] = "ERROR (did not compile — proves the edit landed, not that the guard works)"
        elif failing != c["expect"]:
            if not failing and not c["expect"]:
                scores[name] = f"GREEN as predicted ({passing} passing) — the harness is not scoring the edit itself"
            elif not failing:
                scores[name] = "NOT CAUGHT — every test stayed green with the guard's subject broken"
            else:
                scores[name] = f"WRONG PREDICTION: red = {sorted(failing)}, predicted {sorted(c['expect'])}"
        elif not c["expect"]:
            # A control that predicted NO reds is a negative control, and calling that "CAUGHT"
            # would report the harness catching something when the point is that it caught nothing.
            scores[name] = f"GREEN as predicted ({passing} passing) — the harness scores the mutation, not the edit"
        elif c.get("forbid_msg") and c["forbid_msg"] in out:
            scores[name] = (f"WRONG ASSERTION: the failure also mentions {c['forbid_msg']!r}, so more "
                            f"than the predicted check fired")
        elif c["expect_msg"] and c["expect_msg"] not in out:
            scores[name] = (f"WRONG ASSERTION: {sorted(failing)} red, but the failure never mentions "
                            f"{c['expect_msg']!r} — a different check inside the test fired")
        else:
            scores[name] = f"CAUGHT by exactly {sorted(failing)} ({passing} still passing)"
        print(f"  {name}: {scores[name]}\n      {c['why']}")

    ok = sum(1 for v in scores.values() if v.startswith("CAUGHT") or v.startswith("GREEN as predicted"))
    print(f"\n{ok}/{len(wanted)} controls scored as predicted")
    sys.exit(0 if ok == len(wanted) else 1)


if __name__ == "__main__":
    main()
