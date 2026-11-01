package ekman

import (
	"fmt"
	"maps"
	"net/http"
	"net/mail"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"
)

// Validator is implemented by handler inputs that check themselves. ekman calls Validate after
// binding and before the handler; any recorded problem becomes a 422.
type Validator interface {
	Validate(v *Validation)
}

// Validation collects per-field problems. The first message recorded for a field wins.
type Validation struct {
	fields map[string]string
}

// Check records msg for field unless ok.
func (v *Validation) Check(ok bool, field, msg string) {
	if ok {
		return
	}
	if v.fields == nil {
		v.fields = map[string]string{}
	}
	if _, seen := v.fields[field]; !seen {
		v.fields[field] = msg
	}
}

// Required checks that value is not blank.
func (v *Validation) Required(field, value string) {
	v.Check(strings.TrimSpace(value) != "", field, "is required")
}

// MaxLen checks that value has at most n characters (runes).
func (v *Validation) MaxLen(field, value string, n int) {
	v.Check(utf8.RuneCountInString(value) <= n, field, fmt.Sprintf("must be at most %d characters", n))
}

// OneOf checks that value is one of allowed. An empty value passes; pair with Required.
func (v *Validation) OneOf(field, value string, allowed ...string) {
	v.Check(value == "" || slices.Contains(allowed, value), field, "must be one of: "+strings.Join(allowed, ", "))
}

// Email checks that value is a bare email address. An empty value passes; pair with Required.
func (v *Validation) Email(field, value string) {
	if value == "" {
		return
	}
	a, err := mail.ParseAddress(value)
	v.Check(err == nil && a.Address == value && a.Name == "", field, "must be a valid email address")
}

// URL checks that value is an absolute http or https URL. An empty value passes.
func (v *Validation) URL(field, value string) {
	if value == "" {
		return
	}
	u, err := url.Parse(value)
	v.Check(err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "", field, "must be an http(s) URL")
}

// Valid reports whether no problem was recorded.
func (v *Validation) Valid() bool { return len(v.fields) == 0 }

// Fields returns a copy of the recorded problems.
func (v *Validation) Fields() map[string]string { return maps.Clone(v.fields) }

// Err returns a 422 *Error carrying the fields, or nil when valid.
func (v *Validation) Err() error {
	if v.Valid() {
		return nil
	}
	return &Error{Status: http.StatusUnprocessableEntity, Code: "invalid_input", Message: "input is invalid", Fields: v.Fields()}
}
