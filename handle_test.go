package ekman

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

type bindIn struct {
	ID      int64      `path:"id"`
	Slug    string     `path:"slug"`
	Limit   *int       `query:"limit"`
	Tags    []string   `query:"tag"`
	Since   time.Time  `query:"since"`
	Until   *time.Time `query:"until"`
	Verbose bool       `query:"verbose"`
	Trace   string     `header:"x-trace"`
	Title   string     `json:"title"`
	Count   int        `json:"count"`
}

type bindOut struct {
	ID      int64     `json:"id"`
	Slug    string    `json:"slug"`
	Limit   *int      `json:"limit"`
	Tags    []string  `json:"tags"`
	Since   time.Time `json:"since"`
	Until   bool      `json:"untilSet"`
	Verbose bool      `json:"verbose"`
	Trace   string    `json:"trace"`
	Title   string    `json:"title"`
	Count   int       `json:"count"`
}

func echo(_ context.Context, _ Caller, in *bindIn) (bindOut, error) {
	return bindOut{in.ID, in.Slug, in.Limit, in.Tags, in.Since, in.Until != nil, in.Verbose, in.Trace, in.Title, in.Count}, nil
}

func bindModule() Module {
	return Module{Name: "bind", Routes: func(r *Router) {
		Handle(r, "GET /things/{id}/{slug}", Public(), echo)
		Handle(r, "PUT /things/{id}/{slug}", Public(), echo)
		Handle(r, "POST /things/{id}/{slug}", Public(), echo)
	}}
}

func TestBindAll(t *testing.T) {
	_, h := newApp(t, Options{}, bindModule())
	rec := do(t, h, call{
		method: "PUT",
		path:   "/api/v1/things/42/hello?limit=5&tag=a&tag=b&since=2026-01-02T03:04:05Z&until=2026-02-01T00:00:00Z&verbose=true",
		body:   `{"title":"x","count":3,"ID":999}`,
		header: http.Header{"X-Trace": {"t-1"}},
	})
	expectStatus(t, rec, 200)
	var out bindOut
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.ID != 42 || out.Slug != "hello" || out.Limit == nil || *out.Limit != 5 || len(out.Tags) != 2 ||
		out.Since.Year() != 2026 || !out.Until || !out.Verbose || out.Trace != "t-1" || out.Title != "x" || out.Count != 3 {
		t.Fatalf("bad binding: %+v", out)
	}
}

func TestBindGetIgnoresBodyAndDefaults201ForPost(t *testing.T) {
	_, h := newApp(t, Options{}, bindModule())
	rec := do(t, h, call{method: "GET", path: "/api/v1/things/1/x"})
	expectStatus(t, rec, 200)
	if !strings.Contains(rec.Body.String(), `"limit":null`) {
		t.Fatalf("absent optional query should be nil: %s", rec.Body.String())
	}
	rec = do(t, h, call{method: "POST", path: "/api/v1/things/1/x", body: `{}`})
	expectStatus(t, rec, 201)
}

func TestBindErrors(t *testing.T) {
	_, h := newApp(t, Options{}, bindModule())
	tests := []struct {
		name   string
		c      call
		status int
		code   string
		field  string
	}{
		{"bad path int", call{method: "GET", path: "/api/v1/things/abc/x"}, 400, "invalid_param", "id"},
		{"bad query int", call{method: "GET", path: "/api/v1/things/1/x?limit=z"}, 400, "invalid_param", "limit"},
		{"bad bool", call{method: "GET", path: "/api/v1/things/1/x?verbose=maybe"}, 400, "invalid_param", "verbose"},
		{"bad time", call{method: "GET", path: "/api/v1/things/1/x?since=yesterday"}, 400, "invalid_param", "since"},
		{"bad json", call{method: "PUT", path: "/api/v1/things/1/x", body: `{"title":`}, 400, "invalid_json", ""},
		{"wrong json type", call{method: "PUT", path: "/api/v1/things/1/x", body: `{"count":"many"}`}, 400, "invalid_json", ""},
		{"two values", call{method: "PUT", path: "/api/v1/things/1/x", body: `{} {}`}, 400, "invalid_json", ""},
		{"media type", call{method: "PUT", path: "/api/v1/things/1/x", body: `{}`, header: http.Header{"Content-Type": {"text/plain"}}}, 415, "unsupported_media_type", ""},
		{"too large", call{method: "PUT", path: "/api/v1/things/1/x", body: `{"title":"` + strings.Repeat("a", maxBodyBytes) + `"}`}, 413, "too_large", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, h, tt.c)
			expectStatus(t, rec, tt.status)
			e := decodeErr(t, rec)
			if e.Code != tt.code || (tt.field != "" && e.Fields[tt.field] == "") {
				t.Fatalf("got %+v", e)
			}
			if e.RequestID == "" {
				t.Fatal("missing requestId")
			}
		})
	}
}

type createIn struct {
	Title string `json:"title"`
	Email string `json:"email"`
}

func (in *createIn) Validate(v *Validation) {
	v.Required("title", in.Title)
	v.MaxLen("title", in.Title, 5)
	v.Email("email", in.Email)
}

type item struct {
	Title string `json:"title"`
}

