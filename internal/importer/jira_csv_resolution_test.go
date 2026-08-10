package importer

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/model"
)

// jira_csv_resolution_test.go — the column a Jira CSV export carries that says whether the work was
// FINISHED or ABANDONED, and which jiraRowMapper read for eight merges as if it were not there.
//
// ⚠⚠ THE EVIDENCE WAS ALREADY IN THIS PACKAGE AND THE QUESTION WAS NOT ASKED — #79's shape, one file
// over. jira_csv_dates.go's own header for jiraCSVResolved says, as its argument for why the
// completed_at gate matters on this transport:
//
//	"a Jira CSV export carries `Resolution` for cancelled work too ("Won't Do", "Cannot
//	 Reproduce" — both observed on the real instance), and every one of those rows has a
//	 Resolved date."
//
// That is exactly right, and nobody asked whether the gate ever CATCHES one. It does not: those rows
// are `Status = Closed`, mapJiraStatus maps "closed" to done, the gate passes, and the completion
// time lands on abandoned work. #74's decision — "CompletedAt means FINISHED, not left the board" —
// is not held on this transport, and the file that states the decision is the file that records the
// counter-example.
//
// MEASURED 2026-08-09 against a real Jira (jira.atlassian.com, anonymous REST + the issue-navigator
// "csv-all-fields" export view). NEGATIVE-CONTROLLED FIRST, so no 200 is read as a blanket answer:
// fabricated host ⇒ no resolution · fabricated VIEW on the real host ⇒ 400 text/html · fabricated
// PROJECT in the JQL ⇒ 400 text/html · fabricated REST path on the real host ⇒ 404.
// Re-run it with scripts/w34-jira-csv-resolution-probe.py.
//
//	project JRASERVER, resolved issues                      43,687
//	  ... whose Status maps to Track `done`                 43,587
//	  ... AND whose Resolution says the work was ABANDONED  26,649   ← 61% of the import
//	  ... of those, carrying a Resolved date                26,649   (all of them)
//	issues whose STATUS is "Cancelled" — the only cancellation
//	  signal mapJiraStatus can currently see                     0
//
// So a full CSV import of that project reports {imported:43587, skipped:0, warnings:[]} while 61% of
// the work it recorded as DELIVERED was abandoned, each row carrying a completion time that
// analytics' resolution-stats query counts as throughput and cycle time (it selects on
// `completed_at IS NOT NULL` with NO status predicate). This item's "data loss reported as success"
// shape, TENTH INSTANCE, in the one transport a customer can run without credentials.
//
// ⚠ AND THE CANCELLATION BRANCH mapJiraStatus ALREADY SHIPS IS UNREACHABLE ON THAT INSTANCE.
// `case "cancelled", "canceled", "won't do", "won't fix"` reads the STATUS column; measured against
// /rest/api/2/status, "Won't Do" and "Won't Fix" are not statuses there at all — they are
// RESOLUTIONS (5,373 and 6,498 issues). Track already declares what those words mean. It declares it
// about the wrong column.
//
// ⚠⚠ WHY THE CLASSIFIER IS mapJiraStatus ITSELF AND NOT A NEW TABLE — THE LOAD-BEARING DESIGN
// DECISION. This merge invents NO vocabulary. The resolution word is looked up in Track's OWN
// shipped word→status table, and only three things can happen:
//
//	the word maps to StatusCancelled  ⇒ the row imports as cancelled instead of done, and #74's
//	                                    existing gate then correctly withholds the completion time.
//	                                    REPORTED, naming the path.
//	the word maps to StatusDone       ⇒ it agrees with the status. Nothing changes, nothing is said.
//	the word is not in the table      ⇒ NOTHING CHANGES and it is REPORTED with a count. "Duplicate"
//	                                    (4,938), "Timed out" (3,528), "Obsolete" (1,073) and
//	                                    "Fixed" (13,411) all land here. Which further resolutions
//	                                    mean cancelled is a product decision with real numbers behind
//	                                    it, and a session inventing thirteen mappings is exactly what
//	                                    #73's `undefined` rule and #76's `triage`/`duplicate` refusal
//	                                    exist to prevent. The report puts the numbers in front of a
//	                                    human on the first import instead.
//
// ⚠ IT CAN ONLY EVER MOVE done → cancelled. A row that imported as anything else is untouched by the
// resolution, because a Resolution on a non-resolved row is Jira's own inconsistency and not
// something Track should reinterpret. TestJiraCSVResolution_OnlyEverActsOnADoneRow pins that.

