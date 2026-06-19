package migrate_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"

	"github.com/talyvor/track/internal/migrate"
	"github.com/talyvor/track/migrations"
)

// freshDB connects to the admin DB in TRACK_TEST_DATABASE_URL, (re)creates a single
// empty test database, connects to it, and registers cleanup that drops it. The
// migrate-package tests run sequentially, so one reused database gives each a clean
// slate. Per-DATABASE isolation (not per-schema) because the migrations create
// extensions (uuid-ossp, pg_trgm, vector), which are database-scoped. Skips without
// the URL. The DB name is a fixed literal — DDL identifiers can't be bound as
// parameters, and a compile-time constant (never user input) keeps the SQL safe.
func freshDB(t *testing.T) *pgx.Conn {
	t.Helper()
	admin := os.Getenv("TRACK_TEST_DATABASE_URL")
	if admin == "" {
		t.Skip("TRACK_TEST_DATABASE_URL not set — skipping real-PG migrate test")
	}
	ctx := context.Background()
	ac, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("admin connect: %v", err)
	}
	defer ac.Close(ctx)
	if _, err := ac.Exec(ctx, `DROP DATABASE IF EXISTS track_migrate_test WITH (FORCE)`); err != nil {
		t.Fatalf("pre-drop: %v", err)
	}
	if _, err := ac.Exec(ctx, `CREATE DATABASE track_migrate_test`); err != nil {
		t.Fatalf("create db: %v", err)
	}

	cfg, err := pgx.ParseConfig(admin)
	if err != nil {
		t.Fatalf("parse admin dsn: %v", err)
	}
	cfg.Database = "track_migrate_test"
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_ = conn.Close(bg)
		ac2, err := pgx.Connect(bg, admin)
		if err != nil {
			return
		}
		defer ac2.Close(bg)
		_, _ = ac2.Exec(bg, `DROP DATABASE IF EXISTS track_migrate_test WITH (FORCE)`)
	})
	return conn
}

// TestUp_AppliesAllRealMigrations_AndIdempotent — applies the real embedded set to
// an empty DB, records every one, creates real schema, and re-running is a clean
// no-op. (Proves the runner actually exercises the production migrations.)
func TestUp_AppliesAllRealMigrations_AndIdempotent(t *testing.T) {
	conn := freshDB(t)
	ctx := context.Background()
	migs, err := migrate.Load(migrations.FS)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(migs) == 0 {
		t.Fatal("no migrations embedded")
	}

	applied, err := migrate.Up(ctx, conn, migs)
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	if len(applied) != len(migs) {
		t.Fatalf("applied %d, want all %d", len(applied), len(migs))
	}
	var recorded int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&recorded); err != nil {
		t.Fatalf("count: %v", err)
	}
	if recorded != len(migs) {
		t.Fatalf("recorded %d, want %d", recorded, len(migs))
	}
	// Real schema actually exists (a table from 0001_core, not just a mock).
	var present bool
	if err := conn.QueryRow(ctx, `SELECT to_regclass('public.workspaces') IS NOT NULL`).Scan(&present); err != nil {
		t.Fatalf("regclass: %v", err)
	}
	if !present {
		t.Error("workspaces table missing after up — migrations did not really run")
	}
	// Idempotent: a second up applies nothing.
	again, err := migrate.Up(ctx, conn, migs)
	if err != nil {
		t.Fatalf("up (re-run): %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("re-run applied %d, want 0 (idempotent)", len(again))
	}
	st, err := migrate.StatusOf(ctx, conn, migs)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(st.Applied) != len(migs) || len(st.Pending) != 0 {
		t.Fatalf("status applied=%d pending=%d, want %d/0", len(st.Applied), len(st.Pending), len(migs))
	}
}

