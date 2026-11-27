# Troubleshooting

## Service exits during startup

- Confirm `DATABASE_URL` is set, has a supported scheme, and points to an existing database.
- Check that the database accepts connections from the app host and that the app role can apply schema migrations.
- If `REDIS_URL` is set, verify that Redis is reachable and that the URL's credentials and TLS options are correct. The shared analysis worker must connect during startup.
- Use `--log-level=debug` for request and cache detail. Set `--log-color=never` if ANSI escapes make collected logs difficult to read.

## Analysis submissions fail or remain pending

- Confirm the requested Go module path is valid and the source is available through the Go module proxy.
- Inspect service logs for downloader or analyzer errors.
- With Redis enabled, verify connectivity and inspect the `goreportcard:analysis:` queue/job keys. Queue saturation returns HTTP 503; wait for active jobs to finish and retry.
- Without Redis, in-memory job state is visible only to the current process and is lost on restart. Use one process for local mode or configure Redis for multi-instance operation.

## Reports or recent repositories appear stale

Redis report and recent-list entries expire after five minutes. Successful writes invalidate the corresponding entry. Check Redis connectivity and the relational database if stale data persists beyond the TTL.

## Release or deployment failed

- Ensure the release was started from `main` and the changelog has a versioned heading with notes.
- Check the Release workflow's native Docker builds, six CLI artifacts, and checksum step.
- Confirm the production environment variables/secrets are configured and the SSH host key matches `PRODUCTION_KNOWN_HOSTS`.
- The deploy workflow verifies the Docker Hub manifest includes `linux/amd64` and `linux/arm64` before connecting to the host.
