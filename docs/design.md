# ekman design (v0.1 contract)

ekman is an API-only, batteries-included Go framework. Like the Ekman spiral, each layer of
an API is turned by the one above it: request → middleware → typed handler → database.

This file is the contract for v0.1. Code implements it. When code needs to deviate, change this
file in the same commit and say why.

## Principles

1. **net/http all the way down.** `App.Handler()` is an `http.Handler`; middleware is
   `func(http.Handler) http.Handler`; any stdlib handler mounts with `Router.Raw`.
2. **No globals.** Everything hangs off `*App`. Context-first everywhere.
3. **Every route states its access.** Access is a required positional argument, not an option:
   `ekman.Public()`, `ekman.SignedIn()`, or `ekman.Requires(perm)`. Nothing defaults open; the
   zero `Access{}` fails at boot.
4. **Declared permissions only.** A route or role naming a permission no module declared fails
   at boot (`App.Handler`/`App.Main`). A typo can't reach production.
5. **One way to add a feature:** write a `Module`, pass it to `App.Register`.
6. **Validation at the boundary, plain Go.** Input types implement `Validate(v *ekman.Validation)`.
   No reflection tags for rules.
7. **One error shape on the wire**:
   `{"error":{"code":"not_found","message":"...","fields":{...},"requestId":"..."}}`. This
   includes ekman's own 404/405/413/415 answers.
8. **OpenAPI 3.1 is derived** from registered routes and their Go types. Clients generate from it
   (e.g. `openapi-typescript`). No custom codegen.
9. **Zero dependencies.** The module's `go.mod` has no `require` lines; everything is stdlib,
   tests included. The database layer is `database/sql`: the *application* imports a driver
   (e.g. `github.com/jackc/pgx/v5/stdlib`, driver name `"pgx"`) and ekman never does. OAuth 2
   (authorization code + PKCE) is implemented on `net/http`. Integration tests that need a real
   driver live in separate nested modules (`internal/dbtest`, `examples/todo`).
10. **v0.1 targets PostgreSQL.** Migrations take a Postgres advisory lock and the bundled SQL
    uses `$n` placeholders, `bytea` and `timestamptz`.

## Packages

```
github.com/sakib412/ekman            App, Options, Module, Router, Handle, Access, Caller,
                                     Error, Validation, RouteInfo, OpenAPI, Main
github.com/sakib412/ekman/db         Open, New, DB.Q, DB.Tx, AfterCommit, Migrate (forward-only, per module)
github.com/sakib412/ekman/rbac       Permission, Role, GrantSource, Set, Registry
github.com/sakib412/ekman/auth       Config, Provider (Google, GitHub, OAuth2), Directory, Store (Postgres, Memory)
github.com/sakib412/ekman/mail       Sender, Message; drivers: Log, Brevo (HTTPS API)
github.com/sakib412/ekman/storage    Bucket, Receive (safe multipart upload); driver: Dir
github.com/sakib412/ekman/ekmantest  test app, as-user requests, schema-per-test DB
```

## Usage (the README a stranger reads)

```go
package main

import _ "github.com/jackc/pgx/v5/stdlib" // you bring the driver

func main() {
	database, err := db.Open("pgx", os.Getenv("DATABASE_URL"))
	if err != nil { log.Fatal(err) }

	todos := todo.New(database)

	app, err := ekman.New(ekman.Options{
		DB:     database,
		Roles:  []rbac.Role{{Name: "admin", Permissions: []rbac.Permission{rbac.All}}},
		Grants: myGrants,                        // rbac.GrantSource: user id -> role names
		Auth: &auth.Config{
			Providers: []auth.Provider{auth.Google(id, secret)},
			Directory: myDirectory,              // verified identity -> user id (or refuse)
		},
	})
	if err != nil { log.Fatal(err) }
	app.Register(todos.Module())
	app.Main() // serve (default) | migrate | openapi
}
```

