package storage

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/lbenicio/goreportcard/check"
)

const (
	maxOpenConnections = 20
	maxIdleConnections = 5
	insertBatchSize    = 1000
	migrationLockID    = int64(0x475243504f535447)
)

//go:embed migrations/*.sql migrations/mysql/*.sql
var migrationFiles embed.FS

// databaseDialect identifies the SQL dialect used by a relational store.
type databaseDialect uint8

const (
	postgresDialect databaseDialect = iota
	mysqlDialect
)

// SQLStore persists reports in normalized relational tables.
type SQLStore struct {
	db      *sql.DB
	dialect databaseDialect
}

// PostgresStore is kept as an alias for callers that use the PostgreSQL-specific opener.
type PostgresStore = SQLStore

// MySQLStore is the relational store returned by OpenMySQL.
type MySQLStore = SQLStore

// OpenPostgres opens a PostgreSQL pool, verifies connectivity, and applies migrations.
func OpenPostgres(ctx context.Context, dsn string) (*PostgresStore, error) {
	return openPostgres(ctx, dsn, sql.Open)
}

func openPostgres(ctx context.Context, dsn string, openDB func(string, string) (*sql.DB, error)) (*PostgresStore, error) {
	return openSQLStore(ctx, dsn, postgresDialect, "pgx", "PostgreSQL", openDB)
}

// OpenMySQL opens a MySQL or MariaDB pool from a mysql:// or mariadb:// URL.
func OpenMySQL(ctx context.Context, databaseURL string) (*MySQLStore, error) {
	return openMySQL(ctx, databaseURL, nil)
}

func openMySQL(ctx context.Context, databaseURL string, openDB func(string, string) (*sql.DB, error)) (*MySQLStore, error) {
	config, err := mysqlConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	if openDB != nil {
		return openSQLStore(ctx, config.FormatDSN(), mysqlDialect, "mysql", "MySQL/MariaDB", openDB)
	}
	connector, err := mysqlDriver.NewConnector(config)
	if err != nil {
		return nil, fmt.Errorf("configure MySQL connection: %w", err)
	}
	return initializeSQLStore(ctx, sql.OpenDB(connector), mysqlDialect, "MySQL/MariaDB")
}

// OpenDatabase chooses a relational database driver from DATABASE_URL's scheme.
func OpenDatabase(ctx context.Context, databaseURL string) (*SQLStore, error) {
	return openDatabase(ctx, databaseURL, nil)
}

func openDatabase(ctx context.Context, databaseURL string, openDB func(string, string) (*sql.DB, error)) (*SQLStore, error) {
	if strings.TrimSpace(databaseURL) == "" {
		return nil, errors.New("DATABASE_URL is required")
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "postgres", "postgresql":
		if openDB == nil {
			return OpenPostgres(ctx, databaseURL)
		}
		return openPostgres(ctx, databaseURL, openDB)
	case "mysql", "mariadb":
		return openMySQL(ctx, databaseURL, openDB)
	default:
		return nil, fmt.Errorf("unsupported DATABASE_URL scheme %q (want postgres, mysql, or mariadb)", parsed.Scheme)
	}
}

func mysqlDSN(databaseURL string) (string, error) {
	config, err := mysqlConfig(databaseURL)
	if err != nil {
		return "", err
	}
	return config.FormatDSN(), nil
}

func mysqlConfig(databaseURL string) (*mysqlDriver.Config, error) {
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse MySQL DATABASE_URL: %w", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if (scheme != "mysql" && scheme != "mariadb") || parsed.Hostname() == "" {
		return nil, errors.New("MySQL DATABASE_URL must use mysql:// or mariadb:// and include a host")
	}
	databaseName := strings.TrimPrefix(parsed.EscapedPath(), "/")
	if databaseName == "" {
		return nil, errors.New("MySQL DATABASE_URL must include a database name")
	}
	databaseName, err = url.PathUnescape(databaseName)
	if err != nil {
		return nil, fmt.Errorf("parse MySQL database name: %w", err)
	}

	config := mysqlDriver.NewConfig()
	if parsed.User != nil {
		config.User = parsed.User.Username()
		config.Passwd, _ = parsed.User.Password()
	}
	config.Net = "tcp"
	port := parsed.Port()
	if port == "" {
		port = "3306"
	}
	config.Addr = net.JoinHostPort(parsed.Hostname(), port)
	config.DBName = databaseName

	query := parsed.Query()
	if _, ok := query["strict"]; ok {
		return nil, errors.New("MySQL DATABASE_URL option strict is no longer supported by the MySQL driver")
	}
	query.Set("parseTime", "true")
	dsn := config.FormatDSN()
	if encodedQuery := query.Encode(); encodedQuery != "" {
		dsn += "?" + encodedQuery
	}
	config, err = mysqlDriver.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse MySQL connection options: %w", err)
	}
	if parsed.User != nil {
		config.User = parsed.User.Username()
		config.Passwd, _ = parsed.User.Password()
	}
	return config, nil
}

