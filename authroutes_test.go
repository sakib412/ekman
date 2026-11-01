package ekman

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sakib412/ekman/auth"
	"github.com/sakib412/ekman/rbac"
)

// fakeProvider records the verifier it was given and only accepts it back with code "good".
type fakeProvider struct{ verifier, redirect string }

func (p *fakeProvider) Name() string { return "fake" }

func (p *fakeProvider) AuthCodeURL(state, verifier, redirectURL string) string {
	p.verifier, p.redirect = verifier, redirectURL
	return "https://idp.test/authorize?state=" + url.QueryEscape(state)
}

func (p *fakeProvider) Exchange(_ context.Context, code, verifier, redirectURL string) (auth.Identity, error) {
	if code != "good" || verifier != p.verifier || redirectURL != p.redirect {
		return auth.Identity{}, fmt.Errorf("bad exchange")
	}
	return auth.Identity{Provider: "fake", Subject: "s-1", Email: "ada@example.com", EmailVerified: true}, nil
}

type flow struct {
	t       *testing.T
	h       http.Handler
	cookies map[string]*http.Cookie
}

func (f *flow) do(method, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for _, c := range f.cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	for _, c := range rec.Result().Cookies() {
		if c.MaxAge < 0 {
			delete(f.cookies, c.Name)
		} else {
			f.cookies[c.Name] = c
		}
	}
	return rec
}

func authApp(t *testing.T, cfg auth.Config) (*flow, *fakeProvider) {
	p := &fakeProvider{}
	cfg.Providers = []auth.Provider{p}
	if cfg.Store == nil {
		cfg.Store = auth.MemoryStore()
	}
	if cfg.Directory == nil {
		cfg.Directory = auth.DirectoryFunc(func(_ context.Context, id auth.Identity) (string, error) {
			if !id.EmailVerified {
				return "", Forbidden("verified email required")
			}
			return "user-" + id.Subject, nil
		})
	}
	_, h := newApp(t, Options{
		Auth:  &cfg,
		Roles: []rbac.Role{{Name: "admin", Permissions: []rbac.Permission{rbac.All}}},
		Grants: rbac.GrantFunc(func(_ context.Context, uid string) ([]string, error) {
			return []string{"admin", "ghost"}, nil
		}),
	}, Module{Name: "todo", Permissions: []rbac.Permission{permManage}})
	return &flow{t: t, h: h, cookies: map[string]*http.Cookie{}}, p
}

func (f *flow) signIn(next string) *httptest.ResponseRecorder {
	f.t.Helper()
	rec := f.do("GET", "/api/v1/auth/fake/start?next="+url.QueryEscape(next))
	expectStatus(f.t, rec, http.StatusFound)
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if f.cookies[oauthCookie] == nil {
		f.t.Fatal("no state cookie")
	}
	return f.do("GET", "/api/v1/auth/fake/callback?code=good&state="+url.QueryEscape(loc.Query().Get("state")))
}

func TestOAuthFlow(t *testing.T) {
	f, p := authApp(t, auth.Config{PublicURL: "https://api.example.com", AfterLogin: "https://app.example.com/home"})
	rec := f.signIn("/events/x?tab=1")
	expectStatus(t, rec, http.StatusFound)
	if got := rec.Header().Get("Location"); got != "https://app.example.com/events/x?tab=1" {
		t.Fatalf("redirect %q", got)
	}
	if p.redirect != "https://api.example.com/api/v1/auth/fake/callback" {
		t.Fatalf("redirect URL %q", p.redirect)
	}
	s, hint := f.cookies[sessionCookie], f.cookies[hintCookie]
	if s == nil || !s.HttpOnly || !s.Secure || s.SameSite != http.SameSiteLaxMode || hint == nil || hint.Value != "1" || hint.HttpOnly {
		t.Fatalf("cookies %+v %+v", s, hint)
	}
	if f.cookies[oauthCookie] != nil {
		t.Fatal("state cookie must be cleared")
	}

	rec = f.do("GET", "/api/v1/auth/me")
	var me Me
	_ = json.Unmarshal(rec.Body.Bytes(), &me)
	if me.UserID != "user-s-1" || len(me.Roles) != 1 || me.Roles[0] != "admin" || len(me.Permissions) != 1 || me.Permissions[0] != permManage {
		t.Fatalf("me %+v", me)
	}

	expectStatus(t, f.do("POST", "/api/v1/auth/logout"), 204)
	if f.cookies[sessionCookie] != nil || f.cookies[hintCookie] != nil {
		t.Fatal("logout must clear cookies")
	}
	rec = f.do("GET", "/api/v1/auth/me")
	if compact(rec.Body.String()) != `{"userId":"","permissions":[],"roles":[]}` {
		t.Fatal(rec.Body.String())
	}
}