```go
package todo

//go:embed migrations/*.sql
var migrations embed.FS

const Manage rbac.Permission = "todo.manage"

func (s *Service) Module() ekman.Module {
	return ekman.Module{
		Name:        "todo",
		Migrations:  migrations,
		Permissions: []rbac.Permission{Manage},
		Routes: func(r *ekman.Router) {
			ekman.Handle(r, "GET /todos", ekman.Public(), s.list)
			ekman.Handle(r, "POST /todos", ekman.Requires(Manage), s.create)
			ekman.Handle(r, "PUT /todos/{id}/done", ekman.SignedIn(), s.markDone)
		},
	}
}

type CreateIn struct {
	Title string `json:"title"`
}

func (in *CreateIn) Validate(v *ekman.Validation) {
	v.Required("title", in.Title)
	v.MaxLen("title", in.Title, 200)
}

func (s *Service) create(ctx context.Context, c ekman.Caller, in *CreateIn) (Todo, error) {
	return s.insert(ctx, c.UserID(), in.Title)
}

type DoneIn struct {
	ID int64 `path:"id"`
}

func (s *Service) markDone(ctx context.Context, c ekman.Caller, in *DoneIn) (ekman.NoContent, error) {
	t, err := s.get(ctx, in.ID)
	if err != nil { return ekman.NoContent{}, err }
	if err := c.RequireSelfOr(t.OwnerID, Manage); err != nil { return ekman.NoContent{}, err }
	return ekman.NoContent{}, s.setDone(ctx, in.ID)
}
```

## Root package API