func openSQLStore(ctx context.Context, dsn string, dialect databaseDialect, driverName, label string, openDB func(string, string) (*sql.DB, error)) (*SQLStore, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("DATABASE_URL is required")
	}

	db, err := openDB(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s connection: %w", label, err)
	}
	return initializeSQLStore(ctx, db, dialect, label)
}

func initializeSQLStore(ctx context.Context, db *sql.DB, dialect databaseDialect, label string) (*SQLStore, error) {
	db.SetMaxOpenConns(maxOpenConnections)
	db.SetMaxIdleConns(maxIdleConnections)
	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetConnMaxIdleTime(5 * time.Minute)

	store := &SQLStore{db: db, dialect: dialect}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect to %s: %w", label, err)
	}
	if err := store.Migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}

	return store, nil
}

func (s *SQLStore) bind(query string) string {
	if s.dialect != mysqlDialect {
		return query
	}
	var bound strings.Builder
	for i := 0; i < len(query); i++ {
		if query[i] == '$' {
			j := i + 1
			for j < len(query) && query[j] >= '0' && query[j] <= '9' {
				j++
			}
			if j > i+1 {
				bound.WriteByte('?')
				i = j - 1
				continue
			}
		}
		bound.WriteByte(query[i])
	}
	return bound.String()
}

// Close closes the relational database connection pool.
func (s *SQLStore) Close() error {
	return s.db.Close()
}

// Migrate applies the embedded, versioned SQL migrations for this database dialect.
func (s *SQLStore) Migrate(ctx context.Context) error {
	return s.migrate(ctx, migrationFiles)
}

func (s *SQLStore) migrate(ctx context.Context, migrations fs.FS) error {
	pattern := "migrations/*.sql"
	if s.dialect == mysqlDialect {
		pattern = "migrations/mysql/*.sql"
	}
	files, err := fs.Glob(migrations, pattern)
	if err != nil {
		return fmt.Errorf("list %s migrations: %w", s.label(), err)
	}
	sort.Strings(files)

	tx, releaseLock, err := s.beginMigration(ctx)
	if err != nil {
		return err
	}
	defer func() {
		tx.Rollback()
		if releaseLock != nil {
			_ = releaseLock()
		}
	}()

	migrationTable := `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`
	if s.dialect == mysqlDialect {
		migrationTable = `
			CREATE TABLE IF NOT EXISTS schema_migrations (
				version INTEGER PRIMARY KEY,
				applied_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
			) ENGINE=InnoDB`
	}
	if _, err := tx.ExecContext(ctx, migrationTable); err != nil {
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
	// database/sql closes rows at EOF and reports driver close failures through Rows.Err.
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read applied migrations: %w", err)
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

		migration, err := fs.ReadFile(migrations, file)
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
		if _, err := tx.ExecContext(ctx, s.bind("INSERT INTO schema_migrations (version) VALUES ($1)"), version); err != nil {
			return fmt.Errorf("record migration %q: %w", base, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit %s migrations: %w", s.label(), err)
	}
	if releaseLock != nil {
		unlock := releaseLock
		releaseLock = nil
		if err := unlock(); err != nil {
			return fmt.Errorf("release MySQL migration lock: %w", err)
		}
	}
	return nil
}

func (s *SQLStore) label() string {
	if s.dialect == mysqlDialect {
		return "MySQL/MariaDB"
	}
	return "PostgreSQL"
}

func (s *SQLStore) beginMigration(ctx context.Context) (*sql.Tx, func() error, error) {
	if s.dialect == postgresDialect {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("begin PostgreSQL migration transaction: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", migrationLockID); err != nil {
			tx.Rollback()
			return nil, nil, fmt.Errorf("lock PostgreSQL migrations: %w", err)
		}
		return tx, nil, nil
	}

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("connect for MySQL migrations: %w", err)
	}
	releaseLock := func() error {
		defer conn.Close()
		var released sql.NullInt64
		if err := conn.QueryRowContext(ctx, "SELECT RELEASE_LOCK(?)", "goreportcard_schema_migrations").Scan(&released); err != nil {
			return err
		}
		if !released.Valid || released.Int64 != 1 {
			return errors.New("MySQL migration lock was not released")
		}
		return nil
	}
	var acquired sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 10)", "goreportcard_schema_migrations").Scan(&acquired); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("lock MySQL migrations: %w", err)
	}
	if !acquired.Valid || acquired.Int64 != 1 {
		conn.Close()
		return nil, nil, errors.New("lock MySQL migrations: timed out waiting for migration lock")
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		_ = releaseLock()
		return nil, nil, fmt.Errorf("begin MySQL migration transaction: %w", err)
	}
	return tx, releaseLock, nil
}

