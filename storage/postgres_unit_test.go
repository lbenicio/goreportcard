package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lbenicio/goreportcard/check"
)

type sqlTestStep struct {
	op           string
	contains     string
	columns      []string
	values       [][]driver.Value
	err          error
	rowsErr      error
	closeErr     error
	affected     int64
	affectedErr  error
	lastInsertID int64
}

type sqlTestScript struct {
	mu          sync.Mutex
	steps       []sqlTestStep
	calls       []string
	unexpected  []string
	beginErr    error
	pingErr     error
	pingCalls   int
	closeCalls  int
	commitErr   error
	rollbackErr error
}

var (
	sqlTestScriptsMu sync.Mutex
	sqlTestScripts   = make(map[string]*sqlTestScript)
	sqlTestNextID    atomic.Uint64
)

func init() {
	sql.Register("goreportcard-storage-test", sqlTestDriver{})
}

type sqlTestDriver struct{}

func (sqlTestDriver) Open(name string) (driver.Conn, error) {
	sqlTestScriptsMu.Lock()
	script := sqlTestScripts[name]
	sqlTestScriptsMu.Unlock()
	if script == nil {
		return nil, fmt.Errorf("unknown test SQL script %q", name)
	}
	return &sqlTestConn{script: script}, nil
}

type sqlTestConn struct{ script *sqlTestScript }

func (c *sqlTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}
func (c *sqlTestConn) Close() error {
	c.script.mu.Lock()
	c.script.closeCalls++
	c.script.mu.Unlock()
	return nil
}
func (c *sqlTestConn) Ping(context.Context) error {
	c.script.mu.Lock()
	c.script.pingCalls++
	err := c.script.pingErr
	c.script.mu.Unlock()
	return err
}
func (c *sqlTestConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *sqlTestConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	if c.script.beginErr != nil {
		return nil, c.script.beginErr
	}
	return &sqlTestTx{script: c.script}, nil
}

func (c *sqlTestConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	step, ok := c.script.take("exec", query)
	if !ok {
		return nil, errors.New("unexpected SQL execution")
	}
	if step.err != nil {
		return nil, step.err
	}
	return sqlTestResult{affected: step.affected, affectedErr: step.affectedErr, lastInsertID: step.lastInsertID}, nil
}

func (c *sqlTestConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	step, ok := c.script.take("query", query)
	if !ok {
		return nil, errors.New("unexpected SQL query")
	}
	if step.err != nil {
		return nil, step.err
	}
	return &sqlTestRows{step: step}, nil
}

type sqlTestTx struct{ script *sqlTestScript }

func (tx *sqlTestTx) Commit() error   { return tx.script.commitErr }
func (tx *sqlTestTx) Rollback() error { return tx.script.rollbackErr }

type sqlTestResult struct {
	affected     int64
	affectedErr  error
	lastInsertID int64
}

func (r sqlTestResult) LastInsertId() (int64, error) {
	return r.lastInsertID, nil
}
func (r sqlTestResult) RowsAffected() (int64, error) { return r.affected, r.affectedErr }

type sqlTestRows struct {
	step sqlTestStep
	idx  int
}

func (r *sqlTestRows) Columns() []string { return r.step.columns }
func (r *sqlTestRows) Close() error      { return r.step.closeErr }
func (r *sqlTestRows) Next(dest []driver.Value) error {
	if r.idx < len(r.step.values) {
		copy(dest, r.step.values[r.idx])
		r.idx++
		return nil
	}
	if r.step.rowsErr != nil {
		err := r.step.rowsErr
		r.step.rowsErr = nil
		return err
	}
	return io.EOF
}

func (s *sqlTestScript) take(op, query string) (sqlTestStep, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, op+" "+strings.Join(strings.Fields(query), " "))
	if len(s.steps) == 0 {
		s.unexpected = append(s.unexpected, fmt.Sprintf("unexpected %s: %s", op, query))
		return sqlTestStep{}, false
	}
	step := s.steps[0]
	if step.op != op || (step.contains != "" && !strings.Contains(query, step.contains)) {
		s.unexpected = append(s.unexpected, fmt.Sprintf("got %s %q; want %s containing %q", op, query, step.op, step.contains))
		return sqlTestStep{}, false
	}
	s.steps = s.steps[1:]
	return step, true
}

