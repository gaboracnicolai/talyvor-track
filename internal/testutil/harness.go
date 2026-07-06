// Package testutil is Track's real-Postgres integration-test harness.
//
// It is TEST INFRASTRUCTURE ONLY — it changes no production behavior. New(t)
// provisions a fresh, isolated database, applies the FULL schema by invoking the
// production migration runner (internal/migrate over the embedded migrations — the
// harness never hand-rolls or re-embeds schema), and returns a *pgxpool.Pool plus
// seed helpers built on the real stores.
//
// Same env + graceful-skip contract as the migration-runner tests: without
// TRACK_TEST_DATABASE_URL the test SKIPS cleanly (devs without Postgres), and in CI
// (where the var points at the real service) it runs for real. Each New() gets its
// OWN database, so tests — and whole packages — are parallel-safe, and the database
// is dropped on cleanup (t.Cleanup runs on pass AND failure, so nothing leaks).
package testutil

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"regexp"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/talyvor/track/internal/customfield"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/migrate"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/team"
	"github.com/talyvor/track/internal/workspace"
	"github.com/talyvor/track/migrations"
)

// safeIdent guards the database name interpolated into CREATE/DROP DATABASE.
// Database identifiers can't be bound as query parameters, so they must be
// interpolated; the name is always harness-generated ("track_test_<hex>", below),
// never user input, and this check makes that invariant explicit and enforced.
var safeIdent = regexp.MustCompile(`^[a-z0-9_]+$`)

func mustSafeIdent(name string) string {
	if !safeIdent.MatchString(name) {
		panic("testutil: refusing unsafe database identifier: " + name)
	}
	return name
}

// DB is a real-Postgres fixture bound to one test: an isolated database with the
// full migrated schema, plus a connection pool and seed helpers.
type DB struct {
	Pool  *pgxpool.Pool
	admin string
	name  string
	seq   atomic.Int64
}

// New provisions a fresh isolated database, applies all migrations via the
// production runner, and returns a ready DB. Skips the test when
// TRACK_TEST_DATABASE_URL is unset.
func New(t *testing.T) *DB {
	t.Helper()
	admin := os.Getenv("TRACK_TEST_DATABASE_URL")
	if admin == "" {
		t.Skip("TRACK_TEST_DATABASE_URL not set — skipping real-Postgres integration test")
	}
	ctx := context.Background()
	name := "track_test_" + randToken(t) // unique per New() → parallel-safe

	// 1. Create the isolated database from the admin connection.
	admConn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("testutil: admin connect: %v", err)
	}
	if err := dropDB(ctx, admConn, name); err != nil { // idempotent pre-clean
		t.Fatalf("testutil: pre-drop %s: %v", name, err)
	}
	if err := createDB(ctx, admConn, name); err != nil {
		t.Fatalf("testutil: create %s: %v", name, err)
	}
	_ = admConn.Close(ctx)

	// 2. Apply the real schema via the production migration runner — NOT a
	//    re-embedded copy. A single conn so migrate's advisory lock holds.
	migConn := connectTo(t, ctx, admin, name)
	migs, err := migrate.Load(migrations.FS)
	if err != nil {
		t.Fatalf("testutil: load migrations: %v", err)
	}
	if _, err := migrate.Up(ctx, migConn, migs); err != nil {
		t.Fatalf("testutil: migrate up: %v", err)
	}
	_ = migConn.Close(ctx)

	// 3. Pool for the stores.
	poolCfg, err := pgxpool.ParseConfig(admin)
	if err != nil {
		t.Fatalf("testutil: parse pool config: %v", err)
	}
	poolCfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		t.Fatalf("testutil: open pool: %v", err)
	}

	d := &DB{Pool: pool, admin: admin, name: name}
	t.Cleanup(d.teardown)
	return d
}

// teardown closes the pool and drops the database. Registered via t.Cleanup, so it
// runs whether the test passes or fails — no leaked databases.
func (d *DB) teardown() {
	d.Pool.Close()
	ctx := context.Background()
	admConn, err := pgx.Connect(ctx, d.admin)
	if err != nil {
		return
	}
	defer admConn.Close(ctx)
	_ = dropDB(ctx, admConn, d.name)
}

// ---- seed helpers (built on the real stores; each fatals on error) ----

// Workspace creates one isolated workspace (unique slug) and returns it.
func (d *DB) Workspace(t *testing.T) *model.Workspace {
	t.Helper()
	tok := d.token()
	ws, err := workspace.NewStore(d.Pool).Create(context.Background(), model.Workspace{
		Name: "Workspace " + tok,
		Slug: "ws-" + tok,
	})
	if err != nil {
		t.Fatalf("testutil: seed workspace: %v", err)
	}
	return ws
}