```go
type Options struct {
	DB        *db.DB            // required when any module has Migrations, or Auth uses the Postgres store
	Auth      *auth.Config      // nil: SignedIn/Requires routes fail at boot
	Roles     []rbac.Role
	Baseline  []rbac.Permission // held by every signed-in user (e.g. "profile.edit_own")
	Grants    rbac.GrantSource  // nil: only Baseline applies
	APIPrefix string            // default "/api/v1"; "/" = mount at the root; auth routes mount under it too
	Addr      string            // default ":" + $PORT, else ":8080"
	CORS      []string          // allowed origins for credentialed requests; empty = same-origin only
	Logger    *slog.Logger      // default JSON to stderr
	Info      Info              // OpenAPI title/version/description
}

func New(opts Options) (*App, error)          // validates Options (prefix, CORS origins, auth config)
func (a *App) Register(mods ...Module)        // before Handler/Main; panics after
func (a *App) Use(mw ...Middleware)           // app middleware, outermost first, inside ekman's stack
func (a *App) Handler() (http.Handler, error) // validates everything once, builds the stack
func (a *App) Migrate(ctx context.Context) error
func (a *App) OpenAPI() ([]byte, error)
func (a *App) Routes() []RouteInfo            // sorted by Pattern, then Method
func (a *App) Main()                          // serve | migrate | openapi; SIGINT/SIGTERM graceful (30s)

type Module struct {
	Name        string            // unique; namespaces migrations; "ekman" is reserved
	Migrations  fs.FS             // NNNN_name.sql, forward-only, applied in Register order
	Permissions []rbac.Permission // every permission this module's routes or code checks
	Routes      func(r *Router)
}

type Middleware = func(http.Handler) http.Handler

type Router struct{ /* unexported */ }
func (r *Router) Group(prefix string, mw ...Middleware) *Router
func (r *Router) Raw(pattern string, access Access, h http.Handler, opts ...RouteOption) // method optional

// Handle registers a typed JSON endpoint. pattern is a Go 1.22 ServeMux pattern relative to
// APIPrefix, with a method ("GET /events/{slug}"). In binds from `path:"x"`, `query:"x"`,
// `header:"X"` tags (scalars, pointers for optional values, encoding.TextUnmarshaler such as
// time.Time, and slices for repeated query values); remaining exported fields decode from the
// JSON body (not on GET/HEAD; at most 1 MiB; an empty body leaves them zero). If *In implements
// Validator, it runs before h. Out encodes as JSON: 200, or 201 for POST, or 204 for NoContent,
// or the Status option. A nil slice Out encodes as [].
//
// An exported In field of type *http.Request receives the request, and ekman leaves the body
// unread for the handler (multipart uploads via storage.Receive, other non-JSON bodies). Such an
// In has no JSON body fields (boot error otherwise: bind the rest from path/query/header or tag
// them `json:"-"`), so the body is never consumed twice. Its body is undocumented unless the
// route uses Accepts. Prefer this to Raw whenever the answer is JSON: params, Validate, Caller
// and encoding stay typed. Use Raw when the handler writes the response itself (CSV,
// streaming, redirects).
func Handle[In, Out any](r *Router, pattern string, access Access, h func(ctx context.Context, c Caller, in *In) (Out, error), opts ...RouteOption)

type Access struct{ /* unexported */ }
func Public() Access                    // Caller may be anonymous
func SignedIn() Access                  // any session; 401 otherwise
func Requires(p rbac.Permission) Access // 401 anonymous, 403 missing permission; rbac.All is rejected at boot

type Caller struct{ /* unexported; built by ekman */ }
func (c Caller) Anonymous() bool
func (c Caller) UserID() string
func (c Caller) Can(p rbac.Permission) bool
func (c Caller) Permissions() []rbac.Permission
func (c Caller) Require(p rbac.Permission) error
func (c Caller) RequireSelfOr(owner string, p rbac.Permission) error
func CallerFrom(ctx context.Context) Caller // for middleware and Raw handlers

type RouteOption func(*RouteInfo)
func Summary(s string) RouteOption
func Status(code int) RouteOption
func Tag(t string) RouteOption           // free-form metadata app middleware reads
func Use(mw ...Middleware) RouteOption   // per-route middleware
// Accepts documents a request body the handler reads itself (Raw routes, and Handle routes whose
// In has a *http.Request field; boot error elsewhere). Produces documents the success body a Raw
// handler writes, at Status (default 200); boot error on Handle routes. Repeat per media type;
// invalid or repeated media types and method-less Raw patterns fail at boot.
func Accepts[T any](mediaType string) RouteOption // e.g. Accepts[AvatarForm]("multipart/form-data")
func Produces[T any](mediaType string) RouteOption // e.g. Produces[string]("text/csv")

type Content struct {
	MediaType string
	Type      reflect.Type // string → {"type":"string"}; File → binary; structs → object schema
}
// File stands for binary data in Accepts/Produces (a multipart file field, a download):
// {"type":"string","format":"binary"}. Documentation only.
type File struct{}

type RouteInfo struct {
	Method, Pattern, Module, Summary string // Pattern is the full path incl. APIPrefix
	Access                          string // "public" | "signed_in" | permission
	Tags                            []string
	Status                          int    // resolved success status (0 on Raw routes without Status)
	In, Out                         reflect.Type // nil on Raw routes
	Accepts, Produces               []Content    // from the Accepts/Produces options
}
func (ri RouteInfo) HasTag(t string) bool
func RouteFrom(ctx context.Context) (RouteInfo, bool)

type NoContent struct{}

// Enum is implemented by named string types with a fixed value set (kind, status, tier...).
// Each is a components schema pkg.Type with `enum`, so openapi-typescript emits a union.
// Handle rejects other values: 400 invalid_param for path/query/header parameters, 422
// invalid_input for JSON body fields at any depth (field keys like "items[0].kind"), merged
// with Validate's messages. "" passes, like OneOf. EnumValues is called once, at boot.
type Enum interface{ EnumValues() []string }

type Validator interface{ Validate(v *Validation) }
type Validation struct{ /* field -> message; first message per field wins */ }
func (v *Validation) Check(ok bool, field, msg string)
func (v *Validation) Required(field, value string)
func (v *Validation) MaxLen(field, value string, n int)        // runes
func (v *Validation) OneOf(field, value string, allowed ...string) // "" passes
func (v *Validation) Email(field, value string)                // "" passes
func (v *Validation) URL(field, value string)                  // http(s) only; "" passes
func (v *Validation) Valid() bool
func (v *Validation) Fields() map[string]string
func (v *Validation) Err() error // 422 "invalid_input" with Fields, or nil

type Error struct {
	Status  int
	Code    string
	Message string
	Fields  map[string]string
}
func NotFound(msg string) *Error
func BadRequest(code, msg string) *Error
func Unauthorized(msg string) *Error
func Forbidden(msg string) *Error
func Conflict(code, msg string) *Error
func Unprocessable(code, msg string) *Error
// Any other error becomes a logged 500 with a generic message. sql.ErrNoRows maps to 404;
// *http.MaxBytesError to 413.

// For Raw handlers:
func WriteError(w http.ResponseWriter, r *http.Request, err error)
func WriteJSON(w http.ResponseWriter, status int, v any)
func RequestID(ctx context.Context) string

type Me struct{ UserID string; Permissions []rbac.Permission; Roles []string } // GET /auth/me body
```

