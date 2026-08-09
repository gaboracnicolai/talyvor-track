#!/usr/bin/env python3
"""w34-jira-csv-labels-controls.py — the positive-control campaign behind the repeated-`Labels` fix.

A guard that has never been observed failing is not a guard. Each control below injects ONE defect
into internal/importer/csv.go, runs the suite, and requires:

  1. the anchor to be present EXACTLY as many times as expected, asserted BEFORE the edit
     (#71's lesson: a substitution matching nothing is byte-indistinguishable from a working guard);
  2. the named test to go RED **and to SAY the thing it is supposed to say** — a red for the wrong
     reason is not a catch (#76's C1, and #78's C10, which reddened on a neighbouring t.Fatalf and
     never reached the assertion it was written for);
  3. a COMPANION test to stay GREEN, so nothing passes by breaking the build (#74's C1: a control
     that cannot tell "the guard caught it" from "nothing compiled" is not a control);
  4. the file restored sha256-identical FROM THE ORIGINAL BYTES — never by reverse substitution,
     which is how #76's C7 silently prepended a deleted case above the package comment and turned
     every later verdict into noise.

Multi-edit controls are staged CUMULATIVELY (each edit applied to the result of the last), because
recomputing both from the original text silently discards all but the last write — #99's "a control
that applies half of itself".

    TRACK_TEST_DATABASE_URL=postgres://... python3 scripts/w34-jira-csv-labels-controls.py

The DSN is required: two controls drive the async runner against real Postgres, and testutil FAILS
rather than skips without it, so a missing database would show up as a red for the wrong reason
rather than as a silent green.
"""

import hashlib
import os
import pathlib
import subprocess
import sys

REPO = pathlib.Path(__file__).resolve().parent.parent
TARGET = REPO / "internal/importer/csv.go"

# ── the shipped text each control mutates ───────────────────────────────────────────────
MAPPER_CALL = 'Labels:      splitLabelColumns(ci.getAll(row, "Labels")),'
MAPPER_CALL_OLD = 'Labels:      splitLabels(ci.get(row, "Labels")),'

BUILD_INDEX = """		k := strings.TrimSpace(strings.ToLower(h))
		out[k] = append(out[k], i)"""
BUILD_INDEX_LAST_WINS = """		k := strings.TrimSpace(strings.ToLower(h))
		out[k] = []int{i}"""

GET_FIRST = """	idxs := ci[strings.ToLower(key)]
	if len(idxs) == 0 || idxs[0] >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[idxs[0]])"""
GET_LAST = """	idxs := ci[strings.ToLower(key)]
	if len(idxs) == 0 || idxs[len(idxs)-1] >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[idxs[len(idxs)-1]])"""

GETALL_BODY = """	out := []string{}
	for _, idx := range ci[strings.ToLower(key)] {
		if idx >= len(row) {
			continue
		}
		if v := strings.TrimSpace(row[idx]); v != "" {
			out = append(out, v)
		}
	}
	return out"""
GETALL_FIRST_ONLY = """	out := []string{}
	for _, idx := range ci[strings.ToLower(key)] {
		if idx >= len(row) {
			continue
		}
		if v := strings.TrimSpace(row[idx]); v != "" {
			out = append(out, v)
			break
		}
	}
	return out"""
GETALL_KEEPS_EMPTIES = """	out := []string{}
	for _, idx := range ci[strings.ToLower(key)] {
		if idx >= len(row) {
			continue
		}
		out = append(out, strings.TrimSpace(row[idx]))
	}
	return out"""

SPLIT_COLUMNS = """	out := []string{}
	for _, c := range cells {
		out = append(out, splitLabels(c)...)
	}
	return out"""
SPLIT_COLUMNS_FIRST_ONLY = """	out := []string{}
	for _, c := range cells {
		out = append(out, splitLabels(c)...)
		break
	}
	return out"""
SPLIT_COLUMNS_NIL = """	var out []string
	for _, c := range cells {
		out = append(out, splitLabels(c)...)
	}
	return out"""