// Workspaces creates n isolated workspaces.
func (d *DB) Workspaces(t *testing.T, n int) []*model.Workspace {
	t.Helper()
	out := make([]*model.Workspace, n)
	for i := range out {
		out[i] = d.Workspace(t)
	}
	return out
}

// Team creates a team in workspaceID (unique identifier within the workspace).
func (d *DB) Team(t *testing.T, workspaceID string) *model.Team {
	t.Helper()
	tok := d.token()
	tm, err := team.NewStore(d.Pool).Create(context.Background(), model.Team{
		WorkspaceID: workspaceID,
		Name:        "Team " + tok,
		Identifier:  "T" + tok,
	})
	if err != nil {
		t.Fatalf("testutil: seed team: %v", err)
	}
	return tm
}

// Issue seeds an issue in workspaceID. If teamID is "", a fresh team is created in
// that workspace (issue.Create requires a real team).
func (d *DB) Issue(t *testing.T, workspaceID, teamID string) *model.Issue {
	t.Helper()
	if teamID == "" {
		teamID = d.Team(t, workspaceID).ID
	}
	tok := d.token()
	iss, err := issue.NewStore(d.Pool).Create(context.Background(), model.Issue{
		WorkspaceID: workspaceID,
		TeamID:      teamID,
		Title:       "Issue " + tok,
		CreatorID:   "creator-" + tok,
	})
	if err != nil {
		t.Fatalf("testutil: seed issue: %v", err)
	}
	return iss
}

// Comment seeds a comment on issueID and returns it.
func (d *DB) Comment(t *testing.T, issueID, body string) *model.Comment {
	t.Helper()
	c, err := issue.NewStore(d.Pool).CreateComment(context.Background(), model.Comment{
		IssueID:  issueID,
		AuthorID: "author-" + d.token(),
		Body:     body,
	})
	if err != nil {
		t.Fatalf("testutil: seed comment: %v", err)
	}
	return c
}

// CustomField creates a workspace-scoped text custom field and returns it.
func (d *DB) CustomField(t *testing.T, workspaceID, name string) *customfield.CustomField {
	t.Helper()
	f, err := customfield.NewStore(d.Pool).CreateField(context.Background(), customfield.CustomField{
		WorkspaceID: workspaceID,
		Name:        name,
		Type:        customfield.FieldTypeText,
	})
	if err != nil {
		t.Fatalf("testutil: seed custom field: %v", err)
	}
	return f
}

// SetFieldValue sets a custom-field value on issueID. Resolves the issue's workspace internally so
// the seeder signature stays stable while SetValue is workspace-scoped (SEC-5).
func (d *DB) SetFieldValue(t *testing.T, issueID, fieldID, value string) {
	t.Helper()
	var ws string
	if err := d.Pool.QueryRow(context.Background(), `SELECT workspace_id FROM issues WHERE id=$1`, issueID).Scan(&ws); err != nil {
		t.Fatalf("testutil: resolve issue workspace: %v", err)
	}
	if err := customfield.NewStore(d.Pool).SetValue(context.Background(), issueID, fieldID, ws, value); err != nil {
		t.Fatalf("testutil: set field value: %v", err)
	}
}

// ---- internals ----

// token returns a per-DB monotonic suffix, used to keep seeded slugs/identifiers
// unique within an isolated database.
func (d *DB) token() string {
	return strconv.FormatInt(d.seq.Add(1), 10)
}

func randToken(t *testing.T) string {
	t.Helper()
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("testutil: rand: %v", err)
	}
	return hex.EncodeToString(b[:]) // 16 lowercase-hex chars → matches safeIdent
}

func connectTo(t *testing.T, ctx context.Context, admin, dbName string) *pgx.Conn {
	t.Helper()
	cfg, err := pgx.ParseConfig(admin)
	if err != nil {
		t.Fatalf("testutil: parse dsn: %v", err)
	}
	cfg.Database = dbName
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("testutil: connect %s: %v", dbName, err)
	}
	return conn
}

// createDB / dropDB interpolate the database name because DDL cannot bind an
// identifier as a query parameter. The name is harness-generated, regex-validated
// (mustSafeIdent), and quoted with pgx.Identifier.Sanitize — never user input — so
// there is no injection surface here.
func createDB(ctx context.Context, conn *pgx.Conn, name string) error {
	_, err := conn.Exec(ctx, createDatabaseStmt(name))
	return err
}

func dropDB(ctx context.Context, conn *pgx.Conn, name string) error {
	_, err := conn.Exec(ctx, dropDatabaseStmt(name))
	return err
}

func createDatabaseStmt(name string) string {
	return "CREATE DATABASE " + pgx.Identifier{mustSafeIdent(name)}.Sanitize()
}

func dropDatabaseStmt(name string) string {
	return "DROP DATABASE IF EXISTS " + pgx.Identifier{mustSafeIdent(name)}.Sanitize() + " WITH (FORCE)"
}
