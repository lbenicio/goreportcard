# Testing

## Standard checks

Run these from the repository root:

```sh
make lint
make test
make build
```

`make lint` formats Go files and runs `go vet`, `staticcheck`, and `misspell`. `make test` runs `scripts/test.sh` over packages under `src/`, writes `coverage.txt`, and fails unless aggregate statement coverage is above 98%.

## Redis tests

The Redis-backed tests use `TEST_REDIS_URL` when set. Otherwise, `scripts/test.sh` starts a temporary local Redis server and requires `redis-server`, `redis-cli`, and Python 3. The temporary service is shut down after the command finishes.

## Database integration tests

Set `TEST_DATABASE_URL` to a disposable PostgreSQL database to enable PostgreSQL integration tests. Set `TEST_MYSQL_DATABASE_URL` to a disposable MySQL or MariaDB administrative URL for those integration tests. The tests create temporary schemas/databases and remove them afterward; they require permissions to do so.

Never point these variables at production data. Tests that need an unset database URL are skipped.

## Coverage

The test script aggregates statement coverage across tested packages and enforces a value greater than 98%. When adding branches, error handling, or new package logic, add focused tests so coverage and behavior remain checked.
