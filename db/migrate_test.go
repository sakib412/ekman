package db

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"
)

func TestLoadOrdersAndChecksums(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/0002_b.sql": {Data: []byte("SELECT 2;")},
		"migrations/0001_a.sql": {Data: []byte("SELECT 1;")},
		"migrations/README.md":  {Data: []byte("ignored")},
	}
	ms, err := Load(Source{Module: "m", FS: fsys})
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 || ms[0].File != "0001_a.sql" || ms[1].Version != 2 {
		t.Fatalf("got %+v", ms)
	}
	if len(ms[0].Checksum) != 64 || ms[0].Checksum == ms[1].Checksum {
		t.Fatalf("checksums %q %q", ms[0].Checksum, ms[1].Checksum)
	}
}

func TestLoadRoot(t *testing.T) {
	ms, err := Load(Source{Module: "m", FS: fstest.MapFS{"0001_a.sql": {Data: []byte("x")}}})
	if err != nil || len(ms) != 1 {
		t.Fatalf("%v %v", ms, err)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := map[string]fstest.MapFS{
		"does not match": {"1_a.sql": {Data: []byte("x")}},
		"share number":   {"0001_a.sql": {Data: []byte("x")}, "0001_b.sql": {Data: []byte("y")}},
	}
	for want, fsys := range tests {
		if _, err := Load(Source{Module: "m", FS: fsys}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want %q", err, want)
		}
	}
	if _, err := Load(Source{FS: fstest.MapFS{}}); err == nil {
		t.Error("empty module name accepted")
	}
}

func TestMigrateDuplicateModule(t *testing.T) {
	fsys := fstest.MapFS{"0001_a.sql": {Data: []byte("x")}}
	err := Migrate(context.Background(), &DB{}, []Source{{"m", fsys}, {"m", fsys}})
	if err == nil || !strings.Contains(err.Error(), "listed twice") {
		t.Fatal(err)
	}
}

func TestAfterCommitWithoutTx(t *testing.T) {
	ran := false
	AfterCommit(context.Background(), func(context.Context) { ran = true })
	if !ran {
		t.Fatal("AfterCommit without a Tx must run immediately")
	}
}