Binding errors: 400 `invalid_param` (with `fields`), 400 `invalid_json`, 413 `too_large`,
415 `unsupported_media_type`. Validation: 422 `invalid_input`. Unmatched: 404 `not_found`,
405 `method_not_allowed` (with `Allow`).

Stack (outer → inner): request id → recover → access log → CORS → **route match** → session →
app `Use` → group middleware → route middleware → access check → bind → validate → handler →
encode. The route is matched before the session so app middleware can call `RouteFrom`, and
unmatched requests never touch the session store.

- Request id: `X-Request-Id` is accepted when it is ≤64 safe characters, else generated; it is
  echoed in the response header and every error body.
- CORS: only exact origins in `Options.CORS` get credentialed CORS headers; preflights from other
  origins get 403. State-changing requests whose `Origin` is neither allowed nor the request's
  own host get 403 (CSRF backstop on top of SameSite=Lax).

Always mounted: `GET /healthz` (no DB, no session), `GET {prefix}/openapi.json` (not listed in
the document itself).

Boot validation (`Handler`, `Main`, `Migrate`, `OpenAPI`; all errors reported together):
duplicate or reserved module names, bad migration file names, migrations without `DB`, duplicate
or conflicting routes, zero `Access`, unknown methods, path tags without a matching wildcard,
unbindable field types, a `*http.Request` field next to JSON body fields (or twice, or tagged),
undeclared permissions in routes/roles/baseline, `Requires(rbac.All)`,
SignedIn/Requires routes without `Auth`, `Enum` types with no or duplicate values.

OpenAPI: one operation per route (`operationId` like `getTodosById`, tagged with the module
name, `x-ekman-access`), named structs as `components.schemas` keyed `pkg.Type`, pointers
nullable (`type: [T, "null"]` or `anyOf` with null), `time.Time` as `date-time`, json tags
honored (`omitempty`/`omitzero` → optional, `,string`, `-`), request bodies exclude bound
fields (`pkg.TypeBody`), `Enum` types as named string schemas with `enum` (and a 422 response
on operations whose body holds one), a shared `Error` schema/response, Raw routes documented by
`Accepts`/`Produces` (otherwise path wildcards and the status only), a `Permission` string enum
of every declared permission, and a `session` cookie security scheme on SignedIn/Requires operations.
Output is deterministic.

## db

```go
func Open(driverName, dsn string) (*DB, error) // sql.Open; lazy like database/sql
func New(pool *sql.DB) *DB
func (d *DB) Close() error
func (d *DB) SQL() *sql.DB
// Q returns the transaction in ctx if any, else the pool. sqlc-compatible (DBTX).
func (d *DB) Q(ctx context.Context) Querier
// Tx runs fn in a transaction carried by ctx. Nested Tx calls join the outer one.
// An error or panic rolls back.
func (d *DB) Tx(ctx context.Context, fn func(ctx context.Context) error) error
// AfterCommit runs fn after the surrounding Tx commits (immediately when no Tx; never on rollback).
func AfterCommit(ctx context.Context, fn func(ctx context.Context))
type Querier interface { ExecContext; PrepareContext; QueryContext; QueryRowContext } // database/sql signatures
type Source struct{ Module string; FS fs.FS }
type Migration struct{ Module, File string; Version int; SQL, Checksum string }
// Load reads NNNN_name.sql files at the FS root or inside the lone subdirectory an embed
// pattern like "migrations/*.sql" yields; non-.sql files are ignored.
func Load(src Source) ([]Migration, error)
// Migrate applies pending files under a Postgres advisory lock, each file in its own
// transaction, records (module, file, sha256) in ekman_migrations, and fails if an applied
// file's checksum changed.
func Migrate(ctx context.Context, d *DB, sources []Source) error
```