// The measured shape of the export, trimmed to the columns this file is about. Every Status/
// Resolution pair below was OBSERVED on the real instance (see the probe script).
const jiraCSVWithResolutions = "Summary,Description,Status,Priority,Resolution,Resolved\n" +
	"Abandoned as wontfix,d,Closed,High,Won't Fix,23/Mar/2026 4:59 PM\n" +
	"Abandoned as wontdo,d,Closed,High,Won't Do,25/Feb/2026 4:59 PM\n" +
	"Actually finished,d,Closed,High,Done,06/Aug/2026 8:06 PM\n" +
	"Unclassifiable outcome,d,Closed,High,Duplicate,15/Jul/2026 2:34 PM\n" +
	"Still in flight,d,In Progress,High,Won't Fix,17/Jul/2026 6:05 AM\n"

func mappedByTitle(t *testing.T, csv string) map[string]model.Issue {
	t.Helper()
	imp, store := newTestImporter()
	if _, err := imp.ImportJiraCSV(context.Background(), "ws-1", "team-1", strings.NewReader(csv)); err != nil {
		t.Fatalf("ImportJiraCSV: %v", err)
	}
	out := map[string]model.Issue{}
	for _, i := range store.created {
		out[i.Title] = i
	}
	return out
}

func warningsFor(t *testing.T, csv string) []string {
	t.Helper()
	imp, _ := newTestImporter()
	res, err := imp.ImportJiraCSV(context.Background(), "ws-1", "team-1", strings.NewReader(csv))
	if err != nil {
		t.Fatalf("ImportJiraCSV: %v", err)
	}
	return res.Warnings
}

// THE DEFECT ITSELF. Two rows Jira resolved with words Track's own mapper already maps to
// cancelled; both currently import as done, both currently carry a completion time.
func TestJiraCSVResolution_AbandonedWorkDoesNotImportAsDone(t *testing.T) {
	got := mappedByTitle(t, jiraCSVWithResolutions)
	for _, title := range []string{"Abandoned as wontfix", "Abandoned as wontdo"} {
		i, ok := got[title]
		if !ok {
			t.Fatalf("%q did not import at all", title)
		}
		if i.Status != model.StatusCancelled {
			t.Errorf("%q: status = %q, want %q — Jira resolved it with a word Track maps to cancelled",
				title, i.Status, model.StatusCancelled)
		}
	}
}

// THE CONSEQUENCE THAT REACHES ANALYTICS. resolution-stats selects on `completed_at IS NOT NULL`
// with no status predicate, so an abandoned row carrying one is counted as delivered work.
func TestJiraCSVResolution_AbandonedWorkCarriesNoCompletionTime(t *testing.T) {
	got := mappedByTitle(t, jiraCSVWithResolutions)
	for _, title := range []string{"Abandoned as wontfix", "Abandoned as wontdo"} {
		if c := got[title].CompletedAt; c != nil {
			t.Errorf("%q: CompletedAt = %v, want nil — abandoned work is not delivered work", title, c)
		}
	}
}

