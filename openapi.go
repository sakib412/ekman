package ekman

import (
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/sakib412/ekman/rbac"
)

// obj is a JSON object in the generated document; encoding/json sorts its keys, which keeps
// the output deterministic.
type obj = map[string]any

func (a *App) generateOpenAPI(routes []*route, reg *rbac.Registry) ([]byte, error) {
	g := &schemaGen{defs: obj{}, names: map[reflect.Type]string{}, taken: map[string]reflect.Type{}}
	paths := obj{}
	opIDs := map[string]int{}
	for _, rt := range routes {
		ri := rt.info
		if ri.hidden || ri.Method == "" {
			continue
		}
		op, err := g.operation(rt, a.opts.APIPrefix, opIDs)
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", ri.Method, ri.Pattern, err)
		}
		p := openAPIPath(ri.Pattern)
		item, _ := paths[p].(obj)
		if item == nil {
			item = obj{}
			paths[p] = item
		}
		item[strings.ToLower(ri.Method)] = op
	}

	if len(g.errs) > 0 {
		return nil, errors.Join(g.errs...)
	}

	perm := obj{"type": "string", "description": "A permission declared by a module."}
	if perms := reg.Permissions(); len(perms) > 0 {
		perm["enum"] = perms
	}
	g.defs["Permission"] = perm
	g.defs["Error"] = obj{
		"type":     "object",
		"required": []string{"error"},
		"properties": obj{"error": obj{
			"type":     "object",
			"required": []string{"code", "message"},
			"properties": obj{
				"code":      obj{"type": "string"},
				"message":   obj{"type": "string"},
				"fields":    obj{"type": "object", "additionalProperties": obj{"type": "string"}},
				"requestId": obj{"type": "string"},
			},
		}},
	}
	info := obj{"title": a.opts.Info.Title, "version": a.opts.Info.Version}
	if a.opts.Info.Description != "" {
		info["description"] = a.opts.Info.Description
	}
	doc := obj{
		"openapi": "3.1.0",
		"info":    info,
		"paths":   paths,
		"components": obj{
			"schemas": g.defs,
			"responses": obj{"Error": obj{
				"description": "Error",
				"content":     obj{"application/json": obj{"schema": ref("Error")}},
			}},
			"securitySchemes": obj{"session": obj{"type": "apiKey", "in": "cookie", "name": sessionCookie}},
		},
	}
	return json.MarshalIndent(doc, "", "  ")
}

func ref(name string) obj { return obj{"$ref": "#/components/schemas/" + name} }

func errRef() obj { return obj{"$ref": "#/components/responses/Error"} }

// openAPIPath converts a ServeMux pattern path to an OpenAPI path template.
func openAPIPath(p string) string {
	p = strings.TrimSuffix(p, "{$}")
	return strings.ReplaceAll(p, "...}", "}")
}

func (g *schemaGen) operation(rt *route, prefix string, opIDs map[string]int) (obj, error) {
	ri := rt.info
	op := obj{"tags": []string{ri.Module}, "x-ekman-access": ri.Access}
	id := operationID(ri.Method, strings.TrimPrefix(ri.Pattern, prefix))
	opIDs[id]++
	if n := opIDs[id]; n > 1 {
		id += strconv.Itoa(n)
	}
	op["operationId"] = id
	if ri.Summary != "" {
		op["summary"] = ri.Summary
	}
	responses := obj{"default": errRef()}
	switch rt.access.kind {
	case accessRequires:
		responses["403"] = errRef()
		fallthrough
	case accessSignedIn:
		responses["401"] = errRef()
		op["security"] = []obj{{"session": []string{}}}
	}

	var params []obj
	status := ri.Status
	if ri.In == nil { // Raw route: path wildcards are all we know
		for _, name := range wildcards(ri.Pattern) {
			params = append(params, obj{"name": name, "in": "path", "required": true, "schema": obj{"type": "string"}})
		}
		if status == 0 {
			status = http.StatusOK
		}
		responses[strconv.Itoa(status)] = obj{"description": http.StatusText(status)}
	} else {
		p, err := newPlan(ri.In)
		if err != nil {
			return nil, err
		}
		for _, prm := range p.params {
			params = append(params, obj{
				"name": prm.name, "in": prm.src.String(), "required": prm.src == inPath,
				"schema": g.paramSchema(prm.field.Type),
			})
		}
		if len(p.params) > 0 {
			responses["400"] = errRef()
		}
		if p.hasBody && ri.Method != http.MethodGet && ri.Method != http.MethodHead {
			op["requestBody"] = obj{
				"required": true,
				"content":  obj{"application/json": obj{"schema": g.bodySchema(ri.In, p)}},
			}
			responses["400"] = errRef()
		}
		if reflect.PointerTo(ri.In).Implements(reflect.TypeFor[Validator]()) || (p.enums != nil && op["requestBody"] != nil) {
			responses["422"] = errRef()
		}
		resp := obj{"description": http.StatusText(status)}
		if status != http.StatusNoContent && ri.Out != noContentType {
			resp["content"] = obj{"application/json": obj{"schema": g.schema(ri.Out)}}
		}
		responses[strconv.Itoa(status)] = resp
	}
	if len(params) > 0 {
		op["parameters"] = params
	}
	op["responses"] = responses
	return op, nil
}

