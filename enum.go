package ekman

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// Enum is implemented by named string types that take a fixed set of values:
//
//	type Tier string
//
//	func (Tier) EnumValues() []string { return []string{"gold", "silver", "bronze"} }
//
// The OpenAPI document defines each Enum type once, as a components schema named pkg.Type
// with an `enum` list, so generated clients get a union type. Handle rejects other values on
// input: a path, query or header parameter answers 400 invalid_param, a JSON body field
// 422 invalid_input (merged with the input's own Validate). The empty string passes, like
// Validation.OneOf; pair it with Validation.Required. EnumValues is called once, at boot.
type Enum interface {
	EnumValues() []string
}

var enumType = reflect.TypeFor[Enum]()

// enumValues returns t's values when t is a string type implementing Enum.
func enumValues(t reflect.Type) ([]string, bool) {
	if t.Kind() != reflect.String {
		return nil, false
	}
	switch {
	case t.Implements(enumType):
		return reflect.Zero(t).Interface().(Enum).EnumValues(), true
	case reflect.PointerTo(t).Implements(enumType):
		return reflect.New(t).Interface().(Enum).EnumValues(), true
	}
	return nil, false
}

func enumMessage(values []string) string { return "must be one of: " + strings.Join(values, ", ") }

// enumCheck records Enum values outside their set in val; path is v's JSON path.
type enumCheck func(v reflect.Value, path string, val *Validation)

// enumCompiler builds the enumCheck for a body type once per route.
type enumCompiler struct {
	memo map[reflect.Type]*enumCheck
	busy map[reflect.Type]bool
}

func newEnumCompiler() *enumCompiler {
	return &enumCompiler{memo: map[reflect.Type]*enumCheck{}, busy: map[reflect.Type]bool{}}
}

// reaches reports whether any Enum is reachable from t through JSON-decoded values.
func reaches(t reflect.Type, seen map[reflect.Type]bool) bool {
	if seen[t] {
		return false
	}
	seen[t] = true
	if _, ok := enumValues(t); ok {
		return true
	}
	if opaqueJSON(t) {
		return false
	}
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
		return reaches(t.Elem(), seen)
	case reflect.Struct:
		for _, f := range jsonFields(t, nil) {
			if reaches(f.Type, seen) {
				return true
			}
		}
	}
	return false
}

// opaqueJSON reports types whose JSON form ekman does not look into.
func opaqueJSON(t reflect.Type) bool {
	return t == timeType || t == rawJSONType || reflect.PointerTo(t).Implements(reflect.TypeFor[json.Unmarshaler]())
}

// compile returns the check for t, or nil when no Enum is reachable from it.
func (c *enumCompiler) compile(t reflect.Type) enumCheck {
	if slot, ok := c.memo[t]; ok {
		if c.busy[t] { // a recursive type: resolve the slot when the check runs
			return func(v reflect.Value, path string, val *Validation) { (*slot)(v, path, val) }
		}
		return *slot
	}
	if !reaches(t, map[reflect.Type]bool{}) {
		c.memo[t] = new(enumCheck)
		return nil
	}
	slot := new(enumCheck)
	c.memo[t], c.busy[t] = slot, true
	*slot = c.build(t, nil)
	delete(c.busy, t)
	return *slot
}

// build compiles t's check; skip names struct fields bound from elsewhere (top-level In only).
func (c *enumCompiler) build(t reflect.Type, skip map[string]bool) enumCheck {
	if values, ok := enumValues(t); ok {
		return func(v reflect.Value, path string, val *Validation) {
			if s := v.String(); s != "" && !slices.Contains(values, s) {
				val.Check(false, path, enumMessage(values))
			}
		}
	}
	switch t.Kind() {
	case reflect.Pointer:
		elem := c.compile(t.Elem())
		return func(v reflect.Value, path string, val *Validation) {
			if !v.IsNil() {
				elem(v.Elem(), path, val)
			}
		}
	case reflect.Slice, reflect.Array:
		elem := c.compile(t.Elem())
		return func(v reflect.Value, path string, val *Validation) {
			for i := range v.Len() {
				elem(v.Index(i), fmt.Sprintf("%s[%d]", path, i), val)
			}
		}
	case reflect.Map:
		elem := c.compile(t.Elem())
		return func(v reflect.Value, path string, val *Validation) {
			for it := v.MapRange(); it.Next(); {
				elem(it.Value(), joinField(path, fmt.Sprint(it.Key().Interface())), val)
			}
		}
	case reflect.Struct:
		type field struct {
			index []int
			name  string
			check enumCheck
		}
		var fields []field
		for _, f := range jsonFields(t, skip) {
			if check := c.compile(f.Type); check != nil {
				fields = append(fields, field{f.Index, f.Name, check})
			}
		}
		return func(v reflect.Value, path string, val *Validation) {
			for _, f := range fields {
				if fv, err := v.FieldByIndexErr(f.index); err == nil {
					f.check(fv, joinField(path, f.name), val)
				}
			}
		}
	}
	return nil
}

func joinField(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

// jsonFields lists t's fields as encoding/json sees them, with Name set to the JSON key.
// Promoted fields of embedded structs are included; fields named in skip are not.
func jsonFields(t reflect.Type, skip map[string]bool) []reflect.StructField {
	var out []reflect.StructField
	for _, f := range reflect.VisibleFields(t) {
		if skip[f.Name] || !f.IsExported() {
			continue
		}
		name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" && opts == "" {
			continue
		}
		if f.Anonymous && name == "" && derefType(f.Type).Kind() == reflect.Struct {
			continue // its fields are listed by VisibleFields
		}
		if name == "" {
			name = f.Name
		}
		f.Name = name
		out = append(out, f)
	}
	return out
}
