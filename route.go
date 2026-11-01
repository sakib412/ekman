package ekman

import (
	"context"
	"reflect"
	"slices"
)

// RouteInfo describes a registered route. Middleware reads it with RouteFrom; the OpenAPI
// document is generated from it.
type RouteInfo struct {
	Method  string // "" for a Raw route registered without a method
	Pattern string // full path including APIPrefix, e.g. "/api/v1/todos/{id}"
	Module  string
	Summary string
	Access  string // "public" | "signed_in" | permission
	Tags    []string
	Status  int          // success status; 0 means the default (200, 201 for POST, 204 for NoContent)
	In, Out reflect.Type // nil for Raw routes

	mw     []Middleware
	hidden bool // left out of the OpenAPI document
}

// RouteOption adjusts a route at registration.
type RouteOption func(*RouteInfo)

// Summary sets the one-line OpenAPI summary.
func Summary(s string) RouteOption { return func(ri *RouteInfo) { ri.Summary = s } }

// Status overrides the success status code.
func Status(code int) RouteOption { return func(ri *RouteInfo) { ri.Status = code } }

// Tag attaches free-form metadata that app middleware can read through RouteFrom.
func Tag(t string) RouteOption { return func(ri *RouteInfo) { ri.Tags = append(ri.Tags, t) } }

// Use adds per-route middleware, run after app middleware and before the access check.
func Use(mw ...Middleware) RouteOption {
	return func(ri *RouteInfo) { ri.mw = append(ri.mw, mw...) }
}

func hidden() RouteOption { return func(ri *RouteInfo) { ri.hidden = true } }

// HasTag reports whether the route carries tag t.
func (ri RouteInfo) HasTag(t string) bool { return slices.Contains(ri.Tags, t) }

type routeKey struct{}

// RouteFrom returns the matched route for a request context.
func RouteFrom(ctx context.Context) (RouteInfo, bool) {
	ri, ok := ctx.Value(routeKey{}).(*RouteInfo)
	if !ok {
		return RouteInfo{}, false
	}
	cp := *ri
	cp.Tags = slices.Clone(ri.Tags)
	cp.mw = nil
	return cp, true
}
