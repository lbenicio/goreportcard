package storage

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lbenicio/goreportcard/check"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	maxOpenConnections = 20
	maxIdleConnections = 5
	insertBatchSize    = 1000
	migrationLockID    = int64(0x475243504f535447)
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// PostgresStore persists reports in normalized PostgreSQL tables.
type PostgresStore struct {
	db *sql.DB
}

// OpenPostgres opens a PostgreSQL pool, verifies connectivity, and applies migrations.
func OpenPostgres(ctx context.Context, dsn string) (*PostgresStore, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("DATABASE_URL is required")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL connection: %w", err)
	}
	db.SetMaxOpenConns(maxOpenConnections)
	db.SetMaxIdleConns(maxIdleConnections)
	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetConnMaxIdleTime(5 * time.Minute)

	store := &PostgresStore{db: db}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	if err := store.Migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}

	return store, nil
}

// Close closes the PostgreSQL connection pool.
func (s *PostgresStore) Close() error {
	return s.db.Close()
}

// Migrate applies embedded, versioned SQL migrations atomically.
func (s *PostgresStore) Migrate(ctx context.Context) error {
	files, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("list PostgreSQL migrations: %w", err)
	}
	sort.Strings(files)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin PostgreSQL migration transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", migrationLockID); err != nil {
		return fmt.Errorf("lock PostgreSQL migrations: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}

	applied := make(map[int]struct{})
	rows, err := tx.QueryContext(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return fmt.Errorf("read applied migrations: %w", err)
	}
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			rows.Close()
			return fmt.Errorf("scan applied migration: %w", err)
		}
		applied[version] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read applied migrations: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close applied migrations: %w", err)
	}

	for _, file := range files {
		base := path.Base(file)
		versionText := strings.SplitN(base, "_", 2)[0]
		version, err := strconv.Atoi(versionText)
		if err != nil {
			return fmt.Errorf("invalid migration filename %q: %w", base, err)
		}
		if _, ok := applied[version]; ok {
			continue
		}

		migration, err := migrationFiles.ReadFile(file)
		if err != nil {
			return fmt.Errorf("read migration %q: %w", base, err)
		}
		// Keep migrations to plain semicolon-delimited statements; database/sql's
		// extended protocol does not accept a multi-command string in one Exec.
		for _, statement := range strings.Split(string(migration), ";") {
			statement = strings.TrimSpace(statement)
			if statement == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("apply migration %q: %w", base, err)
			}
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", version); err != nil {
			return fmt.Errorf("record migration %q: %w", base, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit PostgreSQL migrations: %w", err)
	}
	return nil
}