// A resolution the importer ACTED on is reported, for the reason every Via field in this package
// exists: a silent reinterpretation of a shipped mapping is indistinguishable from a bug.
func TestJiraCSVResolution_TheOverrideIsReported(t *testing.T) {
	ws := warningsFor(t, jiraCSVWithResolutions)
	if !hasLineContaining(ws, `resolution "Won't Fix"`) || !hasLineContaining(ws, `imported as "cancelled"`) {
		t.Errorf("no warning names the override; warnings = %#v", ws)
	}
}

// A resolution Track's vocabulary does not know changes NOTHING and is reported with its count, so
// the decision about it arrives with a number attached instead of being invented here.
func TestJiraCSVResolution_UnclassifiableResolutionIsReportedAndChangesNothing(t *testing.T) {
	got := mappedByTitle(t, jiraCSVWithResolutions)
	i := got["Unclassifiable outcome"]
	if i.Status != model.StatusDone {
		t.Errorf("Duplicate row: status = %q, want %q — an unclassifiable resolution must change nothing",
			i.Status, model.StatusDone)
	}
	if i.CompletedAt == nil {
		t.Errorf("Duplicate row: CompletedAt = nil, want the date — an unclassifiable resolution must change nothing")
	}
	ws := warningsFor(t, jiraCSVWithResolutions)
	if !hasLineContaining(ws, `resolution "Duplicate"`) {
		t.Errorf("no warning names the unclassifiable resolution; warnings = %#v", ws)
	}
}

// A resolution that AGREES with the status says nothing. Silence here is what keeps the report
// readable on the 13,411-issue "Fixed"-shaped case being a single line rather than the whole report.
func TestJiraCSVResolution_AnAgreeingResolutionIsSilent(t *testing.T) {
	ws := warningsFor(t, jiraCSVWithResolutions)
	if hasLineContaining(ws, `resolution "Done"`) {
		t.Errorf("a resolution that agrees with the status must not be reported; warnings = %#v", ws)
	}
	if got := mappedByTitle(t, jiraCSVWithResolutions)["Actually finished"]; got.Status != model.StatusDone || got.CompletedAt == nil {
		t.Errorf("agreeing row changed: status=%q completed=%v", got.Status, got.CompletedAt)
	}
}

// THE SCOPE LIMIT, PINNED. The resolution can only ever move done → cancelled.
func TestJiraCSVResolution_OnlyEverActsOnADoneRow(t *testing.T) {
	i := mappedByTitle(t, jiraCSVWithResolutions)["Still in flight"]
	if i.Status != model.StatusInProgress {
		t.Errorf("in-flight row: status = %q, want %q — a Resolution must not reinterpret a non-done row",
			i.Status, model.StatusInProgress)
	}
}

// A CSV with NO Resolution column must behave byte-identically to before this merge. Every fixture
// in jira_csv_dates_test.go is that shape, so this is the guarantee that nothing already working moved.
func TestJiraCSVResolution_AbsentColumnChangesNothing(t *testing.T) {
	const noResolution = "Summary,Description,Status,Priority,Resolved,Created,Updated\n" +
		"Closed work,d,Closed,High,25/Mar/2025 10:03 AM,23/Jul/2026 7:36 PM,23/Jul/2026 7:36 PM\n"
	i := mappedByTitle(t, noResolution)["Closed work"]
	if i.Status != model.StatusDone {
		t.Errorf("status = %q, want %q", i.Status, model.StatusDone)
	}
	if i.CompletedAt == nil {
		t.Errorf("CompletedAt = nil, want the date — an absent Resolution column must change nothing")
	}
	if ws := warningsFor(t, noResolution); len(ws) != 0 {
		t.Errorf("absent Resolution column produced warnings %#v, want none", ws)
	}
}

