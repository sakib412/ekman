// Package ekmantest runs an ekman app in tests: an httptest server, requests as a given user
// without OAuth, and a per-test Postgres schema.
package ekmantest

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sakib412/ekman"
	"github.com/sakib412/ekman/auth"
	"github.com/sakib412/ekman/internal/testhook"
	"github.com/sakib412/ekman/rbac"
)

// identityHeader carries As() identities from Client to the test server's wrapper, which
// moves them into the request context. Only that wrapper reads it; ekman itself never does,
// so the header grants nothing on a production server.
const identityHeader = "Ekmantest-Identity"

// Client talks to an app served by httptest.
type Client struct {
	t   testing.TB
	srv *httptest.Server
	app *ekman.App
	id  *testhook.Identity
	hdr http.Header
}

// New builds the app from opts and mods, validates it, and serves it until the test ends.
// When opts.Auth is nil an in-memory session store is configured so SignedIn and Requires
// routes boot; when opts.Logger is nil logs go to t.Log.
func New(t testing.TB, opts ekman.Options, mods ...ekman.Module) *Client {
	t.Helper()
	if opts.Auth == nil {
		opts.Auth = &auth.Config{Store: auth.MemoryStore()}
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(testWriter{t}, nil))
	}
	app, err := ekman.New(opts)
	if err != nil {
		t.Fatalf("ekmantest: New: %v", err)
	}
	app.Register(mods...)
	h, err := app.Handler()
	if err != nil {
		t.Fatalf("ekmantest: boot: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if raw := r.Header.Get(identityHeader); raw != "" {
			r.Header.Del(identityHeader)
			var id testhook.Identity
			if err := json.Unmarshal([]byte(raw), &id); err == nil {
				r = r.WithContext(testhook.With(r.Context(), id))
			}
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return &Client{t: t, srv: srv, app: app}
}

// As returns a client whose requests run as userID holding perms (plus the baseline).
// rbac.All grants every declared permission.
func (c *Client) As(userID string, perms ...rbac.Permission) *Client {
	cp := *c
	cp.id = &testhook.Identity{UserID: userID, Perms: perms}
	return &cp
}

// Anonymous returns a client whose requests carry no identity.
func (c *Client) Anonymous() *Client {
	cp := *c
	cp.id = nil
	return &cp
}

// WithHeader returns a client whose requests carry the header too. It overrides the
// Content-Type Do picks, e.g. for a multipart body.
func (c *Client) WithHeader(key, value string) *Client {
	cp := *c
	cp.hdr = c.hdr.Clone()
	if cp.hdr == nil {
		cp.hdr = http.Header{}
	}
	cp.hdr.Add(key, value)
	return &cp
}

// URL is the test server's base URL.
func (c *Client) URL() string { return c.srv.URL }

// App returns the app under test.
func (c *Client) App() *ekman.App { return c.app }

// Do sends a request. path is the full path including the API prefix ("/api/v1/todos").
// body is sent as is when it is []byte, string or io.Reader, else encoded as JSON; nil sends
// no body.
func (c *Client) Do(method, path string, body any) *Response {
	c.t.Helper()
	var rd io.Reader
	contentType := ""
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	case string:
		rd = strings.NewReader(b)
	case io.Reader:
		rd = b
	default:
		buf, err := json.Marshal(b)
		if err != nil {
			c.t.Fatalf("ekmantest: encode body: %v", err)
		}
		rd, contentType = bytes.NewReader(buf), "application/json"
	}
	req, err := http.NewRequest(method, c.srv.URL+path, rd)
	if err != nil {
		c.t.Fatalf("ekmantest: %v", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, vs := range c.hdr {
		req.Header[k] = vs
	}
	if c.id != nil {
		raw, _ := json.Marshal(c.id)
		req.Header.Set(identityHeader, string(raw))
	}
	resp, err := c.srv.Client().Do(req)
	if err != nil {
		c.t.Fatalf("ekmantest: %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatalf("ekmantest: read body: %v", err)
	}
	return &Response{t: c.t, status: resp.StatusCode, header: resp.Header, body: data}
}

// Response is a fully read HTTP response.
type Response struct {
	t      testing.TB
	status int
	header http.Header
	body   []byte
}

// Status returns the HTTP status code.
func (r *Response) Status() int { return r.status }

// Header returns the response headers.
func (r *Response) Header() http.Header { return r.header }

// Body returns the raw body.
func (r *Response) Body() []byte { return r.body }

// JSON decodes the body into dst, failing the test on error.
func (r *Response) JSON(dst any) {
	r.t.Helper()
	if err := json.Unmarshal(r.body, dst); err != nil {
		r.t.Fatalf("ekmantest: decode %q: %v", r.body, err)
	}
}

// ErrorCode returns error.code from an ekman error body, or "".
func (r *Response) ErrorCode() string {
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(r.body, &e)
	return e.Error.Code
}

type testWriter struct{ t testing.TB }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}