// GetReport loads one report and its normalized check, file, and error rows.
func (s *PostgresStore) GetReport(ctx context.Context, repo string) (Report, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return Report{}, fmt.Errorf("begin report read: %w", err)
	}
	defer tx.Rollback()

	var report Report
	var repositoryID int64
	var grade string
	err = tx.QueryRowContext(ctx, `
		SELECT r.repository_id, r.module_path, p.version, p.resolved_repo, p.average, p.grade,
			p.file_count, p.issue_count, p.did_error, p.refreshed_at
		FROM repositories AS r
		JOIN reports AS p ON p.repository_id = r.repository_id
		WHERE r.module_path = $1`, repo).Scan(
		&repositoryID,
		&report.Repo,
		&report.Version,
		&report.ResolvedRepo,
		&report.Average,
		&grade,
		&report.Files,
		&report.Issues,
		&report.DidError,
		&report.LastRefresh,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Report{}, fmt.Errorf("%w: %s", ErrReportNotFound, repo)
	}
	if err != nil {
		return Report{}, fmt.Errorf("read report %q: %w", repo, err)
	}
	report.Grade = check.Grade(grade)
	report.Checks = make([]check.Score, 0)
	checkIndexes := make(map[int]int)

	rows, err := tx.QueryContext(ctx, `
		SELECT check_ordinal, name, description, weight, percentage, error_message
		FROM report_checks
		WHERE repository_id = $1
		ORDER BY check_ordinal`, repositoryID)
	if err != nil {
		return Report{}, fmt.Errorf("read checks for %q: %w", repo, err)
	}
	for rows.Next() {
		var ordinal int
		var score check.Score
		if err := rows.Scan(&ordinal, &score.Name, &score.Description, &score.Weight, &score.Percentage, &score.Error); err != nil {
			rows.Close()
			return Report{}, fmt.Errorf("scan check for %q: %w", repo, err)
		}
		score.FileSummaries = make([]check.FileSummary, 0)
		checkIndexes[ordinal] = len(report.Checks)
		report.Checks = append(report.Checks, score)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Report{}, fmt.Errorf("read checks for %q: %w", repo, err)
	}
	if err := rows.Close(); err != nil {
		return Report{}, fmt.Errorf("close checks for %q: %w", repo, err)
	}

	fileIndexes := make(map[int]map[int]int)
	rows, err = tx.QueryContext(ctx, `
		SELECT check_ordinal, file_ordinal, filename, file_url
		FROM report_check_files
		WHERE repository_id = $1
		ORDER BY check_ordinal, file_ordinal`, repositoryID)
	if err != nil {
		return Report{}, fmt.Errorf("read files for %q: %w", repo, err)
	}
	for rows.Next() {
		var checkOrdinal, fileOrdinal int
		var file check.FileSummary
		if err := rows.Scan(&checkOrdinal, &fileOrdinal, &file.Filename, &file.FileURL); err != nil {
			rows.Close()
			return Report{}, fmt.Errorf("scan file summary for %q: %w", repo, err)
		}
		checkIndex, ok := checkIndexes[checkOrdinal]
		if !ok {
			rows.Close()
			return Report{}, fmt.Errorf("file summary references missing check %d for %q", checkOrdinal, repo)
		}
		file.Errors = make([]check.Error, 0)
		fileIndexes[checkOrdinal] = ensureIndexMap(fileIndexes[checkOrdinal])
		fileIndexes[checkOrdinal][fileOrdinal] = len(report.Checks[checkIndex].FileSummaries)
		report.Checks[checkIndex].FileSummaries = append(report.Checks[checkIndex].FileSummaries, file)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Report{}, fmt.Errorf("read files for %q: %w", repo, err)
	}
	if err := rows.Close(); err != nil {
		return Report{}, fmt.Errorf("close files for %q: %w", repo, err)
	}

	rows, err = tx.QueryContext(ctx, `
		SELECT check_ordinal, file_ordinal, line_number, error_string
		FROM report_check_errors
		WHERE repository_id = $1
		ORDER BY check_ordinal, file_ordinal, error_ordinal`, repositoryID)
	if err != nil {
		return Report{}, fmt.Errorf("read errors for %q: %w", repo, err)
	}
	for rows.Next() {
		var checkOrdinal, fileOrdinal int
		var detail check.Error
		if err := rows.Scan(&checkOrdinal, &fileOrdinal, &detail.LineNumber, &detail.ErrorString); err != nil {
			rows.Close()
			return Report{}, fmt.Errorf("scan file error for %q: %w", repo, err)
		}
		checkIndex, checkExists := checkIndexes[checkOrdinal]
		fileIndex, fileExists := fileIndexes[checkOrdinal][fileOrdinal]
		if !checkExists || !fileExists {
			rows.Close()
			return Report{}, fmt.Errorf("file error references missing summary %d/%d for %q", checkOrdinal, fileOrdinal, repo)
		}
		report.Checks[checkIndex].FileSummaries[fileIndex].Errors = append(
			report.Checks[checkIndex].FileSummaries[fileIndex].Errors,
			detail,
		)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Report{}, fmt.Errorf("read errors for %q: %w", repo, err)
	}
	if err := rows.Close(); err != nil {
		return Report{}, fmt.Errorf("close errors for %q: %w", repo, err)
	}

	if err := tx.Commit(); err != nil {
		return Report{}, fmt.Errorf("commit report read: %w", err)
	}
	return report, nil
}

