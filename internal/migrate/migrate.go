// Package migrate is Track's forward-only SQL migration runner.
//
// Migrations are plain *.sql files named NNNN_name.sql (e.g. 0001_core.sql),
// applied in ascending numeric order, each in its OWN transaction. Applied
// versions are recorded in schema_migrations with a sha256 checksum, so a file
// edited after it was applied is detected and refused. The runner is idempotent
// (re-running applies nothing) and FAIL-CLOSED: any error rolls that migration
// back, stops, and returns — it never continues past a failure.
//
// Scope: this targets a FRESH database (the CI substrate + new deployments). A
// deployment whose schema was already applied via docker-entrypoint-initdb.d has
// no schema_migrations record; baselining such a database (marking the current
// set applied without re-running it) is a separate, deliberate step and is NOT in
// this runner yet — see the PR notes.
package migrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// advisoryLockKey serializes concurrent `up` runs cluster-wide. A second runner
// taking this session-level pg_advisory_lock blocks until the first finishes, then
// sees the applied set and no-ops. The value is an arbitrary fixed constant.
const advisoryLockKey int64 = 7_320_119_540_023_117

var fileRE = regexp.MustCompile(`^(\d+)_.+\.sql$`)

// Migration is one forward-only migration file.
type Migration struct {
	Version  string // numeric prefix, e.g. "0001"
	Name     string // full filename, e.g. "0001_core.sql"
	SQL      string
	Checksum string // sha256 hex of the file bytes
}

// Load reads every NNNN_name.sql from fsys, checksums it, and returns them sorted
// by version. Rejects a malformed filename or a duplicate version.
func Load(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("migrate: read dir: %w", err)
	}
	var migs []Migration
	seen := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		mm := fileRE.FindStringSubmatch(e.Name())
		if mm == nil {
			return nil, fmt.Errorf("migrate: malformed migration filename %q (want NNNN_name.sql)", e.Name())
		}
		b, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, fmt.Errorf("migrate: read %s: %w", e.Name(), err)
		}
		version := mm[1]
		if prev, dup := seen[version]; dup {
			return nil, fmt.Errorf("migrate: duplicate version %s (%s and %s)", version, prev, e.Name())
		}
		seen[version] = e.Name()
		sum := sha256.Sum256(b)
		migs = append(migs, Migration{
			Version:  version,
			Name:     e.Name(),
			SQL:      string(b),
			Checksum: hex.EncodeToString(sum[:]),
		})
	}
	sort.Slice(migs, func(i, j int) bool { return migs[i].Version < migs[j].Version })
	return migs, nil
}

// ensureSQL creates the bookkeeping table. EnsureSQL is exported for tests that
// need to seed an applied-state directly.
const EnsureSQL = `CREATE TABLE IF NOT EXISTS schema_migrations (
    version    TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    checksum   TEXT NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`

type appliedRow struct {
	version  string
	name     string
	checksum string
}

// Status is the applied-vs-pending view.
type Status struct {
	Applied []Migration
	Pending []Migration
}

// Up applies all pending migrations in order, each in its own transaction, under a
// cluster-wide advisory lock. Returns the versions applied this run (empty when
// already up to date). Fail-closed: on any error it stops and returns, having
// rolled back the failing migration and recorded nothing for it.
func Up(ctx context.Context, conn *pgx.Conn, migs []Migration) (applied []string, err error) {
	// Serialize concurrent runners (session lock on this single connection).
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, advisoryLockKey); err != nil {
		return nil, fmt.Errorf("migrate: advisory lock: %w", err)
	}
	defer func() { _, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, advisoryLockKey) }()

	if _, err := conn.Exec(ctx, EnsureSQL); err != nil {
		return nil, fmt.Errorf("migrate: ensure schema_migrations: %w", err)
	}
	rows, err := readApplied(ctx, conn)
	if err != nil {
		return nil, fmt.Errorf("migrate: read applied: %w", err)
	}
	if err := validate(migs, rows); err != nil {
		return nil, err // checksum mismatch / missing file / gap — refuse before touching anything
	}
	done := map[string]bool{}
	for _, a := range rows {
		done[a.version] = true
	}
	for _, m := range migs {
		if done[m.Version] {
			continue
		}
		if err := applyOne(ctx, conn, m); err != nil {
			// Fail closed: the migration rolled back in its own tx; stop here.
			return applied, fmt.Errorf("migrate: applying %s: %w", m.Name, err)
		}
		applied = append(applied, m.Version)
	}
	return applied, nil
}

// applyOne runs one migration and records it ATOMICALLY in a single transaction:
// if the SQL fails, the deferred Rollback discards both the schema change and the
// record, so a failed migration is never marked applied.
func applyOne(ctx context.Context, conn *pgx.Conn, m Migration) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful Commit
	if _, err := tx.Exec(ctx, m.SQL); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO schema_migrations (version, name, checksum) VALUES ($1, $2, $3)`,
		m.Version, m.Name, m.Checksum,
	); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// StatusOf reports applied vs pending without mutating anything (a missing
// schema_migrations table means nothing is applied yet).
func StatusOf(ctx context.Context, conn *pgx.Conn, migs []Migration) (Status, error) {
	var present bool
	if err := conn.QueryRow(ctx, `SELECT to_regclass('public.schema_migrations') IS NOT NULL`).Scan(&present); err != nil {
		return Status{}, err
	}
	done := map[string]bool{}
	if present {
		rows, err := readApplied(ctx, conn)
		if err != nil {
			return Status{}, err
		}
		for _, a := range rows {
			done[a.version] = true
		}
	}
	var st Status
	for _, m := range migs {
		if done[m.Version] {
			st.Applied = append(st.Applied, m)
		} else {
			st.Pending = append(st.Pending, m)
		}
	}
	return st, nil
}

func readApplied(ctx context.Context, conn *pgx.Conn) ([]appliedRow, error) {
	rows, err := conn.Query(ctx, `SELECT version, name, checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []appliedRow
	for rows.Next() {
		var a appliedRow
		if err := rows.Scan(&a.version, &a.name, &a.checksum); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// validate refuses to apply if the recorded state is inconsistent with the files:
//   - an applied version with no corresponding file (deleted after apply);
//   - an applied file whose checksum changed (edited after apply);
//   - a gap / out-of-order applied set (applied must be a contiguous prefix of the
//     sorted files — you may not have N applied while N-1 is pending).
func validate(migs []Migration, applied []appliedRow) error {
	byVersion := make(map[string]Migration, len(migs))
	for _, m := range migs {
		byVersion[m.Version] = m
	}
	appliedSet := make(map[string]bool, len(applied))
	for _, a := range applied {
		f, ok := byVersion[a.version]
		if !ok {
			return fmt.Errorf("migrate: applied migration %s (%s) has no file on disk (deleted after apply?); refusing", a.version, a.name)
		}
		if f.Checksum != a.checksum {
			return fmt.Errorf("migrate: checksum mismatch for %s: recorded %s, file is %s — a migration was edited after it was applied; refusing", f.Name, short(a.checksum), short(f.Checksum))
		}
		appliedSet[a.version] = true
	}
	pendingSeen := ""
	for _, m := range migs {
		if appliedSet[m.Version] {
			if pendingSeen != "" {
				return fmt.Errorf("migrate: out-of-order/gap: %s is applied but the earlier %s is pending; refusing", m.Version, pendingSeen)
			}
			continue
		}
		if pendingSeen == "" {
			pendingSeen = m.Version
		}
	}
	return nil
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