func TestOAuthFlowLocalOriginIsNotSecure(t *testing.T) {
	f, _ := authApp(t, auth.Config{PublicURL: "http://binarysust.localhost"})
	rec := f.signIn("//evil.example/x")
	if got := rec.Header().Get("Location"); got != "/" {
		t.Fatalf("protocol-relative next must be ignored, got %q", got)
	}
	if f.cookies[sessionCookie].Secure || f.cookies[hintCookie].Secure {
		t.Fatal("cookies on a .localhost origin must not be Secure")
	}
}

func TestOAuthFailures(t *testing.T) {
	f, _ := authApp(t, auth.Config{})
	expectStatus(t, f.do("GET", "/api/v1/auth/nope/start"), 404)

	// Callback without the state cookie.
	expectStatus(t, f.do("GET", "/api/v1/auth/fake/callback?code=good&state=x"), 400)

	// Wrong state.
	f.do("GET", "/api/v1/auth/fake/start")
	rec := f.do("GET", "/api/v1/auth/fake/callback?code=good&state=forged")
	expectStatus(t, rec, 400)
	if decodeErr(t, rec).Code != "invalid_state" {
		t.Fatal(rec.Body.String())
	}

	// Tampered cookie.
	rec = f.do("GET", "/api/v1/auth/fake/start")
	loc, _ := url.Parse(rec.Header().Get("Location"))
	c := f.cookies[oauthCookie]
	c.Value = strings.Replace(c.Value, ".", "x.", 1)
	expectStatus(t, f.do("GET", "/api/v1/auth/fake/callback?code=good&state="+loc.Query().Get("state")), 400)

	// Provider exchange fails.
	rec = f.do("GET", "/api/v1/auth/fake/start")
	loc, _ = url.Parse(rec.Header().Get("Location"))
	rec = f.do("GET", "/api/v1/auth/fake/callback?code=bad&state="+loc.Query().Get("state"))
	expectStatus(t, rec, http.StatusBadGateway)

	// Provider reported an error.
	rec = f.do("GET", "/api/v1/auth/fake/start")
	loc, _ = url.Parse(rec.Header().Get("Location"))
	expectStatus(t, f.do("GET", "/api/v1/auth/fake/callback?error=access_denied&state="+loc.Query().Get("state")), 401)
}

func TestDirectoryRefusal(t *testing.T) {
	f, _ := authApp(t, auth.Config{Directory: auth.DirectoryFunc(func(context.Context, auth.Identity) (string, error) {
		return "", Forbidden("not invited")
	})})
	rec := f.signIn("")
	expectStatus(t, rec, 403)
	if e := decodeErr(t, rec); e.Message != "not invited" {
		t.Fatal(e)
	}
	if f.cookies[sessionCookie] != nil {
		t.Fatal("refused sign-in must not create a session")
	}
}

func TestExpiredSessionIsAnonymousAndSlides(t *testing.T) {
	store := auth.MemoryStore()
	f, _ := authApp(t, auth.Config{Store: store, TTL: time.Hour})
	f.signIn("")
	token := f.cookies[sessionCookie].Value
	ctx := context.Background()

	// Pretend the session was last refreshed long ago: the next request slides it.
	_ = store.Touch(ctx, hashToken(token), time.Now().Add(10*time.Minute))
	f.do("GET", "/api/v1/auth/me")
	sess, err := store.Get(ctx, hashToken(token))
	if err != nil || time.Until(sess.ExpiresAt) < 50*time.Minute {
		t.Fatalf("session not slid: %v %v", sess.ExpiresAt, err)
	}

	_ = store.Touch(ctx, hashToken(token), time.Now().Add(-time.Second))
	rec := f.do("GET", "/api/v1/auth/me")
	var me Me
	_ = json.Unmarshal(rec.Body.Bytes(), &me)
	if me.UserID != "" || f.cookies[sessionCookie] != nil {
		t.Fatal("expired session must be anonymous and cleared")
	}
}

func TestStateSigning(t *testing.T) {
	a := &App{stateKey: deriveKey([]byte(strings.Repeat("k", 32)), "p")}
	v := a.signState(oauthState{Provider: "g", State: "s", Expires: time.Now().Add(time.Minute).Unix()})
	if st, ok := a.verifyState(v); !ok || st.State != "s" {
		t.Fatal("round trip")
	}
	expired := a.signState(oauthState{Expires: time.Now().Add(-time.Minute).Unix()})
	if _, ok := a.verifyState(expired); ok {
		t.Fatal("expired state accepted")
	}
	other := &App{stateKey: deriveKey([]byte(strings.Repeat("x", 32)), "p")}
	if _, ok := other.verifyState(v); ok {
		t.Fatal("state signed with another key accepted")
	}
}

func TestSafeNext(t *testing.T) {
	for next, want := range map[string]bool{
		"/a": true, "/a?b=c": true, "": false, "a": false, "//evil.com": false,
		"/\\evil.com": false, "https://evil.com": false, "/a\nb": false,
	} {
		if safeNext(next) != want {
			t.Errorf("safeNext(%q) != %v", next, want)
		}
	}
}
