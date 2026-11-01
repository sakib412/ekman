package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Source is one module's migrations.
type Source struct {
	Module string
	FS     fs.FS
}

// Migration is one parsed migration file.
type Migration struct {
	Module   string
	File     string // base name, e.g. "0001_init.sql"
	Version  int
	SQL      string
	Checksum string // hex sha256 of SQL
}

var migrationName = regexp.MustCompile(`^(\d{4,})_[A-Za-z0-9_.-]+\.sql$`)

// lockKey is an arbitrary constant ("ekman" in ASCII) shared by every ekman app migrating the
// same database, so concurrent deploys serialize.
const lockKey int64 = 0x656b6d616e

const ledgerDDL = `CREATE TABLE IF NOT EXISTS ekman_migrations (
	module     text        NOT NULL,
	file       text        NOT NULL,
	checksum   text        NOT NULL,
	applied_at timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (module, file)
)`

// Load reads a source's migration files: NNNN_name.sql at the FS root, or inside the single
// subdirectory an embed pattern like "migrations/*.sql" produces. Files are ordered by number.
func Load(src Source) ([]Migration, error) {
	if src.Module == "" {
		return nil, errors.New("db: migration source without module name")
	}
	dir, err := migrationDir(src.FS)
	if err != nil {
		return nil, fmt.Errorf("db: module %q: %w", src.Module, err)
	}
	entries, err := fs.ReadDir(src.FS, dir)
	if err != nil {
		return nil, fmt.Errorf("db: module %q: %w", src.Module, err)
	}
	var out []Migration
	seen := map[int]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		m := migrationName.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("db: module %q: %q does not match NNNN_name.sql", src.Module, e.Name())
		}
		v, _ := strconv.Atoi(m[1])
		if prev, dup := seen[v]; dup {
			return nil, fmt.Errorf("db: module %q: %q and %q share number %d", src.Module, prev, e.Name(), v)
		}
		seen[v] = e.Name()
		body, err := fs.ReadFile(src.FS, path.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("db: module %q: %w", src.Module, err)
		}
		sum := sha256.Sum256(body)
		out = append(out, Migration{
			Module: src.Module, File: e.Name(), Version: v,
			SQL: string(body), Checksum: hex.EncodeToString(sum[:]),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// migrationDir descends through directories that hold nothing but one subdirectory, which is
// what //go:embed migrations/*.sql yields.
func migrationDir(fsys fs.FS) (string, error) {
	dir := "."
	for {
		entries, err := fs.ReadDir(fsys, dir)
		if err != nil {
			return "", err
		}
		if len(entries) == 1 && entries[0].IsDir() {
			dir = path.Join(dir, entries[0].Name())
			continue
		}
		return dir, nil
	}
}

// Migrate applies pending migrations in source order, each file in its own transaction, under
// a Postgres advisory lock. Applied files are recorded as (module, file, sha256) in
// ekman_migrations; an applied file whose checksum changed is an error, because migrations are
// forward-only.
func Migrate(ctx context.Context, d *DB, sources []Source) error {
	var all []Migration
	modules := map[string]bool{}
	for _, src := range sources {
		if modules[src.Module] {
			return fmt.Errorf("db: module %q listed twice", src.Module)
		}
		modules[src.Module] = true
		ms, err := Load(src)
		if err != nil {
			return err
		}
		all = append(all, ms...)
	}

	conn, err := d.sql.Conn(ctx)
	if err != nil {
		return fmt.Errorf("db: migrate: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, lockKey); err != nil {
		return fmt.Errorf("db: migrate lock: %w", err)
	}
	defer func() {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, lockKey)
	}()
	if _, err := conn.ExecContext(ctx, ledgerDDL); err != nil {
		return fmt.Errorf("db: migrate ledger: %w", err)
	}
	applied, err := appliedChecksums(ctx, conn)
	if err != nil {
		return err
	}
	for _, m := range all {
		key := m.Module + "/" + m.File
		if sum, ok := applied[key]; ok {
			if sum != m.Checksum {
				return fmt.Errorf("db: migration %s was edited after it was applied (checksum mismatch); add a new migration instead", key)
			}
			continue
		}
		if err := apply(ctx, conn, m); err != nil {
			return err
		}
	}
	return nil
}

func appliedChecksums(ctx context.Context, conn *sql.Conn) (map[string]string, error) {
	rows, err := conn.QueryContext(ctx, `SELECT module, file, checksum FROM ekman_migrations`)
	if err != nil {
		return nil, fmt.Errorf("db: migrate ledger: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var mod, file, sum string
		if err := rows.Scan(&mod, &file, &sum); err != nil {
			return nil, err
		}
		out[mod+"/"+file] = sum
	}
	return out, rows.Err()
}

func apply(ctx context.Context, conn *sql.Conn, m Migration) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
		return fmt.Errorf("db: migration %s/%s: %w", m.Module, m.File, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO ekman_migrations (module, file, checksum) VALUES ($1, $2, $3)`,
		m.Module, m.File, m.Checksum); err != nil {
		return fmt.Errorf("db: migration %s/%s: record: %w", m.Module, m.File, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("db: migration %s/%s: commit: %w", m.Module, m.File, err)
	}
	return nil
}
