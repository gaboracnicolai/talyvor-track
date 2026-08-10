#!/usr/bin/env python3
"""w34-jira-resolution-delivered-controls.py — positive controls for the "Fixed" resolution merge.

Every control below has its CATCHER PREDICTED BEFORE THE RUN and the verdict is read from the
PRINTED ASSERTION MESSAGE, never from a list of test names: a crash, a t.Fatal in an unrelated
premise and a real catch are indistinguishable in a list of names.

Three properties this harness holds itself to, each because this queue has been burned by its
absence:
  · EVERY substitution asserts its anchor appears EXACTLY ONCE before it is applied. A control whose
    pattern matches nothing edits zero bytes and is byte-indistinguishable from a working guard.
  · A BUILD FAILURE IS ITS OWN VERDICT, never a catch. A control that does not compile proves the
    compiler works.
  · Files are restored from SAVED BYTES and sha256-compared, so a control cannot leak into the next.

The run TARGETS THE WHOLE REPO so "which tests spoke" is measured rather than assumed.
"""

import hashlib
import os
import re
import subprocess
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DELIVERED = os.path.join(REPO, "internal/importer/jira_resolution_delivered.go")
JOBTEST = os.path.join(REPO, "internal/importer/jira_resolution_delivered_job_test.go")

# The guards, by name, so a verdict can say WHICH spoke.
G_FIXED = "TestFixedIsDeliveredWorkAndIsNotReported"
G_NODATA = "TestFixedResolutionMovesNoData"
G_DEFERRED = "TestTheDeferredDecisionIsStillDeferred"
G_API = "TestJiraAPITransportAlsoStopsReportingFixed"
G_JOB = "TestJobRow_JiraCSV_FixedIsNotReportedAndStillLandsDelivered"
G_SOURCE = "TestSourceDerived_TheResolutionRuleOwnsNoVocabulary"
G_PINNED = "TestPinned_TheMeasuredResolutionVocabularyStillClassifiesAsShipped"

