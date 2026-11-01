package ekman

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/sakib412/ekman/rbac"
)

type Todo struct {
	ID        int64             `json:"id"`
	Title     string            `json:"title"`
	Note      *string           `json:"note"`
	DoneAt    *time.Time        `json:"doneAt,omitempty"`
	CreatedAt time.Time         `json:"createdAt"`
	Labels    []string          `json:"labels"`
	Meta      map[string]int    `json:"meta,omitempty"`
	Owner     *Person           `json:"owner"`
	Perm      rbac.Permission   `json:"perm"`
	Count     int64             `json:"count,string"`
	Skip      string            `json:"-"`
	Raw       json.RawMessage   `json:"raw,omitempty"`
	Extra     map[string]Person `json:"extra,omitempty"`
	Embedded
	unexported int
}

type Embedded struct {
	Source string `json:"source"`
}

type Person struct {
	Name    string   `json:"name"`
	Friends []Person `json:"friends,omitempty"`
}

type updateIn struct {
	ID    int64    `path:"id"`
	Q     []int    `query:"q"`
	Token string   `header:"x-token"`
	Title string   `json:"title"`
	When  *float64 `json:"when"`
}

func (in *updateIn) Validate(v *Validation) {}

func openAPIApp(t *testing.T) *App {
	a, _ := newApp(t, Options{Auth: testAuth(), Info: Info{Title: "Todos", Version: "1.2.3"}},
		Module{Name: "todo", Permissions: []rbac.Permission{"todo.manage", "todo.read"}, Routes: func(r *Router) {
			Handle(r, "GET /todos", Public(), func(context.Context, Caller, *struct{}) ([]Todo, error) { return nil, nil }, Summary("List todos"))
			Handle(r, "PUT /todos/{id}", Requires("todo.manage"), func(context.Context, Caller, *updateIn) (Todo, error) { return Todo{}, nil })
			Handle(r, "DELETE /todos/{id}", SignedIn(), func(context.Context, Caller, *struct {
				ID int64 `path:"id"`
			}) (NoContent, error) {
				return NoContent{}, nil
			})
		}})
	return a
}

