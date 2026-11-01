package todo_test

import (
	"context"
	"strconv"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/sakib412/ekman"
	"github.com/sakib412/ekman/ekmantest"
	"github.com/sakib412/ekman/examples/todo/todo"
)

func TestTodos(t *testing.T) {
	d := ekmantest.DB(t, "pgx") // skips without EKMAN_TEST_DATABASE_URL
	svc := todo.New(d)
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
	r := c.As("ann", todo.Manage).Do("POST", "/api/v1/todos", todo.CreateIn{Title: "write docs"})
	if r.Status() != 201 {
		t.Fatalf("create: %d %s", r.Status(), r.Body())
	}
	var created todo.Todo
	r.JSON(&created)

	path := "/api/v1/todos/" + strconv.FormatInt(created.ID, 10) + "/done"
	if r := c.As("bob").Do("PUT", path, nil); r.Status() != 403 {
		t.Fatalf("someone else's todo: %d", r.Status())
	}
	if r := c.As("ann").Do("PUT", path, nil); r.Status() != 204 {
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
}
