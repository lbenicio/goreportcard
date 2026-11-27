# Configuration

The web server and CLI read environment variables directly. Command-line flags override the matching server or CLI setting. `.env.example` is a template only; source or export it in your shell before starting a process.

## Application settings

| Setting | Default | Purpose |
|---|---|---|
| `DATABASE_URL` | None | Required relational database connection URL. Supports `postgresql://`, `postgres://`, `mysql://`, and `mariadb://` schemes. |
| `REDIS_URL` | Unset | Optional Redis URL. Enables shared analysis-job state and report/recent-repository caches. Use `redis://` or `rediss://`. |
| `HTTP_ADDR` | `:8000` | Server listen address. The `--http` flag overrides it. |
| `LOG_LEVEL` | `info` | Minimum log level: `debug`, `info`, `warn`, or `error`. |
| `LOG_COLOR` | `auto` | Color mode: `auto`, `always`, or `never`. |
| `NO_COLOR` | Unset | Disables automatic terminal coloring when present. `LOG_COLOR=always` explicitly forces colors. |

MySQL and MariaDB URLs accept standard URL-encoded usernames and passwords. Add `tls=true` to the query parameters to enable TLS. PostgreSQL connection options can be provided in the URL, for example `sslmode=disable` for a local development server.

The app requires the database to exist before startup and applies migrations using the configured credentials. If `REDIS_URL` is set, the service also needs to connect to Redis to start its shared analysis worker. Without Redis, analysis-job state is kept in process memory and is suitable only for one local app instance.

## Server flags

- `-h` or `--help`: print server usage.
- `-v` or `--version`: print the build version.
- `--http`: override `HTTP_ADDR`.
- `--log-level`: override `LOG_LEVEL`.
- `--log-color`: override `LOG_COLOR`.

`LOG_COLOR=auto` enables ANSI colors when standard error is a terminal and `NO_COLOR` is not set. `always` and `never` force the behavior.

## CLI flags

The CLI accepts `-h`/`--help`, `-v`/`--version`, `-d` for the project directory, `-t` for a minimum score threshold, `-j` for JSON output, and `--verbose` for per-file check details. It also accepts `--log-level` and `--log-color`.

## Test-only settings

| Setting | Purpose |
|---|---|
| `TEST_REDIS_URL` | Redis endpoint for integration tests. If unset, `scripts/test.sh` starts a temporary local Redis server when the required utilities are installed. |
| `TEST_DATABASE_URL` | Disposable PostgreSQL database used by database integration tests. |
| `TEST_MYSQL_DATABASE_URL` | Disposable MySQL or MariaDB administrative URL used by integration tests that create a temporary database. |

Do not point database integration-test settings at production data. See [Testing](../development/testing.md).
