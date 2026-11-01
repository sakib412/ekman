package ekman

import (
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// maxBodyBytes caps JSON request bodies. Uploads take the *http.Request (an In field or a Raw
// route) and go through storage.Receive, which sets its own limit.
const maxBodyBytes = 1 << 20

type paramSource uint8

const (
	inPath paramSource = iota + 1
	inQuery
	inHeader
)

func (s paramSource) String() string {
	switch s {
	case inPath:
		return "path"
	case inQuery:
		return "query"
	}
	return "header"
}

type param struct {
	src   paramSource
	name  string
	index []int
	field reflect.StructField
	enum  []string // allowed values when the field's base type is an Enum
}

// plan is the reflection done once per route: which fields bind from where.
type plan struct {
	params  []param
	hasBody bool
	enums   enumCheck // checks Enum values in the decoded body; nil when there are none
	request []int     // index of the *http.Request field; nil when In has none
}

var requestType = reflect.TypeFor[*http.Request]()

func newPlan(t reflect.Type) (*plan, error) {
	p := &plan{}
	if t.Kind() != reflect.Struct {
		p.hasBody = true
		p.enums = newEnumCompiler().compile(t)
		return p, nil
	}
	var errs []error
	for _, f := range reflect.VisibleFields(t) {
		if !f.IsExported() || (f.Anonymous && derefType(f.Type).Kind() == reflect.Struct) {
			continue
		}
		src, name := paramTag(f)
		if f.Type == requestType {
			switch {
			case src != 0:
				errs = append(errs, fmt.Errorf("field %s: a *http.Request field cannot have a %s tag", f.Name, src))
			case p.request != nil:
				errs = append(errs, fmt.Errorf("field %s: only one *http.Request field is allowed", f.Name))
			default:
				p.request = f.Index
			}
			continue
		}
		if src == 0 {
			if tag, _, _ := strings.Cut(f.Tag.Get("json"), ","); tag != "-" {
				p.hasBody = true
			}
			continue
		}
		if name == "" {
			errs = append(errs, fmt.Errorf("field %s: empty %s tag", f.Name, src))
			continue
		}
		if !bindable(f.Type, src == inQuery) {
			errs = append(errs, fmt.Errorf("field %s: type %s cannot bind from %s", f.Name, f.Type, src))
			continue
		}
		if src == inHeader {
			name = http.CanonicalHeaderKey(name)
		}
		enum, _ := enumValues(paramBase(f.Type))
		p.params = append(p.params, param{src: src, name: name, index: f.Index, field: f, enum: enum})
	}
	if p.request != nil && p.hasBody {
		errs = append(errs, errors.New("a *http.Request field hands the body to the handler, so no other field may decode from it: "+
			"bind the rest from path, query or header, or tag them `json:\"-\"`"))
	}
	if p.hasBody {
		skip := map[string]bool{}
		for _, prm := range p.params {
			skip[prm.field.Name] = true
		}
		if reaches(t, map[reflect.Type]bool{}) {
			p.enums = newEnumCompiler().build(t, skip)
		}
	}
	return p, errors.Join(errs...)
}

// paramBase is the scalar type a parameter field parses into: T for T, *T, []T and []*T.
func paramBase(t reflect.Type) reflect.Type {
	t = derefType(t)
	if t.Kind() == reflect.Slice && !reflect.PointerTo(t).Implements(textUnmarshaler) {
		t = derefType(t.Elem())
	}
	return t
}

// enumOK reports whether every string in v (a bound parameter field) is allowed.
func enumOK(v reflect.Value, allowed []string) bool {
	switch v.Kind() {
	case reflect.Pointer:
		return v.IsNil() || enumOK(v.Elem(), allowed)
	case reflect.Slice:
		for i := range v.Len() {
			if !enumOK(v.Index(i), allowed) {
				return false
			}
		}
		return true
	}
	s := v.String()
	return s == "" || slices.Contains(allowed, s)
}

// checkEnums records body fields holding values outside their Enum in val.
func (p *plan) checkEnums(in any, val *Validation) {
	if p.enums != nil {
		p.enums(reflect.ValueOf(in).Elem(), "", val)
	}
}

func paramTag(f reflect.StructField) (paramSource, string) {
	if v, ok := f.Tag.Lookup("path"); ok {
		return inPath, v
	}
	if v, ok := f.Tag.Lookup("query"); ok {
		return inQuery, v
	}
	if v, ok := f.Tag.Lookup("header"); ok {
		return inHeader, v
	}
	return 0, ""
}

func derefType(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

var textUnmarshaler = reflect.TypeFor[encoding.TextUnmarshaler]()

func bindable(t reflect.Type, allowSlice bool) bool {
	if reflect.PointerTo(t).Implements(textUnmarshaler) {
		return true
	}
	switch t.Kind() {
	case reflect.Pointer:
		return t.Elem().Kind() != reflect.Pointer && bindable(t.Elem(), false)
	case reflect.Slice:
		return allowSlice && t.Elem().Kind() != reflect.Uint8 && bindable(t.Elem(), false)
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	}
	return false
}

func setText(v reflect.Value, s string) error {
	if v.Kind() != reflect.Pointer && v.CanAddr() {
		if u, ok := v.Addr().Interface().(encoding.TextUnmarshaler); ok {
			return u.UnmarshalText([]byte(s))
		}
	}
	switch v.Kind() {
	case reflect.Pointer:
		nv := reflect.New(v.Type().Elem())
		if err := setText(nv.Elem(), s); err != nil {
			return err
		}
		v.Set(nv)
	case reflect.String:
		v.SetString(s)
	case reflect.Bool:
		b, err := strconv.ParseBool(s)
		if err != nil {
			return errors.New("must be true or false")
		}
		v.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(s, 10, v.Type().Bits())
		if err != nil {
			return errors.New("must be an integer")
		}
		v.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(s, 10, v.Type().Bits())
		if err != nil {
			return errors.New("must be a non-negative integer")
		}
		v.SetUint(n)
	case reflect.Float32, reflect.Float64:
		n, err := strconv.ParseFloat(s, v.Type().Bits())
		if err != nil {
			return errors.New("must be a number")
		}
		v.SetFloat(n)
	default:
		return fmt.Errorf("unsupported type %s", v.Type())
	}
	return nil
}

// fieldAlloc is FieldByIndex that allocates nil embedded pointers on the way.
func fieldAlloc(v reflect.Value, index []int) reflect.Value {
	for i, x := range index {
		if i > 0 && v.Kind() == reflect.Pointer {
			if v.IsNil() {
				v.Set(reflect.New(v.Type().Elem()))
			}
			v = v.Elem()
		}
		v = v.Field(x)
	}
	return v
}

// bind fills in (a pointer to the input) from the request.
func (p *plan) bind(r *http.Request, in any) error {
	v := reflect.ValueOf(in).Elem()
	if p.request != nil {
		fieldAlloc(v, p.request).Set(reflect.ValueOf(r))
	}
	if p.hasBody && r.Method != http.MethodGet && r.Method != http.MethodHead {
		if err := decodeBody(r, in); err != nil {
			return err
		}
	}
	if len(p.params) == 0 {
		return nil
	}
	var fields map[string]string
	fail := func(name, msg string) {
		if fields == nil {
			fields = map[string]string{}
		}
		fields[name] = msg
	}
	query := r.URL.Query()
	for _, prm := range p.params {
		f := fieldAlloc(v, prm.index)
		// The body decoder matches JSON keys to field names case-insensitively; parameters
		// must only ever come from their declared source.
		f.Set(reflect.Zero(f.Type()))
		var vals []string
		switch prm.src {
		case inPath:
			vals = []string{r.PathValue(prm.name)}
		case inQuery:
			vals = query[prm.name]
		case inHeader:
			vals = r.Header.Values(prm.name)
		}
		if len(vals) == 0 || (len(vals) == 1 && vals[0] == "" && f.Kind() != reflect.String) {
			continue
		}
		if f.Kind() == reflect.Slice && !reflect.PointerTo(f.Type()).Implements(textUnmarshaler) {
			s := reflect.MakeSlice(f.Type(), len(vals), len(vals))
			ok := true
			for i, raw := range vals {
				if err := setText(s.Index(i), raw); err != nil {
					fail(prm.name, err.Error())
					ok = false
				}
			}
			f.Set(s)
			if ok && prm.enum != nil && !enumOK(f, prm.enum) {
				fail(prm.name, enumMessage(prm.enum))
			}
			continue
		}
		if err := setText(f, vals[0]); err != nil {
			fail(prm.name, err.Error())
		} else if prm.enum != nil && !enumOK(f, prm.enum) {
			fail(prm.name, enumMessage(prm.enum))
		}
	}
	if fields != nil {
		return &Error{Status: http.StatusBadRequest, Code: "invalid_param", Message: "invalid request parameters", Fields: fields}
	}
	return nil
}

func decodeBody(r *http.Request, in any) error {
	if ct := r.Header.Get("Content-Type"); ct != "" {
		mt, _, err := mime.ParseMediaType(ct)
		if err != nil || (mt != "application/json" && !strings.HasSuffix(mt, "+json")) {
			return &Error{Status: http.StatusUnsupportedMediaType, Code: "unsupported_media_type", Message: "request body must be application/json"}
		}
	}
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBodyBytes))
	if err := dec.Decode(in); err != nil {
		if errors.Is(err, io.EOF) {
			return nil // empty body: validation decides whether that is acceptable
		}
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return err
		}
		return BadRequest("invalid_json", jsonErrorMessage(err))
	}
	if dec.More() {
		return BadRequest("invalid_json", "request body must be a single JSON value")
	}
	return nil
}

func jsonErrorMessage(err error) string {
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) && typeErr.Field != "" {
		return fmt.Sprintf("field %q must be %s", typeErr.Field, typeErr.Type)
	}
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return fmt.Sprintf("malformed JSON at offset %d", syntaxErr.Offset)
	}
	return "malformed JSON body"
}
