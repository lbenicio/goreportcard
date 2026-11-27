# Go Report Card

[![Go Report Card](https://goreportcard.lbenicio.dev/badge/github.com/lbenicio/goreportcard)](https://goreportcard.lbenicio.dev/report/github.com/lbenicio/goreportcard)
[![Coverage Status](https://codecov.io/github/lbenicio/goreportcard/coverage.svg?branch=main)](https://codecov.io/github/lbenicio/goreportcard?branch=main)
[![License](https://img.shields.io/badge/License-GPLv3-blue.svg)](LICENSE.txt)

A web application that generates a report on the quality of an open source Go project. It uses several measures, including `gofmt`, `go vet`, `go lint` and `gocyclo`. The web interface supports light and dark themes, follows your system preference by default, and remembers a manual selection. To get a report on your own project, try [goreportcard.lbenicio.dev](https://goreportcard.lbenicio.dev).

### Installation

```
git clone https://github.com/lbenicio/goreportcard.git
cd goreportcard
make install
```

The web application supports PostgreSQL 12 or later, MySQL 8.0.16 or later, and MariaDB 10.6 or later. Choose the database with the URL scheme in `DATABASE_URL`:

```sh
# PostgreSQL
DATABASE_URL='postgresql://user:password@localhost:5432/goreportcard?sslmode=disable' make start

# MySQL
DATABASE_URL='mysql://user:password@localhost:3306/goreportcard?charset=utf8mb4' make start

# MariaDB
DATABASE_URL='mariadb://user:password@localhost:3306/goreportcard?charset=utf8mb4' make start
```

The target database must already exist, and the application role needs permission to create and update schema objects on first startup. The application creates and updates its schema using embedded, database-specific SQL migrations. MySQL and MariaDB URLs use standard URL encoding for special characters in usernames or passwords; `tls=true` can be added for TLS connections. Reports are stored in relational tables for repositories, report summaries, checks, file summaries, and file errors; no report payload is stored as JSON.

Set `REDIS_URL` to a `redis://` or `rediss://` connection URL to enable shared analysis job state and five-minute caches for report reads and the recent-repositories list:

```
DATABASE_URL='postgresql://user:password@localhost:5432/goreportcard?sslmode=disable' \
REDIS_URL='redis://localhost:6379/0' make start
```

The selected relational database remains the source of truth for completed reports. Redis stores queued/running analysis status and coordinates one analysis worker across app instances, so a submission can be polled through any instance and queued work can resume after a process restart. Successful report writes and recent-repository updates invalidate their Redis cache entries. The homepage also keeps a short-lived in-process snapshot and refreshes it in the background, so slow database or Redis reads do not delay the page. If Redis is unavailable, report and recent-repository cache reads fall back to the relational database, while analysis submissions return an error instead of using inconsistent per-process job state.

For local development, `REDIS_URL` may be omitted when running a single app process; analysis job state then remains in that process's memory. Configure Redis for production and whenever running more than one app instance.

Copy `.env.example` to `.env` and edit the database settings for local use. The app does not load `.env` automatically; export its values in your shell before starting the server:

```sh
cp .env.example .env
set -a
. ./.env
set +a
make start
```

The server supports `-h`/`--help` and `-v`/`--version`. Set `HTTP_ADDR` or pass `--http` to change the listen address. `LOG_LEVEL` or `--log-level` accepts `debug`, `info`, `warn`, and `error`; routine request and cache messages are logged at `debug`. `LOG_COLOR` or `--log-color` accepts `auto`, `always`, and `never`. Automatic coloring is enabled for terminal output and honors `NO_COLOR`.

Once the server starts, you should see

```
Running on :8000...
```

Navigate to `localhost:8000` and you should see the Go Report Card front page.

### Command Line Interface

There is also a CLI available for grading applications on your local machine.

Example usage:
```
git clone https://github.com/lbenicio/goreportcard.git
cd goreportcard
make install
go install ./src/cmd/goreportcard
goreportcard
```

The CLI supports `-h`/`--help` and `-v`/`--version`. Use `--verbose` to include per-file check details. `--log-level` and `--log-color` accept the same values as the server settings above.

```
Grade .......... A+  99.9%
Files ................ 362
Issues ................. 2
gofmt ............... 100%
go_vet ............... 99%
gocyclo .............. 99%
golint .............. 100%
ineffassign ......... 100%
license ............. 100%
misspell ............ 100%
```

Verbose output:

```
goreportcard --verbose
```

Verbose mode adds file names and line-level errors for checks that report file details.

### Contributing

Go Report Card is an open source project run by volunteers, and contributions are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for development and pull request guidelines, and [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) for community standards. For questions or feature proposals, contact the maintainer through the [contact form](https://lbenicio.dev/contact/). For security reports, see [SECURITY.md](SECURITY.md).

### License

The code is licensed under the GNU General Public License v3.0. See [LICENSE.txt](LICENSE.txt) for the full terms.
