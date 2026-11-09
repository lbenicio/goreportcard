# Contributing to Go Report Card

Contributions are welcome. Please read this guide and the [Code of Conduct](CODE_OF_CONDUCT.md) before submitting a pull request.

## Before you start

- Review the existing documentation and recent changes so your contribution fits the project.
- For questions or feature proposals that need maintainer feedback, use the [maintainer contact form](https://lbenicio.dev/contact/).
- Keep pull requests focused. Describe the problem, the change, and any behavior or compatibility impact.
- Add or update tests and documentation when behavior changes.
- For user-visible changes, add a suitable entry to `CHANGELOG.md`. Maintainers handle release versioning.

## Development setup

The project uses Go 1.27.3 or newer. From the repository root, install the analyzer tools used by the project:

```sh
make install
```

## Checks

Run the same checks used by CI before submitting:

```sh
make lint
make install
make test
make build
```

`make lint` includes formatting (`gofmt -s`), `go vet`, `staticcheck`, and `misspell`. You can run the formatting check alone with `make fmt`.

PostgreSQL integration tests use `TEST_DATABASE_URL`. Set it only to a disposable test database: the tests create and drop a temporary schema and require permission to do so. MySQL/MariaDB integration tests use `TEST_MYSQL_DATABASE_URL`; they create and drop a temporary database and require permission to do so. Tests that need either URL are skipped when it is unset.

## Database changes

The relational store supports PostgreSQL, MySQL, and MariaDB. Add schema migrations and tests for each supported dialect when changing the schema: PostgreSQL migrations belong in `storage/migrations/`, and MySQL/MariaDB migrations belong in `storage/migrations/mysql/`. Redis is an optional report cache; the selected relational database remains the source of truth.
