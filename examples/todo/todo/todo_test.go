package todo_test

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"strconv"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/sakib412/ekman"
	"github.com/sakib412/ekman/db"
	"github.com/sakib412/ekman/ekmantest"
	"github.com/sakib412/ekman/examples/todo/todo"
	"github.com/sakib412/ekman/storage"
)

func TestTodos(t *testing.T) {
	d := ekmantest.DB(t, "pgx") // skips without EKMAN_TEST_DATABASE_URL
	svc := todo.New(d, storage.Dir(t.TempDir(), "https://files.example.com"))
	opts := ekman.Options{DB: d}
	c := ekmantest.New(t, opts, svc.Module())
	if err := c.App().Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}

	if r := c.Do("POST", "/api/v1/todos", todo.CreateIn{Title: "x"}); r.Status() != 401 {
		t.Fatalf("anonymous create: %d", r.Status())
	}
	if r := c.As("bob").Do("POST", "/api/v1/todos", todo.CreateIn{Title: "x"}); r.Status() != 403 {
		t.Fatalf("create without permission: %d", r.Status())
	}
	if r := c.As("ann", todo.Manage).Do("POST", "/api/v1/todos", todo.CreateIn{}); r.Status() != 422 {
		t.Fatalf("empty title: %d", r.Status())
	}
	r := c.As("ann", todo.Manage).Do("POST", "/api/v1/todos", todo.CreateIn{Title: "x", Priority: "urgent"})
	if r.Status() != 422 || !strings.Contains(string(r.Body()), `"priority":"must be one of: low, normal, high"`) {
		t.Fatalf("unknown priority: %d %s", r.Status(), r.Body())
	}
	r = c.As("ann", todo.Manage).Do("POST", "/api/v1/todos", todo.CreateIn{Title: "write docs"})
	if r.Status() != 201 {
		t.Fatalf("create: %d %s", r.Status(), r.Body())
	}
	var created todo.Todo
	r.JSON(&created)
	if created.Priority != todo.Normal {
		t.Fatalf("default priority: %q", created.Priority)
	}
	if r := c.As("ann", todo.Manage).Do("POST", "/api/v1/todos", todo.CreateIn{Title: "ship", Priority: todo.High}); r.Status() != 201 {
		t.Fatalf("create high: %d %s", r.Status(), r.Body())
	}

	path := "/api/v1/todos/" + strconv.FormatInt(created.ID, 10)
	if r := c.As("bob").Do("PUT", path+"/done", nil); r.Status() != 403 {
		t.Fatalf("someone else's todo: %d", r.Status())
	}
	if r := c.As("ann").Do("PUT", path+"/done", nil); r.Status() != 204 {
		t.Fatalf("owner: %d %s", r.Status(), r.Body())
	}
	if r := c.As("ann").Do("PUT", "/api/v1/todos/999999/done", nil); r.Status() != 404 {
		t.Fatalf("missing todo: %d", r.Status())
	}
	var list []todo.Todo
	c.Do("GET", "/api/v1/todos?done=true", nil).JSON(&list)
	if len(list) != 1 || !list[0].Done {
		t.Fatalf("list: %+v", list)
	}
	c.Do("GET", "/api/v1/todos?priority=high", nil).JSON(&list)
	if len(list) != 1 || list[0].Title != "ship" {
		t.Fatalf("by priority: %+v", list)
	}
	if r := c.Do("GET", "/api/v1/todos?priority=urgent", nil); r.Status() != 400 || r.ErrorCode() != "invalid_param" {
		t.Fatalf("unknown priority filter: %d %s", r.Status(), r.Body())
	}

	r = c.Do("GET", "/api/v1/todos.csv", nil)
	if r.Status() != 200 || !strings.HasPrefix(r.Header().Get("Content-Type"), "text/csv") ||
		!strings.HasPrefix(string(r.Body()), "id,title,owner_id,done,priority,created_at\n") || !strings.Contains(string(r.Body()), ",ship,ann,false,high,") {
		t.Fatalf("csv: %d %s", r.Status(), r.Body())
	}

	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
	upload := func(as *ekmantest.Client, content []byte) *ekmantest.Response {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		fw, _ := mw.CreateFormFile("file", "a.png")
		_, _ = fw.Write(content)
		_ = mw.Close()
		return as.WithHeader("Content-Type", mw.FormDataContentType()).Do("PUT", path+"/attachment", body.Bytes())
	}
	if r := upload(c.As("bob"), png); r.Status() != 403 {
		t.Fatalf("upload to someone else's todo: %d %s", r.Status(), r.Body())
	}
	if r := upload(c.As("ann"), []byte("just text")); r.Status() != 415 || r.ErrorCode() != "unsupported_type" {
		t.Fatalf("text upload: %d %s", r.Status(), r.Body())
	}
	r = upload(c.As("ann"), png)
	if r.Status() != 200 {
		t.Fatalf("upload: %d %s", r.Status(), r.Body())
	}
	var attached todo.Todo
	r.JSON(&attached)
	if attached.AttachmentURL == nil || !strings.HasPrefix(*attached.AttachmentURL, "https://files.example.com/todos/") ||
		!strings.HasSuffix(*attached.AttachmentURL, ".png") {
		t.Fatalf("attachment: %+v", attached)
	}
}

// TestOpenAPI needs no database: the pool is lazy and booting never connects.
func TestOpenAPI(t *testing.T) {
	d, err := db.Open("pgx", "postgres://localhost/unused")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	c := ekmantest.New(t, ekman.Options{DB: d}, todo.New(d, storage.Dir(t.TempDir(), "")).Module())
	raw, err := c.App().OpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths      map[string]map[string]map[string]any
		Components struct{ Schemas map[string]any }
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	js := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	if got := js(doc.Components.Schemas["todo.Priority"]); got != `{"enum":["low","normal","high"],"type":"string"}` {
		t.Errorf("Priority: %s", got)
	}
	if got := js(doc.Paths["/api/v1/todos.csv"]["get"]["responses"]); !strings.Contains(got, `"200":{"content":{"text/csv":{"schema":{"type":"string"}}}`) {
		t.Errorf("csv export: %s", got)
	}
	if got := js(doc.Paths["/api/v1/todos/{id}/attachment"]["put"]["requestBody"]); got != `{"content":{"multipart/form-data":{"schema":{"$ref":"#/components/schemas/todo.AttachmentForm"}}},"required":true}` {
		t.Errorf("attachment body: %s", got)
	}
	if got := js(doc.Components.Schemas["todo.AttachmentForm"]); got != `{"properties":{"file":{"format":"binary","type":"string"}},"required":["file"],"type":"object"}` {
		t.Errorf("attachment form: %s", got)
	}
}
