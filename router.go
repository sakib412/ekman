package ekman

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
)

// Router registers a module's routes. Paths are relative to Options.APIPrefix.
type Router struct {
	b      *builder
	module string
	prefix string
	mw     []Middleware
}

type route struct {
	info   *RouteInfo
	access Access
	h      http.Handler // the endpoint itself: bind, validate, call, encode (or a Raw handler)
	mw     []Middleware // group middleware, then route Use middleware
}

// builder collects routes and registration errors while modules' Routes funcs run.
type builder struct {
	apiPrefix string
	routes    []*route
	errs      []error
}

func (b *builder) errorf(format string, args ...any) {
	b.errs = append(b.errs, fmt.Errorf(format, args...))
}

// Group returns a router whose routes share a path prefix and middleware. Group middleware
// runs after app middleware and before route middleware.
func (r *Router) Group(prefix string, mw ...Middleware) *Router {
	if !strings.HasPrefix(prefix, "/") || strings.HasSuffix(prefix, "/") || strings.ContainsAny(prefix, " {}") {
		r.b.errorf("module %q: group prefix %q must start with / and not end with / or contain wildcards", r.module, prefix)
	}
	return &Router{b: r.b, module: r.module, prefix: r.prefix + prefix, mw: append(slices.Clone(r.mw), mw...)}
}

// Raw mounts a plain http.Handler. pattern is a ServeMux pattern relative to APIPrefix; the
// method may be omitted. The access check still applies; read the caller with CallerFrom and
// write errors with WriteError.
func (r *Router) Raw(pattern string, access Access, h http.Handler, opts ...RouteOption) {
	method, path, err := splitPattern(pattern, false)
	if err != nil {
		r.b.errorf("module %q: %w", r.module, err)
		return
	}
	r.add(method, path, access, h, &RouteInfo{}, opts)
}

func (r *Router) add(method, path string, access Access, h http.Handler, info *RouteInfo, opts []RouteOption) {
	info.Method = method
	info.Pattern = joinPath(r.b.apiPrefix+r.prefix, path)
	info.Module = r.module
	info.Access = access.String()
	for _, o := range opts {
		o(info)
	}
	if access.kind == accessInvalid {
		r.b.errorf("module %q: %s %s: access is required (ekman.Public, ekman.SignedIn or ekman.Requires)", r.module, method, info.Pattern)
	}
	r.b.routes = append(r.b.routes, &route{
		info:   info,
		access: access,
		h:      h,
		mw:     append(slices.Clone(r.mw), info.mw...),
	})
}

func knownMethod(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return true
	}
	return false
}

// splitPattern parses "GET /path" into method and path.
func splitPattern(pattern string, needMethod bool) (method, path string, err error) {
	method, path, found := strings.Cut(strings.TrimSpace(pattern), " ")
	if !found {
		method, path = "", method
	}
	path = strings.TrimSpace(path)
	if method != "" && !knownMethod(method) {
		return "", "", fmt.Errorf("pattern %q: unknown method %q", pattern, method)
	}
	if needMethod && method == "" {
		return "", "", fmt.Errorf("pattern %q: method is required, e.g. \"GET /things\"", pattern)
	}
	if !strings.HasPrefix(path, "/") {
		return "", "", fmt.Errorf("pattern %q: path must start with /", pattern)
	}
	return method, path, nil
}

// joinPath joins base and path. A trailing slash would make a ServeMux subtree pattern, which
// an API almost never wants, so it is pinned with {$}.
func joinPath(base, path string) string {
	if path == "/" && base != "" {
		return base
	}
	p := base + path
	if strings.HasSuffix(p, "/") {
		p += "{$}"
	}
	return p
}

// wildcards returns the names of {name} and {name...} segments in a path.
func wildcards(path string) []string {
	var out []string
	for {
		i := strings.IndexByte(path, '{')
		if i < 0 {
			return out
		}
		j := strings.IndexByte(path[i:], '}')
		if j < 0 {
			return out
		}
		name := strings.TrimSuffix(path[i+1:i+j], "...")
		if name != "$" {
			out = append(out, name)
		}
		path = path[i+j+1:]
	}
}