// THE LINEAR CSV PATH IS UNTOUCHED, and that is a measurement, not an oversight: nothing in this
// environment can fetch a Linear CSV export (it is produced in-app behind authentication), so
// whether it even HAS a resolution-shaped column is unmeasured. Guessing it is #75's move.
func TestJiraCSVResolution_LinearCSVIsUntouched(t *testing.T) {
	const linearWithResolution = "Title,Description,Status,Priority,Resolution\n" +
		"Linear work,d,Done,High,Won't Fix\n"
	imp, store := newTestImporter()
	if _, err := imp.ImportLinearCSV(context.Background(), "ws-1", "team-1", strings.NewReader(linearWithResolution)); err != nil {
		t.Fatalf("ImportLinearCSV: %v", err)
	}
	if len(store.created) != 1 {
		t.Fatalf("imported %d rows, want 1", len(store.created))
	}
	if got := store.created[0].Status; got != model.StatusDone {
		t.Errorf("Linear row status = %q, want %q — the Jira resolution rule must not reach this transport", got, model.StatusDone)
	}
}

func hasLineContaining(lines []string, want string) bool {
	for _, l := range lines {
		if strings.Contains(l, want) {
			return true
		}
	}
	return false
}

// ── rule 1: source-derived ───────────────────────────────────────────────────────────────────────
//
// THE PROPERTY THAT MAKES THIS MERGE DEFENSIBLE IS THAT IT INVENTS NO VOCABULARY, and a property is
// worth nothing unless something fails when it stops holding. applyJiraResolution classifies by
// asking mapJiraStatus; the moment it grows a word list of its own — one `case "duplicate":` added
// by a future session in a hurry — the argument in the file header becomes false while every
// behavioural test above stays green. This reads the SHIPPED source and refuses that.
//
// ⚠ IT NOW GUARDS TWO TRANSPORTS. The rule was renamed from applyJiraCSVResolution when the Jira
// API transport started calling it, so this is the guard standing between BOTH importers and an
// invented vocabulary — and the 7,214 issues on the measured Cloud instance whose resolutions Track
// deliberately does not read are exactly the pressure that would make somebody add one.
// ⚠⚠ THE SEAM MOVED AND THIS GUARD SPOKE, WHICH IS WHY IT IS EXTENDED RATHER THAN REPOINTED.
// jira_resolution_delivered.go inserted mapJiraResolution between applyJiraResolution and
// mapJiraStatus, and it DOES carry a vocabulary — one word, "fixed". Changing the call this guard
// looks for from mapJiraStatus(raw) to mapJiraResolution(raw) and stopping there would have left the
// vocabulary living somewhere nothing reads: a future `"duplicate": model.StatusCancelled,` added to
// that table would answer the open question silently, which is the exact failure this rule was built
// for. So the rule now holds THREE things, and the third is a PINNED LIST rather than a parse —
// a source-derived absence cannot see a word being ADDED to a map it does not enumerate, and it
// cannot see one being removed either.
func TestSourceDerived_TheResolutionRuleOwnsNoVocabulary(t *testing.T) {
	const file = "jira_csv_resolution.go"
	lits := stringLiteralsIn(t, file, "applyJiraResolution")
	if len(lits) != 0 {
		t.Errorf("%s: applyJiraResolution carries its own word literals %q — the rule must classify "+
			"only through mapJiraResolution, which is what makes it invent no vocabulary inline", file, lits)
	}
	// ⚠ AND IT MUST NOT PASS BY THE FUNCTION HAVING NO SWITCH AT ALL: a rule that only ever asserts
	// an ABSENCE is green on a deleted function. Assert the call it is supposed to be making.
	if !sourceContains(t, file, "mapJiraResolution(raw)") {
		t.Errorf("%s: applyJiraResolution no longer calls mapJiraResolution(raw) — rule 1 was asserting "+
			"an absence and would have stayed green on a rewrite that hardcoded its own table", file)
	}
	// The new link in the chain: the classifier that owns the resolution-only table must itself hold
	// no inline words, and must still fall back to the one table that defines cancellation, so
	// "Won't Fix"/"Won't Do" cannot be forked into a second vocabulary that drifts.
	const deliveredFile = "jira_resolution_delivered.go"
	if lits := stringLiteralsIn(t, deliveredFile, "mapJiraResolution"); len(lits) != 0 {
		t.Errorf("%s: mapJiraResolution carries inline word literals %q — the words belong in the "+
			"pinned table below, where something reads them", deliveredFile, lits)
	}
	if !sourceContains(t, deliveredFile, "mapJiraStatus(raw)") {
		t.Errorf("%s: mapJiraResolution no longer falls back to mapJiraStatus(raw) — the cancellation "+
			"vocabulary would then have two homes, which is the drift this rule exists to prevent",
			deliveredFile)
	}
	// ⚠ THE PINNED HALF. Every word here is one a session decided a real instance's provider calls
	// DELIVERED. Adding one is answering #82's deferred decision; removing one puts 19,698 of 29,512
	// measured issues (66.7%) back under "Track cannot read that word". Either way it must be a
	// deliberate edit to this list and not a quiet edit to that map.
	wantDelivered := map[string]model.IssueStatus{"fixed": model.StatusDone}
	if len(jiraResolutionDelivered) != len(wantDelivered) {
		t.Errorf("jiraResolutionDelivered = %v, want exactly %v — the resolution-only vocabulary "+
			"changed size; that is #82's open decision being answered or unanswered, not a refactor",
			jiraResolutionDelivered, wantDelivered)
	}
	for word, want := range wantDelivered {
		if got, ok := jiraResolutionDelivered[word]; !ok || got != want {
			t.Errorf("jiraResolutionDelivered[%q] = (%q, present=%v), want (%q, present=true)",
				word, got, ok, want)
		}
	}
	for word := range jiraResolutionDelivered {
		if _, ok := wantDelivered[word]; !ok {
			t.Errorf("jiraResolutionDelivered gained %q — a word this list has never measured; "+
				"see jira_resolution_delivered.go for what a new entry has to be justified by", word)
		}
	}
}

