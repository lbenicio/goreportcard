# Caching

Redis caching is optional and best-effort for report reads and the recent repository list. The relational database remains authoritative.

## Redis caches

- Completed report cache keys use `goreportcard:report:v1:` followed by the repository path.
- The recent list uses `goreportcard:recent-repos:v1`.
- Both cache entries expire after five minutes.
- A successful report save or recent-list update invalidates its matching cache entry.
- Cache read, decode, write, and invalidation failures are logged; report/recent reads fall back to the relational store where possible.

Redis also stores analysis-job state, which is separate from these expiring report caches. See [Analysis jobs](analysis-jobs.md) and [Redis operations](../operations/redis.md).

## Homepage snapshot

The homepage keeps a short-lived in-process snapshot of recent repositories and refreshes it in the background. This lets it serve the last available list without waiting for a slow database read. This snapshot is local to each web process; Redis and the relational database are shared across app instances.
