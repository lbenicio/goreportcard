# Architecture

Go Report Card serves a web interface and a separate CLI. Both use the same repository checks. The web service stores completed reports in a relational database and can use Redis for caching and shared analysis jobs.

## Local index

- [System overview](system-overview.md): request flow and package responsibilities.
- [Data model and persistence](data-model.md): relational database responsibilities and tables.
- [Analysis jobs](analysis-jobs.md): submission, queueing, worker coordination, and polling.
- [Caching](caching.md): Redis cache keys, expiration, fallback, and invalidation.
