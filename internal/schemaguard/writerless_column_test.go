package schemaguard

// NO COLUMN MAY BE READ ON A SURFACE WHEN NOTHING WRITES IT.
//
// A column that no production statement ever SETS does not hold data. If it has
// no DEFAULT it is NULL for every row that has ever existed; if it has a
// constant DEFAULT it holds that constant forever. Either way a filter on it
// matches a fixed set, an aggregate over it returns a fixed number, and an API
// field carrying it ships a constant dressed as a measurement.
//
// THIS REPOSITORY HAS ALREADY WRITTEN THAT DEFECT TWICE:
//
//	guests.last_seen_at   — migration 0014, no DEFAULT, SELECTed, scanned, serialized
//	                        `omitempty`, and declared in the SPA's own contract at
//	                        frontend/src/api/types.ts. Four statements touch the table
//	                        and none writes it. Found by hand; see
//	                        internal/guest/last_seen_at_writerless_realpg_test.go.
//	members.avatar_url    — migration 0001, TEXT NOT NULL DEFAULT '', so it is the
//	                        empty string for every member row that has ever existed.
//	                        Returned by the member roster API, by the MCP list_members
//	                        tool and by the analytics workload rollup. Found by this
//	                        census, which is why this file exists.
//
// Fixing an instance does not stop the next one: the columns stay in the schema,
// so the next person to write a query sees them in `\d members` and uses them.
// Enumerating them by hand does not stop it either — both instances above were
// found by accident, years apart. So this guard DERIVES the population instead:
// the shipped schema comes from a real migrated database and the writes come
// from the real Go tree, and every column that falls out is either accounted for
// here with a reason or it reds this test.
//
// ⚠ THIS GUARD ADDS NO PERMISSION AND REMOVES NONE. It changes no behaviour, no
// threshold and no API. It records what is true today and reds when that moves.

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/testutil"
)

// repoRoot is this package's directory relative to the module root, inverted.
const repoRoot = "../.."

// productionDirs are the trees a shipped binary is built from. Tests are
// excluded deliberately: a column written only by a test is not written.
var productionDirs = []string{"internal", "cmd"}

// ---------------------------------------------------------------------------
// DECLARED ACCOUNTS
//
// Every column the census cannot see a writer for must appear in exactly one of
// the two maps below, with a reason a human had to type. An unaccounted column
// reds this test. The two maps fail in opposite directions and both are checked:
// a stale dynamicBuilder entry hides a real writerless column, and a stale
// writerless entry accuses a column that has since been given a writer.
// ---------------------------------------------------------------------------

// dynamicBuilders are columns written by Go that assembles a SET clause at run
// time. A census of SQL TEXT structurally cannot see these — the column name is
// a map key or a format argument, not part of any literal.
//
// Each entry names the file and the source anchor that does the writing, and the
// test asserts the anchor is still there. That matters more than it looks: if a
// builder stops naming the column, the column becomes genuinely writerless and
// this entry would otherwise go on quietly excusing it forever.
var dynamicBuilders = map[string]dynamicBuilder{
	"milestones.completed_at": {
		file:   "internal/milestone/store.go",
		anchor: `"name": {}, "description": {}, "status": {}, "target_date": {}, "completed_at": {},`,
		why: "internal/milestone Store.Update builds `SET k = $n` for every key in the " +
			"`updatable` allow-list. Reachable over the wire: the handler decodes an " +
			"arbitrary map[string]any from the PATCH body and passes it straight through.",
	},
	"issue_templates.updated_at": {
		file:   "internal/template/store.go",
		anchor: `set = append(set, fmt.Sprintf("updated_at = $%d", n))`,
		why: "internal/template Store.Update appends `updated_at = $n` to the SET clause " +
			"unconditionally on every update, outside the caller-supplied fields.",
	},
}

type dynamicBuilder struct {
	file   string
	anchor string
	why    string
}