// GetReport loads one report and its normalized check, file, and error rows.
func (s *SQLStore) GetReport(ctx context.Context, repo string) (Report, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return Report{}, fmt.Errorf("begin report read: %w", err)
	}
	defer tx.Rollback()

	var report Report
	var repositoryID int64
	var grade string
	err = tx.QueryRowContext(ctx, s.bind(`
		SELECT r.repository_id, r.module_path, p.version, p.resolved_repo, p.average, p.grade,
			p.file_count, p.issue_count, p.did_error, p.refreshed_at
		FROM repositories AS r
		JOIN reports AS p ON p.repository_id = r.repository_id
		WHERE r.module_path = $1`), repo).Scan(
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

	rows, err := tx.QueryContext(ctx, s.bind(`
		SELECT check_ordinal, name, description, weight, percentage, error_message
		FROM report_checks
		WHERE repository_id = $1
		ORDER BY check_ordinal`), repositoryID)
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
	// database/sql closes rows at EOF and reports driver close failures through Rows.Err.
	if err := rows.Err(); err != nil {
		rows.Close()
		return Report{}, fmt.Errorf("read checks for %q: %w", repo, err)
	}
	fileIndexes := make(map[int]map[int]int)
	rows, err = tx.QueryContext(ctx, s.bind(`
		SELECT check_ordinal, file_ordinal, filename, file_url
		FROM report_check_files
		WHERE repository_id = $1
		ORDER BY check_ordinal, file_ordinal`), repositoryID)
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
	rows, err = tx.QueryContext(ctx, s.bind(`
		SELECT check_ordinal, file_ordinal, line_number, error_string
		FROM report_check_errors
		WHERE repository_id = $1
		ORDER BY check_ordinal, file_ordinal, error_ordinal`), repositoryID)
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
	if err := tx.Commit(); err != nil {
		return Report{}, fmt.Errorf("commit report read: %w", err)
	}
	return report, nil
}

