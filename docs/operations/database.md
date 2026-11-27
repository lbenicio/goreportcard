# Database operations

## Supported engines

The web app supports:

- PostgreSQL 12 or later (`postgres://` or `postgresql://`).
- MySQL 8.0.16 or later (`mysql://`).
- MariaDB 10.6 or later (`mariadb://`).

`DATABASE_URL` selects the engine by its scheme. The target database must already exist. The configured application account needs permission to create and update schema objects when migrations run at startup.

## Schema and migration files

The PostgreSQL schema is embedded from `src/storage/migrations/`; MySQL and MariaDB use `src/storage/migrations/mysql/`. The relational schema stores reports, individual check results, files, and line-level errors in related tables. Foreign keys use cascading deletes for dependent report data.

When changing schema behavior, update both migration sets and exercise both database implementations. The integration tests create temporary schemas or databases and should use disposable credentials; see [Testing](../development/testing.md).

## Backups and storage

Back up the relational database using the database provider's supported backup process and verify that the backup can be restored before relying on it. Redis is not a backup of completed reports. Database usage can grow with stored reports and their child check/file/error rows; review table-level storage statistics before selecting a retention or cleanup policy.

Do not delete report rows directly without considering foreign-key relationships and application statistics. A database restore or cleanup plan should be based on a verified backup and the active migration/schema version.