func newSQLTestStore(t *testing.T, steps ...sqlTestStep) (*PostgresStore, *sqlTestScript) {
	t.Helper()
	id := fmt.Sprintf("script-%d", sqlTestNextID.Add(1))
	script := &sqlTestScript{steps: append([]sqlTestStep(nil), steps...)}
	sqlTestScriptsMu.Lock()
	sqlTestScripts[id] = script
	sqlTestScriptsMu.Unlock()
	db, err := sql.Open("goreportcard-storage-test", id)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close scripted database: %v", err)
		}
		sqlTestScriptsMu.Lock()
		delete(sqlTestScripts, id)
		sqlTestScriptsMu.Unlock()
	})
	return &PostgresStore{db: db}, script
}

func assertSQLTestScript(t *testing.T, script *sqlTestScript) {
	t.Helper()
	script.mu.Lock()
	defer script.mu.Unlock()
	if len(script.steps) != 0 || len(script.unexpected) != 0 {
		t.Fatalf("SQL script incomplete: remaining steps=%+v, unexpected=%v; calls=%v", script.steps, script.unexpected, script.calls)
	}
}

func sqlQuery(contains string, columns []string, values ...[]driver.Value) sqlTestStep {
	return sqlTestStep{op: "query", contains: contains, columns: columns, values: values}
}

func sqlExec(contains string, affected int64) sqlTestStep {
	return sqlTestStep{op: "exec", contains: contains, affected: affected}
}

func TestPostgresMigrateAppliesEmbeddedMigrations(t *testing.T) {
	steps := []sqlTestStep{
		sqlExec("pg_advisory_xact_lock", 0),
		sqlExec("CREATE TABLE IF NOT EXISTS schema_migrations", 0),
		sqlQuery("SELECT version FROM schema_migrations", []string{"version"}),
	}
	files, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
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
	store, script := newSQLTestStore(t, steps...)
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertSQLTestScript(t, script)
}

func TestPostgresMigrateSkipsAppliedMigration(t *testing.T) {
	store, script := newSQLTestStore(t,
		sqlExec("pg_advisory_xact_lock", 0),
		sqlExec("CREATE TABLE IF NOT EXISTS schema_migrations", 0),
		sqlQuery("SELECT version FROM schema_migrations", []string{"version"}, []driver.Value{int64(1)}),
	)
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertSQLTestScript(t, script)
}

func TestPostgresMigratePropagatesTransactionAndStatementErrors(t *testing.T) {
	wantErr := errors.New("simulated database failure")
	t.Run("begin", func(t *testing.T) {
		store, script := newSQLTestStore(t)
		script.beginErr = wantErr
		if err := store.Migrate(context.Background()); !errors.Is(err, wantErr) || !strings.Contains(err.Error(), "begin PostgreSQL migration transaction") {
			t.Fatalf("Migrate() error = %v, want begin error wrapping %v", err, wantErr)
		}
		assertSQLTestScript(t, script)
	})
	t.Run("lock", func(t *testing.T) {
		store, script := newSQLTestStore(t, sqlTestStep{op: "exec", contains: "pg_advisory_xact_lock", err: wantErr})
		if err := store.Migrate(context.Background()); !errors.Is(err, wantErr) || !strings.Contains(err.Error(), "lock PostgreSQL migrations") {
			t.Fatalf("Migrate() error = %v, want lock error wrapping %v", err, wantErr)
		}
		assertSQLTestScript(t, script)
	})
}

func TestOpenPostgresUsesInjectedDatabaseOpener(t *testing.T) {
	wantErr := errors.New("driver open failed")
	called := false
	_, err := openPostgres(context.Background(), "dsn", func(driverName, dsn string) (*sql.DB, error) {
		called = true
		if driverName != "pgx" || dsn != "dsn" {
			t.Fatalf("opener args = (%q, %q), want (pgx, dsn)", driverName, dsn)
		}
		return nil, wantErr
	})
	if !called || !errors.Is(err, wantErr) || !strings.Contains(err.Error(), "open PostgreSQL connection") {
		t.Fatalf("openPostgres() = (called=%t, err=%v), want wrapped open error", called, err)
	}
}

