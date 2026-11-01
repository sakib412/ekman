# Changelog

All notable changes to ekman are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and ekman uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html); while it is v0.x, minor versions
may change experimental APIs (see `docs/design.md`).

## [Unreleased]

## [0.2.0] - 2026-11-02

Three gaps found by the first production consumer, closed without new dependencies.

### Added

- `ekman.Enum` (`EnumValues() []string`): named string types become `components.schemas`
  entries with `enum`, so `openapi-typescript` emits unions. `Handle` rejects other values:
  400 `invalid_param` for path/query/header parameters, 422 `invalid_input` for JSON body
  fields at any depth (keys like `items[0].kind`), merged with `Validate`. `""` passes. Enum
  types with no or duplicate values fail at boot.
- Typed handlers can take the request: an exported `In` field of type `*http.Request` receives
  it and leaves the body unread (multipart uploads with `storage.Receive`). Such an `In` may not
  have JSON body fields; that, a second request field or a tagged one fail at boot.
- `ekman.Accepts[T](mediaType)` and `ekman.Produces[T](mediaType)` route options document
  bodies the handler reads or writes itself (Raw routes; `Accepts` also on request-taking
  `Handle` routes). `ekman.File` stands for binary data (`format: binary`), `string` for text.
  `RouteInfo.Accepts`/`RouteInfo.Produces` (`[]ekman.Content`) expose them to middleware.
- `Options.OpenAPIHook func(doc map[string]any) error` edits the generated document once at
  boot; `/openapi.json` and `App.OpenAPI` both carry the result.
- `ekmantest.Client.WithHeader(key, value)` for requests that need extra headers, such as a
  multipart `Content-Type`.
- `examples/todo` exercises all of the above: a `Priority` enum, `PUT /todos/{id}/attachment`
  upload, `GET /todos.csv` export, and a `servers` entry added by the hook.

### Changed

- Operations whose JSON body contains an `Enum` list a 422 response even when `In` has no
  `Validate` method.
- `storage.Receive` may now be called from a `Handle` route (through the `*http.Request` field)
  as well as from `Router.Raw`.

## [0.1.0] - 2026-11-02

First release: typed handlers on `net/http` with required access (`Public`, `SignedIn`,
`Requires`), modules with migrations and declared permissions, boot-time validation, a derived
OpenAPI 3.1 document, Postgres via `database/sql` with nested transactions and per-module
migrations, OAuth 2 sign-in (Google, GitHub, any provider) with PKCE, sessions, RBAC, mail
(`Log`, `Brevo`), safe uploads (`storage.Receive`, `storage.Dir`) and `ekmantest`. Zero
dependencies.

[Unreleased]: https://github.com/sakib412/ekman/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/sakib412/ekman/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/sakib412/ekman/releases/tag/v0.1.0
