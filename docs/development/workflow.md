# Development workflow

## Repository layout

- `src/`: server, CLI, checks, download logic, handlers, storage, tools, and embedded assets.
- `src/assets/`: templates, stylesheets, scripts, and images embedded in the server.
- `src/storage/migrations/`: PostgreSQL migrations.
- `src/storage/migrations/mysql/`: MySQL and MariaDB migrations.
- `scripts/`: tool installation, formatting, and test orchestration.
- `.github/workflows/`: CI, coverage, release, and production deployment workflows.
- `docs/`: user, architecture, operations, and development documentation.

Dependencies are managed with the root `go.mod` and `go.sum`. Do not add a `vendor/` tree. `make install` downloads modules and installs the analyzer tools at versions tracked by the module.

## Routine commands

```sh
make install
make lint
make test
make build
```

`make start` starts the HTTP service. `go install ./src/cmd/goreportcard` builds the CLI. See [Installation](../getting-started/installation.md) and [Testing](testing.md).

## Code and documentation changes

- Keep database behavior aligned across PostgreSQL, MySQL, and MariaDB.
- Add migrations and tests for both supported SQL dialect families when the schema changes.
- Update the root `CHANGELOG.md` for user-visible changes.
- Put lasting technical documentation in the relevant `docs/` category and update its category `README.md` plus `docs/README.md` index when adding a page.
- Follow the root [Contributing guide](../../CONTRIBUTING.md) for pull request expectations.
