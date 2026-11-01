package ekman

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
)

// NoContent is the Out type of handlers that answer 204 with no body.
type NoContent struct{}

var noContentType = reflect.TypeFor[NoContent]()

// Handle registers a typed JSON endpoint. pattern is a ServeMux pattern relative to APIPrefix,
// with a method ("GET /events/{slug}"). In binds from `path:"x"`, `query:"x"` and `header:"X"`
// tags; its other exported fields decode from the JSON body (except on GET and HEAD). If *In
// implements Validator it runs before h. Out encodes as JSON with status 200, 201 for POST,
// 204 for NoContent, or the Status option.
func Handle[In, Out any](r *Router, pattern string, access Access, h func(ctx context.Context, c Caller, in *In) (Out, error), opts ...RouteOption) {
	method, path, err := splitPattern(pattern, true)
	if err != nil {
		r.b.errorf("module %q: %w", r.module, err)
		return
	}
	inType, outType := reflect.TypeFor[In](), reflect.TypeFor[Out]()
	p, err := newPlan(inType)
	if err != nil {
		r.b.errorf("module %q: %s: input %s: %w", r.module, pattern, inType, err)
		return
	}
	names := wildcards(path)
	for _, prm := range p.params {
		if prm.src == inPath && !slices.Contains(names, prm.name) {
			r.b.errorf("module %q: %s: field %s binds path value %q, which the pattern does not have", r.module, pattern, prm.field.Name, prm.name)
		}
	}

	info := &RouteInfo{In: inType, Out: outType}
	status := 0 // resolved after options apply
	endpoint := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		in := new(In)
		if err := p.bind(req, in); err != nil {
			WriteError(w, req, err)
			return
		}
		var val Validation
		p.checkEnums(in, &val)
		if v, ok := any(in).(Validator); ok {
			v.Validate(&val)
		}
		if err := val.Err(); err != nil {
			WriteError(w, req, err)
			return
		}
		ctx := req.Context()
		out, err := h(ctx, CallerFrom(ctx), in)
		if err != nil {
			WriteError(w, req, err)
			return
		}
		if status == http.StatusNoContent {
			w.WriteHeader(status)
			return
		}
		body, err := marshalOut(out)
		if err != nil {
			WriteError(w, req, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	})
	r.add(method, path, access, endpoint, info, opts)
	status = successStatus(info)
	info.Status = status
}

func successStatus(info *RouteInfo) int {
	switch {
	case info.Status != 0:
		return info.Status
	case info.Out == noContentType:
		return http.StatusNoContent
	case info.Method == http.MethodPost:
		return http.StatusCreated
	}
	return http.StatusOK
}

// marshalOut encodes a handler result; a nil slice becomes [] so clients get an array.
func marshalOut(out any) ([]byte, error) {
	if v := reflect.ValueOf(out); v.Kind() == reflect.Slice && v.IsNil() {
		return []byte("[]\n"), nil
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