// stringLiteralsIn is caseLiteralsOf without its non-empty floor: this guard's whole point is that the
// count is ZERO, so the shared helper's "cannot pass by finding nothing" Fatal is the wrong shape
// here. Duplicated deliberately rather than loosened — status_fidelity_test.go's floor is load-
// bearing for the mappers and weakening it there to serve this file would blind that guard.
// ⚠ IT COLLECTS EVERY STRING LITERAL IN THE BODY, NOT ONLY THOSE IN `case` CLAUSES, AND THAT
// WIDENING WAS FORCED BY A CONTROL RATHER THAN CHOSEN. The first version inspected CaseClause nodes
// only; a control that added `if raw == "Duplicate" { return model.StatusCancelled, nil }` — the
// most natural way a session in a hurry would answer the open question — left it perfectly GREEN.
// A vocabulary is a vocabulary whichever syntax carries it. `""` is excluded by the caller, because
// the empty-value check is a shape test rather than a word.
func stringLiteralsIn(t *testing.T, file, fn string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err) // a broken root is a FAILURE, never an empty pass
	}
	var out []string
	var found bool
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Name.Name != fn {
			continue
		}
		found = true
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			bl, ok := n.(*ast.BasicLit)
			if !ok || bl.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(bl.Value)
			if err != nil {
				t.Fatalf("unquote %s in %s: %v", bl.Value, fn, err)
			}
			if s != "" {
				out = append(out, s)
			}
			return true
		})
	}
	if !found {
		t.Fatalf("%s: function %q not found — this guard reads the shipped source and its root moved", file, fn)
	}
	return out
}

func sourceContains(t *testing.T, file, want string) bool {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	return strings.Contains(string(b), want)
}

