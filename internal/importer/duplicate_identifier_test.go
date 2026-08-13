package importer

// duplicate_identifier_test.go — the pipeline half of duplicate_identifier.go, driven through run()
// against a stand-in store so the class is covered where no database and no corpus exist.
// duplicate_identifier_job_test.go is the same finding on real Postgres through the async runner.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/model"
)

// recordingUpserter accepts every write and reports each key as INSERTED the first time and as an
// UPDATE afterwards — the shape UpsertByIdentifier's `RETURNING (xmax = 0)` really has.
//
//	refuse   keys the #71 predicate declines, every time (a human owns that identifier)
//	failOnce keys whose FIRST write fails with an ordinary error — a dropped connection, a
//	         statement timeout — and whose second succeeds
type recordingUpserter struct {
	seen     map[string]bool
	refuse   map[string]bool
	failOnce map[string]bool
	writes   []string
}

func newRecordingUpserter() *recordingUpserter {
	return &recordingUpserter{seen: map[string]bool{}, refuse: map[string]bool{}, failOnce: map[string]bool{}}
}

func (r *recordingUpserter) refusing(keys ...string) *recordingUpserter {
	for _, k := range keys {
		r.refuse[k] = true
	}
	return r
}

func (r *recordingUpserter) failingOnce(keys ...string) *recordingUpserter {
	for _, k := range keys {
		r.failOnce[k] = true
	}
	return r
}

func (r *recordingUpserter) Create(_ context.Context, i model.Issue) (*model.Issue, error) {
	r.writes = append(r.writes, "create:"+i.Title)
	return &i, nil
}

func (r *recordingUpserter) UpsertByIdentifier(_ context.Context, i model.Issue) (*model.Issue, bool, error) {
	if r.refuse[i.Identifier] {
		return nil, false, model.ErrIdentifierNotImportOwned
	}
	if r.failOnce[i.Identifier] {
		delete(r.failOnce, i.Identifier)
		return nil, false, errors.New("write failed: connection reset by peer")
	}
	inserted := !r.seen[i.Identifier]
	r.seen[i.Identifier] = true
	r.writes = append(r.writes, "upsert:"+i.Identifier)
	return &i, inserted, nil
}

func dupWarningsFor(out *ImportResult, key string) []string {
	var got []string
	for _, w := range out.Warnings {
		if strings.Contains(w, "named the issue already written as") && strings.Contains(w, key) {
			got = append(got, w)
		}
	}
	return got
}

func rowWithKey(n int, key, title string) SourceRow {
	return SourceRow{RowNum: n, Issue: model.Issue{Identifier: key, Title: title}}
}

// TestRun_ASecondRowUnderTheSameIdentifierIsReportedOnce — the note fires on the ROWS AFTER the
// first, once per collision, and not at all on a key that arrives once.
func TestRun_ASecondRowUnderTheSameIdentifierIsReportedOnce(t *testing.T) {
	store := newRecordingUpserter()
	imp := New(store)
	src := &sliceSource{rows: []SourceRow{
		rowWithKey(2, "ENG-1", "first"),
		rowWithKey(3, "ENG-1", "second"),
		rowWithKey(4, "ENG-1", "third"),
		rowWithKey(5, "ENG-2", "only"),
	}}

	out, err := imp.run(context.Background(), "ws", "team", src)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// PREMISE: every row landed. A note count is meaningless if rows were refused.
	if out.Imported != 4 || out.Skipped != 0 || out.Refused != 0 {
		t.Fatalf("premise: imported=%d skipped=%d refused=%d, want 4/0/0 (%v)",
			out.Imported, out.Skipped, out.Refused, out.Errors)
	}
	got := dupWarningsFor(out, "ENG-1")
	if len(got) != 1 {
		t.Fatalf("want ONE grouped line for ENG-1, got %d: %q\n(all warnings: %q)", len(got), got, out.Warnings)
	}
	// TWO rows collided with the first, so the line must say 2 — a line that said 1, or that
	// counted the first row too, would be a plausible wrong number.
	if !strings.HasPrefix(got[0], "2 further row(s)") {
		t.Errorf("the line does not count the colliding rows: %q", got[0])
	}
	if extra := dupWarningsFor(out, "ENG-2"); len(extra) != 0 {
		t.Errorf("a key that arrived once was reported as a duplicate: %q", extra)
	}
}