## rbac

```go
type Permission string
const All Permission = "*" // only inside Role.Permissions
type Role struct{ Name, Title string; Permissions []Permission }
type GrantSource interface{ RolesOf(ctx context.Context, userID string) ([]string, error) }
type GrantFunc func(ctx context.Context, userID string) ([]string, error)
type Set struct{ /* immutable */ }
func NewSet(perms ...Permission) Set
func (s Set) Has(p Permission) bool
func (s Set) List() []Permission // sorted copy
func (s Set) Len() int
type Registry struct{ /* declared perms, roles, baseline */ }
func NewRegistry(declared []Permission, roles []Role, baseline []Permission) (*Registry, error)
func (r *Registry) Resolve(roleNames []string) Set // baseline + roles; All expands to every declared permission; unknown names ignored
func (r *Registry) Expand(perms []Permission) Set  // All → every declared permission
func (r *Registry) Role(name string) (Role, bool)
func (r *Registry) Declared(p Permission) bool
func (r *Registry) Permissions() []Permission
func (r *Registry) Baseline() []Permission
func (r *Registry) Roles() []Role
```
Unknown role names returned by a GrantSource are ignored and logged by ekman's session layer.

## auth

Mounted under APIPrefix: `GET /auth/{provider}/start?next=/path`, `GET /auth/{provider}/callback`,
`POST /auth/logout`, `GET /auth/me` → `{userId, permissions, roles}` (Public; anonymous → `userId: ""`).
Failures (bad state, provider error, Directory refusal) answer in the JSON error shape. `next`
must be a same-origin path (`/x`, never `//x`); the callback redirects to `AfterLogin`, and when
`AfterLogin` is absolute, to its origin with `next`'s path.

```go
type Config struct {
	Providers  []Provider
	Directory  Directory     // required when Providers is non-empty
	Store      Store         // nil: Postgres store (ekman ships its migration as module "ekman")
	TTL        time.Duration // default 30 days, sliding (refreshed at most every TTL/10)
	PublicURL  string        // browser-facing origin for redirect URLs and cookie security
	AfterLogin string        // default "/"
	Secret     []byte        // HMAC key for the OAuth state cookie, ≥32 bytes; default $EKMAN_SECRET
}
type Identity struct{ Provider, Subject, Email, Name, AvatarURL string; EmailVerified bool }
type Directory interface {
	// UserFor maps a verified identity to an app user id. Return an *ekman.Error (e.g. Forbidden) to refuse.
	UserFor(ctx context.Context, id Identity) (userID string, err error)
}
type DirectoryFunc func(ctx context.Context, id Identity) (string, error)
type Provider interface {
	Name() string
	AuthCodeURL(state, verifier, redirectURL string) string
	Exchange(ctx context.Context, code, verifier, redirectURL string) (Identity, error)
}
func Google(clientID, clientSecret string) Provider // OIDC userinfo endpoint
func GitHub(clientID, clientSecret string) Provider // /user + /user/emails (primary verified email)
type OAuth2Config struct {
	Name, ClientID, ClientSecret, AuthURL, TokenURL string
	Scopes     []string
	Identity   func(ctx context.Context, client *http.Client, accessToken string) (Identity, error)
	HTTPClient *http.Client
}
func OAuth2(cfg OAuth2Config) Provider // any standards-compliant provider (code + PKCE S256)
func Challenge(verifier string) string  // PKCE S256
func GetJSON(ctx context.Context, client *http.Client, accessToken, url string, dst any) error
func IsLocalOrigin(origin string) bool  // localhost, 127.0.0.1, ::1, *.localhost
type Session struct{ TokenHash []byte; UserID string; CreatedAt, ExpiresAt time.Time }
var ErrNoSession error // Store.Get on a missing or expired session
type Store interface {
	Create(ctx context.Context, s Session) error
	Get(ctx context.Context, tokenHash []byte) (Session, error)
	Touch(ctx context.Context, tokenHash []byte, expires time.Time) error
	Delete(ctx context.Context, tokenHash []byte) error
	DeleteUser(ctx context.Context, userID string) error
}
func PostgresStore(d *db.DB) Store // over database/sql
func MemoryStore() Store
```
Cookies: `ekman_session` (HttpOnly, SameSite=Lax, token hashed at rest) and `ekman_hint=1`
(JS-readable, no secret) so static frontends skip API calls for anonymous visitors. OAuth state +
PKCE verifier live in a 10-minute signed cookie (`ekman_oauth`, HMAC-SHA256 with a key derived
from `Config.Secret`/`EKMAN_SECRET`; with neither, a random per-process key and a warning).
Every cookie is `Secure` unless the origin is local per `IsLocalOrigin` (`PublicURL` when set,
else the request host): local dev proxies such as `http://app.localhost` serve plain HTTP.
A new sign-in deletes the browser's previous session.

