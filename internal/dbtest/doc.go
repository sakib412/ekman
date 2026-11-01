// Package dbtest holds ekman's Postgres integration tests. It is a separate module so the
// root module stays dependency-free while these tests use a real driver (pgx). Run with
// EKMAN_TEST_DATABASE_URL set; without it every test skips.
package dbtest
