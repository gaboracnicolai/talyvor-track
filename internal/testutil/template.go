package testutil

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/talyvor/track/internal/migrate"
	"github.com/talyvor/track/migrations"
)

// schemaTemplate returns the name of a database that holds the fully migrated schema, built ONCE per
// schema version by the production migration runner. New() copies it (CREATE DATABASE … TEMPLATE)
// instead of running every migration for every test: the copy costs about half of create + migrate,
// and a package like internal/importer provisions ~145 databases per run (B18.34 — it was using ~70%
// of CI's 120s test budget).
//
// The name carries a hash of every migration's name and checksum, so an added or edited migration
// builds a new template and a stale one is never copied. Test binaries run in parallel against one
// server, so the build is serialised on an advisory lock, done under a temporary name, and renamed into
// place only once complete — a copy never starts from a half-migrated template. The template refuses
// connections: a session connected to it would make every concurrent copy fail.
var (
	templateMu    sync.Mutex
	templateNames = map[string]string{} // admin DSN → template name, cached per process
)

const templateLockKey int64 = 1834_0927

func schemaTemplate(t *testing.T, ctx context.Context, admin string) string {
	t.Helper()
	templateMu.Lock()
	defer templateMu.Unlock()
	if name, ok := templateNames[admin]; ok {
		return name
	}

	migs, err := migrate.Load(migrations.FS)
	if err != nil {
		t.Fatalf("testutil: load migrations: %v", err)
	}
	h := sha256.New()
	for _, m := range migs {
		h.Write([]byte(m.Name + "\x00" + m.Checksum + "\n"))
	}
	name := "track_tmpl_" + hex.EncodeToString(h.Sum(nil))[:16]

	admConn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("testutil: admin connect: %v", err)
	}
	defer func() { _ = admConn.Close(ctx) }()
	if _, err := admConn.Exec(ctx, `SELECT pg_advisory_lock($1)`, templateLockKey); err != nil {
		t.Fatalf("testutil: template lock: %v", err)
	}
	defer func() { _, _ = admConn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, templateLockKey) }()

	var exists bool
	if err := admConn.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&exists); err != nil {
		t.Fatalf("testutil: look up template: %v", err)
	}
	if !exists {
		build := name + "_build"
		if err := dropDB(ctx, admConn, build); err != nil { // what a build that died left behind
			t.Fatalf("testutil: drop stale template build: %v", err)
		}
		if err := createDB(ctx, admConn, build); err != nil {
			t.Fatalf("testutil: create template build: %v", err)
		}
		migConn := connectTo(t, ctx, admin, build) // a single conn so migrate's advisory lock holds
		if _, err := migrate.Up(ctx, migConn, migs); err != nil {
			_ = migConn.Close(ctx)
			t.Fatalf("testutil: migrate template: %v", err)
		}
		_ = migConn.Close(ctx)
		if _, err := admConn.Exec(ctx, "ALTER DATABASE "+quoteIdent(build)+" RENAME TO "+quoteIdent(name)); err != nil {
			t.Fatalf("testutil: publish template: %v", err)
		}
		if _, err := admConn.Exec(ctx, "ALTER DATABASE "+quoteIdent(name)+" WITH ALLOW_CONNECTIONS false"); err != nil {
			t.Fatalf("testutil: seal template: %v", err)
		}
	}
	templateNames[admin] = name
	return name
}

func quoteIdent(name string) string { return pgx.Identifier{mustSafeIdent(name)}.Sanitize() }

func createFromTemplateStmt(name, template string) string {
	return "CREATE DATABASE " + quoteIdent(name) + " TEMPLATE " + quoteIdent(template)
}
