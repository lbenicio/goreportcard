# Analysis jobs

Web report submissions run asynchronously. The browser receives a status response and polls until the analysis completes or fails.

## Submission behavior

- `POST /checks` starts or refreshes an analysis.
- A non-refreshing `GET /checks` request returns a report redirect if a stored report is available; otherwise it queues analysis and returns job status.
- Concurrent requests for the same repository reuse an active job. A refresh request can restart a completed analysis.
- The in-process manager runs one analysis at a time and limits active job history. A full queue returns HTTP 503.
- Job status is temporary; completed results are saved to the relational database.

## Redis mode

When `REDIS_URL` is set, job queue and status data live in Redis. Any app instance can answer polling requests, and one Redis-lease-coordinated worker processes the shared queue. The worker recovers queued/processing work after a process restart and uses expiring status records.

The Redis job backend must be reachable during app startup. If Redis is not configured, the service uses its in-memory manager; that mode is for a single local instance because other processes cannot see its job state.

## Related code

- In-process manager and HTTP response flow: `src/handlers/analysis_jobs.go`.
- Redis queue, status, and worker lease: `src/handlers/analysis_jobs_redis.go`.
- Browser polling and submission: `src/assets` and `src/handlers/checks.go`.
