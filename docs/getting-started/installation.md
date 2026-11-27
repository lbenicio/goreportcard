# Installation

## Requirements

- Go 1.27.3 or newer for local builds.
- A supported relational database for the web service: PostgreSQL 12 or later, MySQL 8.0.16 or later, or MariaDB 10.6 or later.
- Redis for shared analysis jobs when running more than one service instance. A single local instance can use in-process job state without Redis.
- The analyzer tools installed by `make install` before the service runs repository checks.

The web service also invokes Git, Go, `unzip`, and `wc` while analyzing submitted repositories. The production container includes these runtime tools.

## Install and run the web service

Clone the repository, install the pinned analyzer tools, and set a database URL before starting:

```sh
git clone https://github.com/lbenicio/goreportcard.git
cd goreportcard
make install
DATABASE_URL='postgresql://user:password@localhost:5432/goreportcard?sslmode=disable' make start
```

The database must already exist. On first startup, the application applies its embedded schema migrations, so the configured database role needs permission to create and update schema objects. See [Configuration](configuration.md) for all variables and [Database operations](../operations/database.md) for engine details.

To build all Go packages without installing the analyzer tools:

```sh
make build
```

The server listens on `:8000` by default. Open `http://localhost:8000` in a browser.

## Install the CLI

Install the analyzer tools, then install the standalone CLI:

```sh
make install
go install ./src/cmd/goreportcard
goreportcard --help
```

Release archives for Linux, macOS, and Windows on amd64 and arm64 are attached to GitHub releases. See the [release process](../operations/releases.md).

## Environment file

Use the tracked [.env.example](../../.env.example) as a template. Copy it to `.env`, edit the values, and export them before starting the app; Go Report Card does not automatically read `.env` files:

```sh
cp .env.example .env
set -a
. ./.env
set +a
make start
```