// ── rule 2: the measured vocabulary, pinned by hand ──────────────────────────────────────────────
//
// ⚠ RULE 1 CANNOT SEE A DELETION AND THAT IS NOT A THEORY. Deleting `case "won't fix"` from
// mapJiraStatus moves the mapper and any parse of it TOGETHER, so a source-derived rule stays green
// while 6,498 issues on the measured instance silently go back to importing as delivered work.
// #72 proved that by running it and #73 re-proved it; this is the rule that reds.
//
// Every word below was OBSERVED on the real instance (/rest/api/2/resolution, 23 values) with the
// issue count measured by JQL against project JRASERVER. The EXPECTATION is what this merge decided,
// and each of the three classes is represented, so the table cannot be satisfied by a constant.
func TestPinned_TheMeasuredResolutionVocabularyStillClassifiesAsShipped(t *testing.T) {
	type want struct {
		status model.IssueStatus
		note   string // "" ⇒ silent
		issues int    // measured on project JRASERVER among done-mapping statuses
	}
	// MEASURED 2026-08-09. See scripts/w34-jira-csv-resolution-probe.py.
	measured := map[string]want{
		// acted on — Track's own vocabulary already reads these as cancellation
		"Won't Fix": {model.StatusCancelled, viaResolutionCancelled, 6498},
		"Won't Do":  {model.StatusCancelled, viaResolutionCancelled, 5373},
		// agrees with the status — silent
		"Done": {model.StatusDone, "", 274},
		// ⚠ MOVED OUT OF THE "refused" CLASS BELOW, AND THE COUNT IS LEFT AS #82 MEASURED IT because
		// it is a fact about a DIFFERENT instance (project JRASERVER) and is not this session's to
		// restate. It was the largest row in this table and it was filed under "the decision this
		// merge deliberately did not make" — but that decision is whether abandoned-SHAPED work
		// should move done → cancelled, and "Fixed" cannot move anywhere: the provider's own
		// description of it is "A fix for this issue is checked into the tree and tested", the row is
		// already done, and reclassifying it was never on the table. Two questions, one class.
		// See jira_resolution_delivered.go for the whole-population re-measurement on Jira Cloud.
		"Fixed": {model.StatusDone, "", 13411},
		// refused and reported — the decision #82 deliberately did not make, STILL NOT MADE
		"Duplicate":          {model.StatusDone, viaResolutionUnreadable, 4938},
		"Timed out":          {model.StatusDone, viaResolutionUnreadable, 3528},
		"Answered":           {model.StatusDone, viaResolutionUnreadable, 1860},
		"Obsolete":           {model.StatusDone, viaResolutionUnreadable, 1073},
		"Not a bug":          {model.StatusDone, viaResolutionUnreadable, 669},
		"Cannot Reproduce":   {model.StatusDone, viaResolutionUnreadable, 632},
		"Invalid":            {model.StatusDone, viaResolutionUnreadable, 592},
		"Tracked Elsewhere":  {model.StatusDone, viaResolutionUnreadable, 548},
		"Handled by Support": {model.StatusDone, viaResolutionUnreadable, 550},
		"Incomplete":         {model.StatusDone, viaResolutionUnreadable, 113},
	}
	// The floor: all three classes must be present, or a table that collapsed to one outcome would
	// still satisfy every row of itself.
	seen := map[string]int{}
	for raw, w := range measured {
		gotStatus, notes := applyJiraResolution(raw, model.StatusDone)
		if gotStatus != w.status {
			t.Errorf("resolution %q (%d issues measured): status = %q, want %q", raw, w.issues, gotStatus, w.status)
		}
		switch {
		case w.note == "" && len(notes) != 0:
			t.Errorf("resolution %q: reported %#v, want silence", raw, notes)
		case w.note != "" && (len(notes) != 1 || notes[0].Via != w.note):
			t.Errorf("resolution %q: notes = %#v, want exactly one via %q", raw, notes, w.note)
		}
		seen[w.note]++
	}
	for _, class := range []string{"", viaResolutionCancelled, viaResolutionUnreadable} {
		if seen[class] == 0 {
			t.Fatalf("the pinned table no longer exercises the %q class — it can be satisfied by a "+
				"classifier that collapsed to one answer", class)
		}
	}
}
