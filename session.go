package ekman

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/sakib412/ekman/auth"
	"github.com/sakib412/ekman/internal/testhook"
	"github.com/sakib412/ekman/rbac"
)

const (
	sessionCookie = "ekman_session"
	hintCookie    = "ekman_hint"
	oauthCookie   = "ekman_oauth"
)

// sessionMiddleware resolves the Caller: from ekmantest's internal hook, else from the session
// cookie, roles from Grants and permissions from the registry.
func (a *App) sessionMiddleware(reg *rbac.Registry) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			var c Caller
			if id, ok := testhook.From(ctx); ok {
				if id.UserID != "" {
					c = Caller{userID: id.UserID, perms: reg.Expand(append(reg.Baseline(), id.Perms...))}
				}
			} else if a.store != nil {
				var err error
				if c, err = a.callerFromSession(w, r, reg); err != nil {
					WriteError(w, r, err)
					return
				}
			}
			next.ServeHTTP(w, r.WithContext(withCaller(ctx, c)))
		})
	}
}

func (a *App) callerFromSession(w http.ResponseWriter, r *http.Request, reg *rbac.Registry) (Caller, error) {
	ck, err := r.Cookie(sessionCookie)
	if err != nil || ck.Value == "" {
		return Caller{}, nil
	}
	ctx := r.Context()
	hash := hashToken(ck.Value)
	sess, err := a.store.Get(ctx, hash)
	if errors.Is(err, auth.ErrNoSession) {
		a.clearSessionCookies(w, r)
		return Caller{}, nil
	}
	if err != nil {
		return Caller{}, err
	}
	ttl := a.opts.Auth.TTL
	now := time.Now()
	// Slide the expiry at most once per tenth of the TTL to keep writes off the hot path.
	if sess.ExpiresAt.Sub(now) < ttl-ttl/10 {
		exp := now.Add(ttl)
		if err := a.store.Touch(ctx, hash, exp); err != nil {
			return Caller{}, err
		}
		a.setSessionCookies(w, r, ck.Value, exp)
	}
	var roles []string
	if a.opts.Grants != nil {
		names, err := a.opts.Grants.RolesOf(ctx, sess.UserID)
		if err != nil {
			return Caller{}, err
		}
		for _, n := range names {
			if _, ok := reg.Role(n); ok {
				roles = append(roles, n)
			} else {
				a.log.WarnContext(ctx, "ekman: unknown role from grant source ignored", "role", n, "user_id", sess.UserID, "request_id", RequestID(ctx))
			}
		}
		slices.Sort(roles)
		roles = slices.Compact(roles)
	}
	return Caller{userID: sess.UserID, perms: reg.Resolve(roles), roles: roles}, nil
}

func hashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

func randToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// secure decides the Secure cookie flag with auth.IsLocalOrigin: off only for local
// development origins (from PublicURL when set, else from the request's Host).
func (a *App) secure(r *http.Request) bool {
	if a.opts.Auth != nil && a.opts.Auth.PublicURL != "" {
		return !auth.IsLocalOrigin(a.opts.Auth.PublicURL)
	}
	return !auth.IsLocalOrigin("http://" + r.Host)
}

func (a *App) setSessionCookies(w http.ResponseWriter, r *http.Request, token string, exp time.Time) {
	maxAge := int(time.Until(exp).Seconds())
	secure := a.secure(r)
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", MaxAge: maxAge, Expires: exp,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
	// The hint carries no secret: it only tells static frontends a session probably exists.
	http.SetCookie(w, &http.Cookie{
		Name: hintCookie, Value: "1", Path: "/", MaxAge: maxAge, Expires: exp,
		Secure: secure, SameSite: http.SameSiteLaxMode,
	})
}

func (a *App) clearSessionCookies(w http.ResponseWriter, r *http.Request) {
	secure := a.secure(r)
	for _, name := range []string{sessionCookie, hintCookie} {
		http.SetCookie(w, &http.Cookie{
			Name: name, Path: "/", MaxAge: -1, HttpOnly: name == sessionCookie,
			Secure: secure, SameSite: http.SameSiteLaxMode,
		})
	}
}