// TestUp_BrokenMigration_FailsClosed — a broken migration must roll back, NOT be
// recorded, and STOP the run (later migrations don't apply). RED without the
// per-migration transaction + stop logic (0002 gets recorded and 0003 runs anyway);
// GREEN with it.
func TestUp_BrokenMigration_FailsClosed(t *testing.T) {
	conn := freshDB(t)
	ctx := context.Background()
	fsys := fstest.MapFS{
		"0001_ok.sql": {Data: []byte(`CREATE TABLE ok_one (id INT PRIMARY KEY);`)},
		// 3rd statement violates the PK at runtime → the whole migration must fail.
		"0002_broken.sql": {Data: []byte("CREATE TABLE broke (id INT PRIMARY KEY);\nINSERT INTO broke (id) VALUES (1);\nINSERT INTO broke (id) VALUES (1);")},
		"0003_after.sql":  {Data: []byte(`CREATE TABLE never_ran (id INT PRIMARY KEY);`)},
	}
	migs, err := migrate.Load(fsys)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	applied, err := migrate.Up(ctx, conn, migs)
	if err == nil {
		t.Fatal("expected an error from the broken migration")
	}
	if len(applied) != 1 || applied[0] != "0001" {
		t.Fatalf("applied=%v, want [0001]", applied)
	}
	var rec int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM schema_migrations WHERE version='0002'`).Scan(&rec); err != nil {
		t.Fatalf("count 0002: %v", err)
	}
	if rec != 0 {
		t.Error("FAIL-OPEN: broken migration 0002 was recorded in schema_migrations")
	}
	var present bool
	conn.QueryRow(ctx, `SELECT to_regclass('public.broke') IS NOT NULL`).Scan(&present)
	if present {
		t.Error("FAIL-OPEN: table 'broke' from the failed migration persisted — must roll back")
	}
	conn.QueryRow(ctx, `SELECT to_regclass('public.never_ran') IS NOT NULL`).Scan(&present)
	if present {
		t.Error("FAIL-OPEN: migration 0003 ran AFTER a failure — must stop")
	}
}

// TestUp_ChecksumMismatch_Refused — a migration edited after being applied (checksum
// no longer matches the recorded one) is detected and refused.
func TestUp_ChecksumMismatch_Refused(t *testing.T) {
	conn := freshDB(t)
	ctx := context.Background()
	v1 := fstest.MapFS{"0001_a.sql": {Data: []byte(`CREATE TABLE a (id INT);`)}}
	migs, _ := migrate.Load(v1)
	if _, err := migrate.Up(ctx, conn, migs); err != nil {
		t.Fatalf("initial up: %v", err)
	}
	// Same version+filename, DIFFERENT bytes → different checksum (edited after apply).
	v2 := fstest.MapFS{"0001_a.sql": {Data: []byte("CREATE TABLE a (id INT);\n-- edited after apply")}}
	migs2, _ := migrate.Load(v2)
	_, err := migrate.Up(ctx, conn, migs2)
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("want checksum-mismatch error, got: %v", err)
	}
}

// TestUp_GapInSequence_Refused — a non-contiguous applied set (a later migration
// recorded while an earlier one is still pending) is detected and refused.
func TestUp_GapInSequence_Refused(t *testing.T) {
	conn := freshDB(t)
	ctx := context.Background()
	fsys := fstest.MapFS{
		"0001_a.sql": {Data: []byte(`CREATE TABLE a (id INT);`)},
		"0002_b.sql": {Data: []byte(`CREATE TABLE b (id INT);`)},
		"0003_c.sql": {Data: []byte(`CREATE TABLE c (id INT);`)},
	}
	migs, _ := migrate.Load(fsys)
	// Apply 0001 normally (creates schema_migrations + records 0001).
	if _, err := migrate.Up(ctx, conn, migs[:1]); err != nil {
		t.Fatalf("apply 0001: %v", err)
	}
	// Seed the gap: mark 0003 applied while 0002 stays pending.
	m3 := migs[2]
	if _, err := conn.Exec(ctx,
		`INSERT INTO schema_migrations (version, name, checksum) VALUES ($1, $2, $3)`,
		m3.Version, m3.Name, m3.Checksum); err != nil {
		t.Fatalf("seed gap: %v", err)
	}
	_, err := migrate.Up(ctx, conn, migs)
	if err == nil || !strings.Contains(err.Error(), "out-of-order") {
		t.Fatalf("want gap/out-of-order error, got: %v", err)
	}
}
