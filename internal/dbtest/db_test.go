package dbtest

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/sakib412/ekman/db"
	"github.com/sakib412/ekman/ekmantest"
)

//go:embed testdata/shop/migrations/*.sql
var shopMigrations embed.FS

func count(t *testing.T, ctx context.Context, d *db.DB, q string) int {
	t.Helper()
	var n int
	if err := d.Q(ctx).QueryRowContext(ctx, q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMigrate(t *testing.T) {
	d := ekmantest.DB(t, "pgx")
	ctx := context.Background()
	src := []db.Source{{Module: "shop", FS: shopMigrations}}
	if err := db.Migrate(ctx, d, src); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, d, src); err != nil {
		t.Fatalf("second run must be a no-op: %v", err)
	}
	if n := count(t, ctx, d, `SELECT count(*) FROM ekman_migrations WHERE module = 'shop'`); n != 2 {
		t.Fatalf("ledger rows = %d", n)
	}
	if n := count(t, ctx, d, `SELECT count(*) FROM items WHERE price_cents = 0`); n != 1 {
		t.Fatalf("multi-statement migration did not run fully: %d", n)
	}

	edited := fstest.MapFS{
		"0001_items.sql": {Data: []byte("CREATE TABLE items (id int);")},
		"0002_price.sql": {Data: []byte("SELECT 1;")},
	}
	err := db.Migrate(ctx, d, []db.Source{{Module: "shop", FS: edited}})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("edited migration must fail: %v", err)
	}

	broken := fstest.MapFS{
		"0001_ok.sql":  {Data: []byte("CREATE TABLE other (id int);")},
		"0002_bad.sql": {Data: []byte("CREATE TABLE half (id int); SELECT nope_nope();")},
	}
	if err := db.Migrate(ctx, d, []db.Source{{Module: "other", FS: broken}}); err == nil {
		t.Fatal("broken migration must fail")
	}
	if n := count(t, ctx, d, `SELECT count(*) FROM ekman_migrations WHERE module = 'other'`); n != 1 {
		t.Fatalf("only the good file may be recorded, got %d", n)
	}
	if n := count(t, ctx, d, `SELECT count(*) FROM information_schema.tables WHERE table_name = 'half' AND table_schema = current_schema()`); n != 0 {
		t.Fatal("a failed migration must roll back")
	}
}

func TestTx(t *testing.T) {
	d := ekmantest.DB(t, "pgx")
	ctx := context.Background()
	if _, err := d.Q(ctx).ExecContext(ctx, `CREATE TABLE t (v int)`); err != nil {
		t.Fatal(err)
	}
	var after []string
	err := d.Tx(ctx, func(ctx context.Context) error {
		_, _ = d.Q(ctx).ExecContext(ctx, `INSERT INTO t VALUES (1)`)
		db.AfterCommit(ctx, func(context.Context) { after = append(after, "outer") })
		return d.Tx(ctx, func(ctx context.Context) error { // joins the outer tx
			_, _ = d.Q(ctx).ExecContext(ctx, `INSERT INTO t VALUES (2)`)
			db.AfterCommit(ctx, func(context.Context) { after = append(after, "inner") })
			if len(after) != 0 {
				t.Error("AfterCommit ran before commit")
			}
			// Visible inside the tx, not outside it.
			if n := count(t, ctx, d, `SELECT count(*) FROM t`); n != 2 {
				t.Errorf("inside tx: %d", n)
			}
			if n := count(t, context.Background(), d, `SELECT count(*) FROM t`); n != 0 {
				t.Errorf("outside tx: %d", n)
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(after, ",") != "outer,inner" {
		t.Fatalf("after-commit hooks: %v", after)
	}

	boom := errors.New("boom")
	ran := false
	err = d.Tx(ctx, func(ctx context.Context) error {
		_, _ = d.Q(ctx).ExecContext(ctx, `INSERT INTO t VALUES (3)`)
		db.AfterCommit(ctx, func(context.Context) { ran = true })
		return d.Tx(ctx, func(ctx context.Context) error { return boom })
	})
	if !errors.Is(err, boom) || ran {
		t.Fatalf("rollback: err=%v hookRan=%v", err, ran)
	}
	func() {
		defer func() { _ = recover() }()
		_ = d.Tx(ctx, func(ctx context.Context) error {
			_, _ = d.Q(ctx).ExecContext(ctx, `INSERT INTO t VALUES (4)`)
			panic("oops")
		})
	}()
	if n := count(t, ctx, d, `SELECT count(*) FROM t`); n != 2 {
		t.Fatalf("rows after rollbacks = %d, want 2", n)
	}
	err = d.Q(ctx).QueryRowContext(ctx, `SELECT v FROM t WHERE v = 99`).Scan(new(int))
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
}