// SaveReport atomically replaces the current report and all of its child rows.
func (s *SQLStore) SaveReport(ctx context.Context, report Report) error {
	if report.Repo == "" {
		return errors.New("cannot save report without a repository path")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin report write: %w", err)
	}
	defer tx.Rollback()

	var repositoryID int64
	newRepository := false
	if s.dialect == mysqlDialect {
		result, insertErr := tx.ExecContext(ctx, "INSERT INTO repositories (module_path) VALUES (?)", report.Repo)
		if insertErr == nil {
			repositoryID, err = result.LastInsertId()
			if err != nil {
				return fmt.Errorf("read repository ID for %q: %w", report.Repo, err)
			}
			newRepository = true
		} else {
			var mysqlErr *mysqlDriver.MySQLError
			if !errors.As(insertErr, &mysqlErr) || mysqlErr.Number != 1062 {
				return fmt.Errorf("create or find repository %q: %w", report.Repo, insertErr)
			}
			err = tx.QueryRowContext(ctx, "SELECT repository_id FROM repositories WHERE module_path = ?", report.Repo).Scan(&repositoryID)
			if err != nil {
				return fmt.Errorf("create or find repository %q: %w", report.Repo, err)
			}
		}
	} else {
		err = tx.QueryRowContext(ctx, `
			INSERT INTO repositories (module_path)
			VALUES ($1)
			ON CONFLICT (module_path) DO NOTHING
			RETURNING repository_id`, report.Repo).Scan(&repositoryID)
		newRepository = err == nil
		if errors.Is(err, sql.ErrNoRows) {
			err = tx.QueryRowContext(ctx,
				"SELECT repository_id FROM repositories WHERE module_path = $1", report.Repo,
			).Scan(&repositoryID)
		}
		if err != nil {
			return fmt.Errorf("create or find repository %q: %w", report.Repo, err)
		}
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

	reportArgs := []any{
		repositoryID,
		report.Version,
		report.ResolvedRepo,
		report.Average,
		string(grade),
		report.Files,
		report.Issues,
		report.DidError,
		report.LastRefresh,
	}
	if s.dialect == mysqlDialect {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO reports (
				repository_id, version, resolved_repo, average, grade,
				file_count, issue_count, did_error, refreshed_at
			)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON DUPLICATE KEY UPDATE
				version = VALUES(version),
				resolved_repo = VALUES(resolved_repo),
				average = VALUES(average),
				grade = VALUES(grade),
				file_count = VALUES(file_count),
				issue_count = VALUES(issue_count),
				did_error = VALUES(did_error),
				refreshed_at = VALUES(refreshed_at)`, reportArgs...)
	} else {
		err = tx.QueryRowContext(ctx, s.bind(`
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
			RETURNING repository_id`), reportArgs...).Scan(&repositoryID)
	}
	if err != nil {
		return fmt.Errorf("upsert report for %q: %w", report.Repo, err)
	}

	if _, err := tx.ExecContext(ctx, s.bind("DELETE FROM report_checks WHERE repository_id = $1"), repositoryID); err != nil {
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

	if err := insertRowsForDialect(ctx, tx, s.dialect, "report_checks",
		[]string{"repository_id", "check_ordinal", "name", "description", "weight", "percentage", "error_message"}, checkRows,
	); err != nil {
		return fmt.Errorf("insert checks for %q: %w", report.Repo, err)
	}
	if err := insertRowsForDialect(ctx, tx, s.dialect, "report_check_files",
		[]string{"repository_id", "check_ordinal", "file_ordinal", "filename", "file_url"}, fileRows,
	); err != nil {
		return fmt.Errorf("insert file summaries for %q: %w", report.Repo, err)
	}
	if err := insertRowsForDialect(ctx, tx, s.dialect, "report_check_errors",
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
	return insertRowsForDialect(ctx, tx, postgresDialect, table, columns, rows)
}

func insertRowsForDialect(ctx context.Context, tx *sql.Tx, dialect databaseDialect, table string, columns []string, rows [][]any) error {
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
				if dialect == mysqlDialect {
					query.WriteByte('?')
				} else {
					fmt.Fprintf(&query, "$%d", len(args)+1)
				}
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
func (s *SQLStore) AddRecent(ctx context.Context, repo string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin recent repository update: %w", err)
	}
	defer tx.Rollback()

	var repositoryID int64
	if err := tx.QueryRowContext(ctx,
		s.bind("SELECT repository_id FROM repositories WHERE module_path = $1"), repo,
	).Scan(&repositoryID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s", ErrRepositoryNotFound, repo)
		}
		return fmt.Errorf("find recent repository %q: %w", repo, err)
	}
	if s.dialect == mysqlDialect {
		var singletonID int
		if err := tx.QueryRowContext(ctx, "SELECT singleton_id FROM app_statistics WHERE singleton_id = 1 FOR UPDATE").Scan(&singletonID); err != nil {
			return fmt.Errorf("lock recent repositories: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT IGNORE INTO recent_repositories (repository_id) VALUES (?)", repositoryID); err != nil {
			return fmt.Errorf("add recent repository %q: %w", repo, err)
		}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM recent_repositories
			WHERE recent_id NOT IN (
				SELECT recent_id FROM (
					SELECT recent_id
					FROM recent_repositories
					ORDER BY recent_id DESC
					LIMIT 5
				) AS newest
			)`); err != nil {
			return fmt.Errorf("trim recent repositories: %w", err)
		}
	} else {
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
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit recent repository update: %w", err)
	}
	return nil
}

// RecentRepos returns the five most recently added distinct repositories.
func (s *SQLStore) RecentRepos(ctx context.Context) ([]string, error) {
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
func (s *SQLStore) HighScores(ctx context.Context) (int64, []HighScore, error) {
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
		SELECT r.module_path, p.average * 100, p.file_count
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
	// database/sql closes rows at EOF and reports driver close failures through Rows.Err.
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, nil, fmt.Errorf("read high scores: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, fmt.Errorf("commit high scores read: %w", err)
	}
	return count, scores, nil
}

// DeleteRepository removes a repository and its report/recent rows atomically.
func (s *SQLStore) DeleteRepository(ctx context.Context, repo string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin repository deletion: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, s.bind("DELETE FROM repositories WHERE module_path = $1"), repo)
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
func (s *SQLStore) DuplicateRepositoryPaths(ctx context.Context) ([]string, error) {
	query := `
		SELECT duplicate.module_path
		FROM repositories AS duplicate
		WHERE duplicate.module_path <> lower(duplicate.module_path)
			AND EXISTS (
				SELECT 1
				FROM repositories AS canonical
				WHERE canonical.module_path = lower(duplicate.module_path)
			)
		ORDER BY duplicate.module_path`
	if s.dialect == mysqlDialect {
		query = `
			SELECT duplicate.module_path
			FROM repositories AS duplicate
			WHERE BINARY duplicate.module_path <> BINARY lower(duplicate.module_path)
				AND EXISTS (
					SELECT 1
					FROM repositories AS canonical
					WHERE BINARY canonical.module_path = BINARY lower(duplicate.module_path)
				)
			ORDER BY duplicate.module_path`
	}
	rows, err := s.db.QueryContext(ctx, query)
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