func TestOpenPostgresHandlesInjectedPingMigrationAndSuccessPaths(t *testing.T) {
	wantErr := errors.New("scripted startup failure")
	t.Run("ping failure closes database", func(t *testing.T) {
		store, script := newSQLTestStore(t)
		script.pingErr = wantErr
		_, err := openPostgres(context.Background(), "dsn", func(string, string) (*sql.DB, error) {
			return store.db, nil
		})
		if !errors.Is(err, wantErr) || !strings.Contains(err.Error(), "connect to PostgreSQL") {
			t.Fatalf("openPostgres() error = %v, want connection error", err)
		}
		assertSQLTestScript(t, script)
		assertSQLTestResources(t, script, 1, 1)
	})

	t.Run("migration failure closes database", func(t *testing.T) {
		store, script := newSQLTestStore(t, sqlTestStep{op: "exec", contains: "pg_advisory_xact_lock", err: wantErr})
		opened, err := openPostgres(context.Background(), "dsn", func(string, string) (*sql.DB, error) {
			return store.db, nil
		})
		if opened != nil || !errors.Is(err, wantErr) || !strings.Contains(err.Error(), "lock PostgreSQL migrations") {
			t.Fatalf("openPostgres() = (%v, %v), want migration error", opened, err)
		}
		assertSQLTestScript(t, script)
		assertSQLTestResources(t, script, 1, 1)
	})

	t.Run("successful startup returns usable store", func(t *testing.T) {
		store, script := newSQLTestStore(t,
			sqlExec("pg_advisory_xact_lock", 0),
			sqlExec("CREATE TABLE IF NOT EXISTS schema_migrations", 0),
			sqlQuery("SELECT version FROM schema_migrations", []string{"version"}, []driver.Value{int64(1)}),
		)
		opened, err := openPostgres(context.Background(), "dsn", func(string, string) (*sql.DB, error) {
			return store.db, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if opened == nil || opened.db != store.db {
			t.Fatalf("openPostgres() returned store %p, want non-nil store for scripted database", opened)
		}
		assertSQLTestScript(t, script)
		assertSQLTestResources(t, script, 1, 0)
		if err := opened.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		assertSQLTestResources(t, script, 1, 1)
	})
}

func assertSQLTestResources(t *testing.T, script *sqlTestScript, wantPings, wantCloses int) {
	t.Helper()
	script.mu.Lock()
	defer script.mu.Unlock()
	if script.pingCalls != wantPings || script.closeCalls != wantCloses {
		t.Fatalf("database resource calls = (pings=%d, closes=%d), want (%d, %d)", script.pingCalls, script.closeCalls, wantPings, wantCloses)
	}
}

func TestOpenPostgresRejectsMissingURLAndCanceledConnection(t *testing.T) {
	if _, err := OpenPostgres(context.Background(), " \t "); err == nil || !strings.Contains(err.Error(), "DATABASE_URL is required") {
		t.Fatalf("OpenPostgres(empty URL) error = %v", err)
	}
	if _, err := OpenPostgres(context.Background(), "not a valid PostgreSQL URL"); err == nil || !strings.Contains(err.Error(), "connect to PostgreSQL") {
		t.Fatalf("OpenPostgres(invalid URL) error = %v, want connection error", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := OpenPostgres(ctx, "postgres://localhost/goreportcard"); err == nil || !strings.Contains(err.Error(), "connect to PostgreSQL") {
		t.Fatalf("OpenPostgres(canceled context) error = %v, want connection error", err)
	}
}

func TestPostgresClose(t *testing.T) {
	store, _ := newSQLTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestPostgresGetReportReassemblesNormalizedRows(t *testing.T) {
	wantRefresh := mustTestTime()
	store, script := newSQLTestStore(t,
		sqlQuery("FROM repositories AS r", []string{"id", "repo", "version", "resolved", "average", "grade", "files", "issues", "did_error", "refreshed"}, []driver.Value{int64(17), "github.com/example/repo", "v1", "github.com/example/repo", float64(.9), "A", int64(3), int64(1), false, wantRefresh}),
		sqlQuery("FROM report_checks", []string{"ordinal", "name", "description", "weight", "percentage", "error"}, []driver.Value{int64(0), "gofmt", "format", float64(.5), float64(.9), ""}),
		sqlQuery("FROM report_check_files", []string{"check", "file", "filename", "url"},
			[]driver.Value{int64(0), int64(0), "main.go", "https://example.test/main.go"},
			[]driver.Value{int64(0), int64(1), "clean.go", "https://example.test/clean.go"}),
		sqlQuery("FROM report_check_errors", []string{"check", "file", "line", "error"}, []driver.Value{int64(0), int64(0), int64(8), "bad format"}),
	)

	got, err := store.GetReport(context.Background(), "github.com/example/repo")
	if err != nil {
		t.Fatal(err)
	}
	if got.Repo != "github.com/example/repo" || got.Grade != check.Grade("A") || !got.LastRefresh.Equal(wantRefresh) {
		t.Fatalf("report metadata = %+v", got)
	}
	if len(got.Checks) != 1 || len(got.Checks[0].FileSummaries) != 2 {
		t.Fatalf("report hierarchy = %+v", got.Checks)
	}
	file := got.Checks[0].FileSummaries[0]
	if file.Filename != "main.go" || len(file.Errors) != 1 || file.Errors[0] != (check.Error{LineNumber: 8, ErrorString: "bad format"}) {
		t.Fatalf("file summary = %+v", file)
	}
	if clean := got.Checks[0].FileSummaries[1]; clean.Filename != "clean.go" || len(clean.Errors) != 0 {
		t.Fatalf("clean file summary = %+v", clean)
	}
	assertSQLTestScript(t, script)
}

func TestPostgresGetReportNotFound(t *testing.T) {
	store, script := newSQLTestStore(t, sqlQuery("FROM repositories AS r", []string{"id"}))
	_, err := store.GetReport(context.Background(), "missing/repo")
	if !errors.Is(err, ErrReportNotFound) {
		t.Fatalf("GetReport() error = %v, want ErrReportNotFound", err)
	}
	assertSQLTestScript(t, script)
}

func TestPostgresSaveReportInsertsNewRepositoryAndChildren(t *testing.T) {
	report := Report{
		Repo: "github.com/example/new", Average: .92, Files: 4, Issues: 1,
		Checks: []check.Score{{Name: "gofmt", Description: "format", Weight: .5, Percentage: .92, FileSummaries: []check.FileSummary{{
			Filename: "main.go", FileURL: "https://example.test/main.go", Errors: []check.Error{{LineNumber: 9, ErrorString: "format issue"}},
		}}}},
	}
	store, script := newSQLTestStore(t,
		sqlQuery("INSERT INTO repositories", []string{"repository_id"}, []driver.Value{int64(21)}),
		sqlExec("UPDATE app_statistics", 1),
		sqlQuery("INSERT INTO reports", []string{"repository_id"}, []driver.Value{int64(21)}),
		sqlExec("DELETE FROM report_checks", 1),
		sqlExec("INSERT INTO report_checks", 1),
		sqlExec("INSERT INTO report_check_files", 1),
		sqlExec("INSERT INTO report_check_errors", 1),
	)
	if err := store.SaveReport(context.Background(), report); err != nil {
		t.Fatal(err)
	}
	assertSQLTestScript(t, script)
}

func TestPostgresSaveReportExistingRepositoryAndEmptyChildren(t *testing.T) {
	report := Report{Repo: "github.com/example/existing", Average: .74}
	store, script := newSQLTestStore(t,
		sqlQuery("INSERT INTO repositories", []string{"repository_id"}),
		sqlQuery("SELECT repository_id FROM repositories", []string{"repository_id"}, []driver.Value{int64(8)}),
		sqlQuery("INSERT INTO reports", []string{"repository_id"}, []driver.Value{int64(8)}),
		sqlExec("DELETE FROM report_checks", 0),
	)
	if err := store.SaveReport(context.Background(), report); err != nil {
		t.Fatal(err)
	}
	assertSQLTestScript(t, script)
}

func TestPostgresSaveReportRejectsMissingRepositoryPath(t *testing.T) {
	store, script := newSQLTestStore(t)
	if err := store.SaveReport(context.Background(), Report{}); err == nil || !strings.Contains(err.Error(), "without a repository path") {
		t.Fatalf("SaveReport() error = %v, want missing repository path error", err)
	}
	assertSQLTestScript(t, script)
}

func TestPostgresAddRecentAndRecentRepos(t *testing.T) {
	store, script := newSQLTestStore(t,
		sqlQuery("SELECT repository_id FROM repositories", []string{"id"}, []driver.Value{int64(2)}),
		sqlExec("LOCK TABLE recent_repositories", 0),
		sqlExec("INSERT INTO recent_repositories", 1),
		sqlExec("DELETE FROM recent_repositories", 0),
		sqlQuery("FROM recent_repositories AS recent", []string{"module_path"}, []driver.Value{"github.com/example/recent"}),
	)
	ctx := context.Background()
	if err := store.AddRecent(ctx, "github.com/example/recent"); err != nil {
		t.Fatal(err)
	}
	repos, err := store.RecentRepos(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0] != "github.com/example/recent" {
		t.Fatalf("RecentRepos() = %v", repos)
	}
	assertSQLTestScript(t, script)
}

func TestPostgresAddRecentReportsMissingRepository(t *testing.T) {
	store, script := newSQLTestStore(t, sqlQuery("SELECT repository_id FROM repositories", []string{"id"}))
	if err := store.AddRecent(context.Background(), "missing/repo"); !errors.Is(err, ErrRepositoryNotFound) {
		t.Fatalf("AddRecent() error = %v, want ErrRepositoryNotFound", err)
	}
	assertSQLTestScript(t, script)
}

func TestPostgresHighScores(t *testing.T) {
	store, script := newSQLTestStore(t,
		sqlQuery("SELECT repository_count", []string{"count"}, []driver.Value{int64(4)}),
		sqlQuery("FROM reports AS p", []string{"repo", "score", "files"}, []driver.Value{"github.com/example/top", float64(99), int64(120)}),
	)
	count, scores, err := store.HighScores(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count != 4 || len(scores) != 1 || scores[0] != (HighScore{Repo: "github.com/example/top", Score: 99, Files: 120}) {
		t.Fatalf("HighScores() = (%d, %+v)", count, scores)
	}
	assertSQLTestScript(t, script)
}

func TestPostgresDeleteRepositoryExistingAndMissing(t *testing.T) {
	store, script := newSQLTestStore(t,
		sqlExec("DELETE FROM repositories", 1),
		sqlExec("UPDATE app_statistics", 1),
		sqlExec("DELETE FROM repositories", 0),
	)
	deleted, err := store.DeleteRepository(context.Background(), "github.com/example/existing")
	if err != nil || !deleted {
		t.Fatalf("DeleteRepository(existing) = (%t, %v)", deleted, err)
	}
	deleted, err = store.DeleteRepository(context.Background(), "github.com/example/missing")
	if err != nil || deleted {
		t.Fatalf("DeleteRepository(missing) = (%t, %v), want false, nil", deleted, err)
	}
	assertSQLTestScript(t, script)
}

func TestPostgresDuplicateRepositoryPaths(t *testing.T) {
	store, script := newSQLTestStore(t,
		sqlQuery("WHERE duplicate.module_path <> lower", []string{"module_path"}, []driver.Value{"github.com/Example/repo"}),
	)
	got, err := store.DuplicateRepositoryPaths(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "github.com/Example/repo" {
		t.Fatalf("DuplicateRepositoryPaths() = %v", got)
	}
	assertSQLTestScript(t, script)
}

func TestInsertRowsBatchesLargeInsert(t *testing.T) {
	rows := make([][]any, insertBatchSize+1)
	for i := range rows {
		rows[i] = []any{int64(i), fmt.Sprintf("row-%d", i)}
	}
	store, script := newSQLTestStore(t,
		sqlExec("INSERT INTO test_rows", 0),
		sqlExec("INSERT INTO test_rows", 0),
	)
	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := insertRows(context.Background(), tx, "test_rows", []string{"id", "name"}, rows); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	assertSQLTestScript(t, script)
}

func mustTestTime() (result time.Time) {
	return time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
}