CONTROLS = [
    dict(
        name="C1 revert: the delivered table is consulted with a key that can never match",
        file=DELIVERED,
        old="jiraResolutionDelivered[strings.ToLower(strings.TrimSpace(raw))]",
        new="jiraResolutionDelivered[strings.ToUpper(strings.TrimSpace(raw))]",
        predict_red=[G_FIXED, G_NODATA, G_API, G_JOB, G_PINNED],
        predict_green=[G_DEFERRED, G_SOURCE],
        why="main's behaviour restored. ⚠ CAUGHT BY FIVE GUARDS AND THEREFORE JUSTIFIES NONE OF "
            "THEM INDIVIDUALLY — stated, not hidden. Its value is the OTHER half: the source-derived "
            "rule stays GREEN, because a table that is present but never hit parses identically to "
            "one that works. That is this guard set's measured limit.",
    ),
    dict(
        name="C2 reclassify: 'fixed' is made to MOVE the row (done -> cancelled)",
        file=DELIVERED,
        old='"fixed": model.StatusDone,',
        new='"fixed": model.StatusCancelled,',
        # ⚠ MY BLAST-RADIUS PREDICTION FOR THIS CONTROL WAS WRONG AND THE TWO EXTRA NAMES ARE KEPT
        # HERE RATHER THAN QUIETLY ABSORBED. I predicted the six guards that mention resolutions.
        # Two MORE spoke — #83's cycle-time job test and #93's resolution-window job test — and the
        # reason is a fact about this package that is worth more than the control: THEIR FIXTURES
        # ALREADY USE `Fixed` AS THE RESOLUTION OF A DELIVERED, DATED, DONE ISSUE
        # (jira_csv_created_job_test.go:55, analytics_window_job_test.go:95, and
        # aicost_null_series_job_test.go:105 too). Three sessions reached for that word to MEAN
        # delivered work while the classifier was telling operators it could not read it. The
        # falsification is in the safe direction — more guards spoke, never fewer — and it is
        # evidence for the merge rather than against it.
        predict_red=[G_FIXED, G_NODATA, G_API, G_JOB, G_SOURCE, G_PINNED,
                     "TestJobRow_JiraCSV_CycleTimeOfAnImportedIssueIsNotNegative",
                     "TestResolutionReport_AnImportedBacklogOlderThanTheWindowIsNotAMeasuredZero"],
        predict_green=[G_DEFERRED],
        why="THE NO-DATA-MOVES INVARIANT IS THE WHOLE ARGUMENT that this is a session call and not "
            "#82's deferred decision. If nothing reds here, that argument is decoration. It also "
            "proves the pinned list checks the VALUE and not merely the key.",
    ),
    dict(
        name="C3 widen: '#82's open decision answered silently ('duplicate' declared delivered)",
        file=DELIVERED,
        old='\t"fixed": model.StatusDone,\n',
        new='\t"fixed": model.StatusDone,\n\t"duplicate": model.StatusDone,\n',
        # ⚠ ALSO UNDER-PREDICTED, AND KEPT WRONG-THEN-CORRECTED FOR THE SAME REASON AS C2. I named
        # the three guards this merge touches; #82's own four — which assert "Duplicate" is reported
        # and changes nothing, on both transports and through Postgres — spoke as well, because
        # declaring `duplicate` delivered is precisely the thing they were built to refuse. The
        # widening is therefore held by SEVEN independent assertions, not three.
        predict_red=[G_DEFERRED, G_SOURCE, G_PINNED,
                     "TestJiraAPIResolution_UnreadableResolutionIsReportedAndChangesNothing",
                     "TestJiraAPIResolution_UnreadableResolutionIsReportedAndChangesNothing/Duplicate",
                     "TestJiraCSVResolution_UnclassifiableResolutionIsReportedAndChangesNothing",
                     "TestJobRow_JiraCSV_AbandonedWorkLandsCancelledAndUndatedInPostgres"],
        predict_green=[G_FIXED, G_NODATA, G_API, G_JOB],
        why="THE CONTROL THE GUARD EXTENSION EXISTS FOR. A session in a hurry answers the deferred "
            "decision by adding one map entry; 8,661 measured issues change meaning. The floor and "
            "the pinned list must both speak, and the four Fixed-specific guards must NOT — a "
            "widening is invisible to a test that only ever asks about 'Fixed'.",
    ),
    dict(
        name="C4 drift: a cancellation word is re-spelled INLINE, behaviour identical",
        file=DELIVERED,
        old="\tmeaning, _ := mapJiraStatus(raw)\n\treturn meaning",
        new="\tif strings.EqualFold(strings.TrimSpace(raw), \"won't fix\") {\n"
            "\t\treturn model.StatusCancelled\n\t}\n\tmeaning, _ := mapJiraStatus(raw)\n\treturn meaning",
        predict_red=[G_SOURCE],
        predict_green=[G_FIXED, G_NODATA, G_DEFERRED, G_API, G_JOB, G_PINNED],
        why="THE MUTATION ONLY ONE GUARD CAN SEE, and it had to be manufactured to be behaviourally "
            "inert: mapJiraStatus already answers cancelled for that word, so EVERY behavioural test "
            "stays green while the cancellation vocabulary quietly acquires a second home. This is "
            "the exact drift #82's rule 1 was built to refuse, at its new address.",
    ),
    dict(
        name="C5 vacuity: the job fixture loses its Fixed row",
        file=JOBTEST,
        old='\t"Fixed row,d,Closed,High,Fixed,06/Aug/2026 8:06 PM\\n" +\n',
        new="",
        predict_red=[G_JOB],
        predict_green=[G_FIXED, G_NODATA, G_DEFERRED, G_API, G_SOURCE, G_PINNED],
        why="THE END-TO-END TEST ASSERTS AN ABSENCE, and an absence is satisfied perfectly by an "
            "import that never saw the row. The PREMISE (imported == 3) must fire instead of the "
            "target assertion quietly passing — so the verdict here is read from the MESSAGE, which "
            "must name the premise and not the warnings list.",
        expect_message_contains="PREMISE",
    ),
    dict(
        name="C6 INVERTED: the same predicate spelled differently — must stay GREEN",
        file=DELIVERED,
        old="strings.ToLower(strings.TrimSpace(raw))",
        new="strings.TrimSpace(strings.ToLower(raw))",
        predict_red=[],
        predict_green=[G_FIXED, G_NODATA, G_DEFERRED, G_API, G_JOB, G_SOURCE, G_PINNED],
        why="Trim-then-lower and lower-then-trim are the same function on every input. If anything "
            "reds here the guards are pinning BYTES rather than behaviour, and the campaign above "
            "would be scoring text edits.",
    ),
]


def sha(path):
    with open(path, "rb") as f:
        return hashlib.sha256(f.read()).hexdigest()[:12]


