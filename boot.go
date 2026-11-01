package ekman

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"sort"
	"strings"

	"github.com/sakib412/ekman/db"
	"github.com/sakib412/ekman/rbac"
)

// boot is everything derived from the registered modules, computed once.
type boot struct {
	handler  http.Handler
	routes   []*route // sorted by pattern, then method
	registry *rbac.Registry
	openapi  []byte
	sources  []db.Source
}

func (a *App) build() (*boot, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.built {
		a.built = true
		a.boot, a.err = a.doBuild()
	}
	return a.boot, a.err
}

// migrationsFS is implemented by session stores that ship a schema (auth.PostgresStore).
type migrationsFS interface{ Migrations() fs.FS }

func (a *App) doBuild() (*boot, error) {
	var errs []error
	errorf := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	b := &boot{}

	if s, ok := a.store.(migrationsFS); ok {
		b.sources = append(b.sources, db.Source{Module: internalModule, FS: s.Migrations()})
	}
	seen := map[string]bool{internalModule: true}
	var declared []rbac.Permission
	for _, m := range a.mods {
		switch {
		case strings.TrimSpace(m.Name) == "" || strings.ContainsAny(m.Name, " /"):
			errorf("module name %q must be non-empty without spaces or slashes", m.Name)
		case m.Name == internalModule:
			errorf("module name %q is reserved", m.Name)
		case seen[m.Name]:
			errorf("module %q registered twice", m.Name)
		}
		seen[m.Name] = true
		declared = append(declared, m.Permissions...)
		if m.Migrations != nil {
			if a.opts.DB == nil {
				errorf("module %q has migrations but Options.DB is nil", m.Name)
			}
			b.sources = append(b.sources, db.Source{Module: m.Name, FS: m.Migrations})
		}
	}
	for _, src := range b.sources {
		if _, err := db.Load(src); err != nil {
			errs = append(errs, err)
		}
	}

	reg, err := rbac.NewRegistry(declared, a.opts.Roles, a.opts.Baseline)
	if err != nil {
		errs = append(errs, err)
		// Keep going without roles so route errors are reported in the same run.
		reg, _ = rbac.NewRegistry(validOnly(declared), nil, nil)
	}
	b.registry = reg

	rb := &builder{apiPrefix: a.opts.APIPrefix}
	internal := &Router{b: rb, module: internalModule}
	if a.opts.Auth != nil {
		a.authRoutes(internal)
	}
	internal.Raw("GET /openapi.json", Public(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b.openapi)
	}), hidden())
	for _, m := range a.mods {
		if m.Routes != nil {
			m.Routes(&Router{b: rb, module: m.Name})
		}
	}
	errs = append(errs, rb.errs...)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	keys := map[string]string{}
	for _, rt := range rb.routes {
		ri := rt.info
		key := strings.TrimSpace(ri.Method + " " + ri.Pattern)
		if prev, dup := keys[key]; dup {
			errorf("route %s registered by module %q and module %q", key, prev, ri.Module)
			continue
		}
		keys[key] = ri.Module
		if rt.access.kind == accessRequires {
			switch {
			case rt.access.perm == rbac.All:
				errorf("route %s: Requires(rbac.All) is not allowed; rbac.All only belongs in roles", key)
			case !reg.Declared(rt.access.perm):
				errorf("route %s: permission %q is not declared by any module (add it to Module.Permissions)", key, rt.access.perm)
			}
		}
		if (rt.access.kind == accessSignedIn || rt.access.kind == accessRequires) && a.opts.Auth == nil {
			errorf("route %s requires a signed-in caller but Options.Auth is nil", key)
		}
		if err := muxHandle(mux, key, a.routeHandler(rt, reg)); err != nil {
			errorf("route %s (module %q): %v", key, ri.Module, err)
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	b.routes = rb.routes
	sort.SliceStable(b.routes, func(i, j int) bool {
		if b.routes[i].info.Pattern != b.routes[j].info.Pattern {
			return b.routes[i].info.Pattern < b.routes[j].info.Pattern
		}
		return b.routes[i].info.Method < b.routes[j].info.Method
	})
	doc, err := a.generateOpenAPI(b.routes, reg)
	if err != nil {
		return nil, fmt.Errorf("openapi: %w", err)
	}
	b.openapi = doc
	b.handler = chain(dispatch(mux),
		requestIDMiddleware(a.log),
		recoverMiddleware,
		accessLogMiddleware,
		corsMiddleware(a.opts.CORS),
	)
	return b, nil
}

func validOnly(perms []rbac.Permission) []rbac.Permission {
	var out []rbac.Permission
	for _, p := range perms {
		if p != "" && p != rbac.All && !strings.ContainsAny(string(p), " \t\n") {
			out = append(out, p)
		}
	}
	return out
}

// muxHandle turns ServeMux's panic on conflicting patterns into an error.
func muxHandle(mux *http.ServeMux, pattern string, h http.Handler) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("%v", p)
		}
	}()
	mux.Handle(pattern, h)
	return nil
}

// routeHandler builds a route's chain: route info → session → app middleware → group and
// route middleware → access check → endpoint.
func (a *App) routeHandler(rt *route, reg *rbac.Registry) http.Handler {
	access := rt.access
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := access.check(CallerFrom(r.Context())); err != nil {
			WriteError(w, r, err)
			return
		}
		rt.h.ServeHTTP(w, r)
	})
	inner := chain(h, rt.mw...)
	inner = chain(inner, a.appMW...)
	inner = a.sessionMiddleware(reg)(inner)
	info := rt.info
	label := strings.TrimSpace(info.Method + " " + info.Pattern)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if st := stateFrom(ctx); st != nil {
			st.route = label
		}
		inner.ServeHTTP(w, r.WithContext(context.WithValue(ctx, routeKey{}, info)))
	})
}

// dispatch serves the mux, but answers unmatched requests (404, 405) in ekman's error shape.
func dispatch(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, pattern := mux.Handler(r)
		if pattern != "" {
			mux.ServeHTTP(w, r)
			return
		}
		rec := &headerRecorder{header: http.Header{}}
		h.ServeHTTP(rec, r)
		switch rec.status {
		case http.StatusNotFound:
			WriteError(w, r, NotFound("no route for "+r.Method+" "+r.URL.Path))
		case http.StatusMethodNotAllowed:
			w.Header().Set("Allow", rec.header.Get("Allow"))
			WriteError(w, r, &Error{Status: http.StatusMethodNotAllowed, Code: "method_not_allowed", Message: r.Method + " is not allowed here"})
		default:
			mux.ServeHTTP(w, r) // e.g. a path-cleaning redirect
		}
	})
}

// headerRecorder captures what the mux's fallback handlers would write.
type headerRecorder struct {
	header http.Header
	status int
}

func (h *headerRecorder) Header() http.Header { return h.header }
func (h *headerRecorder) WriteHeader(code int) {
	if h.status == 0 {
		h.status = code
	}
}
func (h *headerRecorder) Write(b []byte) (int, error) {
	if h.status == 0 {
		h.status = http.StatusOK
	}
	return len(b), nil
}