## mail

```go
type Message struct{ To []string; Subject, Text, HTML string; ReplyTo string }
type Sender interface{ Send(ctx context.Context, m Message) error }
func Log(l *slog.Logger) Sender            // dev
func Brevo(apiKey, fromEmail, fromName string) Sender // POST https://api.brevo.com/v3/smtp/email
```
Both reject a message without recipients, subject, or body.

## storage

```go
type Object struct{ Key, URL, ContentType string; Size int64 }
type Bucket interface {
	Put(ctx context.Context, key string, r io.Reader, contentType string) (Object, error)
	Delete(ctx context.Context, key string) error // missing key is not an error
}
func Dir(root, publicBaseURL string) Bucket // atomic writes; keys must stay inside root (ErrInvalidKey)
type Policy struct{ MaxBytes int64; Types []string; Prefix string } // MaxBytes default 10 MiB; Types required
// Receive streams one multipart file field, enforces size, sniffs the real content type
// (http.DetectContentType; the client's claim is ignored), generates a random key under Prefix
// with an extension for common types, and stores it. Errors are *ekman.Error: 400
// invalid_upload/missing_file/empty_file, 413 too_large, 415 unsupported_type.
func Receive(r *http.Request, field string, b Bucket, p Policy) (Object, error)
```
Use it from a `Handle` route whose `In` has a `*http.Request` field, or from a `Router.Raw`
handler (the request body must not be consumed by JSON binding).

## ekmantest

```go
func New(t testing.TB, opts ekman.Options, mods ...ekman.Module) *Client // httptest server; Auth nil → memory store
func (c *Client) As(userID string, perms ...rbac.Permission) *Client     // fake session (+ baseline; All works)
func (c *Client) Anonymous() *Client
func (c *Client) Do(method, path string, body any) *Response              // full path; non-[]byte/string/io.Reader bodies JSON-encoded
func (c *Client) URL() string
func (c *Client) App() *ekman.App
func (r *Response) Status() int
func (r *Response) JSON(dst any)
func (r *Response) Body() []byte
func (r *Response) Header() http.Header
func (r *Response) ErrorCode() string
// DB opens $EKMAN_TEST_DATABASE_URL with the test's driver in a fresh schema dropped at test
// end (skips when unset).
func DB(t testing.TB, driverName string) *db.DB
```
`As` works through an `internal/testhook` context value set by ekmantest's own server wrapper.
Applications cannot import the internal package, and a handler built by `App.Handler` ignores
ekmantest's header, so there is no production bypass.

## Experimental in v0.x

`storage.Receive` policy shape, `mail.Brevo`, `auth.Directory` signature (account linking may
change it), `auth.OAuth2Config`, `RouteInfo.Tags`, OpenAPI schema details, `ekmantest.DB`.
Stable intent: `App`, `Module`, `Handle`, `Access`, `Caller`, `Error`, `Validation`, `db`, `rbac`.