def run_tests():
    """Whole repo. Returns (build_failed, {test_name: message})."""
    env = dict(os.environ)
    env.setdefault("TRACK_TEST_DATABASE_URL", os.environ.get("TRACK_TEST_DATABASE_URL", ""))
    p = subprocess.run(
        ["go", "test", "./...", "-count=1", "-timeout", "600s"],
        cwd=REPO, env=env, capture_output=True, text=True)
    out = p.stdout + p.stderr
    if "[build failed]" in out or "cannot use" in out or "undefined:" in out or "declared and not used" in out:
        return True, {}
    failures = {}
    current = None
    for line in out.splitlines():
        m = re.match(r"^\s*--- FAIL: (\S+)", line)
        if m:
            current = m.group(1)
            failures.setdefault(current, [])
            continue
        if current and re.match(r"^\s+\S+_test\.go:\d+:", line.strip()) is None and line.strip().startswith("---"):
            current = None
        if current and "_test.go:" in line:
            failures[current].append(line.strip())
    return False, {k: " | ".join(v)[:300] for k, v in failures.items()}


def main():
    if not os.environ.get("TRACK_TEST_DATABASE_URL"):
        print("REFUSING TO RUN: TRACK_TEST_DATABASE_URL unset. A control campaign whose database is "
              "missing scores every real-Postgres guard as 'did not speak'.")
        return 2

    print("=== BASELINE (must be fully green, or every verdict below is unreadable) ===")
    bad, base = run_tests()
    if bad:
        print("BASELINE DOES NOT BUILD — stop.")
        return 2
    if base:
        print("BASELINE IS RED:", base)
        return 2
    print("baseline green\n")

    score = []
    for c in CONTROLS:
        path = c["file"]
        with open(path, "rb") as f:
            original = f.read()
        before = sha(path)
        text = original.decode()

        n = text.count(c["old"])
        if n != 1:
            print(f"!! {c['name']}\n   ANCHOR APPEARS {n} TIMES, want exactly 1 — control NOT APPLIED, "
                  f"scored as INVALID (a no-op control is indistinguishable from a working guard)\n")
            score.append((c["name"], "INVALID-ANCHOR"))
            continue

        with open(path, "w") as f:
            f.write(text.replace(c["old"], c["new"]))
        assert sha(path) != before, "the write did not change the file"

        build_failed, fails = run_tests()

        with open(path, "wb") as f:
            f.write(original)
        assert sha(path) == before, f"RESTORE FAILED for {path}"

        print(f"── {c['name']}")
        print(f"   why: {c['why']}")
        if build_failed:
            print("   VERDICT: BUILD FAILED — this is NOT a catch. The compiler spoke, no guard did.\n")
            score.append((c["name"], "BUILD-FAILED"))
            continue

        red = sorted(fails)
        pred_red, pred_green = sorted(c["predict_red"]), c["predict_green"]
        print(f"   predicted red : {pred_red}")
        print(f"   actually red  : {red}")
        for name in red:
            print(f"     · {name}: {fails[name]}")
        ok = red == pred_red
        for g in pred_green:
            if g in fails:
                ok = False
                print(f"   !! {g} was predicted GREEN and spoke")
        if "expect_message_contains" in c:
            joined = " ".join(fails.values())
            if c["expect_message_contains"] not in joined:
                ok = False
                print(f"   !! the message does not contain {c['expect_message_contains']!r} — the red "
                      f"is not for the reason this control exists")
        if not pred_red and not red:
            print("   VERDICT: STAYED GREEN AS PREDICTED (inverted control)\n")
            score.append((c["name"], "GREEN-AS-PREDICTED"))
        elif ok:
            print("   VERDICT: CAUGHT, BY EXACTLY THE PREDICTED GUARDS\n")
            score.append((c["name"], "CAUGHT-AS-PREDICTED"))
        else:
            print("   VERDICT: PREDICTION FALSIFIED — read the lists above before believing this merge\n")
            score.append((c["name"], "PREDICTION-FALSIFIED"))

    print("=== SCORE ===")
    for n, v in score:
        print(f"  {v:22s} {n}")
    good = sum(1 for _, v in score if v in ("CAUGHT-AS-PREDICTED", "GREEN-AS-PREDICTED"))
    print(f"  {good}/{len(score)} as predicted")
    return 0 if good == len(score) else 1


if __name__ == "__main__":
    sys.exit(main())
