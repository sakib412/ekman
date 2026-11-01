package storage

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sakib412/ekman"
)

var pngHeader = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

func upload(t *testing.T, field, filename string, content []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	_ = w.WriteField("caption", "hello")
	fw, err := w.CreateFormFile(field, filename)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write(content)
	_ = w.Close()
	req := httptest.NewRequest("POST", "/upload", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}

func TestReceive(t *testing.T) {
	root := t.TempDir()
	b := Dir(root, "https://cdn.example.com/files/")
	content := append(append([]byte{}, pngHeader...), bytes.Repeat([]byte{1}, 100)...)
	obj, err := Receive(upload(t, "file", "evil.html", content), "file", b, Policy{Types: []string{"image/png"}, Prefix: "avatars/", MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if obj.ContentType != "image/png" || obj.Size != int64(len(content)) || !strings.HasPrefix(obj.Key, "avatars/") || !strings.HasSuffix(obj.Key, ".png") {
		t.Fatalf("%+v", obj)
	}
	if obj.URL != "https://cdn.example.com/files/"+obj.Key {
		t.Fatal(obj.URL)
	}
	got, err := os.ReadFile(filepath.Join(root, obj.Key))
	if err != nil || !bytes.Equal(got, content) {
		t.Fatal("stored content differs", err)
	}
	if err := b.Delete(context.Background(), obj.Key); err != nil {
		t.Fatal(err)
	}
	if err := b.Delete(context.Background(), obj.Key); err != nil {
		t.Fatal("deleting a missing key must not fail")
	}
}

func TestReceiveRejects(t *testing.T) {
	b := Dir(t.TempDir(), "/files")
	png := Policy{Types: []string{"image/png"}, MaxBytes: 64}
	big := append(append([]byte{}, pngHeader...), bytes.Repeat([]byte{1}, 100)...)
	cases := []struct {
		name   string
		req    *http.Request
		field  string
		status int
		code   string
	}{
		{"spoofed type", upload(t, "file", "a.png", []byte("<html><script>x</script>")), "file", 415, "unsupported_type"},
		{"too large", upload(t, "file", "a.png", big), "file", 413, "too_large"},
		{"missing field", upload(t, "other", "a.png", pngHeader), "file", 400, "missing_file"},
		{"empty", upload(t, "file", "a.png", nil), "file", 400, "empty_file"},
		{"not multipart", httptest.NewRequest("POST", "/", strings.NewReader("{}")), "file", 400, "invalid_upload"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Receive(c.req, c.field, b, png)
			var e *ekman.Error
			if !errors.As(err, &e) || e.Status != c.status || e.Code != c.code {
				t.Fatalf("err = %v", err)
			}
		})
	}
	if _, err := Receive(upload(t, "file", "a.png", pngHeader), "file", b, Policy{}); err == nil {
		t.Fatal("empty Types must be rejected")
	}
}

func TestDirKeys(t *testing.T) {
	b := Dir(t.TempDir(), "/f")
	for _, key := range []string{"", "/abs", "../up", "a/../../b", "a\\b", "a//b", ".."} {
		if _, err := b.Put(context.Background(), key, strings.NewReader("x"), "text/plain"); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("key %q: err = %v", key, err)
		}
	}
	obj, err := b.Put(context.Background(), "a b/c.txt", strings.NewReader("x"), "text/plain")
	if err != nil || obj.URL != "/f/a%20b/c.txt" {
		t.Fatal(obj, err)
	}
}