// writerless are columns MEASURED to have no writer at all: not an INSERT column
// list, not a SET clause, not a dynamic builder, not a COPY, not a migration
// backfill. They hold their DEFAULT — or NULL — for every row that will ever
// exist.
//
// An entry here is a statement about the CURRENT writers, not a permanent ban.
// Give one a real writer and this test tells you to delete its entry.
var writerless = map[string]string{
	"guests.last_seen_at": "No DEFAULT, so NULL forever. The complete set of statements against " +
		"`guests` is the INSERT at internal/guest/store.go:320 and the deactivating UPDATE at " +
		":410, and neither names it. Still SELECTed, scanned, serialized and declared in the " +
		"SPA contract. See internal/guest/last_seen_at_writerless_realpg_test.go.",

	"members.avatar_url": "DEFAULT '' and NOT NULL, so the empty string forever. The complete " +
		"set of statements against `members` is five — INSERT at internal/workspace/store.go:152, " +
		"INSERT at internal/member/mgmt.go:71, UPDATE SET role at :118, DELETE at :158 and a " +
		"SELECT ... FOR UPDATE at :169 — and not one names it. There is no dynamic update " +
		"builder in internal/member. THREE SHIPPED SURFACES READ IT: the member roster API " +
		"(memberView.avatar_url), the MCP list_members tool, and the analytics workload rollup. " +
		"Whether Track should acquire avatars, or stop shipping the field, is a product " +
		"decision and this guard takes neither.",
}

// ⚠ NOT IN THE MAP ABOVE, AND THE REASON IS A DISTINCTION THIS GUARD'S FIRST RUN
// FORCED RATHER THAN ONE ITS AUTHOR MADE: `automation_rules.updated_at` has
// DEFAULT now(), and there is no UPDATE statement against that table anywhere in
// the tree — the only writes are the INSERT at internal/automation/engine.go:296
// and the DELETE at :549. It is therefore NOT writerless: the database writes a
// real, varying timestamp into every row at INSERT. What is true of it is milder
// and different — it equals created_at forever, so a column named `updated_at`
// never records an update. Nothing reads it today, so that is inert rather than a
// defect, and it is recorded here rather than in the map because folding the two
// classes together would overstate the writerless finding.

// bannedPredicate is the subset of `writerless` whose column NAME is unique
// across the whole schema, so a whole-tree search for it cannot be confused with
// a same-named column on another table.
//
// ⚠ THE LIMIT IS PART OF THE GUARD, NOT AN OMISSION. `updated_at` exists on 14
// tables, so `automation_rules.updated_at` CANNOT be checked this way — a search
// for the bare name would hit thirteen tables where it is written and predicated
// perfectly legitimately. TestPredicateBanReportsWhatItCannotCheck asserts the
// skipped set is exactly what is declared here, so this limit cannot grow in
// silence.
// It is EMPTY today — both writerless columns have schema-unique names, so the
// ban covers all of them — and the mechanism is kept because the first
// writerless column with a shared name (`updated_at` is on 14 tables, `completed_at`
// on 2) must be declared here rather than quietly dropping out of the ban.
var notNameUnique = map[string]string{}

// ---------------------------------------------------------------------------
// VACUITY FLOORS
//
// Hard literals, deliberately NOT derived from the inputs they defend. Without
// them an empty schema and a blind extractor both produce a beautifully clean
// census over nothing at all, which is the failure this repository keeps
// catching in other people's instruments.
// ---------------------------------------------------------------------------

const (
	minProductTables  = 25
	minSchemaColumns  = 250
	minWrittenColumns = 100
	minSQLLiterals    = 3000
)

// ---------------------------------------------------------------------------
// HALF ONE: the shipped schema, from a real migrated database.
// ---------------------------------------------------------------------------

type column struct {
	table, name string
	def         *string
	nullable    string
}

func (c column) qualified() string { return c.table + "." + c.name }

