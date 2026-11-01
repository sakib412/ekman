package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBrevo(t *testing.T) {
	var got map[string]any
	var key string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key = r.Header.Get("api-key")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"messageId":"<x@brevo>"}`))
	}))
	defer srv.Close()
	s := Brevo("k-1", "noreply@example.com", "Example").(*brevo)
	s.endpoint = srv.URL
	err := s.Send(context.Background(), Message{To: []string{"a@example.com", "b@example.com"}, Subject: "Hi", Text: "t", HTML: "<p>h</p>", ReplyTo: "r@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(got)
	want := `{"htmlContent":"<p>h</p>","replyTo":{"email":"r@example.com"},"sender":{"email":"noreply@example.com","name":"Example"},"subject":"Hi","textContent":"t","to":[{"email":"a@example.com"},{"email":"b@example.com"}]}`
	if strings.TrimSpace(b.String()) != want || key != "k-1" {
		t.Fatalf("payload %s key %q", b.String(), key)
	}
}

func TestBrevoError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"unauthorized","message":"Key not found"}`))
	}))
	defer srv.Close()
	s := Brevo("bad", "noreply@example.com", "").(*brevo)
	s.endpoint = srv.URL
	err := s.Send(context.Background(), Message{To: []string{"a@example.com"}, Subject: "Hi", Text: "t"})
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "Key not found") {
		t.Fatal(err)
	}
}

func TestValidateAndLog(t *testing.T) {
	var buf bytes.Buffer
	s := Log(slog.New(slog.NewTextHandler(&buf, nil)))
	for _, m := range []Message{{Subject: "s", Text: "t"}, {To: []string{"a@x"}, Text: "t"}, {To: []string{"a@x"}, Subject: "s"}} {
		if s.Send(context.Background(), m) == nil {
			t.Errorf("invalid message accepted: %+v", m)
		}
	}
	if err := s.Send(context.Background(), Message{To: []string{"a@x"}, Subject: "Welcome", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "Welcome") {
		t.Fatal(buf.String())
	}
}
