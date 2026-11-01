package ekman

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type Tier string

func (Tier) EnumValues() []string { return []string{"gold", "silver"} }

type Kind string

func (*Kind) EnumValues() []string { return []string{"a", "b"} } // pointer receiver works too

type emptyEnum string

func (emptyEnum) EnumValues() []string { return nil }

type member struct {
	Tier   Tier            `json:"tier"`
	Kinds  []Kind          `json:"kinds,omitempty"`
	Best   *Tier           `json:"best"`
	Sub    *member         `json:"sub,omitempty"`
	ByName map[string]Tier `json:"byName,omitempty"`
}

type enumIn struct {
	enumQuery
	Note string `json:"note"`
	member
}

type enumQuery struct {
	Filter Tier   `query:"tier"`
	Many   []Tier `query:"many"`
	Opt    *Kind  `header:"x-kind"`
}

func (in *enumIn) Validate(v *Validation) { v.Required("note", in.Note) }

func enumModule() Module {
	return Module{Name: "enum", Routes: func(r *Router) {
		Handle(r, "POST /members", Public(), func(_ context.Context, _ Caller, in *enumIn) (member, error) {
			return in.member, nil
		})
		Handle(r, "GET /members", Public(), func(_ context.Context, _ Caller, in *enumQuery) ([]Tier, error) {
			return in.Many, nil
		})
	}}
}

func TestEnumOpenAPI(t *testing.T) {
	a, _ := newApp(t, Options{}, enumModule())
	raw, err := a.OpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths      map[string]map[string]json.RawMessage
		Components struct{ Schemas map[string]json.RawMessage }
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	schemas := doc.Components.Schemas
	if got := string(schemas["ekman.Tier"]); compact(got) != `{"enum":["gold","silver"],"type":"string"}` {
		t.Fatalf("Tier = %s", got)
	}
	if got := string(schemas["ekman.Kind"]); compact(got) != `{"enum":["a","b"],"type":"string"}` {
		t.Fatalf("Kind = %s", got)
	}
	body := compact(string(schemas["ekman.enumInBody"]))
	for _, want := range []string{
		`"tier":{"$ref":"#/components/schemas/ekman.Tier"}`,
		`"kinds":{"items":{"$ref":"#/components/schemas/ekman.Kind"},"type":"array"}`,
		`"best":{"anyOf":[{"$ref":"#/components/schemas/ekman.Tier"},{"type":"null"}]}`,
		`"byName":{"additionalProperties":{"$ref":"#/components/schemas/ekman.Tier"},"type":"object"}`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body schema missing %s:\n%s", want, body)
		}
	}
	post := compact(string(doc.Paths["/api/v1/members"]["post"]))
	for _, want := range []string{
		`{"in":"query","name":"tier","required":false,"schema":{"$ref":"#/components/schemas/ekman.Tier"}}`,
		`{"in":"query","name":"many","required":false,"schema":{"items":{"$ref":"#/components/schemas/ekman.Tier"},"type":"array"}}`,
		`{"in":"header","name":"X-Kind","required":false,"schema":{"$ref":"#/components/schemas/ekman.Kind"}}`,
		`"422":{"$ref":"#/components/responses/Error"}`,
	} {
		if !strings.Contains(post, want) {
			t.Errorf("operation missing %s:\n%s", want, post)
		}
	}
	get := compact(string(doc.Paths["/api/v1/members"]["get"]))
	if !strings.Contains(get, `"schema":{"items":{"$ref":"#/components/schemas/ekman.Tier"},"type":"array"}`) {
		t.Errorf("response schema: %s", get)
	}
	if strings.Contains(get, `"422"`) {
		t.Errorf("GET without a body or Validate has no 422: %s", get)
	}
}

func TestEnumParams(t *testing.T) {
	_, h := newApp(t, Options{}, enumModule())
	rec := do(t, h, call{method: "GET", path: "/api/v1/members?tier=gold&many=silver&many=gold", header: http.Header{"X-Kind": {"b"}}})
	expectStatus(t, rec, 200)
	if compact(rec.Body.String()) != `["silver","gold"]` {
		t.Fatal(rec.Body.String())
	}
	rec = do(t, h, call{method: "GET", path: "/api/v1/members?tier=&many="})
	expectStatus(t, rec, 200) // empty passes, like Validation.OneOf

	rec = do(t, h, call{method: "GET", path: "/api/v1/members?tier=bronze&many=gold&many=x", header: http.Header{"X-Kind": {"c"}}})
	expectStatus(t, rec, 400)
	e := decodeErr(t, rec)
	if e.Code != "invalid_param" || e.Fields["tier"] != "must be one of: gold, silver" || e.Fields["many"] == "" || e.Fields["X-Kind"] != "must be one of: a, b" {
		t.Fatalf("%+v", e)
	}
}

func TestEnumBody(t *testing.T) {
	_, h := newApp(t, Options{}, enumModule())
	rec := do(t, h, call{method: "POST", path: "/api/v1/members",
		body: `{"note":"n","tier":"gold","best":"silver","kinds":["a","b"],"sub":{"tier":"silver"},"byName":{"x":"gold"}}`})
	expectStatus(t, rec, 201)

	rec = do(t, h, call{method: "POST", path: "/api/v1/members",
		body: `{"tier":"bronze","best":"x","kinds":["a","z"],"sub":{"tier":"y","sub":{"tier":"deep"}},"byName":{"k":"q"}}`})
	expectStatus(t, rec, 422)
	e := decodeErr(t, rec)
	want := map[string]string{
		"tier":         "must be one of: gold, silver",
		"best":         "must be one of: gold, silver",
		"kinds[1]":     "must be one of: a, b",
		"sub.tier":     "must be one of: gold, silver",
		"sub.sub.tier": "must be one of: gold, silver",
		"byName.k":     "must be one of: gold, silver",
		"note":         "is required", // Validate still runs and merges
	}
	if e.Code != "invalid_input" || len(e.Fields) != len(want) {
		t.Fatalf("%+v", e)
	}
	for k, v := range want {
		if e.Fields[k] != v {
			t.Errorf("%s = %q, want %q", k, e.Fields[k], v)
		}
	}
}

func TestEnumWithoutValuesFailsAtBoot(t *testing.T) {
	a, err := New(Options{Logger: quietLogger()})
	if err != nil {
		t.Fatal(err)
	}
	a.Register(Module{Name: "bad", Routes: func(r *Router) {
		Handle(r, "GET /x", Public(), func(context.Context, Caller, *struct{}) (emptyEnum, error) { return "", nil })
	}})
	if _, err := a.Handler(); err == nil || !strings.Contains(err.Error(), "EnumValues returned no values") {
		t.Fatalf("err = %v", err)
	}
}
