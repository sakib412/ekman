package ekman

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

func TestRequestID(t *testing.T) {
	var seen string
	_, h := newApp(t, Options{}, Module{Name: "m", Routes: func(r *Router) {
		Handle(r, "GET /x", Public(), func(ctx context.Context, _ Caller, _ *struct{}) (NoContent, error) {
			seen = RequestID(ctx)
			return NoContent{}, nil
		})
	}})
	rec := do(t, h, call{method: "GET", path: "/api/v1/x", header: http.Header{"X-Request-Id": {"abc-123"}}})
	if rec.Header().Get("X-Request-Id") != "abc-123" || seen != "abc-123" {
		t.Fatalf("incoming id not kept: %q %q", rec.Header().Get("X-Request-Id"), seen)
	}
	rec = do(t, h, call{method: "GET", path: "/api/v1/x", header: http.Header{"X-Request-Id": {"bad id"}}})
	if id := rec.Header().Get("X-Request-Id"); len(id) != 24 || id != seen {
		t.Fatalf("unsafe id should be replaced, got %q", id)
	}
}

func TestAccessLog(t *testing.T) {
	var buf bytes.Buffer
	_, h := newApp(t, Options{Logger: slog.New(slog.NewTextHandler(&buf, nil)), Auth: testAuth()}, Module{Name: "m", Routes: func(r *Router) {
		Handle(r, "GET /x/{id}", SignedIn(), func(context.Context, Caller, *struct{}) (NoContent, error) { return NoContent{}, nil })
	}})
	do(t, h, call{method: "GET", path: "/api/v1/x/7", as: as("u9")})
	line := buf.String()
	for _, want := range []string{"msg=request", "status=204", `route="GET /api/v1/x/{id}"`, "user_id=u9", "request_id="} {
		if !strings.Contains(line, want) {
			t.Errorf("log line missing %q: %s", want, line)
		}
	}
}

func TestCORS(t *testing.T) {
	_, h := newApp(t, Options{CORS: []string{"https://app.example.com"}}, Module{Name: "m", Routes: func(r *Router) {
		Handle(r, "POST /x", Public(), noop)
	}})
	pre := do(t, h, call{method: "OPTIONS", path: "/api/v1/x", header: http.Header{
		"Origin": {"https://app.example.com"}, "Access-Control-Request-Method": {"POST"}, "Access-Control-Request-Headers": {"content-type"},
	}})
	expectStatus(t, pre, 204)
	if pre.Header().Get("Access-Control-Allow-Origin") != "https://app.example.com" ||
		pre.Header().Get("Access-Control-Allow-Credentials") != "true" ||
		pre.Header().Get("Access-Control-Allow-Headers") != "content-type" {
		t.Fatal(pre.Header())
	}
	bad := do(t, h, call{method: "OPTIONS", path: "/api/v1/x", header: http.Header{
		"Origin": {"https://evil.example"}, "Access-Control-Request-Method": {"POST"},
	}})
	expectStatus(t, bad, 403)

	ok := do(t, h, call{method: "POST", path: "/api/v1/x", header: http.Header{"Origin": {"https://app.example.com"}}})
	expectStatus(t, ok, 204)
	if ok.Header().Get("Access-Control-Allow-Origin") == "" {
		t.Fatal("missing CORS header on actual request")
	}
	foreign := do(t, h, call{method: "POST", path: "/api/v1/x", header: http.Header{"Origin": {"https://evil.example"}}})
	expectStatus(t, foreign, 403)
	same := do(t, h, call{method: "POST", path: "/api/v1/x", header: http.Header{"Origin": {"http://example.com"}}})
	expectStatus(t, same, 204) // httptest requests have Host example.com
	get := do(t, h, call{method: "GET", path: "/healthz", header: http.Header{"Origin": {"https://evil.example"}}})
	expectStatus(t, get, 200)
	if get.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("foreign origin must not get CORS headers")
	}
}
