package ekmantest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	"github.com/sakib412/ekman/db"
)

// EnvDatabaseURL names the variable holding the Postgres URL for database tests.
const EnvDatabaseURL = "EKMAN_TEST_DATABASE_URL"

// DB opens $EKMAN_TEST_DATABASE_URL with driverName (the test imports the driver, e.g.
// pgx/v5/stdlib as "pgx") inside a fresh schema that is dropped when the test ends, so tests
// can migrate and write freely and run in parallel. It skips the test when the variable is
// unset. The schema is selected with the search_path connection parameter, which the pgx and
// lib/pq drivers pass to the server.
func DB(t testing.TB, driverName string) *db.DB {
	t.Helper()
	raw := os.Getenv(EnvDatabaseURL)
	if raw == "" {
		t.Skip(EnvDatabaseURL + " is not set")
	}
	admin, err := db.Open(driverName, raw)
	if err != nil {
		t.Fatalf("ekmantest: %v", err)
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "ekmantest_" + hex.EncodeToString(b)
	ctx := context.Background()
	if _, err := admin.SQL().ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		admin.Close()
		t.Fatalf("ekmantest: create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.SQL().ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
		admin.Close()
	})
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("ekmantest: %s must be a URL: %v", EnvDatabaseURL, err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	d, err := db.Open(driverName, u.String())
	if err != nil {
		t.Fatalf("ekmantest: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}
