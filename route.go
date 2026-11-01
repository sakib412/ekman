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

	Accepts  []Content // request bodies the handler reads itself (Accepts option)
	Produces []Content // success bodies a Raw handler writes (Produces option)

	mw           []Middleware
	hidden       bool // left out of the OpenAPI document
	takesRequest bool // a Handle route whose In has a *http.Request field
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

// Content documents a body the handler reads or writes itself: a media type and the Go type
// whose schema describes it.
type Content struct {
	MediaType string       // e.g. "text/csv", "multipart/form-data"
	Type      reflect.Type // string: {"type":"string"}; File: binary; structs: an object schema
}

// File stands for binary data in Accepts and Produces: a multipart file field or a whole
// download. Its schema is {"type":"string","format":"binary"}. It is documentation only and
// has nothing to encode or decode.
type File struct{}

var fileType = reflect.TypeFor[File]()

// Accepts documents the request body of a route whose handler reads it: a Raw route, or a
// Handle route whose In has a *http.Request field. T describes the body; for
// multipart/form-data use a struct of the form fields (keyed by their json names) with File
// for files. Repeat the option for each accepted media type.
func Accepts[T any](mediaType string) RouteOption {
	return func(ri *RouteInfo) { ri.Accepts = append(ri.Accepts, Content{mediaType, reflect.TypeFor[T]()}) }
}

// Produces documents the success response body a Raw handler writes, at the Status option's
// code (default 200): e.g. Produces[string]("text/csv") or Produces[File]("application/pdf").
// Repeat the option for each media type. Handle routes always produce JSON from Out.
func Produces[T any](mediaType string) RouteOption {
	return func(ri *RouteInfo) { ri.Produces = append(ri.Produces, Content{mediaType, reflect.TypeFor[T]()}) }
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
	return ri.clone(), true
}

// clone copies ri so callers cannot change the registered route.
func (ri *RouteInfo) clone() RouteInfo {
	cp := *ri
	cp.Tags = slices.Clone(ri.Tags)
	cp.Accepts = slices.Clone(ri.Accepts)
	cp.Produces = slices.Clone(ri.Produces)
	cp.mw = nil
	return cp
}