# The two mapper call sites are byte-identical, so a control that wants ONE of them anchors on the
# surrounding line instead. linearRowMapper's Labels line is followed by its closing brace + notes.
LINEAR_BLOCK = """			Labels:      splitLabelColumns(ci.getAll(row, "Labels")),
		},
		notes: collectNotes(rawStatus, status, statusOK, statusFallback{}, rawPrio, prio, prioOK),
	}, nil
}"""
LINEAR_BLOCK_REVERTED = """			Labels:      splitLabels(ci.get(row, "Labels")),
		},
		notes: collectNotes(rawStatus, status, statusOK, statusFallback{}, rawPrio, prio, prioOK),
	}, nil
}"""

# ── the campaign ────────────────────────────────────────────────────────────────────────
# edits:      [(old, new, expected_occurrences)] applied CUMULATIVELY
# red:        (test regex, substring the failure output MUST contain)
# green:      test regex that must still pass
CONTROLS = [
    ("C1  the whole fix reverted — index and mapper together (the shipped pre-merge behaviour)",
     [(BUILD_INDEX, BUILD_INDEX_LAST_WINS, 1),
      (GET_FIRST, GET_LAST, 1),
      (MAPPER_CALL, MAPPER_CALL_OLD, 2)],
     ("TestJiraCSVLabels_AnIssueNarrowerThanTheExportKeepsItsLabels", "want [whl-fy27q1 whl-fy27q1-20]"),
     "TestJiraCSVMapper_CapturesTheDueDateColumn"),

    ("C2  the mapper half only — the call sites stop asking for every column",
     [(MAPPER_CALL, MAPPER_CALL_OLD, 2)],
     ("TestJiraCSVLabels_TheWidestRowKeepsEveryLabel", "want [2.4.3 accessibility"),
     "TestJiraCSVMapper_CapturesResolvedOnADoneRow"),

    ("C3  the index half only — buildIndex keeps just the last occurrence again",
     [(BUILD_INDEX, BUILD_INDEX_LAST_WINS, 1)],
     ("TestColumnIndex_GetAllReturnsEveryOccurrenceInHeaderOrder", "want [alpha gamma]"),
     "TestJiraCSVLabels_ASingleCommaJoinedColumnIsUnchanged"),

    ("C4  getAll blinded to everything after the first value",
     [(GETALL_BODY, GETALL_FIRST_ONLY, 1)],
     ("TestJiraCSVLabels_TheWidestRowKeepsEveryLabel", "want [2.4.3 accessibility"),
     "TestJiraCSVLabels_ASingleCommaJoinedColumnIsUnchanged"),

    ("C5  get returns the LAST occurrence again — must red the accessor's own test and NOTHING else",
     [(GET_FIRST, GET_LAST, 1)],
     ("TestColumnIndex_GetNamesTheFirstOccurrenceNotTheLast", 'want "alpha" (the first column of that name)'),
     "TestJiraCSVLabels_TheWidestRowKeepsEveryLabel"),

    ("C6  getAll stops dropping the padding empties — the whole reason its own test exists",
     [(GETALL_BODY, GETALL_KEEPS_EMPTIES, 1)],
     ("TestColumnIndex_GetAllReturnsEveryOccurrenceInHeaderOrder", "want [alpha gamma]"),
     "TestJiraCSVLabels_TheWidestRowKeepsEveryLabel"),

    ("C7  splitLabelColumns stops concatenating past the first cell",
     [(SPLIT_COLUMNS, SPLIT_COLUMNS_FIRST_ONLY, 1)],
     ("TestJiraCSVLabels_TwoColumnsAreBothRead", "want [whl-fy27q1 whl-fy27q1-20]"),
     "TestJiraCSVLabels_ASingleCommaJoinedColumnIsUnchanged"),

    ("C8  splitLabelColumns returns nil instead of an empty slice — the JSON `[]` promise",
     [(SPLIT_COLUMNS, SPLIT_COLUMNS_NIL, 1)],
     ("TestJiraCSVLabels_AbsentAndEmptyStayEmptyNotNil", "Labels is nil"),
     "TestJiraCSVLabels_TheWidestRowKeepsEveryLabel"),

    ("C9  the fix applied to Jira only — Linear left on the collapsing accessor",
     [(LINEAR_BLOCK, LINEAR_BLOCK_REVERTED, 1)],
     ("TestLinearCSVLabels_TheSharedIndexTreatsBothTransportsAlike", "want [alpha beta]"),
     "TestJiraCSVLabels_TheWidestRowKeepsEveryLabel"),

    ("C10 the mapper half reverted, measured AT THE DATABASE through the async runner",
     [(MAPPER_CALL, MAPPER_CALL_OLD, 2)],
     ("TestJobRow_JiraCSV_EveryRepeatedLabelColumnLandsInPostgres", "labels in Postgres"),
     "TestJobRow_JiraCSV_SingleAndAbsentLabelColumnsAreUnchanged"),
]


