# Redis operations

Set `REDIS_URL` to a `redis://` or `rediss://` URL to enable Redis-backed caches and shared analysis jobs. Redis is optional only for a single local app process.

## Connectivity

When Redis is configured, the service starts a shared analysis worker and requires a successful Redis connection during startup. Confirm the URL scheme, host, port, credentials, TLS configuration, network route, and server health if startup fails while configuring analysis jobs.

The report/recent-repository cache layer is best-effort: cache read and write errors fall back to the relational database where possible and are logged. Analysis-job coordination is different: its state is shared through Redis and is not silently replaced with separate per-process queues.

## Memory and expiration

Report and recent-repository cache entries expire after five minutes. Analysis-job status expires after 30 minutes. Queued, processing, active, and worker-lock keys coordinate work and are managed by the job worker. Avoid flushing a Redis database that contains other applications' keys; the app prefixes its keys with `goreportcard:`.

For local single-process development, unset `REDIS_URL` to use in-memory analysis status. For production or multiple instances, configure a dedicated Redis database/service and monitor its memory limits.