// SaveReport atomically replaces the current report and all of its child rows.
func (s *PostgresStore) SaveReport(ctx context.Context, report Report) error {
	if report.Repo == "" {
		return errors.New("cannot save report without a repository path")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin report write: %w", err)
	}
	defer tx.Rollback()

	var repositoryID int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO repositories (module_path)
		VALUES ($1)
		ON CONFLICT (module_path) DO NOTHING
		RETURNING repository_id`, report.Repo).Scan(&repositoryID)
	newRepository := err == nil
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx,
			"SELECT repository_id FROM repositories WHERE module_path = $1", report.Repo,
		).Scan(&repositoryID)
	}
	if err != nil {
		return fmt.Errorf("create or find repository %q: %w", report.Repo, err)
	}
	if newRepository {
		result, err := tx.ExecContext(ctx, `
			UPDATE app_statistics
			SET repository_count = repository_count + 1
			WHERE singleton_id = 1`)
		if err != nil {
			return fmt.Errorf("increment repository count: %w", err)
		}
		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("check repository count update: %w", err)
		}
		if rowsAffected != 1 {
			return errors.New("repository statistics row is missing")
		}
	}

	grade := report.Grade
	if grade == "" {
		grade = check.GradeFromPercentage(report.Average * 100)
	}

	err = tx.QueryRowContext(ctx, `
		INSERT INTO reports (
			repository_id, version, resolved_repo, average, grade,
			file_count, issue_count, did_error, refreshed_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (repository_id) DO UPDATE SET
			version = EXCLUDED.version,
			resolved_repo = EXCLUDED.resolved_repo,
			average = EXCLUDED.average,
			grade = EXCLUDED.grade,
			file_count = EXCLUDED.file_count,
			issue_count = EXCLUDED.issue_count,
			did_error = EXCLUDED.did_error,
			refreshed_at = EXCLUDED.refreshed_at
		RETURNING repository_id`,
		repositoryID,
		report.Version,
		report.ResolvedRepo,
		report.Average,
		string(grade),
		report.Files,
		report.Issues,
		report.DidError,
		report.LastRefresh,
	).Scan(&repositoryID)
	if err != nil {
		return fmt.Errorf("upsert report for %q: %w", report.Repo, err)
	}

	if _, err := tx.ExecContext(ctx, "DELETE FROM report_checks WHERE repository_id = $1", repositoryID); err != nil {
		return fmt.Errorf("replace checks for %q: %w", report.Repo, err)
	}

	checkRows := make([][]any, 0, len(report.Checks))
	fileRows := make([][]any, 0)
	errorRows := make([][]any, 0)
	for checkOrdinal, score := range report.Checks {
		checkRows = append(checkRows, []any{
			repositoryID,
			checkOrdinal,
			score.Name,
			score.Description,
			score.Weight,
			score.Percentage,
			score.Error,
		})
		for fileOrdinal, file := range score.FileSummaries {
			fileRows = append(fileRows, []any{
				repositoryID,
				checkOrdinal,
				fileOrdinal,
				file.Filename,
				file.FileURL,
			})
			for errorOrdinal, detail := range file.Errors {
				errorRows = append(errorRows, []any{
					repositoryID,
					checkOrdinal,
					fileOrdinal,
					errorOrdinal,
					detail.LineNumber,
					detail.ErrorString,
				})
			}
		}
	}

	if err := insertRows(ctx, tx, "report_checks",
		[]string{"repository_id", "check_ordinal", "name", "description", "weight", "percentage", "error_message"}, checkRows,
	); err != nil {
		return fmt.Errorf("insert checks for %q: %w", report.Repo, err)
	}
	if err := insertRows(ctx, tx, "report_check_files",
		[]string{"repository_id", "check_ordinal", "file_ordinal", "filename", "file_url"}, fileRows,
	); err != nil {
		return fmt.Errorf("insert file summaries for %q: %w", report.Repo, err)
	}
	if err := insertRows(ctx, tx, "report_check_errors",
		[]string{"repository_id", "check_ordinal", "file_ordinal", "error_ordinal", "line_number", "error_string"}, errorRows,
	); err != nil {
		return fmt.Errorf("insert file errors for %q: %w", report.Repo, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit report for %q: %w", report.Repo, err)
	}
	return nil
}

func insertRows(ctx context.Context, tx *sql.Tx, table string, columns []string, rows [][]any) error {
	if len(rows) == 0 {
		return nil
	}
	columnCount := len(columns)
	for start := 0; start < len(rows); start += insertBatchSize {
		end := start + insertBatchSize
		if end > len(rows) {
			end = len(rows)
		}

		var query strings.Builder
		query.WriteString("INSERT INTO ")
		query.WriteString(table)
		query.WriteString(" (")
		query.WriteString(strings.Join(columns, ", "))
		query.WriteString(") VALUES ")

		args := make([]any, 0, (end-start)*columnCount)
		for rowIndex, values := range rows[start:end] {
			if len(values) != columnCount {
				return fmt.Errorf("insert into %s: row has %d values, want %d", table, len(values), columnCount)
			}
			if rowIndex > 0 {
				query.WriteString(", ")
			}
			query.WriteByte('(')
			for columnIndex, value := range values {
				if columnIndex > 0 {
					query.WriteString(", ")
				}
				fmt.Fprintf(&query, "$%d", len(args)+1)
				args = append(args, value)
			}
			query.WriteByte(')')
		}

		if _, err := tx.ExecContext(ctx, query.String(), args...); err != nil {
			return err
		}
	}
	return nil
}

func ensureIndexMap(indexes map[int]int) map[int]int {
	if indexes == nil {
		return make(map[int]int)
	}
	return indexes
}

// AddRecent records an analyzed repository once and keeps only the five newest entries.
func (s *PostgresStore) AddRecent(ctx context.Context, repo string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin recent repository update: %w", err)
	}
	defer tx.Rollback()

	var repositoryID int64
	if err := tx.QueryRowContext(ctx,
		"SELECT repository_id FROM repositories WHERE module_path = $1", repo,
	).Scan(&repositoryID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s", ErrRepositoryNotFound, repo)
		}
		return fmt.Errorf("find recent repository %q: %w", repo, err)
	}
	if _, err := tx.ExecContext(ctx, "LOCK TABLE recent_repositories IN EXCLUSIVE MODE"); err != nil {
		return fmt.Errorf("lock recent repositories: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO recent_repositories (repository_id)
		VALUES ($1)
		ON CONFLICT (repository_id) DO NOTHING`, repositoryID); err != nil {
		return fmt.Errorf("add recent repository %q: %w", repo, err)
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM recent_repositories
		WHERE recent_id NOT IN (
			SELECT recent_id
			FROM recent_repositories
			ORDER BY recent_id DESC
			LIMIT 5
		)`); err != nil {
		return fmt.Errorf("trim recent repositories: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit recent repository update: %w", err)
	}
	return nil
}

// RecentRepos returns the five most recently added distinct repositories.
func (s *PostgresStore) RecentRepos(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.module_path
		FROM recent_repositories AS recent
		JOIN repositories AS r ON r.repository_id = recent.repository_id
		ORDER BY recent.recent_id DESC
		LIMIT 5`)
	if err != nil {
		return nil, fmt.Errorf("read recent repositories: %w", err)
	}
	defer rows.Close()

	repos := make([]string, 0, 5)
	for rows.Next() {
		var repo string
		if err := rows.Scan(&repo); err != nil {
			return nil, fmt.Errorf("scan recent repository: %w", err)
		}
		repos = append(repos, repo)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read recent repositories: %w", err)
	}
	return repos, nil
}

