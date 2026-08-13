#!/usr/bin/env python3
"""w34-corpus-copies-controls.py — positive controls for the corpus-copies merge (tab-a4e9).

WHAT IS BEING CONTROLLED. distinctByContent (jira_csv_date_corpus_helpers_test.go) decides what "an
export" is: the corpus cache is keyed on sha256(repo\\tpath), so the same export committed to two
repositories is two entries, and every census that walks the directory counted them as two
instances. Four CI-runnable cases (corpus_copies_test.go) pin the rule where the corpus is absent;
the date census counts BOTH populations; a new census asserts every pinned layout has a distinct
export behind it. All of that passed on the first run, which is the reason this file exists.

⚠ THE VERDICTS ARE FOUR, NOT TWO. A mutation that did not apply, and a tree that did not compile,
are NOT passes and NOT catches — they are their own verdicts and are printed as such:

    CAUGHT      the predicted test failed, and it is the one named
    NOT CAUGHT  the tree is green with the defect in  (a finding, printed loudly)
    SKIPPED     the anchor did not occur exactly once (arity printed — the mutation never ran)
    NOCOMPILE   the mutation orphaned an import or a symbol; rewrite it, do not score it

Each control: one exact anchor string whose arity in the file is ASSERTED FIRST, applied, the
predicted catcher named BEFORE the run, the package's tests run, the file restored in a finally and
verified by sha256 against the bytes read at start.

    python3 scripts/w34-corpus-copies-controls.py
"""
import hashlib
import pathlib
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
PKG = "./internal/importer/"

HELPERS = ROOT / "internal/importer/jira_csv_date_corpus_helpers_test.go"
DATES = ROOT / "internal/importer/jira_csv_dates.go"
DATES_TEST = ROOT / "internal/importer/jira_csv_dates_test.go"
CENSUS = ROOT / "internal/importer/jira_csv_date_corpus_census_test.go"

# (name, [(file, anchor, replacement), ...], predicted catcher substring, why)
CONTROLS = [
    ("C1 nothing collapses",
     [(HELPERS, "\t\tif seen[k] {\n\t\t\tcontinue\n\t\t}", "\t\tif false {\n\t\t\tcontinue\n\t\t}")],
     "TestDistinctByContent_ByteIdenticalCopiesCollapseToTheFirstName",
     "the whole rule off: three cache entries stay three exports"),

    ("C2 collapse on LENGTH, not content",
     # hex and sum stay referenced: a mutation that orphans an import scores NOCOMPILE, not CAUGHT.
     [(HELPERS, "\t\tk := hex.EncodeToString(sum[:])",
       "\t\tk := hex.EncodeToString(sum[:0]) + string(rune(len(b)))")],
     "TestDistinctByContent_SameLengthDifferentBytesAreTwoExports",
     "the under-report direction: two unrelated exports of equal size become one"),

    ("C3 collapse on NAME",
     [(HELPERS, "\t\tsum := sha256.Sum256(b)", "\t\tsum := sha256.Sum256([]byte(n))\n\t\t_ = b")],
     "TestDistinctByContent_ByteIdenticalCopiesCollapseToTheFirstName",
     "keys on the cache name, which is what already differs between two copies"),

    ("C4 the absent-corpus error stops being IsNotExist",
     [(HELPERS, "\tents, err := os.ReadDir(dir)\n\tif err != nil {\n\t\treturn nil, err\n\t}",
       "\tents, err := os.ReadDir(dir)\n\tif err != nil {\n\t\treturn nil, os.ErrInvalid\n\t}")],
     "TestDistinctByContent_AnAbsentCorpusStillReadsAsNotExist",
     "every census's t.Skip is gated on os.IsNotExist; wrap the error and CI goes hard-red"),

    ("C5 the survivor becomes the LAST name",
     [(HELPERS, "\tsort.Strings(names)", "\tsort.Sort(sort.Reverse(sort.StringSlice(names)))")],
     "TestDistinctByContent_ByteIdenticalCopiesCollapseToTheFirstName",
     "same count, different survivor — a census logging a name would change output for no reason"),

    ("C6 a pinned layout with no export behind it",
     [(DATES, '\t"2/Jan/06",            //     10 (10) —      1 ',
       '\t"2/Jan/2006 15:04:05.000",\n\t"2/Jan/06",            //     10 (10) —      1 '),
      (DATES_TEST, '\t\t"2/Jan/06",', '\t\t"2/Jan/2006 15:04:05.000",\n\t\t"2/Jan/06",')],
     "TestJiraCSVLayoutSupport_EveryPinnedLayoutHasADistinctExportBehindIt",
     "the list test's want is mutated TOO, so only the support census can catch this"),

    ("C7 the new distinct-export floor",
     [(CENSUS, "jiraCSVDateCorpusMinDistinctFiles = 275", "jiraCSVDateCorpusMinDistinctFiles = 999")],
     "TestJiraCSVDateCorpus_TheShippedParserAcceptsWhatRealExportsEmit",
     "proves the deduplicated pass's assertions are reached at all"),

    ("C8 an unreadable entry counted as an export",
     [(HELPERS, "\t\tb, err := os.ReadFile(filepath.Join(dir, n))\n\t\tif err != nil {\n\t\t\tcontinue\n\t\t}",
       "\t\tb, err := os.ReadFile(filepath.Join(dir, n))\n\t\tif err != nil {\n\t\t\tfirst[n] = true\n\t\t\tcontinue\n\t\t}")],
     "TestDistinctByContent_SubdirectoriesAreNotExports",
     "the mechanism that really excludes a directory, now that the IsDir branch is gone"),
]


