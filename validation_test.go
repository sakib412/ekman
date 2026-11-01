package ekman

import (
	"errors"
	"testing"
)

func TestValidation(t *testing.T) {
	var v Validation
	if !v.Valid() || v.Err() != nil {
		t.Fatal("empty validation must be valid")
	}
	v.Required("a", "  ")
	v.Required("a", "") // first message wins
	v.MaxLen("b", "héllo", 5)
	v.MaxLen("c", "toolong", 3)
	v.OneOf("d", "x", "y", "z")
	v.OneOf("d2", "", "y")
	v.Email("e", "Ada <ada@example.com>")
	v.Email("e2", "ada@example.com")
	v.URL("f", "ftp://x")
	v.URL("f2", "https://example.com/p")
	v.Check(false, "g", "custom")
	want := map[string]bool{"a": true, "c": true, "d": true, "e": true, "f": true, "g": true}
	got := v.Fields()
	if len(got) != len(want) {
		t.Fatalf("fields %v", got)
	}
	for k := range want {
		if got[k] == "" {
			t.Errorf("missing %s", k)
		}
	}
	if got["a"] != "is required" || got["g"] != "custom" {
		t.Fatal(got)
	}
	var e *Error
	if !errors.As(v.Err(), &e) || e.Status != 422 || e.Code != "invalid_input" || len(e.Fields) != 6 {
		t.Fatal(v.Err())
	}
}

func TestErrorHelpers(t *testing.T) {
	cases := []struct {
		err    *Error
		status int
		code   string
	}{
		{NotFound("x"), 404, "not_found"},
		{BadRequest("bad", "x"), 400, "bad"},
		{Unauthorized("x"), 401, "unauthorized"},
		{Forbidden("x"), 403, "forbidden"},
		{Conflict("dup", "x"), 409, "dup"},
		{Unprocessable("u", "x"), 422, "u"},
	}
	for _, c := range cases {
		if c.err.Status != c.status || c.err.Code != c.code || c.err.Error() != c.code+": x" {
			t.Errorf("%+v", c.err)
		}
	}
	if e, ok := toError(errors.New("x")); ok || e.Status != 500 {
		t.Fatal("plain error must be a 500")
	}
	if e, ok := toError(&Error{Code: "c"}); !ok || e.Status != 500 {
		t.Fatal("zero status defaults to 500")
	}
}
