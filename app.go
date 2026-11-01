// Package ekman is an API-only Go framework: typed JSON handlers on net/http, explicit access
// rules on every route, declared permissions, Postgres migrations per module, OAuth sign-in and
// a derived OpenAPI 3.1 document.
//
// Like the Ekman spiral, each layer of your API is turned by the one above it:
// request → middleware → typed handler → database.
package ekman

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sakib412/ekman/auth"
	"github.com/sakib412/ekman/db"
	"github.com/sakib412/ekman/rbac"
)

// Options configures an App.
type Options struct {
	DB        *db.DB            // required when any module has Migrations, or Auth uses the Postgres store
	Auth      *auth.Config      // nil: SignedIn/Requires routes fail at boot
	Roles     []rbac.Role       // role definitions; permissions must be declared by modules
	Baseline  []rbac.Permission // held by every signed-in user (e.g. "profile.edit_own")
	Grants    rbac.GrantSource  // user id -> role names; nil: only Baseline applies
	APIPrefix string            // default "/api/v1"; "/" mounts at the root; auth routes mount under it too
	Addr      string            // default ":" + $PORT, else ":8080"
	CORS      []string          // allowed origins for credentialed requests; empty = same-origin only
	Logger    *slog.Logger      // default JSON to stderr
	Info      Info              // OpenAPI title/version

	// OpenAPIHook edits the generated OpenAPI document once, at boot, before it is served or
	// returned by App.OpenAPI: add servers, x- extensions, examples. doc is plain decoded JSON
	// (map[string]any, []any, string, float64, bool); an error fails the boot.
	OpenAPIHook func(doc map[string]any) error
}

// Info is the OpenAPI document's info object.
type Info struct {
	Title       string // default "ekman API"
	Version     string // default "0.0.0"
	Description string
}

// Module is the one way to add a feature: its permissions, migrations and routes.
type Module struct {
	Name        string            // unique; namespaces migrations
	Migrations  fs.FS             // NNNN_name.sql, forward-only, applied in Register order
	Permissions []rbac.Permission // every permission this module's routes or code checks
	Routes      func(r *Router)
}

// internalModule is the name ekman uses for its own routes and migrations.
const internalModule = "ekman"

// App is an ekman application.
type App struct {
	opts     Options
	log      *slog.Logger
	stateKey []byte // HMAC key for the OAuth state cookie
	store    auth.Store

	mu    sync.Mutex
	mods  []Module
	appMW []Middleware
	built bool
	boot  *boot
	err   error
}

// New validates opts and returns an App. Modules, routes and permissions are validated later,
// once, by Handler (or Main, Migrate, OpenAPI).
func New(opts Options) (*App, error) {
	a := &App{opts: opts, log: opts.Logger}
	if a.log == nil {
		a.log = slog.New(slog.NewJSONHandler(os.Stderr, nil))
	}
	var errs []error
	prefix, err := normalizePrefix(opts.APIPrefix)
	if err != nil {
		errs = append(errs, err)
	}
	a.opts.APIPrefix = prefix
	if a.opts.Addr == "" {
		a.opts.Addr = ":8080"
		if port := os.Getenv("PORT"); port != "" {
			a.opts.Addr = ":" + port
		}
	}
	for _, o := range opts.CORS {
		u, err := url.Parse(o)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Path != "" || u.RawQuery != "" {
			errs = append(errs, fmt.Errorf("CORS origin %q must look like https://app.example.com (no path, no wildcard)", o))
		}
	}
	if a.opts.Info.Title == "" {
		a.opts.Info.Title = "ekman API"
	}
	if a.opts.Info.Version == "" {
		a.opts.Info.Version = "0.0.0"
	}
	if opts.Auth != nil {
		errs = append(errs, a.configureAuth()...)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return a, nil
}

func normalizePrefix(p string) (string, error) {
	switch p {
	case "":
		return "/api/v1", nil
	case "/":
		return "", nil
	}
	if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, " {}") {
		return "", fmt.Errorf("APIPrefix %q must start with / and contain no wildcards", p)
	}
	return strings.TrimSuffix(p, "/"), nil
}