def sha(p):
    return hashlib.sha256(p.read_bytes()).hexdigest()


def run_tests():
    r = subprocess.run(["go", "test", PKG, "-count=1"], cwd=ROOT,
                       capture_output=True, text=True)
    return r.returncode, r.stdout + r.stderr


def main():
    originals = {}
    for _, edits, _, _ in CONTROLS:
        for f, _, _ in edits:
            originals[f] = (f.read_text(), sha(f))

    print("BASELINE (no mutation) — must be green, or every verdict below is about the baseline")
    code, out = run_tests()
    if code != 0:
        print(out[-4000:])
        print("BASELINE RED — stop. Nothing below would mean anything.")
        return 1
    print("  baseline: EXIT 0\n")

    verdicts = []
    for name, edits, predicted, why in CONTROLS:
        print(f"--- {name}")
        print(f"    predicted catcher: {predicted}")
        print(f"    why: {why}")
        try:
            applied = True
            for f, anchor, repl in edits:
                text = originals[f][0]
                n = text.count(anchor)
                print(f"    anchor occurs {n}x in {f.name}")
                if n != 1:
                    print(f"    SKIPPED — anchor occurs {n}x, the mutation never ran")
                    verdicts.append((name, "SKIPPED"))
                    applied = False
                    break
                f.write_text(text.replace(anchor, repl))
            if not applied:
                continue

            code, out = run_tests()
            if "build failed" in out or "[build failed]" in out or "cannot use" in out or \
               "undefined:" in out or "declared and not used" in out or "imported and not used" in out:
                print("    NOCOMPILE — a third verdict, not a catch. Rewrite the mutation.")
                print("   ", [l for l in out.splitlines() if ".go:" in l][:4])
                verdicts.append((name, "NOCOMPILE"))
                continue
            if code == 0:
                print("    ⚠⚠ NOT CAUGHT — the suite is GREEN with this defect in.")
                verdicts.append((name, "NOT CAUGHT"))
                continue
            failed = sorted({l.split()[-1] for l in out.splitlines()
                             if l.startswith("--- FAIL:") or l.strip().startswith("--- FAIL:")}
                            - {""})
            failed = sorted({l.replace("--- FAIL:", "").strip().split(" ")[0]
                             for l in out.splitlines() if "--- FAIL:" in l})
            hit = any(predicted in f for f in failed)
            print(f"    failing tests: {failed}")
            if hit:
                print("    CAUGHT — by the predicted test")
                verdicts.append((name, "CAUGHT"))
            else:
                print("    CAUGHT BY SOMETHING ELSE — the prediction was wrong, which is its own finding")
                verdicts.append((name, "CAUGHT-OTHER"))
        finally:
            for f, _, _ in edits:
                f.write_text(originals[f][0])
                assert sha(f) == originals[f][1], f"RESTORE FAILED for {f}"
            print(f"    restored, sha256 verified")

    print("\n=== VERDICTS ===")
    for name, v in verdicts:
        print(f"  {v:<12} {name}")
    bad = [v for _, v in verdicts if v not in ("CAUGHT",)]
    print(f"\n{sum(1 for _, v in verdicts if v == 'CAUGHT')}/{len(verdicts)} CAUGHT by the predicted test")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