// operationID derives a stable camelCase id: "GET /todos/{id}/done" → "getTodosByIdDone".
func operationID(method, p string) string {
	var b strings.Builder
	b.WriteString(strings.ToLower(method))
	for _, seg := range strings.Split(openAPIPath(p), "/") {
		if strings.HasPrefix(seg, "{") {
			b.WriteString("By")
			seg = strings.Trim(seg, "{}")
		}
		for _, word := range strings.FieldsFunc(seg, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
			b.WriteString(strings.ToUpper(word[:1]) + word[1:])
		}
	}
	return b.String()
}

type schemaGen struct {
	defs  obj
	names map[reflect.Type]string
	taken map[string]reflect.Type
	errs  []error // schema problems found while generating (e.g. an empty Enum)
}

var (
	timeType       = reflect.TypeFor[time.Time]()
	permissionType = reflect.TypeFor[rbac.Permission]()
	rawJSONType    = reflect.TypeFor[json.RawMessage]()
	jsonMarshaler  = reflect.TypeFor[json.Marshaler]()
	textMarshaler  = reflect.TypeFor[encoding.TextMarshaler]()
)

// schema returns the JSON Schema for t, registering named structs as components.
func (g *schemaGen) schema(t reflect.Type) obj {
	switch t {
	case timeType:
		return obj{"type": "string", "format": "date-time"}
	case permissionType:
		return ref("Permission")
	case rawJSONType:
		return obj{}
	}
	if values, ok := enumValues(t); ok {
		return ref(g.defineEnum(t, values))
	}
	if t.Kind() == reflect.Pointer {
		return nullable(g.schema(t.Elem()))
	}
	if t.Implements(jsonMarshaler) || reflect.PointerTo(t).Implements(jsonMarshaler) {
		return obj{}
	}
	if t.Implements(textMarshaler) || reflect.PointerTo(t).Implements(textMarshaler) {
		return obj{"type": "string"}
	}
	switch t.Kind() {
	case reflect.Bool:
		return obj{"type": "boolean"}
	case reflect.String:
		return obj{"type": "string"}
	case reflect.Int8, reflect.Int16, reflect.Int32:
		return obj{"type": "integer", "format": "int32"}
	case reflect.Int, reflect.Int64:
		return obj{"type": "integer", "format": "int64"}
	case reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint, reflect.Uint64:
		return obj{"type": "integer", "minimum": 0}
	case reflect.Float32:
		return obj{"type": "number", "format": "float"}
	case reflect.Float64:
		return obj{"type": "number", "format": "double"}
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return obj{"type": "string", "contentEncoding": "base64"}
		}
		return obj{"type": "array", "items": g.schema(t.Elem())}
	case reflect.Array:
		return obj{"type": "array", "items": g.schema(t.Elem()), "minItems": t.Len(), "maxItems": t.Len()}
	case reflect.Map:
		return obj{"type": "object", "additionalProperties": g.schema(t.Elem())}
	case reflect.Struct:
		if t.Name() == "" {
			return g.structSchema(t, nil)
		}
		return ref(g.define(t, "", nil))
	}
	return obj{} // interfaces and anything else: any JSON value
}