// TestRun_ARowThatDidNotLandDoesNotSeedTheDuplicateReport — the ordering rule stated in run(): the
// identifier is remembered AFTER the write is known to have succeeded. A row that did not land
// wrote nothing and overwrote nothing, so a later row under its key is not a collision.
//
// ⚠ THE FIXTURE IS A FAILED WRITE, NOT A REFUSED ONE, AND THE CONTROL RUN IS WHY. Control C2 first
// moved the bookkeeping ABOVE the error check with only a refusal fixture here, and the test STAYED
// GREEN — because a refusal is decided by the conflicting row's creator/team, which do not change
// during a run, so a key refused once is refused every time and the second row never reaches the
// note. The reachable case is an ORDINARY write failure: a dropped connection on row 2 and a
// success on row 5 of the same key, after which the wrong bookkeeping tells the operator their
// export names that issue twice. Both fixtures are driven below; only the second can go red, and
// saying which is which is the point.
func TestRun_ARowThatDidNotLandDoesNotSeedTheDuplicateReport(t *testing.T) {
	t.Run("refused twice — defence in depth, not reachable today", func(t *testing.T) {
		store := newRecordingUpserter().refusing("ENG-9")
		out, err := New(store).run(context.Background(), "ws", "team", &sliceSource{rows: []SourceRow{
			rowWithKey(2, "ENG-9", "first"),
			rowWithKey(3, "ENG-9", "second"),
		}})
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if out.Refused != 2 || out.Imported != 0 {
			t.Fatalf("premise: refused=%d imported=%d, want 2/0 (%v)", out.Refused, out.Imported, out.Errors)
		}
		if got := dupWarningsFor(out, "ENG-9"); len(got) != 0 {
			t.Errorf("two REFUSED rows were reported as an export naming one issue twice: %q", got)
		}
	})

	t.Run("first write fails, second succeeds — the reachable case", func(t *testing.T) {
		store := newRecordingUpserter().failingOnce("ENG-7")
		out, err := New(store).run(context.Background(), "ws", "team", &sliceSource{rows: []SourceRow{
			rowWithKey(2, "ENG-7", "first attempt"),
			rowWithKey(3, "ENG-7", "second attempt"),
		}})
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		// PREMISE: one row failed and one landed. Without this the assertion could pass on a run
		// where the store never failed at all.
		if out.Skipped != 1 || out.Imported != 1 {
			t.Fatalf("premise: skipped=%d imported=%d, want 1/1 (%v)", out.Skipped, out.Imported, out.Errors)
		}
		if got := dupWarningsFor(out, "ENG-7"); len(got) != 0 {
			t.Errorf("a row whose FIRST write failed was counted as an issue already written, so the "+
				"retry was reported as an export naming one issue twice: %q", got)
		}
	})
}

// TestRun_KeylessRowsAreNeverDuplicates — a row with no provider key takes issue.Store.Create,
// which DERIVES a fresh `<team>-<n>` every time, so two of them cannot collide. The note must not
// fire on the Create branch, and this is the fixture that would catch a `written[""]` bug.
func TestRun_KeylessRowsAreNeverDuplicates(t *testing.T) {
	store := newRecordingUpserter()
	imp := New(store)
	src := &sliceSource{rows: []SourceRow{
		{RowNum: 2, Issue: model.Issue{Title: "keyless one"}},
		{RowNum: 3, Issue: model.Issue{Title: "keyless two"}},
	}}

	out, err := imp.run(context.Background(), "ws", "team", src)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out.Imported != 2 {
		t.Fatalf("premise: imported=%d, want 2 (%v)", out.Imported, out.Errors)
	}
	for _, w := range out.Warnings {
		if strings.Contains(w, "named the issue already written as") {
			t.Errorf("two keyless rows were reported as naming one issue twice: %q", w)
		}
	}
}

// TestFieldNote_DuplicateIdentifierRendersTheArithmetic pins the sentence. It is asserted whole
// rather than by substring because the whole of it is the finding: the identifier (the only handle
// on the issue), which columns were overwritten, which were not, and why `imported` disagrees with
// what the workspace holds.
func TestFieldNote_DuplicateIdentifierRendersTheArithmetic(t *testing.T) {
	n := FieldNote{Field: fieldDuplicateIdentifier, Value: "SRCTREEWIN-13889", Via: viaDuplicateInSameImport}
	const want = `1 further row(s) of this import named the issue already written as "SRCTREEWIN-13889" — ` +
		`an export that names one issue more than once does not create more issues: each later row ` +
		`overwrote the earlier row's title, description and labels and left its status, priority and ` +
		`dates untouched, so the imported count is higher than the number of issues in Track`
	if got := n.render(1); got != want {
		t.Errorf("render:\n got %q\nwant %q", got, want)
	}
}