func TestOpenAPIDocument(t *testing.T) {
	a := openAPIApp(t)
	raw, err := a.OpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	get := func(v any, path ...string) any {
		t.Helper()
		for _, p := range path {
			m, ok := v.(map[string]any)
			if !ok {
				t.Fatalf("at %q: not an object in path %v", p, path)
			}
			v = m[p]
		}
		return v
	}
	js := func(v any) string { b, _ := json.Marshal(v); return string(b) }

	if doc["openapi"] != "3.1.0" || get(doc, "info", "title") != "Todos" || get(doc, "info", "version") != "1.2.3" {
		t.Fatal("header")
	}
	if get(doc, "paths", "/api/v1/openapi.json") != nil {
		t.Fatal("openapi.json must be hidden")
	}
	list := get(doc, "paths", "/api/v1/todos", "get")
	if get(list, "operationId") != "getTodos" || get(list, "summary") != "List todos" || get(list, "x-ekman-access") != "public" || get(list, "security") != nil {
		t.Fatal(js(list))
	}
	if js(get(list, "responses", "200", "content", "application/json", "schema")) != `{"items":{"$ref":"#/components/schemas/ekman.Todo"},"type":"array"}` {
		t.Fatal(js(get(list, "responses")))
	}

	put := get(doc, "paths", "/api/v1/todos/{id}", "put")
	if get(put, "operationId") != "putTodosById" || get(put, "x-ekman-access") != "todo.manage" || get(put, "security") == nil {
		t.Fatal(js(put))
	}
	for _, code := range []string{"200", "400", "401", "403", "422", "default"} {
		if get(put, "responses", code) == nil {
			t.Errorf("missing response %s", code)
		}
	}
	wantParams := `[{"in":"path","name":"id","required":true,"schema":{"format":"int64","type":"integer"}},` +
		`{"in":"query","name":"q","required":false,"schema":{"items":{"format":"int64","type":"integer"},"type":"array"}},` +
		`{"in":"header","name":"X-Token","required":false,"schema":{"type":"string"}}]`
	if got := js(get(put, "parameters")); got != wantParams {
		t.Fatalf("params\n%s\nwant\n%s", got, wantParams)
	}
	if js(get(put, "requestBody", "content", "application/json", "schema")) != `{"$ref":"#/components/schemas/ekman.updateInBody"}` {
		t.Fatal(js(get(put, "requestBody")))
	}
	body := get(doc, "components", "schemas", "ekman.updateInBody")
	if js(body) != `{"properties":{"title":{"type":"string"},"when":{"format":"double","type":["number","null"]}},"required":["title","when"],"type":"object"}` {
		t.Fatal(js(body))
	}

	del := get(doc, "paths", "/api/v1/todos/{id}", "delete")
	if js(get(del, "responses", "204")) != `{"description":"No Content"}` || get(del, "responses", "403") != nil {
		t.Fatal(js(get(del, "responses")))
	}

	todo := get(doc, "components", "schemas", "ekman.Todo").(map[string]any)
	props := todo["properties"].(map[string]any)
	checks := map[string]string{
		"note":      `{"type":["string","null"]}`,
		"doneAt":    `{"format":"date-time","type":["string","null"]}`,
		"createdAt": `{"format":"date-time","type":"string"}`,
		"labels":    `{"items":{"type":"string"},"type":"array"}`,
		"meta":      `{"additionalProperties":{"format":"int64","type":"integer"},"type":"object"}`,
		"owner":     `{"anyOf":[{"$ref":"#/components/schemas/ekman.Person"},{"type":"null"}]}`,
		"perm":      `{"$ref":"#/components/schemas/Permission"}`,
		"count":     `{"type":"string"}`,
		"raw":       `{}`,
		"source":    `{"type":"string"}`,
	}
	for k, want := range checks {
		if got := js(props[k]); got != want {
			t.Errorf("%s = %s, want %s", k, got, want)
		}
	}
	for _, k := range []string{"Skip", "unexported", "Embedded"} {
		if _, ok := props[k]; ok {
			t.Errorf("%s must not be a property", k)
		}
	}
	if js(todo["required"]) != `["id","title","note","createdAt","labels","owner","perm","count","source"]` {
		t.Error(js(todo["required"]))
	}
	person := get(doc, "components", "schemas", "ekman.Person")
	if js(get(person, "properties", "friends")) != `{"items":{"$ref":"#/components/schemas/ekman.Person"},"type":"array"}` {
		t.Fatal("recursive type", js(person))
	}
	if js(get(doc, "components", "schemas", "Permission", "enum")) != `["todo.manage","todo.read"]` {
		t.Fatal(js(get(doc, "components", "schemas", "Permission")))
	}
	if get(doc, "components", "schemas", "Error", "properties", "error", "properties", "requestId") == nil {
		t.Fatal("error schema")
	}
	me := get(doc, "components", "schemas", "ekman.Me", "properties", "permissions")
	if js(me) != `{"items":{"$ref":"#/components/schemas/Permission"},"type":"array"}` {
		t.Fatal(js(me))
	}
	start := get(doc, "paths", "/api/v1/auth/{provider}/start", "get")
	if js(get(start, "parameters")) != `[{"in":"path","name":"provider","required":true,"schema":{"type":"string"}}]` || get(start, "responses", "302") == nil {
		t.Fatal(js(start))
	}
}

func TestOpenAPIDeterministic(t *testing.T) {
	first, _ := openAPIApp(t).OpenAPI()
	for range 5 {
		next, _ := openAPIApp(t).OpenAPI()
		if !bytes.Equal(first, next) {
			t.Fatal("OpenAPI output is not deterministic")
		}
	}
	_, h := newApp(t, Options{})
	rec := do(t, h, call{method: "GET", path: "/api/v1/openapi.json"})
	expectStatus(t, rec, 200)
	if !json.Valid(rec.Body.Bytes()) {
		t.Fatal("served document is not JSON")
	}
}

func TestOperationID(t *testing.T) {
	cases := map[[2]string]string{
		{"GET", "/todos/{id}/done"}:  "getTodosByIdDone",
		{"POST", "/a-b/{rest...}"}:   "postABByRest",
		{"GET", "/{$}"}:              "get",
		{"PATCH", "/user_profiles"}:  "patchUserProfiles",
		{"DELETE", "/v2/items/{id}"}: "deleteV2ItemsById",
	}
	for in, want := range cases {
		if got := operationID(in[0], in[1]); got != want {
			t.Errorf("operationID(%v) = %q, want %q", in, got, want)
		}
	}
}