// define registers t (optionally a variant with suffix and skipped fields) and returns its name.
func (g *schemaGen) define(t reflect.Type, suffix string, skip map[string]bool) string {
	if suffix == "" {
		if name, ok := g.names[t]; ok {
			return name
		}
	}
	name := g.claim(t, suffix)
	if suffix == "" {
		g.names[t] = name // before recursing, so self-referential types terminate
	}
	g.defs[name] = g.structSchema(t, skip)
	return name
}

// claim reserves a components name for t: pkg.Type plus suffix, numbered on collisions.
func (g *schemaGen) claim(t reflect.Type, suffix string) string {
	base := typeName(t) + suffix
	name := base
	for i := 2; ; i++ {
		if prev, ok := g.taken[name]; !ok || (prev == t && suffix != "") {
			break
		}
		name = base + "_" + strconv.Itoa(i)
	}
	g.taken[name] = t
	return name
}

// defineEnum registers an Enum type as a string schema with its values.
func (g *schemaGen) defineEnum(t reflect.Type, values []string) string {
	if name, ok := g.names[t]; ok {
		return name
	}
	name := g.claim(t, "")
	g.names[t] = name
	if len(values) == 0 {
		g.errs = append(g.errs, fmt.Errorf("enum %s: EnumValues returned no values", t))
	}
	seen := map[string]bool{}
	for _, v := range values {
		if seen[v] {
			g.errs = append(g.errs, fmt.Errorf("enum %s: value %q listed twice", t, v))
		}
		seen[v] = true
	}
	g.defs[name] = obj{"type": "string", "enum": values}
	return name
}

func typeName(t reflect.Type) string {
	n := t.Name()
	if pkg := path.Base(t.PkgPath()); pkg != "." && pkg != "" {
		n = pkg + "." + n
	}
	return strings.Map(func(r rune) rune {
		if r == '.' || r == '-' || r == '_' || r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			return r
		}
		return '_'
	}, n)
}

func (g *schemaGen) structSchema(t reflect.Type, skip map[string]bool) obj {
	props := obj{}
	required := []string{}
	for _, f := range jsonFields(t, skip) {
		_, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
		var s obj
		if hasOpt(opts, "string") {
			s = obj{"type": "string"}
		} else {
			s = g.schema(f.Type)
		}
		props[f.Name] = s
		if !hasOpt(opts, "omitempty") && !hasOpt(opts, "omitzero") {
			required = append(required, f.Name)
		}
	}
	out := obj{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func hasOpt(opts, want string) bool {
	for _, o := range strings.Split(opts, ",") {
		if o == want {
			return true
		}
	}
	return false
}

// bodySchema is In's schema without the fields bound from path, query or header.
func (g *schemaGen) bodySchema(t reflect.Type, p *plan) obj {
	if t.Kind() != reflect.Struct || len(p.params) == 0 {
		return g.schema(t)
	}
	skip := map[string]bool{}
	for _, prm := range p.params {
		skip[prm.field.Name] = true
	}
	if t.Name() == "" {
		return g.structSchema(t, skip)
	}
	return ref(g.define(t, "Body", skip))
}

func (g *schemaGen) paramSchema(t reflect.Type) obj {
	t = derefType(t)
	if t.Kind() == reflect.Slice && !reflect.PointerTo(t).Implements(textUnmarshaler) {
		return obj{"type": "array", "items": g.paramSchema(t.Elem())}
	}
	if _, ok := enumValues(t); ok {
		return g.schema(t)
	}
	if t != timeType && reflect.PointerTo(t).Implements(textUnmarshaler) {
		return obj{"type": "string"}
	}
	return g.schema(t)
}

// nullable allows null in addition to s (OpenAPI 3.1 / JSON Schema style).
func nullable(s obj) obj {
	if typ, ok := s["type"].(string); ok {
		cp := obj{}
		for k, v := range s {
			cp[k] = v
		}
		cp["type"] = []string{typ, "null"}
		return cp
	}
	if len(s) == 0 {
		return s
	}
	return obj{"anyOf": []obj{s, {"type": "null"}}}
}
