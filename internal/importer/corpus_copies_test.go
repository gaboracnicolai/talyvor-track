package importer

import (
	"os"
	"path/filepath"
	"testing"
)

// corpus_copies_test.go — the half of the corpus-copies finding that RUNS IN CI.
//
// Every corpus census in this package skips when /tmp holds no corpus, and says so loudly. That is
// deliberate and unchanged. But it means the rule the date census now leans on — a byte-identical
// copy is not a second export — would be exercised nowhere CI can see, and the rule is the whole
// reason the deduplicated figures in jira_csv_two_digit_year.go can be believed. These cases build
// their own directory, so the rule is checked everywhere the suite runs and the corpus stays the
// evidence rather than the guard.
//
// ⚠ EVERY CASE NAMES os.ReadDir's OWN COUNT IN THE SAME BREATH. A helper that returned the raw walk
// would satisfy "2 survivors" on a two-file directory and prove nothing; one that returned a
// constant would satisfy it too. So each case states both numbers and requires them to DIFFER where
// collapsing is supposed to bite and to AGREE where it is not.

func writeCorpusFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestDistinctByContent_ByteIdenticalCopiesCollapseToTheFirstName(t *testing.T) {
	dir := t.TempDir()
	// Three cache entries, two of them the same export fetched from a second repository — the exact
	// shape w34-jira-csv-corpus-probe.py produces, because its key is sha256(repo\tpath), not bytes.
	const export = "Issue key,Summary,Status\nPROJ-1,A,Done\n"
	writeCorpusFile(t, dir, "aaa", export)
	writeCorpusFile(t, dir, "bbb", export)
	writeCorpusFile(t, dir, "ccc", "Issue key,Summary,Status\nOTHER-1,B,Done\n")

	raw, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(raw) != 3 {
		t.Fatalf("os.ReadDir = %d entries, want 3 — the fixture is not what this case is about", len(raw))
	}
	got, err := distinctByContent(dir)
	if err != nil {
		t.Fatalf("distinctByContent: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("distinctByContent = %v (%d), want 2 of os.ReadDir's %d — the identical pair is ONE export",
			got, len(got), len(raw))
	}
	// Which copy survives is pinned, so a census that logs a file name does not change its output
	// between runs for no reason.
	if !got["aaa"] || got["bbb"] || !got["ccc"] {
		t.Errorf("distinctByContent = %v, want {aaa, ccc} — the first name in sorted order survives", got)
	}
}

// The direction that would make the helper a lie. If it collapsed on anything WEAKER than the bytes
// — length, say — two unrelated instances that happen to agree on size would be counted as one and
// the census would UNDER-report a real population, which is the same class of error as the
// over-report it exists to fix, pointing the other way.
func TestDistinctByContent_SameLengthDifferentBytesAreTwoExports(t *testing.T) {
	dir := t.TempDir()
	a := "Issue key,Summary,Status\nPROJ-1,A,Done\n"
	b := "Issue key,Summary,Status\nPROJ-2,A,Done\n"
	if len(a) != len(b) {
		t.Fatalf("fixture broken: the two bodies must be the same length (%d vs %d)", len(a), len(b))
	}
	writeCorpusFile(t, dir, "one", a)
	writeCorpusFile(t, dir, "two", b)

	raw, _ := os.ReadDir(dir)
	got, err := distinctByContent(dir)
	if err != nil {
		t.Fatalf("distinctByContent: %v", err)
	}
	if len(got) != 2 || len(raw) != 2 {
		t.Fatalf("os.ReadDir=%d distinctByContent=%v (%d), want 2 and 2 — same length is not same content",
			len(raw), got, len(got))
	}
}

// ⚠ THE SKIP EVERY CENSUS DEPENDS ON MUST SURVIVE THE NEW CALL. Each corpus census answers "no
// corpus here" with t.Skip gated on os.IsNotExist(err). If this helper wrapped the error, an absent
// corpus would stop being a skip and start being a hard failure in CI — the opposite of the posture
// those files argue for, and a red that says nothing about the product.
func TestDistinctByContent_AnAbsentCorpusStillReadsAsNotExist(t *testing.T) {
	_, err := distinctByContent(filepath.Join(t.TempDir(), "definitely-absent"))
	if err == nil {
		t.Fatal("distinctByContent on an absent directory returned no error")
	}
	if !os.IsNotExist(err) {
		t.Fatalf("error = %v, want one os.IsNotExist recognises — every census's skip is gated on "+
			"that predicate", err)
	}

	dir := t.TempDir()
	writeCorpusFile(t, dir, "real", "Issue key,Summary,Status\nPROJ-1,A,Done\n")
	got, err := distinctByContent(dir)
	if err != nil {
		t.Fatalf("distinctByContent on a present directory: %v", err)
	}
	if len(got) != 1 || !got["real"] {
		t.Fatalf("distinctByContent = %v, want {real} — a present corpus must never produce the "+
			"absent-corpus error", got)
	}
}

// A directory inside the cache is not an export, and the census's own e.IsDir() skip must not be the
// only thing saying so — the two counts are compared, so a directory counted on one side and not the
// other would show up as a copy that is not there.
func TestDistinctByContent_SubdirectoriesAreNotExports(t *testing.T) {
	dir := t.TempDir()
	writeCorpusFile(t, dir, "real", "Issue key,Summary,Status\nPROJ-1,A,Done\n")
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	raw, _ := os.ReadDir(dir)
	got, _ := distinctByContent(dir)
	if len(raw) != 2 {
		t.Fatalf("os.ReadDir = %d, want 2 (one file, one directory)", len(raw))
	}
	if len(got) != 1 || !got["real"] {
		t.Errorf("distinctByContent = %v over os.ReadDir's %d entries, want {real}", got, len(raw))
	}
}