func (a *App) configureAuth() []error {
	c := *a.opts.Auth
	var errs []error
	if c.TTL == 0 {
		c.TTL = 30 * 24 * time.Hour
	}
	if c.AfterLogin == "" {
		c.AfterLogin = "/"
	}
	if c.PublicURL != "" {
		u, err := url.Parse(c.PublicURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, fmt.Errorf("auth: PublicURL %q must be an absolute http(s) URL", c.PublicURL))
		}
		c.PublicURL = strings.TrimSuffix(c.PublicURL, "/")
	}
	if len(c.Providers) > 0 && c.Directory == nil {
		errs = append(errs, errors.New("auth: Directory is required when Providers are configured"))
	}
	names := map[string]bool{}
	for _, p := range c.Providers {
		switch {
		case p == nil || p.Name() == "":
			errs = append(errs, errors.New("auth: provider without a name"))
		case names[p.Name()]:
			errs = append(errs, fmt.Errorf("auth: provider %q configured twice", p.Name()))
		}
		if p != nil {
			names[p.Name()] = true
		}
	}
	a.store = c.Store
	if a.store == nil {
		if a.opts.DB == nil {
			errs = append(errs, errors.New("auth: Options.DB is required for the default Postgres session store (or set Auth.Store)"))
		} else {
			a.store = auth.PostgresStore(a.opts.DB)
		}
	}
	secret := c.Secret
	if len(secret) == 0 {
		secret = []byte(os.Getenv("EKMAN_SECRET"))
	}
	switch {
	case len(secret) == 0:
		secret = []byte(newID() + newID() + newID())
		if len(c.Providers) > 0 {
			a.log.Warn("ekman: no auth secret (auth.Config.Secret or EKMAN_SECRET); using a random per-process key, so sign-ins in flight break across restarts and instances")
		}
	case len(secret) < 32:
		errs = append(errs, errors.New("auth: Secret (or EKMAN_SECRET) must be at least 32 bytes"))
	}
	a.stateKey = deriveKey(secret, "ekman/oauth-state/v1")
	a.opts.Auth = &c
	return errs
}

// Register adds modules. It must be called before Handler, Main, Migrate or OpenAPI; it panics
// afterwards because the route table is already frozen.
func (a *App) Register(mods ...Module) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.built {
		panic("ekman: Register called after the app was built")
	}
	a.mods = append(a.mods, mods...)
}

// Use adds app middleware, outermost first. It runs inside ekman's stack: after the session is
// resolved and the route matched (so CallerFrom and RouteFrom work), before route middleware.
func (a *App) Use(mw ...Middleware) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.built {
		panic("ekman: Use called after the app was built")
	}
	a.appMW = append(a.appMW, mw...)
}

// Handler validates modules, routes, roles and permissions once and returns the app's handler.
func (a *App) Handler() (http.Handler, error) {
	b, err := a.build()
	if err != nil {
		return nil, err
	}
	return b.handler, nil
}

// Routes returns every registered route, sorted by path then method. It returns nil when the
// app fails validation.
func (a *App) Routes() []RouteInfo {
	b, err := a.build()
	if err != nil {
		return nil
	}
	out := make([]RouteInfo, len(b.routes))
	for i, r := range b.routes {
		out[i] = r.info.clone()
	}
	return out
}

// OpenAPI returns the OpenAPI 3.1 document for the registered routes.
func (a *App) OpenAPI() ([]byte, error) {
	b, err := a.build()
	if err != nil {
		return nil, err
	}
	return slices.Clone(b.openapi), nil
}

// Migrate applies pending migrations: ekman's own (when the Postgres session store is used),
// then each module's in Register order.
func (a *App) Migrate(ctx context.Context) error {
	b, err := a.build()
	if err != nil {
		return err
	}
	if a.opts.DB == nil {
		if len(b.sources) == 0 {
			return nil
		}
		return errors.New("ekman: Options.DB is required to migrate")
	}
	return db.Migrate(ctx, a.opts.DB, b.sources)
}
