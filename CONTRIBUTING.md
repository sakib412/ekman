# Contributing

Thanks for helping. ekman is small on purpose, so a few house rules:

- **The contract comes first.** `docs/design.md` is the v0.x contract. A change to public
  behavior updates it in the same pull request and says why.
- **Zero dependencies.** The root `go.mod` has no `require` lines. Code that needs a real
  driver (integration tests, examples) lives in a nested module.
- **Commits** follow [Conventional Commits](https://www.conventionalcommits.org/):
  `feat(auth): add Microsoft provider`.
- **Before pushing:**

  ```sh
  gofmt -l .                      # prints nothing
  go vet ./...
  go test -race ./...
  # Postgres integration tests (Docker):
  docker run -d --rm --name ekman-pg -e POSTGRES_PASSWORD=pg -p 55432:5432 postgres:17-alpine
  export EKMAN_TEST_DATABASE_URL='postgres://postgres:pg@localhost:55432/postgres?sslmode=disable'
  (cd internal/dbtest && go test -race ./...)
  (cd examples/todo && go test -race ./...)
  ```

- **Style:** small files per concern, a doc comment on every exported identifier, comments
  that explain why rather than what, no package-level mutable state.
