// Package auth configures sign-in for an ekman app: OAuth providers, the directory that maps a
// verified identity to an app user, and the session store. ekman mounts the HTTP routes.
package auth

import (
	"context"
	"time"
)

// Config enables sign-in. Pass it as ekman.Options.Auth.
type Config struct {
	Providers []Provider
	Directory Directory     // required when Providers is non-empty
	Store     Store         // nil: PostgresStore over Options.DB (ekman applies its migration)
	TTL       time.Duration // session lifetime, sliding; default 30 days
	// PublicURL is the browser-facing origin of the API (e.g. "https://api.example.com"). It
	// builds OAuth redirect URLs and decides the Secure cookie flag. Empty: derived per request.
	PublicURL string
	// AfterLogin is where the callback redirects; default "/". When absolute, a ?next=/path
	// given to /start keeps AfterLogin's origin and replaces its path.
	AfterLogin string
	// Secret keys the HMAC that signs the short-lived OAuth state cookie. At least 32 bytes.
	// Empty: $EKMAN_SECRET; if that is also empty, a random per-process key is used and a
	// warning logged (sign-ins then break across restarts and multiple instances).
	Secret []byte
}

// Identity is what a provider verified about the person signing in.
type Identity struct {
	Provider      string
	Subject       string // stable provider user id
	Email         string
	EmailVerified bool
	Name          string
	AvatarURL     string
}

// Directory maps a verified identity to an app user id. To refuse a sign-in, return an error;
// an *ekman.Error (for example ekman.Forbidden) is sent to the browser as is.
type Directory interface {
	UserFor(ctx context.Context, id Identity) (userID string, err error)
}

// DirectoryFunc adapts a function to Directory.
type DirectoryFunc func(ctx context.Context, id Identity) (string, error)

// UserFor calls f.
func (f DirectoryFunc) UserFor(ctx context.Context, id Identity) (string, error) { return f(ctx, id) }

// Provider is an OAuth 2 identity provider using the authorization-code flow with PKCE.
type Provider interface {
	// Name is the URL segment: /auth/{name}/start.
	Name() string
	// AuthCodeURL returns the provider's consent URL for state and the PKCE verifier.
	AuthCodeURL(state, verifier, redirectURL string) string
	// Exchange trades the code for tokens and returns the verified identity.
	Exchange(ctx context.Context, code, verifier, redirectURL string) (Identity, error)
}
