package ekman

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/sakib412/ekman/auth"
	"github.com/sakib412/ekman/rbac"
)

func noop(context.Context, Caller, *struct{}) (NoContent, error) { return NoContent{}, nil }

func bootErr(t *testing.T, opts Options, mods ...Module) string {
	t.Helper()
	opts.Logger = quietLogger()
	a, err := New(opts)
	if err != nil {
		return err.Error()
	}
	a.Register(mods...)
	_, err = a.Handler()
	if err == nil {
		t.Fatal("expected a boot error")
	}
	return err.Error()
}

func TestBootValidation(t *testing.T) {
	routes := func(f func(r *Router)) Module { return Module{Name: "m", Routes: f} }
	tests := []struct {
		name string
		opts Options
		mods []Module
		want string
	}{
		{"duplicate module", Options{}, []Module{{Name: "a"}, {Name: "a"}}, `module "a" registered twice`},
		{"reserved module", Options{}, []Module{{Name: "ekman"}}, "reserved"},
		{"empty module", Options{}, []Module{{Name: ""}}, "must be non-empty"},
		{"duplicate route", Options{}, []Module{
			routes(func(r *Router) { Handle(r, "GET /x", Public(), noop) }),
			{Name: "n", Routes: func(r *Router) { Handle(r, "GET /x", Public(), noop) }},
		}, `route GET /api/v1/x registered by module "m" and module "n"`},
		{"conflicting route", Options{}, []Module{routes(func(r *Router) {
			Handle(r, "GET /x/{a}", Public(), noop)
			Handle(r, "GET /x/{b}", Public(), noop)
		})}, "conflicts with"},
		{"undeclared route permission", Options{Auth: testAuth()}, []Module{routes(func(r *Router) {
			Handle(r, "GET /x", Requires("todo.mange"), noop)
		})}, `permission "todo.mange" is not declared`},
		{"requires all", Options{Auth: testAuth()}, []Module{routes(func(r *Router) {
			Handle(r, "GET /x", Requires(rbac.All), noop)
		})}, "Requires(rbac.All) is not allowed"},
		{"undeclared role permission", Options{Roles: []rbac.Role{{Name: "r", Permissions: []rbac.Permission{"nope"}}}}, nil, `permission "nope" is not declared`},
		{"undeclared baseline", Options{Baseline: []rbac.Permission{"nope"}}, nil, `baseline: permission "nope"`},
		{"signed in without auth", Options{}, []Module{routes(func(r *Router) {
			Handle(r, "GET /x", SignedIn(), noop)
		})}, "Options.Auth is nil"},
		{"requires without auth", Options{}, []Module{{Name: "m", Permissions: []rbac.Permission{"a.b"}, Routes: func(r *Router) {
			Handle(r, "GET /x", Requires("a.b"), noop)
		}}}, "Options.Auth is nil"},
		{"zero access", Options{}, []Module{routes(func(r *Router) { Handle(r, "GET /x", Access{}, noop) })}, "access is required"},
		{"no method", Options{}, []Module{routes(func(r *Router) { Handle(r, "/x", Public(), noop) })}, "method is required"},
		{"bad method", Options{}, []Module{routes(func(r *Router) { Handle(r, "FETCH /x", Public(), noop) })}, "unknown method"},
		{"path tag without wildcard", Options{}, []Module{routes(func(r *Router) {
			Handle(r, "GET /x", Public(), func(context.Context, Caller, *ownIn) (NoContent, error) { return NoContent{}, nil })
		})}, `binds path value "owner"`},
		{"unbindable field", Options{}, []Module{routes(func(r *Router) {
			Handle(r, "GET /x", Public(), func(context.Context, Caller, *struct {
				M map[string]string `query:"m"`
			}) (NoContent, error) {
				return NoContent{}, nil
			})
		})}, "cannot bind from query"},
		{"migrations without db", Options{}, []Module{{Name: "m", Migrations: fstest.MapFS{"0001_a.sql": {Data: []byte("x")}}}}, "Options.DB is nil"},
		{"bad migration name", Options{}, []Module{{Name: "m", Migrations: fstest.MapFS{"1_a.sql": {Data: []byte("x")}}}}, "NNNN_name.sql"},
		{"bad group", Options{}, []Module{routes(func(r *Router) { r.Group("admin/") })}, "group prefix"},
		{"auth needs db", Options{Auth: &auth.Config{}}, nil, "Options.DB is required"},
		{"auth needs directory", Options{Auth: &auth.Config{Store: auth.MemoryStore(), Providers: []auth.Provider{auth.Google("a", "b")}}}, nil, "Directory is required"},
		{"short secret", Options{Auth: &auth.Config{Store: auth.MemoryStore(), Secret: []byte("short")}}, nil, "at least 32 bytes"},
		{"bad cors", Options{CORS: []string{"https://x.com/"}}, nil, "CORS origin"},
		{"bad prefix", Options{APIPrefix: "api"}, nil, "APIPrefix"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bootErr(t, tt.opts, tt.mods...); !strings.Contains(got, tt.want) {
				t.Fatalf("error %q does not contain %q", got, tt.want)
			}
		})
	}
}

func TestBootReportsAllErrors(t *testing.T) {
	got := bootErr(t, Options{}, Module{Name: "a"}, Module{Name: "a", Routes: func(r *Router) {
		Handle(r, "GET /x", SignedIn(), noop)
	}})
	if !strings.Contains(got, "registered twice") || !strings.Contains(got, "Options.Auth is nil") {
		t.Fatal(got)
	}
}

func TestRegisterAfterBuildPanics(t *testing.T) {
	a, _ := newApp(t, Options{})
	defer func() {
		if recover() == nil {
			t.Fatal("Register after Handler must panic")
		}
	}()
	a.Register(Module{Name: "late"})
}

func TestRoutesSortedAndHealthz(t *testing.T) {
	a, h := newApp(t, Options{Auth: testAuth()}, Module{Name: "m", Routes: func(r *Router) {
		Handle(r, "POST /b", Public(), noop, Summary("make b"))
		Handle(r, "GET /b", Public(), noop)
		Handle(r, "GET /a", Public(), noop)
	}})
	var got []string
	for _, ri := range a.Routes() {
		got = append(got, ri.Method+" "+ri.Pattern)
	}
	want := "GET /api/v1/a,POST /api/v1/auth/logout,GET /api/v1/auth/me,GET /api/v1/auth/{provider}/callback,GET /api/v1/auth/{provider}/start,GET /api/v1/b,POST /api/v1/b,GET /api/v1/openapi.json"
	if strings.Join(got, ",") != want {
		t.Fatalf("routes:\n%s\nwant\n%s", strings.Join(got, ","), want)
	}
	rec := do(t, h, call{method: "GET", path: "/healthz"})
	expectStatus(t, rec, 200)
	if compact(rec.Body.String()) != `{"status":"ok"}` {
		t.Fatal(rec.Body.String())
	}
	rec = do(t, h, call{method: "DELETE", path: "/api/v1/b"})
	expectStatus(t, rec, http.StatusMethodNotAllowed)
	if decodeErr(t, rec).Code != "method_not_allowed" || rec.Header().Get("Allow") == "" {
		t.Fatal(rec.Body.String(), rec.Header())
	}
	rec = do(t, h, call{method: "GET", path: "/nope"})
	expectStatus(t, rec, 404)
	if decodeErr(t, rec).Code != "not_found" {
		t.Fatal(rec.Body.String())
	}
}
