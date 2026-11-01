# ekman

> Like the Ekman spiral, each layer of your API is turned by the one above it.

ekman is an API-only, batteries-included Go framework with **zero dependencies**. Requests flow
through plain `net/http` middleware into typed handlers and down to Postgres; every route says
who may call it, every permission is declared, and the OpenAPI 3.1 document is derived from your
Go types.

```
request → middleware → typed handler → database
```

## Why

- **net/http all the way down.** `App.Handler()` is an `http.Handler`; middleware is
  `func(http.Handler) http.Handler`; any stdlib handler mounts with `Router.Raw`.
- **Nothing defaults open.** Access is a required argument: `ekman.Public()`,
  `ekman.SignedIn()` or `ekman.Requires(perm)`.
- **Typos fail at boot.** A route or role naming a permission no module declared stops the app
  before it serves a request.
- **One error shape** on the wire, including ekman's own 404/405/413/415:
  `{"error":{"code":"not_found","message":"...","fields":{...},"requestId":"..."}}`.
- **Zero dependencies.** stdlib only. You bring your SQL driver; ekman uses `database/sql`.

## Quickstart

```sh
go get github.com/sakib412/ekman
go get github.com/jackc/pgx/v5   # or any database/sql Postgres driver
```

```go
package main

import (
	"log"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib" // you bring the driver

	"github.com/sakib412/ekman"
	"github.com/sakib412/ekman/auth"
	"github.com/sakib412/ekman/db"
	"github.com/sakib412/ekman/rbac"
)

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

A feature is a `Module`: its permissions, migrations and routes.

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
	if err != nil { return ekman.NoContent{}, err } // sql.ErrNoRows → 404
	if err := c.RequireSelfOr(t.OwnerID, Manage); err != nil { return ekman.NoContent{}, err }
	return ekman.NoContent{}, s.setDone(ctx, in.ID)
}
```

```sh
DATABASE_URL=postgres://... go run . migrate   # apply migrations
DATABASE_URL=postgres://... go run .           # serve on :8080 ($PORT respected)
go run . openapi > openapi.json                # then: npx openapi-typescript openapi.json
```

A complete runnable app lives in [`examples/todo`](examples/todo).

## Features

- **Typed handlers** — `Handle[In, Out]` binds `path:`, `query:` and `header:` tags plus a JSON
  body, runs `Validate`, and encodes the result (200, 201 for POST, 204 for `NoContent`).
- **Access and RBAC** — `Public`/`SignedIn`/`Requires`, roles with an `rbac.All` wildcard, a
  baseline for every signed-in user, `Caller.RequireSelfOr(owner, perm)` for ownership checks.
- **Boot validation** — duplicate modules or routes, undeclared permissions, sign-in routes
  without auth, bad migration names: all reported at once, before serving.
- **Middleware stack** — request id, panic recovery, `slog` access log, credentialed CORS with
  a cross-origin write guard, sessions; plus app, group and per-route middleware.
- **OpenAPI 3.1** — derived from routes and Go types, deterministic, served at
  `{prefix}/openapi.json` and printed by `app openapi`.
- **Postgres** — `db.Q(ctx)` (sqlc-compatible), nested `db.Tx` that joins the outer
  transaction, `db.AfterCommit`, and forward-only per-module migrations with checksums under an
  advisory lock.
- **Sign-in** — Google and GitHub (or any OAuth 2 provider) with PKCE, signed state cookie,
  sliding sessions in Postgres or memory, `/auth/me`, and a JS-readable hint cookie for static
  frontends.
- **Mail** — `mail.Log` for development, `mail.Brevo` over HTTPS.
- **Uploads** — `storage.Receive` enforces size, sniffs the real content type, and stores under
  a random key; `storage.Dir` driver.
- **Testing** — `ekmantest.New(t, opts, mods...)`, `.As(user, perms...)` without OAuth, and
  `ekmantest.DB(t, "pgx")` for a throwaway schema per test.
- **Operations** — `GET /healthz`, graceful shutdown on SIGINT/SIGTERM.

## Status

ekman is **v0.x and experimental**. The contract lives in [`docs/design.md`](docs/design.md).

Stable intent: `App`, `Module`, `Handle`, `Access`, `Caller`, `Error`, `Validation`, `db`, `rbac`.

Experimental APIs that may change before v1:

- `storage.Receive` and the `storage.Policy` shape
- `mail.Brevo`
- `auth.Directory` signature (account linking may change it) and `auth.OAuth2Config`
- `RouteInfo.Tags`
- OpenAPI schema details
- `ekmantest.DB`

v0.1 targets PostgreSQL.

## License

[MIT](LICENSE)
