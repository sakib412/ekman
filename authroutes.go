package ekman

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sakib412/ekman/auth"
	"github.com/sakib412/ekman/rbac"
)

const oauthStateTTL = 10 * time.Minute

func (a *App) authRoutes(r *Router) {
	r.Raw("GET /auth/{provider}/start", Public(), http.HandlerFunc(a.authStart),
		Summary("Start OAuth sign-in; redirects to the provider. Optional ?next=/path."), Status(http.StatusFound))
	r.Raw("GET /auth/{provider}/callback", Public(), http.HandlerFunc(a.authCallback),
		Summary("OAuth callback; sets the session cookie and redirects."), Status(http.StatusFound))
	r.Raw("POST /auth/logout", Public(), http.HandlerFunc(a.logout),
		Summary("Sign out and clear the session cookies."), Status(http.StatusNoContent))
	Handle(r, "GET /auth/me", Public(), a.me, Summary("The current caller; userId is empty when anonymous."))
}

// Me is the response of GET /auth/me.
type Me struct {
	UserID      string            `json:"userId"`
	Permissions []rbac.Permission `json:"permissions"`
	Roles       []string          `json:"roles"`
}

func (a *App) me(_ context.Context, c Caller, _ *struct{}) (Me, error) {
	m := Me{UserID: c.UserID(), Permissions: c.Permissions(), Roles: c.roleNames()}
	if m.Permissions == nil {
		m.Permissions = []rbac.Permission{}
	}
	if m.Roles == nil {
		m.Roles = []string{}
	}
	return m, nil
}

func (a *App) provider(name string) auth.Provider {
	for _, p := range a.opts.Auth.Providers {
		if p.Name() == name {
			return p
		}
	}
	return nil
}

type oauthState struct {
	Provider string `json:"p"`
	State    string `json:"s"`
	Verifier string `json:"v"`
	Next     string `json:"n,omitempty"`
	Expires  int64  `json:"e"`
}

func deriveKey(secret []byte, purpose string) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(purpose))
	return m.Sum(nil)
}

func (a *App) signState(s oauthState) string {
	payload, _ := json.Marshal(s)
	p := base64.RawURLEncoding.EncodeToString(payload)
	m := hmac.New(sha256.New, a.stateKey)
	m.Write([]byte(p))
	return p + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (a *App) verifyState(v string) (oauthState, bool) {
	p, sig, ok := strings.Cut(v, ".")
	if !ok {
		return oauthState{}, false
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return oauthState{}, false
	}
	m := hmac.New(sha256.New, a.stateKey)
	m.Write([]byte(p))
	if !hmac.Equal(got, m.Sum(nil)) {
		return oauthState{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(p)
	if err != nil {
		return oauthState{}, false
	}
	var s oauthState
	if json.Unmarshal(payload, &s) != nil || time.Now().Unix() > s.Expires {
		return oauthState{}, false
	}
	return s, true
}

func (a *App) redirectURL(r *http.Request, provider string) string {
	base := a.opts.Auth.PublicURL
	if base == "" {
		scheme := "http"
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	return base + a.opts.APIPrefix + "/auth/" + provider + "/callback"
}

// safeNext accepts only same-origin absolute paths, so ?next= cannot become an open redirect.
func safeNext(next string) bool {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return false
	}
	if strings.ContainsFunc(next, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return false
	}
	u, err := url.Parse(next)
	return err == nil && u.Scheme == "" && u.Host == ""
}

func (a *App) afterLogin(next string) string {
	base := a.opts.Auth.AfterLogin
	if next == "" {
		return base
	}
	u, err := url.Parse(base)
	if err != nil || !u.IsAbs() {
		return next
	}
	n, _ := url.Parse(next)
	u.Path, u.RawPath, u.RawQuery, u.Fragment = n.Path, n.RawPath, n.RawQuery, n.Fragment
	return u.String()
}

func (a *App) authStart(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("provider")
	p := a.provider(name)
	if p == nil {
		WriteError(w, r, NotFound("unknown sign-in provider"))
		return
	}
	next := r.URL.Query().Get("next")
	if !safeNext(next) {
		next = ""
	}
	st := oauthState{Provider: name, State: randToken(), Verifier: randToken(), Next: next, Expires: time.Now().Add(oauthStateTTL).Unix()}
	http.SetCookie(w, &http.Cookie{
		Name: oauthCookie, Value: a.signState(st), Path: a.opts.APIPrefix + "/auth/",
		MaxAge: int(oauthStateTTL.Seconds()), HttpOnly: true, Secure: a.secure(r), SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, p.AuthCodeURL(st.State, st.Verifier, a.redirectURL(r, name)), http.StatusFound)
}

func (a *App) authCallback(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("provider")
	p := a.provider(name)
	if p == nil {
		WriteError(w, r, NotFound("unknown sign-in provider"))
		return
	}
	ck, err := r.Cookie(oauthCookie)
	http.SetCookie(w, &http.Cookie{Name: oauthCookie, Path: a.opts.APIPrefix + "/auth/", MaxAge: -1, HttpOnly: true, Secure: a.secure(r), SameSite: http.SameSiteLaxMode})
	invalid := BadRequest("invalid_state", "sign-in expired or was started in another browser; try again")
	if err != nil {
		WriteError(w, r, invalid)
		return
	}
	st, ok := a.verifyState(ck.Value)
	q := r.URL.Query()
	if !ok || st.Provider != name || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(st.State)) != 1 {
		WriteError(w, r, invalid)
		return
	}
	if e := q.Get("error"); e != "" {
		WriteError(w, r, &Error{Status: http.StatusUnauthorized, Code: "oauth_denied", Message: "the provider did not sign you in: " + e})
		return
	}
	code := q.Get("code")
	if code == "" {
		WriteError(w, r, BadRequest("invalid_callback", "missing code"))
		return
	}
	ctx := r.Context()
	id, err := p.Exchange(ctx, code, st.Verifier, a.redirectURL(r, name))
	if err != nil {
		a.log.WarnContext(ctx, "ekman: oauth exchange failed", "provider", name, "err", err.Error(), "request_id", RequestID(ctx))
		WriteError(w, r, &Error{Status: http.StatusBadGateway, Code: "oauth_failed", Message: "could not verify the sign-in with the provider"})
		return
	}
	userID, err := a.opts.Auth.Directory.UserFor(ctx, id)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	if userID == "" {
		WriteError(w, r, Forbidden("sign-in refused"))
		return
	}
	// A fresh token on every sign-in; drop any session the browser already had (fixation).
	if old, err := r.Cookie(sessionCookie); err == nil && old.Value != "" {
		_ = a.store.Delete(ctx, hashToken(old.Value))
	}
	token := randToken()
	now := time.Now()
	sess := auth.Session{TokenHash: hashToken(token), UserID: userID, CreatedAt: now, ExpiresAt: now.Add(a.opts.Auth.TTL)}
	if err := a.store.Create(ctx, sess); err != nil {
		WriteError(w, r, err)
		return
	}
	a.setSessionCookies(w, r, token, sess.ExpiresAt)
	http.Redirect(w, r, a.afterLogin(st.Next), http.StatusFound)
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	if ck, err := r.Cookie(sessionCookie); err == nil && ck.Value != "" {
		if err := a.store.Delete(r.Context(), hashToken(ck.Value)); err != nil {
			WriteError(w, r, err)
			return
		}
	}
	a.clearSessionCookies(w, r)
	w.WriteHeader(http.StatusNoContent)
}
