package ekman

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

type importForm struct {
	File  File   `json:"file"`
	Note  string `json:"note,omitempty"`
	Tier  Tier   `json:"tier"`
	Extra []File `json:"extra,omitempty"`
}

type exportRow struct {
	Name string `json:"name"`
}

func contentModule() Module {
	noop := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	return Module{Name: "content", Routes: func(r *Router) {
		r.Raw("GET /export.csv", Public(), noop, Summary("Export"),
			Produces[string]("text/csv"), Produces[[]exportRow]("application/json"))
		r.Raw("GET /report/{id}", Public(), noop, Produces[File]("application/pdf"))
		r.Raw("POST /imports", Public(), noop, Accepts[importForm]("multipart/form-data"), Status(http.StatusAccepted))
		Handle(r, "PUT /things/{id}/file", Public(), func(_ context.Context, _ Caller, in *uploadIn) (uploadOut, error) {
			return uploadOut{ID: in.ID}, nil
		}, Accepts[importForm]("multipart/form-data"), Accepts[File]("application/octet-stream"))
	}}
}

func TestContentOpenAPI(t *testing.T) {
	a, _ := newApp(t, Options{}, contentModule())
	raw, err := a.OpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths      map[string]map[string]map[string]json.RawMessage
		Components struct{ Schemas map[string]json.RawMessage }
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	op := func(path, method, key string) string {
		t.Helper()
		return compact(string(doc.Paths["/api/v1"+path][method][key]))
	}

	if got, want := op("/export.csv", "get", "responses"),
		`{"200":{"content":{"application/json":{"schema":{"items":{"$ref":"#/components/schemas/ekman.exportRow"},"type":"array"}},`+
			`"text/csv":{"schema":{"type":"string"}}},"description":"OK"},"default":{"$ref":"#/components/responses/Error"}}`; got != want {
		t.Errorf("export responses\n%s\nwant\n%s", got, want)
	}
	if got := op("/report/{id}", "get", "responses"); !strings.Contains(got, `"application/pdf":{"schema":{"format":"binary","type":"string"}}`) {
		t.Errorf("report: %s", got)
	}
	if got := op("/imports", "post", "requestBody"); got != `{"content":{"multipart/form-data":{"schema":{"$ref":"#/components/schemas/ekman.importForm"}}},"required":true}` {
		t.Errorf("imports body: %s", got)
	}
	if got := op("/imports", "post", "responses"); !strings.Contains(got, `"202":{"description":"Accepted"}`) {
		t.Errorf("imports responses: %s", got)
	}
	form := compact(string(doc.Components.Schemas["ekman.importForm"]))
	if want := `{"properties":{"extra":{"items":{"format":"binary","type":"string"},"type":"array"},"file":{"format":"binary","type":"string"},` +
		`"note":{"type":"string"},"tier":{"$ref":"#/components/schemas/ekman.Tier"}},"required":["file","tier"],"type":"object"}`; form != want {
		t.Errorf("form\n%s\nwant\n%s", form, want)
	}

	body := op("/things/{id}/file", "put", "requestBody")
	if !strings.Contains(body, `"multipart/form-data":{"schema":{"$ref":"#/components/schemas/ekman.importForm"}}`) ||
		!strings.Contains(body, `"application/octet-stream":{"schema":{"format":"binary","type":"string"}}`) {
		t.Errorf("typed upload body: %s", body)
	}
	if got := op("/things/{id}/file", "put", "responses"); !strings.Contains(got, `"200":{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/ekman.uploadOut"}}}`) {
		t.Errorf("typed upload responses: %s", got)
	}

	for _, ri := range a.Routes() {
		if ri.Pattern == "/api/v1/imports" {
			if len(ri.Accepts) != 1 || ri.Accepts[0].MediaType != "multipart/form-data" || ri.Accepts[0].Type != reflect.TypeFor[importForm]() {
				t.Errorf("RouteInfo.Accepts = %+v", ri.Accepts)
			}
			ri.Accepts[0].MediaType = "changed"
		}
	}
	for _, ri := range a.Routes() {
		if ri.Pattern == "/api/v1/imports" && ri.Accepts[0].MediaType != "multipart/form-data" {
			t.Error("Routes must return copies")
		}
	}
}

func TestContentBootErrors(t *testing.T) {
	noop := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	jsonIn := func(context.Context, Caller, *struct {
		Title string `json:"title"`
	}) (NoContent, error) {
		return NoContent{}, nil
	}
	cases := map[string]func(r *Router){
		"Produces is for Raw routes": func(r *Router) {
			Handle(r, "POST /a", Public(), jsonIn, Produces[string]("text/csv"))
		},
		"this route decodes JSON from In": func(r *Router) {
			Handle(r, "POST /b", Public(), jsonIn, Accepts[File]("application/octet-stream"))
		},
		`invalid media type "csv"`: func(r *Router) {
			r.Raw("GET /c", Public(), noop, Produces[string]("csv"))
		},
		`media type "text/csv" documented twice`: func(r *Router) {
			r.Raw("GET /d", Public(), noop, Produces[string]("text/csv"), Produces[File]("text/csv"))
		},
		"need a method": func(r *Router) {
			r.Raw("/e", Public(), noop, Accepts[File]("application/octet-stream"))
		},
	}
	for want, routes := range cases {
		a, err := New(Options{Logger: quietLogger()})
		if err != nil {
			t.Fatal(err)
		}
		a.Register(Module{Name: "m", Routes: routes})
		if _, err := a.Handler(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want error containing %q, got %v", want, err)
		}
	}
}