func TestValidationRunsBeforeHandler(t *testing.T) {
	called := false
	_, h := newApp(t, Options{}, Module{Name: "v", Routes: func(r *Router) {
		Handle(r, "POST /items", Public(), func(_ context.Context, _ Caller, in *createIn) (item, error) {
			called = true
			return item{in.Title}, nil
		})
	}})
	rec := do(t, h, call{method: "POST", path: "/api/v1/items", body: `{"title":"too long","email":"nope"}`})
	expectStatus(t, rec, 422)
	e := decodeErr(t, rec)
	if e.Code != "invalid_input" || e.Fields["title"] == "" || e.Fields["email"] == "" || called {
		t.Fatalf("got %+v called=%v", e, called)
	}
	rec = do(t, h, call{method: "POST", path: "/api/v1/items", body: `{"title":"ok"}`})
	expectStatus(t, rec, 201)
	if compact(rec.Body.String()) != `{"title":"ok"}` {
		t.Fatal(rec.Body.String())
	}
}

func TestHandlerResults(t *testing.T) {
	_, h := newApp(t, Options{}, Module{Name: "r", Routes: func(r *Router) {
		Handle(r, "DELETE /x", Public(), func(context.Context, Caller, *struct{}) (NoContent, error) { return NoContent{}, nil })
		Handle(r, "GET /list", Public(), func(context.Context, Caller, *struct{}) ([]item, error) { return nil, nil })
		Handle(r, "GET /missing", Public(), func(context.Context, Caller, *struct{}) (item, error) {
			return item{}, fmt.Errorf("lookup: %w", sql.ErrNoRows)
		})
		Handle(r, "GET /conflict", Public(), func(context.Context, Caller, *struct{}) (item, error) {
			return item{}, Conflict("taken", "already exists")
		})
		Handle(r, "GET /boom", Public(), func(context.Context, Caller, *struct{}) (item, error) {
			return item{}, errors.New("secret detail")
		})
		Handle(r, "POST /accepted", Public(), func(context.Context, Caller, *struct{}) (item, error) { return item{}, nil }, Status(202))
		Handle(r, "GET /panic", Public(), func(context.Context, Caller, *struct{}) (item, error) { panic("oops") })
	}})
	rec := do(t, h, call{method: "DELETE", path: "/api/v1/x"})
	expectStatus(t, rec, 204)
	if rec.Body.Len() != 0 {
		t.Fatal("204 with body")
	}
	rec = do(t, h, call{method: "GET", path: "/api/v1/list"})
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("nil slice should encode as []: %q", rec.Body.String())
	}
	rec = do(t, h, call{method: "GET", path: "/api/v1/missing"})
	expectStatus(t, rec, 404)
	rec = do(t, h, call{method: "GET", path: "/api/v1/conflict"})
	expectStatus(t, rec, 409)
	if e := decodeErr(t, rec); e.Code != "taken" || e.Message != "already exists" {
		t.Fatal(e)
	}
	rec = do(t, h, call{method: "GET", path: "/api/v1/boom"})
	expectStatus(t, rec, 500)
	if strings.Contains(rec.Body.String(), "secret") {
		t.Fatal("500 leaked the error")
	}
	rec = do(t, h, call{method: "POST", path: "/api/v1/accepted"})
	expectStatus(t, rec, 202)
	rec = do(t, h, call{method: "GET", path: "/api/v1/panic"})
	expectStatus(t, rec, 500)
	if decodeErr(t, rec).Code != "internal" {
		t.Fatal(rec.Body.String())
	}
}

func TestRawAndGroups(t *testing.T) {
	var order []string
	mw := func(name string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}
	a, err := New(Options{Logger: quietLogger()})
	if err != nil {
		t.Fatal(err)
	}
	a.Use(mw("app"))
	a.Register(Module{Name: "g", Routes: func(r *Router) {
		g := r.Group("/admin", mw("group"))
		g.Raw("GET /raw", Public(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ri, _ := RouteFrom(r.Context())
			WriteJSON(w, 200, map[string]any{"pattern": ri.Pattern, "tagged": ri.HasTag("audit")})
		}), Use(mw("route")), Tag("audit"))
		Handle(g, "GET /", Public(), func(context.Context, Caller, *struct{}) (item, error) { return item{"root"}, nil })
	}})
	h, err := a.Handler()
	if err != nil {
		t.Fatal(err)
	}
	rec := do(t, h, call{method: "GET", path: "/api/v1/admin/raw"})
	expectStatus(t, rec, 200)
	if compact(rec.Body.String()) != `{"pattern":"/api/v1/admin/raw","tagged":true}` {
		t.Fatal(rec.Body.String())
	}
	if strings.Join(order, ",") != "app,group,route" {
		t.Fatalf("middleware order %v", order)
	}
	expectStatus(t, do(t, h, call{method: "GET", path: "/api/v1/admin"}), 200)
	expectStatus(t, do(t, h, call{method: "GET", path: "/api/v1/admin/other"}), 404)
}

func TestRootPrefix(t *testing.T) {
	_, h := newApp(t, Options{APIPrefix: "/"}, Module{Name: "m", Routes: func(r *Router) {
		Handle(r, "GET /x", Public(), func(context.Context, Caller, *struct{}) (item, error) { return item{}, nil })
	}})
	expectStatus(t, do(t, h, call{method: "GET", path: "/x"}), 200)
	expectStatus(t, do(t, h, call{method: "GET", path: "/openapi.json"}), 200)
}