def run(pattern):
    r = subprocess.run(
        ["go", "test", "-count=1", "./internal/importer/", "-run", pattern],
        cwd=REPO, capture_output=True, text=True, env=os.environ,
    )
    return r.returncode, r.stdout + r.stderr


def main():
    if not os.environ.get("TRACK_TEST_DATABASE_URL"):
        print("TRACK_TEST_DATABASE_URL is unset — C10 drives real Postgres and would red for the "
              "wrong reason. Set it and re-run.")
        return 2

    original = TARGET.read_bytes()
    original_sha = hashlib.sha256(original).hexdigest()
    print(f"csv.go sha256 {original_sha[:16]}  ({len(original)} bytes)\n")

    # The suite must be GREEN before any control runs, or every verdict below is unreadable.
    code, out = run("Label|TestColumnIndex|TestJiraCSVMapper")
    if code != 0:
        print("the suite is not green before the campaign — nothing below would mean anything")
        print(out[-2000:])
        return 1
    print("baseline: green\n")

    caught = 0
    for name, edits, (red_test, must_say), green_test in CONTROLS:
        text = original.decode()
        ok = True
        for old, new, want_n in edits:                 # CUMULATIVE, never recomputed from original
            n = text.count(old)
            if n != want_n:
                print(f"{name}\n     ⚠ ANCHOR COUNT {n}, EXPECTED {want_n} — NOT RUN (a no-op control "
                      f"is byte-indistinguishable from a working guard)")
                ok = False
                break
            text = text.replace(old, new, want_n)
        if not ok:
            continue

        TARGET.write_bytes(text.encode())
        try:
            red_code, red_out = run(red_test)
            green_code, _ = run(green_test)
        finally:
            TARGET.write_bytes(original)
            if hashlib.sha256(TARGET.read_bytes()).hexdigest() != original_sha:
                print("⚠ RESTORE FAILED — stopping before the next control mutates a corrupt file")
                return 1

        said = must_say in red_out
        verdict = ("CAUGHT" if (red_code != 0 and said and green_code == 0) else "NOT CAUGHT")
        caught += verdict == "CAUGHT"
        print(f"{name}\n     {verdict}: {red_test} {'RED' if red_code else 'GREEN'}"
              f" · says-the-thing {'yes' if said else 'NO'}"
              f" · companion {green_test} {'green' if green_code == 0 else 'RED'}")
        if verdict == "NOT CAUGHT":
            print("     " + "\n     ".join(l for l in red_out.splitlines() if "want" in l or "FAIL" in l)[:600])

    print(f"\n{caught}/{len(CONTROLS)} caught · csv.go restored sha256-identical "
          f"({hashlib.sha256(TARGET.read_bytes()).hexdigest()[:16]})")
    return 0 if caught == len(CONTROLS) else 1


if __name__ == "__main__":
    sys.exit(main())
