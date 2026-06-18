// Package migrations embeds Track's SQL migration files so the `track migrate`
// runner is self-contained — the binary carries its own schema and can apply it
// anywhere (CI, a fresh deploy) without the files on disk.
//
// The same *.sql files are mounted at /docker-entrypoint-initdb.d for first-boot
// initialization of an empty Postgres volume; that path runs only *.sql/*.sh, so
// this .go file is ignored there.
package migrations

import "embed"

// FS holds every migrations/*.sql file, read by internal/migrate.Load.
//
//go:embed *.sql
var FS embed.FS
