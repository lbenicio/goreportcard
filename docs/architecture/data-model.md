# Data model and persistence

The relational database is the source of truth for completed reports. Report data is normalized across relational tables; report payloads are not stored as a single JSON blob in the database.

## Tables

The initial schema contains:

| Table | Contents |
|---|---|
| `repositories` | Canonical Go module paths and repository identifiers. |
| `app_statistics` | Application-wide repository count. |
| `reports` | Current score, grade, module version, file and issue counts, and refresh time. |
| `report_checks` | Score and metadata for each analyzer run. |
| `report_check_files` | Files associated with each check and their source links. |
| `report_check_errors` | Line-level findings for check files. |
| `recent_repositories` | Repositories displayed in the recent list. |

Foreign keys cascade report child rows when their parent report or repository is removed. The leaderboard index is defined in the embedded migrations.

## Migrations

PostgreSQL migrations are embedded from `src/storage/migrations/`. MySQL and MariaDB migrations are embedded from `src/storage/migrations/mysql/`. The app applies the selected engine's schema migrations during startup.

Database changes must keep the PostgreSQL and MySQL/MariaDB schemas aligned. Add and update migration coverage for each supported dialect; see [Development workflow](../development/workflow.md) and [Testing](../development/testing.md).

## Retention and growth

Completed reports, checks, file summaries, and errors remain in the relational database until application data is explicitly removed. Large repositories with many findings can produce many child rows, especially in `report_check_errors`. Redis caches have expiration and do not replace database retention. Plan backups and retention around the database provider's storage limits; do not treat clearing Redis as database cleanup.
