package dbtest

import (
	"context"
	"errors"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/sakib412/ekman"
	"github.com/sakib412/ekman/auth"
	"github.com/sakib412/ekman/ekmantest"
)

type item struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type itemIn struct {
	ID int64 `path:"id"`
}

func TestAppMigrateAndNoRows(t *testing.T) {
	d := ekmantest.DB(t, "pgx")
	ctx := context.Background()
	mod := ekman.Module{
		Name:       "shop",
		Migrations: shopMigrations,
		Routes: func(r *ekman.Router) {
			ekman.Handle(r, "GET /items/{id}", ekman.Public(), func(ctx context.Context, _ ekman.Caller, in *itemIn) (item, error) {
				var it item
				err := d.Q(ctx).QueryRowContext(ctx, `SELECT id, name FROM items WHERE id = $1`, in.ID).Scan(&it.ID, &it.Name)
				return it, err
			})
		},
	}
	// Auth with the default Postgres store: ekman ships the ekman_sessions migration.
	opts := ekman.Options{DB: d, Auth: &auth.Config{}}
	app, err := ekman.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	app.Register(mod)
	if err := app.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var modules []string
	rows, err := d.Q(ctx).QueryContext(ctx, `SELECT module || '/' || file FROM ekman_migrations ORDER BY applied_at, module, file`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		modules = append(modules, s)
	}
	rows.Close()
	if len(modules) != 3 || modules[0] != "ekman/0001_sessions.sql" {
		t.Fatalf("migration order: %v", modules)
	}

	c := ekmantest.New(t, opts, mod)
	if r := c.Do("GET", "/api/v1/items/1", nil); r.Status() != 200 {
		t.Fatalf("%d %s", r.Status(), r.Body())
	}
	if r := c.Do("GET", "/api/v1/items/999", nil); r.Status() != 404 || r.ErrorCode() != "not_found" {
		t.Fatalf("sql.ErrNoRows must map to 404: %d %s", r.Status(), r.Body())
	}
}

func TestPostgresStore(t *testing.T) {
	d := ekmantest.DB(t, "pgx")
	ctx := context.Background()
	app, err := ekman.New(ekman.Options{DB: d, Auth: &auth.Config{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	s := auth.PostgresStore(d)
	now := time.Now().Truncate(time.Microsecond)
	sess := auth.Session{TokenHash: []byte("hash-1"), UserID: "u1", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := s.Create(ctx, sess); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, []byte("hash-1"))
	if err != nil || got.UserID != "u1" || !got.ExpiresAt.Equal(sess.ExpiresAt) {
		t.Fatalf("%+v %v", got, err)
	}
	if err := s.Touch(ctx, []byte("hash-1"), now.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, []byte("hash-1")); !errors.Is(err, auth.ErrNoSession) {
		t.Fatal("expired session returned")
	}
	_ = s.Touch(ctx, []byte("hash-1"), now.Add(time.Hour))
	_ = s.Create(ctx, auth.Session{TokenHash: []byte("hash-2"), UserID: "u1", CreatedAt: now, ExpiresAt: now.Add(time.Hour)})
	if err := s.Delete(ctx, []byte("hash-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, []byte("hash-1")); !errors.Is(err, auth.ErrNoSession) {
		t.Fatal("delete")
	}
	if err := s.DeleteUser(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, []byte("hash-2")); !errors.Is(err, auth.ErrNoSession) {
		t.Fatal("delete user")
	}
	if _, err := s.Get(ctx, []byte("missing")); !errors.Is(err, auth.ErrNoSession) {
		t.Fatal("missing")
	}
}
