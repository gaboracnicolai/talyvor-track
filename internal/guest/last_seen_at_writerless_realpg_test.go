package guest

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/testutil"
)

// THE FINDING, MEASURED RATHER THAN READ — `guests.last_seen_at` IS A COLUMN NOTHING
// CAN EVER WRITE, AND THE REASON IS A DESIGN DECISION RECORDED TWO PARAGRAPHS AWAY.
//
// Migration 0014 creates `last_seen_at TIMESTAMPTZ` — nullable, NO DEFAULT. It is
// SELECTed (it is the last name in `guestColumns`), scanned into `Guest.LastSeenAt`,
// serialized as `last_seen_at,omitempty`, and declared in the SPA's own contract at
// frontend/src/api/types.ts:296 (`GuestRecord.last_seen_at?: string`). The complete set
// of production statements against the table is four, and not one of them writes it:
//
//	store.go:313  INSERT INTO guests (workspace_id, project_id, email, name, role)
//	              ON CONFLICT DO UPDATE SET name, role, project_id, active
//	store.go:361  SELECT <guestColumns> FROM guests WHERE workspace_id = $1 ...
//	store.go:367  SELECT <guestColumns> FROM guests ...
//	store.go:403  UPDATE guests SET active = false WHERE id = $1 AND workspace_id = $2
//
// The two request-path entry points are DB-free by construction: middleware.go:39 calls
// VerifyToken (pure HMAC), and ValidateGuestAccess takes a ctx it explicitly discards
// (`_ = ctx`, store.go:429).
//
// There is no dynamic update builder in this package (internal/milestone has one — an
// `updatable` allow-list that DOES include `completed_at`, which is why that column,
// which looks identical to a text census, is NOT dead and this one is).
//
// ⚠ THE REASON IT CANNOT BE WIRED WITHOUT A DECISION IS IN THIS PACKAGE'S OWN DOC
// COMMENT, WHICH ADVERTISES IT AS A FEATURE: "access tokens (issued after accept) are
// stateless HMAC-SHA256-signed claims, SO MIDDLEWARE NEVER NEEDS TO HIT THE DB ON A HOT
// PATH." middleware.go:39 calls VerifyToken, which is pure HMAC and takes no pool. The
// one request path that knows a guest was just seen is the one path deliberately built
// never to touch the database. So "stamp last_seen_at" is not a missing line — it is a
// request-per-write against the property the design was chosen for, and it is also a
// question about tracking an external collaborator's activity. Neither is a session's
// to answer; the alternative (drop the column, the struct field and the TS field) is a
// shipped-contract change and is not a session's either.
//
// ⚠ WHAT THIS FILE IS FOR. It does not endorse the current state and does not block
// either answer — it makes the change ANNOUNCE ITSELF. Today the column can be wired,
// or dropped, with the whole repository green and nothing to read. After this file both
// directions are red: TestGuestLastSeenAt_ColumnExists reds on a DROP, and
// TestGuestLastSeenAt_NoProductionWriter reds the moment any production statement
// writes it. Controls in ~/talyvor-queue/w325-lastseen-controls-x7p2.py — 8/8 armed, every
// mutation compile-checked before it is run. C6 was NOT armed on its first pass: it dropped
// the column from the container's database with psql and this file stayed green, because
// testutil.New(t) provisions its own fresh database from the migrations and never opens that
// one. Expressed as a drop MIGRATION it reds, which is also the only shape the real change
// could take.

// ─── (A) the column still exists ────────────────────────────
//
// Load-bearing in the DROP direction: without this, deleting the column would leave the
// writerless census below passing vacuously — green because there is nothing to write.

func TestGuestLastSeenAt_ColumnExists(t *testing.T) {
	db := testutil.New(t)
	var dataType, isNullable, colDefault string
	err := db.Pool.QueryRow(context.Background(), `
        SELECT data_type, is_nullable, COALESCE(column_default, '')
        FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'guests' AND column_name = 'last_seen_at'`,
	).Scan(&dataType, &isNullable, &colDefault)
	if err != nil {
		t.Fatalf("guests.last_seen_at is not in the live schema: %v\n"+
			"If it was dropped on purpose, delete this whole file and Guest.LastSeenAt and "+
			"GuestRecord.last_seen_at in frontend/src/api/types.ts with it.", err)
	}
	if dataType != "timestamp with time zone" || isNullable != "YES" || colDefault != "" {
		t.Errorf("guests.last_seen_at = (%s, nullable=%s, default=%q), want (timestamp with time zone, YES, \"\")\n"+
			"A DEFAULT would mean the column can hold a value without any statement writing it, "+
			"which is precisely what the census below assumes it cannot.",
			dataType, isNullable, strconv.Quote(colDefault))
	}
}

