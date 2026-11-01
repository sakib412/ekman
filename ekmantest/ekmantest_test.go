package ekmantest_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sakib412/ekman"
	"github.com/sakib412/ekman/auth"
	"github.com/sakib412/ekman/ekmantest"
	"github.com/sakib412/ekman/rbac"
)

const manage rbac.Permission = "notes.manage"

type note struct {
	Title string `json:"title"`
	By    string `json:"by"`
}

type noteIn struct {
	Title string `json:"title"`
}

func (in *noteIn) Validate(v *ekman.Validation) { v.Required("title", in.Title) }

func module() ekman.Module {
	return ekman.Module{
		Name:        "notes",
		Permissions: []rbac.Permission{manage},
		Routes: func(r *ekman.Router) {
			ekman.Handle(r, "POST /notes", ekman.Requires(manage), func(_ context.Context, c ekman.Caller, in *noteIn) (note, error) {
				return note{Title: in.Title, By: c.UserID()}, nil
			})
		},
	}
}

func TestClient(t *testing.T) {
	c := ekmantest.New(t, ekman.Options{}, module())
	if got := c.Do("POST", "/api/v1/notes", noteIn{"x"}).Status(); got != 401 {
		t.Fatalf("anonymous: %d", got)
	}
	if r := c.As("u1").Do("POST", "/api/v1/notes", noteIn{"x"}); r.Status() != 403 || r.ErrorCode() != "forbidden" {
		t.Fatalf("no perm: %d %s", r.Status(), r.Body())
	}
	r := c.As("u1", manage).Do("POST", "/api/v1/notes", noteIn{"hello"})
	if r.Status() != 201 {
		t.Fatalf("%d %s", r.Status(), r.Body())
	}
	var n note
	r.JSON(&n)
	if n.By != "u1" || n.Title != "hello" {
		t.Fatal(n)
	}
	if r := c.As("admin", rbac.All).Do("POST", "/api/v1/notes", `{"title":""}`); r.Status() != 422 || r.ErrorCode() != "invalid_input" {
		t.Fatalf("%d %s", r.Status(), r.Body())
	}
	if c.As("u1", manage).Anonymous().Do("POST", "/api/v1/notes", noteIn{"x"}).Status() != 401 {
		t.Fatal("Anonymous must drop the identity")
	}
}

func TestIdentityHeaderIsInertOnARealApp(t *testing.T) {
	app, err := ekman.New(ekman.Options{Auth: &auth.Config{Store: auth.MemoryStore()}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	app.Register(module())
	h, err := app.Handler()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/v1/notes", strings.NewReader(`{"title":"x"}`))
	req.Header.Set("Ekmantest-Identity", `{"UserID":"root","Perms":["*"]}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("a production handler must ignore the test identity header, got %d", rec.Code)
	}
}
