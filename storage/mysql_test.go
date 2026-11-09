package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/lbenicio/goreportcard/check"
)

func TestMySQLDSN(t *testing.T) {
	dsn, err := mysqlDSN("mysql://user%40name:p%40ss%3Aword@db.example:3307/goreportcard?tls=true&charset=utf8mb4&loc=UTC")
	if err != nil {
		t.Fatal(err)
	}
	config, err := mysqlDriver.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("parse generated MySQL DSN: %v", err)
	}
	if config.User != "user@name" || config.Passwd != "p@ss:word" {
		t.Fatalf("credentials = (%q, %q), want decoded URL credentials", config.User, config.Passwd)
	}
	if config.Net != "tcp" || config.Addr != "db.example:3307" || config.DBName != "goreportcard" {
		t.Fatalf("connection target = %s://%s/%s, want tcp://db.example:3307/goreportcard", config.Net, config.Addr, config.DBName)
	}
	if !config.ParseTime || config.Loc.String() != "UTC" || config.TLSConfig != "true" || !strings.Contains(dsn, "charset=utf8mb4") {
		t.Fatalf("connection options = %+v, want time parsing, UTC, TLS, and utf8mb4", config)
	}
}

func TestMySQLDSNParsesDriverOptionsAndRejectsRemovedStrictOption(t *testing.T) {
	dsn, err := mysqlDSN("mysql://user:password@localhost/goreportcard?timeout=3s&readTimeout=2s&clientFoundRows=true")
	if err != nil {
		t.Fatal(err)
	}
	config, err := mysqlDriver.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if config.Timeout != 3*time.Second || config.ReadTimeout != 2*time.Second || !config.ClientFoundRows {
		t.Fatalf("driver options = timeout %s, read timeout %s, client found rows %t", config.Timeout, config.ReadTimeout, config.ClientFoundRows)
	}
	if _, err := mysqlDSN("mysql://localhost/goreportcard?strict=true"); err == nil || !strings.Contains(err.Error(), "strict is no longer supported") {
		t.Fatalf("mysqlDSN(strict option) error = %v, want an unsupported-option error", err)
	}
}

func TestMySQLDSNAcceptsMariaDBScheme(t *testing.T) {
	dsn, err := mysqlDSN("mariadb://user:password@localhost/goreportcard")
	if err != nil {
		t.Fatal(err)
	}
	config, err := mysqlDriver.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if config.Addr != "localhost:3306" || config.DBName != "goreportcard" {
		t.Fatalf("MariaDB target = %s/%s, want localhost:3306/goreportcard", config.Addr, config.DBName)
	}
}

func TestOpenDatabaseSelectsDriverFromURLScheme(t *testing.T) {
	for _, tc := range []struct {
		name        string
		databaseURL string
		wantDriver  string
	}{
		{name: "PostgreSQL", databaseURL: "postgres://user:password@localhost/goreportcard", wantDriver: "pgx"},
		{name: "MySQL", databaseURL: "mysql://user:password@localhost/goreportcard", wantDriver: "mysql"},
		{name: "MariaDB", databaseURL: "mariadb://user:password@localhost/goreportcard", wantDriver: "mysql"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantErr := errors.New("stop before connecting")
			called := false
			_, err := openDatabase(context.Background(), tc.databaseURL, func(driverName, _ string) (*sql.DB, error) {
				called = true
				if driverName != tc.wantDriver {
					t.Errorf("driver = %q, want %q", driverName, tc.wantDriver)
				}
				return nil, wantErr
			})
			if !called || !errors.Is(err, wantErr) {
				t.Fatalf("openDatabase() = (called=%t, err=%v), want wrapped opener error", called, err)
			}
		})
	}
}

func TestOpenDatabaseRejectsMissingOrUnsupportedURL(t *testing.T) {
	if _, err := OpenDatabase(context.Background(), "  "); err == nil || err.Error() != "DATABASE_URL is required" {
		t.Fatalf("OpenDatabase(blank URL) error = %v, want required URL error", err)
	}
	if _, err := OpenDatabase(context.Background(), "sqlite://localhost/goreportcard"); err == nil || !strings.Contains(err.Error(), "unsupported DATABASE_URL scheme") {
		t.Fatalf("OpenDatabase(unsupported URL) error = %v, want unsupported scheme error", err)
	}
	for _, databaseURL := range []string{"mysql://localhost", "mysql:///goreportcard", "mysql://localhost/"} {
		if _, err := mysqlDSN(databaseURL); err == nil {
			t.Errorf("mysqlDSN(%q) succeeded, want invalid MySQL URL error", databaseURL)
		}
	}
}

