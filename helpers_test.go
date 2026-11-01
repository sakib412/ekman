package ekman

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sakib412/ekman/auth"
	"github.com/sakib412/ekman/internal/testhook"
	"github.com/sakib412/ekman/rbac"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// testAuth is an auth config usable without a database.
func testAuth() *auth.Config { return &auth.Config{Store: auth.MemoryStore()} }

func newApp(t *testing.T, opts Options, mods ...Module) (*App, http.Handler) {
	t.Helper()
	if opts.Logger == nil {
		opts.Logger = quietLogger()
	}
	a, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.Register(mods...)
	h, err := a.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	return a, h
}

type call struct {
	method, path string
	body         string
	header       http.Header
	as           *testhook.Identity
}

func do(t *testing.T, h http.Handler, c call) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if c.body != "" {
		body = strings.NewReader(c.body)
	}
	req := httptest.NewRequest(c.method, c.path, body)
	if c.body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, vs := range c.header {
		req.Header[k] = vs
	}
	if c.as != nil {
		req = req.WithContext(testhook.With(req.Context(), *c.as))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func as(user string, perms ...rbac.Permission) *testhook.Identity {
	return &testhook.Identity{UserID: user, Perms: perms}
}

func decodeErr(t *testing.T, rec *httptest.ResponseRecorder) errorDetail {
	t.Helper()
	var b errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("not an error body: %q", rec.Body.String())
	}
	return b.Error
}

func expectStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d; body %s", rec.Code, want, rec.Body.String())
	}
}

func compact(s string) string {
	var b bytes.Buffer
	_ = json.Compact(&b, []byte(s))
	return b.String()
}
