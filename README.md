# Go Report Card

[![Go Report Card](https://goreportcard.lbenicio.dev/badge/github.com/lbenicio/goreportcard)](https://goreportcard.lbenicio.dev/report/github.com/lbenicio/goreportcard)
[![Coverage Status](https://codecov.io/github/lbenicio/goreportcard/coverage.svg?branch=main)](https://codecov.io/github/lbenicio/goreportcard?branch=main)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://github.com/kubernetes/dashboard/blob/master/LICENSE.txt)

A web application that generates a report on the quality of an open source Go project. It uses several measures, including `gofmt`, `go vet`, `go lint` and `gocyclo`. To get a report on your own project, try [goreportcard.lbenicio.dev](https://goreportcard.lbenicio.dev).

### Installation

```
git clone https://github.com/lbenicio/goreportcard.git
cd goreportcard
make install
```

The web application requires PostgreSQL 12 or later. Set `DATABASE_URL` to a PostgreSQL connection URL, then run:

```
DATABASE_URL='postgresql://user:password@localhost:5432/goreportcard?sslmode=disable' make start
```

The target database must already exist, and the application role needs permission to create and update schema objects on first startup. The application creates and updates its schema using embedded SQL migrations. Reports are stored in relational tables for repositories, report summaries, checks, file summaries, and file errors; no report payload is stored as JSON in PostgreSQL.

Redis is optional. Set `REDIS_URL` to a `redis://` or `rediss://` connection URL to enable a five-minute cache for report reads:

```
DATABASE_URL='postgresql://user:password@localhost:5432/goreportcard?sslmode=disable' \
REDIS_URL='redis://localhost:6379/0' make start
```

PostgreSQL remains the source of truth; Redis stores serialized report cache entries only, and successful report writes invalidate the corresponding entry. If Redis is unavailable, the application logs a warning and reads reports from PostgreSQL instead.

If you have an existing Badger database, stop the old application and import it before starting the PostgreSQL-backed service:

```
DATABASE_URL='postgresql://user:password@localhost:5432/goreportcard?sslmode=disable' go run ./cmd/import-badger -badger-path ./db
```

Use a new or empty PostgreSQL database for the import; matching repository paths are upserted. The importer reads Badger in read-only mode. Keep a backup of the old database until you have verified the import. The leaderboard is calculated from each repository's current report, and the repository count is maintained transactionally in PostgreSQL.

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
go install ./cmd/goreportcard-cli
goreportcard-cli
```

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
goreportcard-cli -v
```

```
Grade .......... A+  99.9%
Files ................ 362
Issues ................. 2
gofmt ............... 100%
go_vet ............... 99%
go_vet  vendor/github.com/prometheus/client_golang/prometheus/desc.go:25
        error: cannot find package "github.com/prometheus/client_model/go" in any of: (vet)

gocyclo .............. 99%
gocyclo download/download.go:22
        warning: cyclomatic complexity 17 of function download() is high (> 15) (gocyclo)

golint .............. 100%
ineffassign ......... 100%
license ............. 100%
misspell ............ 100%
```

### Contributing

Go Report Card is an open source project run by volunteers, and contributions are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for development and pull request guidelines, and [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) for community standards. For questions or feature proposals, contact the maintainer through the [contact form](https://lbenicio.dev/contact/). For security reports, see [SECURITY.md](SECURITY.md).

### Academic Citation

If you use Go Report Card for academic purposes, please use the following citation:

```
@Misc{schaaf-smith-goreportcard,
    author = {Schaaf, Herman and Smith, Shawn},
    title  = {Go Report Card: A report card for your Go application},
    year   = {2015--},
    url    = {https://www.goreportcard.com/},
    note   = {[Online; accessed <today>]}
}
```

### License

The code is licensed under the permissive Apache v2.0 license. [Read this](https://tldrlegal.com/license/apache-license-2.0-(apache-2.0)) for a summary.
