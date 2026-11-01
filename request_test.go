package ekman

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type uploadIn struct {
	ID   int64  `path:"id"`
	Name string `query:"name"`
	Req  *http.Request
}

type uploadOut struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	Body  string `json:"body"`
	Field string `json:"field"`
}

func uploadModule() Module {
	return Module{Name: "files", Routes: func(r *Router) {
		Handle(r, "POST /things/{id}/raw", Public(), func(_ context.Context, _ Caller, in *uploadIn) (uploadOut, error) {
			b, err := io.ReadAll(in.Req.Body)
			return uploadOut{ID: in.ID, Name: in.Name, Type: in.Req.Header.Get("Content-Type"), Body: string(b)}, err
		})
		Handle(r, "POST /things/{id}/form", Public(), func(_ context.Context, _ Caller, in *uploadIn) (uploadOut, error) {
			if err := in.Req.ParseMultipartForm(1 << 20); err != nil {
				return uploadOut{}, BadRequest("invalid_upload", err.Error())
			}
			f, _, err := in.Req.FormFile("file")
			if err != nil {
				return uploadOut{}, BadRequest("missing_file", err.Error())
			}
			defer f.Close()
			b, _ := io.ReadAll(f)
			return uploadOut{ID: in.ID, Body: string(b), Field: in.Req.FormValue("title")}, nil
		})
	}}
}

func TestRequestFieldGetsUnreadBody(t *testing.T) {
	_, h := newApp(t, Options{}, uploadModule())
	rec := do(t, h, call{method: "POST", path: "/api/v1/things/7/raw?name=n", body: "a,b\n1,2\n",
		header: http.Header{"Content-Type": {"text/csv"}}})
	expectStatus(t, rec, 201) // not 415: the handler owns the body
	var out uploadOut
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.ID != 7 || out.Name != "n" || out.Type != "text/csv" || out.Body != "a,b\n1,2\n" {
		t.Fatalf("%+v", out)
	}

	body := "--XX\r\nContent-Disposition: form-data; name=\"title\"\r\n\r\nhello\r\n" +
		"--XX\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.txt\"\r\nContent-Type: text/plain\r\n\r\nfile body\r\n--XX--\r\n"
	rec = do(t, h, call{method: "POST", path: "/api/v1/things/8/form", body: body,
		header: http.Header{"Content-Type": {"multipart/form-data; boundary=XX"}}})
	expectStatus(t, rec, 201)
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.ID != 8 || out.Body != "file body" || out.Field != "hello" {
		t.Fatalf("%+v", out)
	}

	rec = do(t, h, call{method: "POST", path: "/api/v1/things/x/raw"})
	expectStatus(t, rec, 400) // parameters still bind and fail as usual
}

func TestRequestFieldBootErrors(t *testing.T) {
	cases := map[string]func(r *Router){
		"no other field may decode from it": func(r *Router) {
			Handle(r, "POST /a", Public(), func(context.Context, Caller, *struct {
				Req   *http.Request
				Title string `json:"title"`
			}) (NoContent, error) {
				return NoContent{}, nil
			})
		},
		"only one *http.Request field": func(r *Router) {
			Handle(r, "POST /b", Public(), func(context.Context, Caller, *struct {
				A, B *http.Request
			}) (NoContent, error) {
				return NoContent{}, nil
			})
		},
		"cannot have a query tag": func(r *Router) {
			Handle(r, "POST /c", Public(), func(context.Context, Caller, *struct {
				Req *http.Request `query:"r"`
			}) (NoContent, error) {
				return NoContent{}, nil
			})
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
	// json:"-" fields alongside the request are fine.
	_, _ = newApp(t, Options{}, Module{Name: "ok", Routes: func(r *Router) {
		Handle(r, "POST /d", Public(), func(context.Context, Caller, *struct {
			Req   *http.Request
			Cache string `json:"-"`
		}) (NoContent, error) {
			return NoContent{}, nil
		})
	}})
}
