package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"

func TestChallengeRFC7636(t *testing.T) {
	// Appendix B of RFC 7636.
	if got := Challenge(verifier); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Fatal(got)
	}
}

func TestAuthCodeURL(t *testing.T) {
	p := Google("cid", "secret")
	u, err := url.Parse(p.AuthCodeURL("st", verifier, "https://api.example.com/cb"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	checks := map[string]string{
		"client_id": "cid", "state": "st", "redirect_uri": "https://api.example.com/cb",
		"code_challenge_method": "S256", "code_challenge": Challenge(verifier),
		"response_type": "code", "scope": "openid email profile",
	}
	for k, want := range checks {
		if got := q.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if u.Host != "accounts.google.com" {
		t.Errorf("host %s", u.Host)
	}
}

// fakeIdP serves a token endpoint that checks the PKCE verifier, plus identity endpoints.
func fakeIdP(t *testing.T, githubStyleError bool) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("code_verifier") != verifier || r.Form.Get("code") != "good" || r.Form.Get("client_secret") != "sec" {
			if githubStyleError {
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "bad_verification_code"})
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok", "token_type": "bearer"})
	})
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer tok" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /userinfo", auth(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"sub":"g-1","email":"a@example.com","email_verified":true,"name":"Ada","picture":"https://x/p.png"}`))
	}))
	mux.HandleFunc("GET /user", auth(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":42,"login":"ada","name":"","avatar_url":"https://x/a.png"}`))
	}))
	mux.HandleFunc("GET /user/emails", auth(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"email":"old@example.com","primary":false,"verified":true},{"email":"ada@example.com","primary":true,"verified":true}]`))
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestGoogleExchange(t *testing.T) {
	srv := fakeIdP(t, false)
	p := google("cid", "sec", endpoints{auth: srv.URL + "/auth", token: srv.URL + "/token", api: srv.URL + "/userinfo"})
	id, err := p.Exchange(context.Background(), "good", verifier, "https://cb")
	if err != nil {
		t.Fatal(err)
	}
	want := Identity{Provider: "google", Subject: "g-1", Email: "a@example.com", EmailVerified: true, Name: "Ada", AvatarURL: "https://x/p.png"}
	if id != want {
		t.Fatalf("got %+v", id)
	}
	if _, err := p.Exchange(context.Background(), "bad", verifier, "https://cb"); err == nil || !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("bad code: %v", err)
	}
}

func TestGitHubExchange(t *testing.T) {
	srv := fakeIdP(t, true)
	p := github("cid", "sec", endpoints{auth: srv.URL + "/auth", token: srv.URL + "/token", api: srv.URL})
	id, err := p.Exchange(context.Background(), "good", verifier, "https://cb")
	if err != nil {
		t.Fatal(err)
	}
	if id.Subject != "42" || id.Email != "ada@example.com" || !id.EmailVerified || id.Name != "ada" || id.Provider != "github" {
		t.Fatalf("got %+v", id)
	}
	if _, err := p.Exchange(context.Background(), "nope", "wrong", "https://cb"); err == nil || !strings.Contains(err.Error(), "bad_verification_code") {
		t.Fatalf("200-with-error must fail: %v", err)
	}
}

func TestMemoryStore(t *testing.T) {
	ctx := context.Background()
	s := MemoryStore()
	now := time.Now()
	_ = s.Create(ctx, Session{TokenHash: []byte("a"), UserID: "u1", CreatedAt: now, ExpiresAt: now.Add(time.Hour)})
	_ = s.Create(ctx, Session{TokenHash: []byte("b"), UserID: "u1", CreatedAt: now, ExpiresAt: now.Add(-time.Second)})
	if got, err := s.Get(ctx, []byte("a")); err != nil || got.UserID != "u1" {
		t.Fatal(got, err)
	}
	if _, err := s.Get(ctx, []byte("b")); !errors.Is(err, ErrNoSession) {
		t.Fatal("expired session returned")
	}
	_ = s.Touch(ctx, []byte("b"), now.Add(time.Hour))
	if _, err := s.Get(ctx, []byte("b")); err != nil {
		t.Fatal("touch did not extend")
	}
	_ = s.Delete(ctx, []byte("a"))
	if _, err := s.Get(ctx, []byte("a")); !errors.Is(err, ErrNoSession) {
		t.Fatal("delete")
	}
	_ = s.DeleteUser(ctx, "u1")
	if _, err := s.Get(ctx, []byte("b")); !errors.Is(err, ErrNoSession) {
		t.Fatal("delete user")
	}
}