func TestMySQLBindAndInsertRowsUseQuestionPlaceholders(t *testing.T) {
	store := &SQLStore{dialect: mysqlDialect}
	if got, want := store.bind("SELECT a WHERE a = $1 AND b = $12"), "SELECT a WHERE a = ? AND b = ?"; got != want {
		t.Fatalf("bind() = %q, want %q", got, want)
	}

	rows := [][]any{{int64(1), "first"}, {int64(2), "second"}}
	store, script := newSQLTestStore(t, sqlExec("VALUES (?, ?), (?, ?)", 0))
	if err := insertRowsForDialect(context.Background(), mustBeginTestTx(t, store), mysqlDialect, "test_rows", []string{"id", "name"}, rows); err != nil {
		t.Fatal(err)
	}
	assertSQLTestScript(t, script)
}

func mustBeginTestTx(t *testing.T, store *PostgresStore) *sql.Tx {
	t.Helper()
	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

func TestMySQLAddRecentUsesMySQLLockAndQueries(t *testing.T) {
	store, script := newSQLTestStore(t,
		sqlQuery("SELECT repository_id FROM repositories", []string{"repository_id"}, []driver.Value{int64(4)}),
		sqlQuery("FOR UPDATE", []string{"singleton_id"}, []driver.Value{int64(1)}),
		sqlExec("INSERT IGNORE INTO recent_repositories", 0),
		sqlExec("SELECT recent_id FROM (", 0),
	)
	store.dialect = mysqlDialect
	if err := store.AddRecent(context.Background(), "github.com/example/recent"); err != nil {
		t.Fatal(err)
	}
	assertSQLTestScript(t, script)
	for _, call := range script.calls {
		if strings.Contains(call, "$1") {
			t.Errorf("MySQL query used PostgreSQL placeholders: %s", call)
		}
	}
}

func TestMySQLMigrationUsesMySQLMigrationsAndConnectionLock(t *testing.T) {
	steps := []sqlTestStep{
		sqlQuery("GET_LOCK", []string{"lock"}, []driver.Value{int64(1)}),
		sqlExec("CREATE TABLE IF NOT EXISTS schema_migrations", 0),
		sqlQuery("SELECT version FROM schema_migrations", []string{"version"}),
	}
	files, err := fs.Glob(migrationFiles, "migrations/mysql/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no MySQL migrations are embedded")
	}
	for _, file := range files {
		migration, err := migrationFiles.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, statement := range strings.Split(string(migration), ";") {
			if strings.TrimSpace(statement) != "" {
				steps = append(steps, sqlExec("", 0))
			}
		}
		steps = append(steps, sqlExec("INSERT INTO schema_migrations", 1))
	}
	steps = append(steps, sqlQuery("RELEASE_LOCK", []string{"released"}, []driver.Value{int64(1)}))
	store, script := newSQLTestStore(t, steps...)
	store.dialect = mysqlDialect

	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertSQLTestScript(t, script)
}

func TestMySQLStoreRelationalRoundTrip(t *testing.T) {
	store := newTestMySQLStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	refresh := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.FixedZone("test", 2*60*60))
	want := Report{
		Checks: []check.Score{{
			Name:        "gofmt",
			Description: "formatting check",
			Weight:      0.25,
			Percentage:  0.95,
			FileSummaries: []check.FileSummary{{
				Filename: "main.go",
				FileURL:  "https://example.test/main.go",
				Errors:   []check.Error{{LineNumber: 12, ErrorString: "formatting issue"}},
			}},
		}},
		Average:      0.95,
		Grade:        check.GradeAPlus,
		Files:        100,
		Issues:       1,
		Repo:         "github.com/example/mysql-integration",
		Version:      "v1.2.3",
		ResolvedRepo: "github.com/example/mysql-integration",
		LastRefresh:  refresh,
		DidError:     true,
	}
	if err := store.SaveReport(ctx, want); err != nil {
		t.Fatal(err)
	}
	want.Version = "v1.2.4"
	if err := store.SaveReport(ctx, want); err != nil {
		t.Fatal(err)
	}
	caseVariant := want
	caseVariant.Repo = "github.com/Example/mysql-integration"
	caseVariant.ResolvedRepo = caseVariant.Repo
	if err := store.SaveReport(ctx, caseVariant); err != nil {
		t.Fatal(err)
	}
	duplicates, err := store.DuplicateRepositoryPaths(ctx)
	if err != nil || !reflect.DeepEqual(duplicates, []string{caseVariant.Repo}) {
		t.Fatalf("DuplicateRepositoryPaths() = (%v, %v), want [%q]", duplicates, err, caseVariant.Repo)
	}
	got, err := store.GetReport(ctx, want.Repo)
	if err != nil {
		t.Fatal(err)
	}
	want.LastRefresh = refresh.UTC()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MySQL report round trip mismatch:\n got: %#v\nwant: %#v", got, want)
	}

	if err := store.AddRecent(ctx, want.Repo); err != nil {
		t.Fatal(err)
	}
	recent, err := store.RecentRepos(ctx)
	if err != nil || !reflect.DeepEqual(recent, []string{want.Repo}) {
		t.Fatalf("RecentRepos() = (%v, %v), want [%q]", recent, err, want.Repo)
	}
	count, scores, err := store.HighScores(ctx)
	if err != nil || count != 2 || len(scores) != 2 || scores[0].Repo != want.Repo || scores[0].Score != 95 {
		t.Fatalf("HighScores() = (%d, %+v, %v), want two scores with the original path first", count, scores, err)
	}
	deleted, err := store.DeleteRepository(ctx, caseVariant.Repo)
	if err != nil || !deleted {
		t.Fatalf("DeleteRepository(case variant) = (%t, %v), want true", deleted, err)
	}
	count, _, err = store.HighScores(ctx)
	if err != nil || count != 1 {
		t.Fatalf("repository count after deleting case variant = (%d, %v), want one", count, err)
	}
	deleted, err = store.DeleteRepository(ctx, want.Repo)
	if err != nil || !deleted {
		t.Fatalf("DeleteRepository() = (%t, %v), want true", deleted, err)
	}
	count, _, err = store.HighScores(ctx)
	if err != nil || count != 0 {
		t.Fatalf("repository count after delete = (%d, %v), want zero", count, err)
	}
}