// ─── (B) a full production lifecycle leaves it NULL ─────────

func TestGuestLastSeenAt_NullAfterFullLifecycle(t *testing.T) {
	ctx := context.Background()
	db := testutil.New(t)
	ws := db.Workspace(t)
	store := NewStore(db.Pool, "w325-test-secret-not-a-real-key")

	inv, err := store.CreateInvite(ctx, ws.ID, nil, "guest@example.com", GuestRoleEditor, "")
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	res, err := store.AcceptInvite(ctx, inv.Token, "A Guest")
	if err != nil {
		t.Fatalf("AcceptInvite: %v", err)
	}
	// The middleware path, verbatim: middleware.go:39 calls VerifyToken and nothing else.
	if _, err := store.VerifyToken(res.AccessToken()); err != nil {
		t.Fatalf("VerifyToken: %v", err)
	}
	// ...and the authorization path, which takes a ctx and discards it (`_ = ctx`,
	// store.go:429) — the second place a "seen" stamp would naturally go, and the second
	// one built never to reach the pool.
	if _, err := store.ValidateGuestAccess(ctx, res.AccessToken(), "workspace", ws.ID); err != nil {
		t.Fatalf("ValidateGuestAccess: %v", err)
	}
	gs, err := store.ListGuests(ctx, ws.ID, nil)
	if err != nil {
		t.Fatalf("ListGuests: %v", err)
	}
	if len(gs) != 1 {
		t.Fatalf("ListGuests returned %d guests, want 1", len(gs))
	}
	if err := store.RevokeGuest(ctx, gs[0].ID, ws.ID); err != nil {
		t.Fatalf("RevokeGuest: %v", err)
	}

	// Read the column straight out of Postgres — not through the scan, so a change to
	// guestColumns cannot hide the answer.
	var nonNull int
	if err := db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM guests WHERE workspace_id = $1 AND last_seen_at IS NOT NULL`,
		ws.ID).Scan(&nonNull); err != nil {
		t.Fatalf("count non-null last_seen_at: %v", err)
	}
	if nonNull != 0 {
		t.Errorf("after invite → accept → token verify → list → deactivate, "+
			"%d guest rows have a non-NULL last_seen_at, want 0.\n"+
			"If a writer was added on purpose this test is the record that it changed; "+
			"update it and the header comment above rather than deleting it.", nonNull)
	}

	// ...and the shipped JSON therefore never carries the key at all (omitempty on a nil
	// *time.Time), which is why this is invisible from the wire without the schema.
	blob, err := json.Marshal(gs[0])
	if err != nil {
		t.Fatalf("marshal guest: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(blob, &decoded); err != nil {
		t.Fatalf("unmarshal guest: %v", err)
	}
	if _, present := decoded["last_seen_at"]; present {
		t.Errorf("guest JSON carries last_seen_at = %v; it has never been emitted. "+
			"frontend/src/api/types.ts declares GuestRecord.last_seen_at?: string, so a "+
			"consumer that starts receiving it is a contract change worth noticing.",
			decoded["last_seen_at"])
	}
}

// ─── (C) no production statement writes it ──────────────────
//
// The behavioural test above only covers paths it exercises. This one covers the tree:
// it parses every non-test .go file in the module with go/parser, flattens `+`
// concatenations (resolving package-level string constants, which is how `guestColumns`
// reaches its query), and reads the INSERT column lists and UPDATE SET clauses that
// target `guests`.

var (
	reInsertGuests = regexp.MustCompile(`(?is)INSERT\s+INTO\s+guests\s*\(([^)]*)\)`)
	reUpdateGuests = regexp.MustCompile(`(?is)UPDATE\s+guests\s+SET\s+(.*?)(?:\bWHERE\b|\bRETURNING\b|$)`)
	reDoUpdateSet  = regexp.MustCompile(`(?is)ON\s+CONFLICT\b.*?DO\s+UPDATE\s+SET\s+(.*?)(?:\bWHERE\b|\bRETURNING\b|$)`)
	reAssignedCol  = regexp.MustCompile(`(?i)(?:^|[,\s(])([a-z_][a-z0-9_]*)\s*=(?:[^=]|$)`)
	reWord         = regexp.MustCompile(`[a-zA-Z_][a-zA-Z0-9_]*`)
)

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 12; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not find go.mod above the test's working directory")
	return ""
}

// productionSQLStrings returns every string expression in non-test Go source under root,
// with `+` chains flattened and package-level string constants substituted.
func productionSQLStrings(t *testing.T, root string) []string {
	t.Helper()
	type pkgKey = string

	var files []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "frontend", "bin", "dist", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	fset := token.NewFileSet()
	parsed := make(map[string]*ast.File, len(files))
	consts := map[pkgKey]map[string]string{}
	for _, p := range files {
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		parsed[p] = f
		dir := filepath.Dir(p)
		if consts[dir] == nil {
			consts[dir] = map[string]string{}
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || (gd.Tok != token.CONST && gd.Tok != token.VAR) {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if s, err := strconv.Unquote(lit.Value); err == nil {
							consts[dir][name.Name] = s
						}
					}
				}
			}
		}
	}

	var out []string
	for p, f := range parsed {
		local := consts[filepath.Dir(p)]
		var flatten func(ast.Expr) (string, bool)
		flatten = func(e ast.Expr) (string, bool) {
			switch v := e.(type) {
			case *ast.BasicLit:
				if v.Kind != token.STRING {
					return "", false
				}
				s, err := strconv.Unquote(v.Value)
				return s, err == nil
			case *ast.Ident:
				s, ok := local[v.Name]
				return s, ok
			case *ast.ParenExpr:
				return flatten(v.X)
			case *ast.BinaryExpr:
				if v.Op != token.ADD {
					return "", false
				}
				l, lok := flatten(v.X)
				r, rok := flatten(v.Y)
				if !lok && !rok {
					return "", false
				}
				return l + r, true // a non-string operand contributes "" — a hole, never a false join
			}
			return "", false
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch e := n.(type) {
			case *ast.BinaryExpr:
				if s, ok := flatten(e); ok && s != "" {
					out = append(out, s)
				}
			case *ast.BasicLit:
				if e.Kind == token.STRING {
					if s, err := strconv.Unquote(e.Value); err == nil && s != "" {
						out = append(out, s)
					}
				}
			}
			return true
		})
	}
	return out
}

func TestGuestLastSeenAt_NoProductionWriter(t *testing.T) {
	root := moduleRoot(t)
	stmts := productionSQLStrings(t, root)

	var inserts, updates int
	var writers []string
	for _, s := range stmts {
		for _, m := range reInsertGuests.FindAllStringSubmatch(s, -1) {
			inserts++
			for _, col := range reWord.FindAllString(m[1], -1) {
				if strings.EqualFold(col, "last_seen_at") {
					writers = append(writers, "INSERT INTO guests column list: "+strings.TrimSpace(m[1]))
				}
			}
			for _, cm := range reDoUpdateSet.FindAllStringSubmatch(s, -1) {
				for _, am := range reAssignedCol.FindAllStringSubmatch(cm[1], -1) {
					if strings.EqualFold(am[1], "last_seen_at") {
						writers = append(writers, "ON CONFLICT DO UPDATE SET: "+strings.TrimSpace(cm[1]))
					}
				}
			}
		}
		for _, m := range reUpdateGuests.FindAllStringSubmatch(s, -1) {
			updates++
			for _, am := range reAssignedCol.FindAllStringSubmatch(m[1], -1) {
				if strings.EqualFold(am[1], "last_seen_at") {
					writers = append(writers, "UPDATE guests SET: "+strings.TrimSpace(m[1]))
				}
			}
		}
	}

	// ⚠ THE FLOOR. A census that finds nothing reports "no writer" for the same reason a
	// broken scan does. These two are the statements measured at a41cfd0; if the scan
	// stops seeing them, it has gone blind and must fail rather than pass.
	if inserts < 1 || updates < 1 {
		t.Fatalf("the writerless census went blind: found %d `INSERT INTO guests (...)` and "+
			"%d `UPDATE guests SET ...` statements in %d production string expressions, want >=1 of each.\n"+
			"At a41cfd0 they are internal/guest/store.go:313 and :403. If they were legitimately "+
			"removed, lower this floor deliberately; do not delete it.", inserts, updates, len(stmts))
	}

	if len(writers) > 0 {
		t.Errorf("guests.last_seen_at now has %d production writer(s):\n  %s\n\n"+
			"That is a real change, not a regression — the column was writerless at a41cfd0 and this "+
			"test exists to announce exactly this. Update it, the header comment, and "+
			"TestGuestLastSeenAt_NullAfterFullLifecycle together.",
			len(writers), strings.Join(writers, "\n  "))
	}
}