// volatileDefault matches a DEFAULT that produces a real, varying value per row.
// A column with one is written by the database itself on every INSERT and is not
// writerless in any sense that matters.
var volatileDefault = regexp.MustCompile(`(?i)now\(\)|gen_random_uuid|current_timestamp|nextval`)

// shippedSchema reads information_schema from a from-zero database with the real
// migration chain applied by the PRODUCTION runner — not from a union of CREATE
// TABLE text, which cannot see a column a later migration drops or retypes.
func shippedSchema(t *testing.T) []column {
	t.Helper()
	d := testutil.New(t)
	rows, err := d.Pool.Query(context.Background(), `
		SELECT table_name, column_name, column_default, is_nullable
		FROM information_schema.columns
		WHERE table_schema = 'public'
		ORDER BY table_name, ordinal_position`)
	if err != nil {
		t.Fatalf("read information_schema: %v", err)
	}
	defer rows.Close()

	var out []column
	for rows.Next() {
		var c column
		if err := rows.Scan(&c.table, &c.name, &c.def, &c.nullable); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		// schema_migrations is the migration runner's own bookkeeping, written by
		// migrate.Up rather than by product code. Out of population, and named
		// here rather than filtered silently.
		if c.table == "schema_migrations" {
			continue
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate columns: %v", err)
	}
	return out
}

// ---------------------------------------------------------------------------
// HALF TWO: the writes, from the real Go tree.
// ---------------------------------------------------------------------------

var (
	reInsertCols   = regexp.MustCompile(`(?is)INSERT\s+INTO\s+([a-z_][a-z0-9_]*)\s*\(([^)]*)\)`)
	reInsertOpen   = regexp.MustCompile(`(?is)INSERT\s+INTO\s+([a-z_][a-z0-9_]*)\s*\((?:[^)]*)$`)
	reInsertNoCols = regexp.MustCompile(`(?is)INSERT\s+INTO\s+([a-z_][a-z0-9_]*)\s+(?:SELECT|VALUES)`)
	reUpdateSet    = regexp.MustCompile(`(?is)UPDATE\s+([a-z_][a-z0-9_]*)\s+SET\s+(.*?)(?:\bWHERE\b|\bRETURNING\b|\bFROM\b|$)`)
	reConflictSet  = regexp.MustCompile(`(?is)ON\s+CONFLICT\b.*?DO\s+UPDATE\s+SET\s+(.*?)(?:\bWHERE\b|\bRETURNING\b|$)`)
	reCopyCols     = regexp.MustCompile(`(?is)COPY\s+([a-z_][a-z0-9_]*)\s*\(([^)]*)\)`)
	reInsertTable  = regexp.MustCompile(`(?is)INSERT\s+INTO\s+([a-z_][a-z0-9_]*)`)
	reAssign       = regexp.MustCompile(`(?i)([a-z_][a-z0-9_]*)\s*=`)
	reIdent        = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
)

type writeCensus struct {
	written     map[string]bool   // "table.column" -> true
	sites       map[string]string // "table.column" -> first site that writes it
	literals    int
	unparseable []string // reported, NEVER skipped
	noColumnist []string // INSERT ... SELECT/VALUES with no column list
}

// staticWrites extracts SQL from string literals using go/parser rather than a
// regex over raw bytes, so raw strings, escapes and adjacent concatenation are
// handled by the language's own scanner.
//
// ⚠ WHAT IT CANNOT SEE, STATED SO ITS ZEROES ARE READABLE: a SET clause built at
// run time (see dynamicBuilders), a trigger, and a write issued by pgx's
// CopyFrom API — which takes column names as a Go slice, not as SQL. The last is
// checked for separately by TestNoProductionCopyFromBypassesTheCensus, because
// the day one appears this extractor goes quietly blind.
func staticWrites(t *testing.T) *writeCensus {
	t.Helper()
	wc := &writeCensus{written: map[string]bool{}, sites: map[string]string{}}
	fset := token.NewFileSet()

	for _, dir := range productionDirs {
		err := filepath.Walk(filepath.Join(repoRoot, dir), func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			f, perr := parser.ParseFile(fset, p, nil, 0)
			if perr != nil {
				// R5: a file the walk cannot parse is REPORTED, never skipped —
				// otherwise a syntax error silently shrinks the write census and
				// manufactures writerless columns.
				wc.unparseable = append(wc.unparseable, p+": "+perr.Error())
				return nil
			}
			ast.Inspect(f, func(n ast.Node) bool {
				bl, ok := n.(*ast.BasicLit)
				if !ok || bl.Kind != token.STRING {
					return true
				}
				s, uerr := strconv.Unquote(bl.Value)
				if uerr != nil {
					return true
				}
				wc.literals++
				site := p + ":" + strconv.Itoa(fset.Position(bl.Pos()).Line)
				wc.scan(s, site)
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	return wc
}

func (wc *writeCensus) note(table, col, site string) {
	col = strings.ToLower(strings.TrimSpace(col))
	if !reIdent.MatchString(col) {
		return
	}
	k := strings.ToLower(table) + "." + col
	if !wc.written[k] {
		wc.sites[k] = site
	}
	wc.written[k] = true
}

func (wc *writeCensus) scan(s, site string) {
	up := strings.ToUpper(s)
	if !strings.Contains(up, "INSERT INTO") && !strings.Contains(up, "UPDATE ") && !strings.Contains(up, "COPY ") {
		return
	}
	for _, m := range reInsertCols.FindAllStringSubmatch(s, -1) {
		for _, c := range splitCols(m[2]) {
			wc.note(m[1], c, site+" INSERT")
		}
	}
	for _, m := range reInsertOpen.FindAllStringSubmatch(s, -1) {
		wc.unparseable = append(wc.unparseable,
			site+": INSERT INTO "+m[1]+" column list is not closed inside this literal")
	}
	for _, m := range reInsertNoCols.FindAllStringSubmatch(s, -1) {
		wc.noColumnist = append(wc.noColumnist, site+": INSERT INTO "+m[1]+" with no column list")
	}
	for _, m := range reUpdateSet.FindAllStringSubmatch(s, -1) {
		for _, a := range reAssign.FindAllStringSubmatch(m[2], -1) {
			wc.note(m[1], a[1], site+" UPDATE")
		}
	}
	for _, m := range reConflictSet.FindAllStringSubmatch(s, -1) {
		tm := reInsertTable.FindStringSubmatch(s)
		if tm == nil {
			continue
		}
		for _, a := range reAssign.FindAllStringSubmatch(m[1], -1) {
			if strings.EqualFold(a[1], "excluded") {
				continue
			}
			wc.note(tm[1], a[1], site+" ON CONFLICT")
		}
	}
	for _, m := range reCopyCols.FindAllStringSubmatch(s, -1) {
		for _, c := range splitCols(m[2]) {
			wc.note(m[1], c, site+" COPY")
		}
	}
}

func splitCols(list string) []string {
	var out []string
	for _, part := range strings.Split(list, ",") {
		p := strings.TrimSpace(part)
		if i := strings.IndexAny(p, " \t\n"); i >= 0 {
			p = p[:i]
		}
		p = strings.Trim(p, `"`)
		if reIdent.MatchString(strings.ToLower(p)) {
			out = append(out, p)
		}
	}
	return out
}

// candidates returns every schema column with no statically visible writer,
// minus those the database itself writes via a volatile DEFAULT.
func candidates(schema []column, wc *writeCensus) []column {
	var out []column
	for _, c := range schema {
		if wc.written[strings.ToLower(c.qualified())] {
			continue
		}
		if c.def != nil && volatileDefault.MatchString(*c.def) {
			continue
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].qualified() < out[j].qualified() })
	return out
}

// ---------------------------------------------------------------------------
// THE ASSERTIONS
// ---------------------------------------------------------------------------

// TestEveryWriterlessColumnIsAccountedFor is the census itself. It reds on a
// column nobody has classified — which is what makes it a guard rather than a
// snapshot, because the failure arrives with the migration that adds the column
// rather than years later with the query that misreads it.
func TestEveryWriterlessColumnIsAccountedFor(t *testing.T) {
	schema := shippedSchema(t)
	wc := staticWrites(t)

	// Floors first: everything below is empty without them.
	tables := map[string]bool{}
	for _, c := range schema {
		tables[c.table] = true
	}
	if len(tables) < minProductTables {
		t.Fatalf("VACUITY: only %d product tables in the shipped schema (floor %d) — "+
			"did the migration chain actually apply?", len(tables), minProductTables)
	}
	if len(schema) < minSchemaColumns {
		t.Fatalf("VACUITY: only %d columns in the shipped schema (floor %d)", len(schema), minSchemaColumns)
	}
	if wc.literals < minSQLLiterals {
		t.Fatalf("VACUITY: the AST walk found only %d string literals (floor %d) — "+
			"did it read the tree?", wc.literals, minSQLLiterals)
	}
	if len(wc.written) < minWrittenColumns {
		t.Fatalf("VACUITY: only %d written table.column pairs recovered (floor %d) — "+
			"the SQL extractor is blind, and every 'writerless' column below is an artefact",
			len(wc.written), minWrittenColumns)
	}
	if len(wc.unparseable) > 0 {
		t.Errorf("the write census could not parse %d site(s); a site it cannot read "+
			"manufactures writerless columns, so this is a failure and not a note:\n  %s",
			len(wc.unparseable), strings.Join(wc.unparseable, "\n  "))
	}
	if len(wc.noColumnist) > 0 {
		t.Errorf("%d INSERT statement(s) have no column list, so the census cannot tell "+
			"which columns they write:\n  %s",
			len(wc.noColumnist), strings.Join(wc.noColumnist, "\n  "))
	}

	for _, c := range candidates(schema, wc) {
		q := c.qualified()
		_, dyn := dynamicBuilders[q]
		_, dead := writerless[q]
		switch {
		case dyn && dead:
			t.Errorf("%s is in BOTH dynamicBuilders and writerless — it cannot be both", q)
		case dyn, dead:
			// accounted for
		default:
			def := "no DEFAULT, so NULL for every row"
			if c.def != nil {
				def = "DEFAULT " + *c.def + ", so that constant for every row"
			}
			t.Errorf("UNACCOUNTED WRITERLESS COLUMN: %s (%s).\n"+
				"    No INSERT column list, SET clause, ON CONFLICT assignment or COPY in\n"+
				"    internal/ or cmd/ names it. Either:\n"+
				"      (a) it IS written, by Go that builds a SET clause at run time — add it to\n"+
				"          dynamicBuilders with the file and the source anchor that does it; or\n"+
				"      (b) it is genuinely writerless — add it to writerless with what you\n"+
				"          measured, and check whether anything READS it before you do.", q, def)
		}
	}
}

// TestDeclaredDynamicBuildersStillWriteTheirColumn stops the excuse from
// outliving the write. An entry in dynamicBuilders suppresses a column from the
// census forever; if the builder is refactored and stops naming the column, the
// column silently becomes writerless with a comment explaining why it is fine.
//
// This is the failure mode talyvor-code #76 recorded from the other side — a
// control whose expected catcher had been deleted, so it could never fire and
// nothing said so.
func TestDeclaredDynamicBuildersStillWriteTheirColumn(t *testing.T) {
	if len(dynamicBuilders) == 0 {
		t.Fatal("VACUITY: no dynamic builders declared, so this test verifies nothing")
	}
	for q, b := range dynamicBuilders {
		path := filepath.Join(repoRoot, b.file)
		src, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s: declared builder file %s is unreadable: %v", q, b.file, err)
			continue
		}
		if n := strings.Count(string(src), b.anchor); n != 1 {
			t.Errorf("%s: the declared builder anchor occurs %d times in %s, want exactly 1.\n"+
				"    anchor: %s\n"+
				"    If the builder was refactored, re-derive the anchor from what the file says\n"+
				"    TODAY and confirm it still writes the column. If it no longer writes it, the\n"+
				"    column is writerless — move it to the writerless map.", q, n, b.file, b.anchor)
		}
	}
}

// TestDeclaredWriterlessColumnsAreStillWriterless fails in the other direction:
// a column that has since been given a writer must not keep an entry claiming it
// has none, or the next reader is told a falsehood by a test.
func TestDeclaredWriterlessColumnsAreStillWriterless(t *testing.T) {
	schema := shippedSchema(t)
	wc := staticWrites(t)

	inSchema := map[string]bool{}
	for _, c := range schema {
		inSchema[c.qualified()] = true
	}
	cand := map[string]bool{}
	for _, c := range candidates(schema, wc) {
		cand[c.qualified()] = true
	}
	defaults := map[string]*string{}
	for _, c := range schema {
		defaults[c.qualified()] = c.def
	}
	if len(writerless) == 0 {
		t.Fatal("VACUITY: no writerless columns declared, so this test verifies nothing")
	}
	for q, why := range writerless {
		if !inSchema[q] {
			t.Errorf("%s is declared writerless but is not in the shipped schema — "+
				"dropped or renamed? Delete the entry.\n    was: %s", q, why)
			continue
		}
		if cand[q] {
			continue // still writerless
		}
		// It dropped out of the candidate set. There are exactly TWO ways that
		// happens and they call for opposite repairs, so the message must say
		// WHICH — a failure that misdirects the reader costs somebody an
		// afternoon (talyvor-code #76 is the worked example).
		if site, ok := wc.sites[strings.ToLower(q)]; ok {
			t.Errorf("%s IS NOW WRITTEN, at %s, but is still declared writerless.\n"+
				"    Delete its entry: this guard is a statement about the CURRENT writers,\n"+
				"    not a permanent ban.", q, site)
			continue
		}
		def := "<none>"
		if d := defaults[q]; d != nil {
			def = *d
		}
		t.Errorf("%s IS NOT WRITTEN BY ANY STATEMENT, but it has the volatile DEFAULT %q, so the\n"+
			"    DATABASE writes a real varying value into every row at INSERT. That makes it not\n"+
			"    writerless, and this entry overstates it. What may still be true is the milder\n"+
			"    claim that no UPDATE ever moves it — record that in prose, not in this map.", q, def)
	}
}

// TestNoSQLPredicatesOnAWriterlessColumn is the half that stops the NEXT
// instance. Reading a writerless column into a struct is at worst honestly
// empty; PREDICATING on one, or aggregating it, produces a confident number that
// is structurally fixed — which is how both known instances shipped.
func TestNoSQLPredicatesOnAWriterlessColumn(t *testing.T) {
	checked := 0
	for q := range writerless {
		if _, skip := notNameUnique[q]; skip {
			continue
		}
		col := q[strings.IndexByte(q, '.')+1:]
		re := predicateUse(col)
		for _, dir := range productionDirs {
			err := filepath.Walk(filepath.Join(repoRoot, dir), func(p string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if info.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
					return nil
				}
				src, rerr := os.ReadFile(p)
				if rerr != nil {
					t.Errorf("read %s: %v", p, rerr)
					return nil
				}
				if loc := re.FindIndex(src); loc != nil {
					t.Errorf("%s PREDICATES ON THE WRITERLESS COLUMN %s:\n"+
						"    %s\n"+
						"    Nothing writes that column, so this comparison has a fixed answer for\n"+
						"    every row that will ever exist. See the writerless map in this file for\n"+
						"    what was measured.", p, q, strings.TrimSpace(string(src[loc[0]:loc[1]])))
				}
				return nil
			})
			if err != nil {
				t.Fatalf("walk %s: %v", dir, err)
			}
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("VACUITY: this test checked no columns at all")
	}
}

// predicateUse matches a column used as a PREDICATE or an aggregate subject —
// the uses that silently produce a wrong number. A bare appearance in a SELECT
// column list is not matched: it returns a value that is at least honestly
// empty.
func predicateUse(col string) *regexp.Regexp {
	c := regexp.QuoteMeta(col)
	return regexp.MustCompile(`(?i)FILTER\s*\(\s*WHERE\s+(NOT\s+)?` + c + `\s*\)` +
		`|WHERE\s+(NOT\s+)?` + c + `\s*(=|<|>|!|IS|AND|OR|\)|$)` +
		`|AND\s+(NOT\s+)?` + c + `\s*(=|<|>|!|IS|AND|OR|\)|$)` +
		`|SUM\s*\(\s*` + c + `\s*\)|AVG\s*\(\s*` + c + `\s*\)|COUNT\s*\(\s*` + c + `\s*\)`)
}

// TestPredicateBanReportsWhatItCannotCheck keeps the previous test's limit
// visible. A column whose NAME is not unique across the schema cannot be checked
// by a whole-tree search, because a hit might be a different table's column that
// is written perfectly normally. That is a real limit; what would make it a
// defect is growing in silence.
func TestPredicateBanReportsWhatItCannotCheck(t *testing.T) {
	if len(writerless) == 0 {
		t.Fatal("VACUITY: no writerless columns declared, so this test verifies nothing")
	}
	schema := shippedSchema(t)
	perName := map[string][]string{}
	for _, c := range schema {
		perName[c.name] = append(perName[c.name], c.table)
	}

	got := map[string]bool{}
	for q := range writerless {
		col := q[strings.IndexByte(q, '.')+1:]
		if len(perName[col]) > 1 {
			got[q] = true
		}
	}
	for q := range got {
		if _, ok := notNameUnique[q]; !ok {
			col := q[strings.IndexByte(q, '.')+1:]
			t.Errorf("%s cannot be checked by TestNoSQLPredicatesOnAWriterlessColumn "+
				"(the name %q occurs on %d tables) and is not declared in notNameUnique. "+
				"Declare it, so the gap in the ban is readable.", q, col, len(perName[col]))
		}
	}
	for q := range notNameUnique {
		if !got[q] {
			t.Errorf("%s is declared unswept in notNameUnique but its name IS unique in the "+
				"schema now — delete the entry so the ban covers it.", q)
		}
	}
}

// TestNoProductionCopyFromBypassesTheCensus defends the one write idiom the SQL
// extractor is blind to BY CONSTRUCTION. pgx's CopyFrom takes its column names
// as a Go slice, so no SQL literal mentions them; the day a bulk-import path
// starts using it, every column it writes would be reported writerless.
//
// The census is complete for this repository's idioms today. This test is what
// makes that a maintained fact rather than a note in a comment.
func TestNoProductionCopyFromBypassesTheCensus(t *testing.T) {
	var found []string
	for _, dir := range productionDirs {
		err := filepath.Walk(filepath.Join(repoRoot, dir), func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			// internal/testutil is test scaffolding, not a shipped write path.
			if strings.Contains(filepath.ToSlash(p), "/internal/testutil/") {
				return nil
			}
			src, rerr := os.ReadFile(p)
			if rerr != nil {
				return rerr
			}
			if strings.Contains(string(src), "CopyFrom(") {
				found = append(found, p)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	if len(found) > 0 {
		t.Errorf("production code uses pgx CopyFrom, which the write census in this file "+
			"CANNOT SEE — its column names are a Go slice, not SQL:\n  %s\n"+
			"    Teach staticWrites to read the CopyFrom column slice before this lands, or "+
			"every column it writes will be reported as writerless.", strings.Join(found, "\n  "))
	}
}