func newTestMySQLStore(t *testing.T) *MySQLStore {
	t.Helper()
	databaseURL := os.Getenv("TEST_MYSQL_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_MYSQL_DATABASE_URL to run MySQL/MariaDB integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adminConfig, err := mysqlConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	adminConfig.DBName = ""
	connector, err := mysqlDriver.NewConnector(adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	admin := sql.OpenDB(connector)
	if err := admin.PingContext(ctx); err != nil {
		admin.Close()
		t.Fatalf("connect to TEST_MYSQL_DATABASE_URL: %v", err)
	}

	databaseName := fmt.Sprintf("goreportcard_test_%d_%d", os.Getpid(), time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+databaseName+"` CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"); err != nil {
		admin.Close()
		t.Fatalf("create temporary MySQL test database: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if _, err := admin.ExecContext(cleanupCtx, "DROP DATABASE `"+databaseName+"`"); err != nil {
			t.Errorf("drop temporary MySQL test database: %v", err)
		}
		if err := admin.Close(); err != nil {
			t.Errorf("close MySQL test admin connection: %v", err)
		}
	})

	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Path = "/" + databaseName
	parsed.RawPath = ""
	store, err := OpenMySQL(ctx, parsed.String())
	if err != nil {
		t.Fatalf("open MySQL test store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close MySQL test store: %v", err)
		}
	})
	return store
}

func TestMySQLSaveReportUsesMySQLQueriesAndQuestionPlaceholders(t *testing.T) {
	for _, tc := range []struct {
		name       string
		insertStep sqlTestStep
		wantCount  bool
	}{
		{name: "new repository", insertStep: sqlTestStep{op: "exec", contains: "INSERT INTO repositories", affected: 1, lastInsertID: 21}, wantCount: true},
		{name: "existing repository", insertStep: sqlTestStep{op: "exec", contains: "INSERT INTO repositories", err: &mysqlDriver.MySQLError{Number: 1062}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			steps := []sqlTestStep{tc.insertStep}
			if tc.wantCount {
				steps = append(steps, sqlExec("UPDATE app_statistics", 1))
			} else {
				steps = append(steps, sqlQuery("SELECT repository_id FROM repositories", []string{"repository_id"}, []driver.Value{int64(8)}))
			}
			steps = append(steps,
				sqlExec("ON DUPLICATE KEY UPDATE", 2),
				sqlExec("DELETE FROM report_checks", 0),
				sqlExec("INSERT INTO report_checks", 1),
			)
			store, script := newSQLTestStore(t, steps...)
			store.dialect = mysqlDialect
			report := Report{
				Repo: "github.com/example/mysql",
				Checks: []check.Score{{
					Name:          "gofmt",
					FileSummaries: []check.FileSummary{},
				}},
				LastRefresh: time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC),
			}
			if err := store.SaveReport(context.Background(), report); err != nil {
				t.Fatal(err)
			}
			assertSQLTestScript(t, script)
			for _, call := range script.calls {
				if strings.Contains(call, "$1") || strings.Contains(call, "$2") {
					t.Errorf("MySQL query used PostgreSQL placeholders: %s", call)
				}
			}
		})
	}
}