// HighScores reads the exact repository count and current top 50 eligible reports.
func (s *PostgresStore) HighScores(ctx context.Context) (int64, []HighScore, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return 0, nil, fmt.Errorf("begin high scores read: %w", err)
	}
	defer tx.Rollback()

	var count int64
	if err := tx.QueryRowContext(ctx,
		"SELECT repository_count FROM app_statistics WHERE singleton_id = 1",
	).Scan(&count); err != nil {
		return 0, nil, fmt.Errorf("read repository count: %w", err)
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT r.module_path, p.average * 100::double precision, p.file_count
		FROM reports AS p
		JOIN repositories AS r ON r.repository_id = p.repository_id
		WHERE p.file_count >= 100
		ORDER BY p.average DESC, p.repository_id ASC
		LIMIT 50`)
	if err != nil {
		return 0, nil, fmt.Errorf("read high scores: %w", err)
	}

	scores := make([]HighScore, 0, 50)
	for rows.Next() {
		var score HighScore
		if err := rows.Scan(&score.Repo, &score.Score, &score.Files); err != nil {
			rows.Close()
			return 0, nil, fmt.Errorf("scan high score: %w", err)
		}
		scores = append(scores, score)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, nil, fmt.Errorf("read high scores: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, nil, fmt.Errorf("close high scores: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, fmt.Errorf("commit high scores read: %w", err)
	}
	return count, scores, nil
}

// DeleteRepository removes a repository and its report/recent rows atomically.
func (s *PostgresStore) DeleteRepository(ctx context.Context, repo string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin repository deletion: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, "DELETE FROM repositories WHERE module_path = $1", repo)
	if err != nil {
		return false, fmt.Errorf("delete repository %q: %w", repo, err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("check repository deletion: %w", err)
	}
	if rowsAffected == 0 {
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("commit repository deletion: %w", err)
		}
		return false, nil
	}

	result, err = tx.ExecContext(ctx, `
		UPDATE app_statistics
		SET repository_count = repository_count - 1
		WHERE singleton_id = 1 AND repository_count > 0`)
	if err != nil {
		return false, fmt.Errorf("decrement repository count: %w", err)
	}
	rowsAffected, err = result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("check repository count update: %w", err)
	}
	if rowsAffected != 1 {
		return false, errors.New("repository statistics row is missing or inconsistent")
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit repository deletion: %w", err)
	}
	return true, nil
}

// DuplicateRepositoryPaths returns non-lowercase paths with an exact lowercase counterpart.
func (s *PostgresStore) DuplicateRepositoryPaths(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT duplicate.module_path
		FROM repositories AS duplicate
		WHERE duplicate.module_path <> lower(duplicate.module_path)
			AND EXISTS (
				SELECT 1
				FROM repositories AS canonical
				WHERE canonical.module_path = lower(duplicate.module_path)
			)
		ORDER BY duplicate.module_path`)
	if err != nil {
		return nil, fmt.Errorf("find case-duplicate repositories: %w", err)
	}
	defer rows.Close()

	duplicates := make([]string, 0)
	for rows.Next() {
		var repo string
		if err := rows.Scan(&repo); err != nil {
			return nil, fmt.Errorf("scan duplicate repository: %w", err)
		}
		duplicates = append(duplicates, repo)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read duplicate repositories: %w", err)
	}
	return duplicates, nil
}
