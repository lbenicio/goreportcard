package storage

import (
	"context"
	"database/sql/driver"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lbenicio/goreportcard/check"
)

type failingMigrationFS struct {
	fs.FS
	globErr    error
	unreadable string
	openErr    error
}

func (m failingMigrationFS) Glob(pattern string) ([]string, error) {
	if m.globErr != nil {
		return nil, m.globErr
	}
	return fs.Glob(m.FS, pattern)
}

func (m failingMigrationFS) Open(name string) (fs.File, error) {
	if name == m.unreadable {
		return nil, m.openErr
	}
	return m.FS.Open(name)
}

func TestPostgresMigrateFilesystemErrors(t *testing.T) {
	wantErr := errors.New("scripted migration filesystem failure")
	prefix := []sqlTestStep{
		sqlExec("pg_advisory_xact_lock", 0),
		sqlExec("CREATE TABLE IF NOT EXISTS schema_migrations", 0),
		sqlQuery("SELECT version FROM schema_migrations", []string{"version"}),
	}
	tests := []struct {
		name      string
		migrateFS fs.FS
		steps     []sqlTestStep
		wantText  string
	}{
		{
			name:      "glob directory read",
			migrateFS: failingMigrationFS{FS: fstest.MapFS{}, globErr: wantErr},
			wantText:  "list PostgreSQL migrations",
		},
		{
			name: "invalid migration filename",
			migrateFS: fstest.MapFS{
				"migrations/not-a-number_up.sql": &fstest.MapFile{Data: []byte("CREATE TABLE example (id INT);")},
			},
			steps:    prefix,
			wantText: "invalid migration filename",
		},
		{
			name: "migration file read",
			migrateFS: failingMigrationFS{
				FS:         fstest.MapFS{"migrations/0002_test.sql": &fstest.MapFile{Data: []byte("CREATE TABLE example (id INT);")}},
				unreadable: "migrations/0002_test.sql",
				openErr:    wantErr,
			},
			steps:    prefix,
			wantText: "read migration",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, script := newSQLTestStore(t, tt.steps...)
			err := store.migrate(context.Background(), tt.migrateFS)
			if err == nil || !strings.Contains(err.Error(), tt.wantText) {
				t.Fatalf("migrate() error = %v, want text %q", err, tt.wantText)
			}
			if tt.name != "invalid migration filename" && !errors.Is(err, wantErr) {
				t.Fatalf("migrate() error = %v, want wrapped %v", err, wantErr)
			}
			assertSQLTestScript(t, script)
		})
	}
}

func TestPostgresMigrateFailurePaths(t *testing.T) {
	wantErr := errors.New("scripted PostgreSQL failure")
	migrationSteps := []sqlTestStep{
		sqlExec("pg_advisory_xact_lock", 0),
		sqlExec("CREATE TABLE IF NOT EXISTS schema_migrations", 0),
		sqlQuery("SELECT version FROM schema_migrations", []string{"version"}),
	}
	files, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		data, err := migrationFiles.ReadFile("migrations/" + file.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, statement := range strings.Split(string(data), ";") {
			if strings.TrimSpace(statement) != "" {
				migrationSteps = append(migrationSteps, sqlExec("", 0))
			}
		}
		migrationSteps = append(migrationSteps, sqlExec("INSERT INTO schema_migrations", 1))
	}

	tests := []struct {
		name      string
		steps     []sqlTestStep
		beginErr  error
		commitErr error
		wantText  string
	}{
		{name: "create migration table", steps: []sqlTestStep{migrationSteps[0], {op: "exec", contains: "CREATE TABLE IF NOT EXISTS schema_migrations", err: wantErr}}, wantText: "create migration table"},
		{name: "read applied migrations", steps: []sqlTestStep{migrationSteps[0], migrationSteps[1], {op: "query", contains: "SELECT version FROM schema_migrations", err: wantErr}}, wantText: "read applied migrations"},
		{name: "scan applied migration", steps: []sqlTestStep{migrationSteps[0], migrationSteps[1], sqlQuery("SELECT version FROM schema_migrations", []string{"version"}, []driver.Value{"not an integer"})}, wantText: "scan applied migration"},
		{name: "read applied migration rows", steps: []sqlTestStep{migrationSteps[0], migrationSteps[1], {op: "query", contains: "SELECT version FROM schema_migrations", columns: []string{"version"}, rowsErr: wantErr}}, wantText: "read applied migrations"},
		{name: "close applied migration rows", steps: []sqlTestStep{migrationSteps[0], migrationSteps[1], {op: "query", contains: "SELECT version FROM schema_migrations", columns: []string{"version"}, closeErr: wantErr}}, wantText: "read applied migrations"},
		{name: "apply migration statement", steps: append(append([]sqlTestStep(nil), migrationSteps[:3]...), sqlTestStep{op: "exec", err: wantErr}), wantText: "apply migration"},
		{name: "record migration", steps: append(append([]sqlTestStep(nil), migrationSteps[:len(migrationSteps)-1]...), sqlTestStep{op: "exec", contains: "INSERT INTO schema_migrations", err: wantErr}), wantText: "record migration"},
		{name: "commit", steps: migrationSteps, commitErr: wantErr, wantText: "commit PostgreSQL migrations"},
		{name: "begin", beginErr: wantErr, wantText: "begin PostgreSQL migration transaction"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, script := newSQLTestStore(t, tt.steps...)
			script.beginErr = tt.beginErr
			script.commitErr = tt.commitErr
			err := store.Migrate(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.wantText) {
				t.Fatalf("Migrate() error = %v, want text %q", err, tt.wantText)
			}
			if tt.name != "scan applied migration" && !errors.Is(err, wantErr) {
				t.Fatalf("Migrate() error = %v, want wrapped %v", err, wantErr)
			}
			assertSQLTestScript(t, script)
		})
	}
}

func TestPostgresGetReportFailurePaths(t *testing.T) {
	wantErr := errors.New("scripted PostgreSQL failure")
	base := []sqlTestStep{
		sqlQuery("FROM repositories AS r", []string{"id", "repo", "version", "resolved", "average", "grade", "files", "issues", "did_error", "refreshed"}, []driver.Value{int64(1), "repo", "v1", "repo", float64(.9), "A", int64(1), int64(0), false, mustTestTime()}),
		sqlQuery("FROM report_checks", []string{"ordinal", "name", "description", "weight", "percentage", "error"}, []driver.Value{int64(0), "check", "description", float64(1), float64(1), ""}),
		sqlQuery("FROM report_check_files", []string{"check", "file", "filename", "url"}, []driver.Value{int64(0), int64(0), "main.go", "url"}),
		sqlQuery("FROM report_check_errors", []string{"check", "file", "line", "error"}, []driver.Value{int64(0), int64(0), int64(3), "failure"}),
	}
	tests := []struct {
		name      string
		index     int
		step      sqlTestStep
		beginErr  error
		commitErr error
		wantErr   error
		wantText  string
	}{
		{name: "begin", index: -1, beginErr: wantErr, wantErr: wantErr, wantText: "begin report read"},
		{name: "report query", index: 0, step: sqlTestStep{op: "query", contains: "FROM repositories AS r", err: wantErr}, wantErr: wantErr, wantText: "read report"},
		{name: "report scan", index: 0, step: sqlQuery("FROM repositories AS r", []string{"id"}, []driver.Value{"invalid"}), wantText: "read report"},
		{name: "checks query", index: 1, step: sqlTestStep{op: "query", contains: "FROM report_checks", err: wantErr}, wantErr: wantErr, wantText: "read checks"},
		{name: "check scan", index: 1, step: sqlQuery("FROM report_checks", []string{"ordinal"}, []driver.Value{"invalid"}), wantText: "scan check"},
		{name: "check rows", index: 1, step: sqlTestStep{op: "query", contains: "FROM report_checks", columns: []string{"ordinal"}, rowsErr: wantErr}, wantErr: wantErr, wantText: "read checks"},
		{name: "check close", index: 1, step: sqlTestStep{op: "query", contains: "FROM report_checks", columns: []string{"ordinal"}, closeErr: wantErr}, wantErr: wantErr, wantText: "read checks"},
		{name: "files query", index: 2, step: sqlTestStep{op: "query", contains: "FROM report_check_files", err: wantErr}, wantErr: wantErr, wantText: "read files"},
		{name: "file scan", index: 2, step: sqlQuery("FROM report_check_files", []string{"check"}, []driver.Value{"invalid"}), wantText: "scan file summary"},
		{name: "file references missing check", index: 2, step: sqlQuery("FROM report_check_files", []string{"check", "file", "filename", "url"}, []driver.Value{int64(9), int64(0), "main.go", "url"}), wantText: "references missing check"},
		{name: "file rows", index: 2, step: sqlTestStep{op: "query", contains: "FROM report_check_files", columns: []string{"check"}, rowsErr: wantErr}, wantErr: wantErr, wantText: "read files"},
		{name: "file close", index: 2, step: sqlTestStep{op: "query", contains: "FROM report_check_files", columns: []string{"check"}, closeErr: wantErr}, wantErr: wantErr, wantText: "read files"},
		{name: "errors query", index: 3, step: sqlTestStep{op: "query", contains: "FROM report_check_errors", err: wantErr}, wantErr: wantErr, wantText: "read errors"},
		{name: "error scan", index: 3, step: sqlQuery("FROM report_check_errors", []string{"check"}, []driver.Value{"invalid"}), wantText: "scan file error"},
		{name: "error references missing file", index: 3, step: sqlQuery("FROM report_check_errors", []string{"check", "file", "line", "error"}, []driver.Value{int64(0), int64(8), int64(3), "failure"}), wantText: "references missing summary"},
		{name: "error rows", index: 3, step: sqlTestStep{op: "query", contains: "FROM report_check_errors", columns: []string{"check"}, rowsErr: wantErr}, wantErr: wantErr, wantText: "read errors"},
		{name: "error close", index: 3, step: sqlTestStep{op: "query", contains: "FROM report_check_errors", columns: []string{"check"}, closeErr: wantErr}, wantErr: wantErr, wantText: "read errors"},
		{name: "commit", index: -1, commitErr: wantErr, wantErr: wantErr, wantText: "commit report read"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var steps []sqlTestStep
			switch tt.name {
			case "begin":
			case "commit":
				steps = append(steps, base...)
			default:
				steps = append(steps, base[:tt.index+1]...)
				steps[tt.index] = tt.step
			}
			store, script := newSQLTestStore(t, steps...)
			script.beginErr = tt.beginErr
			script.commitErr = tt.commitErr
			_, err := store.GetReport(context.Background(), "repo")
			if !strings.Contains(errorString(err), tt.wantText) {
				t.Fatalf("GetReport() error = %v, want text %q", err, tt.wantText)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("GetReport() error = %v, want wrapped %v", err, tt.wantErr)
			}
			assertSQLTestScript(t, script)
		})
	}
}

func TestPostgresSaveReportFailurePaths(t *testing.T) {
	wantErr := errors.New("scripted PostgreSQL failure")
	base := []sqlTestStep{
		sqlQuery("INSERT INTO repositories", []string{"id"}, []driver.Value{int64(3)}),
		sqlExec("UPDATE app_statistics", 1),
		sqlQuery("INSERT INTO reports", []string{"id"}, []driver.Value{int64(3)}),
		sqlExec("DELETE FROM report_checks", 0),
	}
	tests := []struct {
		name      string
		steps     []sqlTestStep
		beginErr  error
		commitErr error
		wantErr   error
		wantText  string
	}{
		{name: "begin", beginErr: wantErr, wantErr: wantErr, wantText: "begin report write"},
		{name: "create repository", steps: []sqlTestStep{{op: "query", contains: "INSERT INTO repositories", err: wantErr}}, wantErr: wantErr, wantText: "create or find repository"},
		{name: "find existing repository", steps: []sqlTestStep{sqlQuery("INSERT INTO repositories", []string{"id"}), {op: "query", contains: "SELECT repository_id FROM repositories", err: wantErr}}, wantErr: wantErr, wantText: "create or find repository"},
		{name: "increment statistics", steps: []sqlTestStep{base[0], {op: "exec", contains: "UPDATE app_statistics", err: wantErr}}, wantErr: wantErr, wantText: "increment repository count"},
		{name: "statistics rows affected error", steps: []sqlTestStep{base[0], {op: "exec", contains: "UPDATE app_statistics", affectedErr: wantErr}}, wantErr: wantErr, wantText: "check repository count update"},
		{name: "missing statistics row", steps: []sqlTestStep{base[0], sqlExec("UPDATE app_statistics", 0)}, wantText: "repository statistics row is missing"},
		{name: "upsert report", steps: []sqlTestStep{base[0], base[1], {op: "query", contains: "INSERT INTO reports", err: wantErr}}, wantErr: wantErr, wantText: "upsert report"},
		{name: "delete existing checks", steps: []sqlTestStep{base[0], base[1], base[2], {op: "exec", contains: "DELETE FROM report_checks", err: wantErr}}, wantErr: wantErr, wantText: "replace checks"},
		{name: "commit", steps: base, commitErr: wantErr, wantErr: wantErr, wantText: "commit report"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, script := newSQLTestStore(t, tt.steps...)
			script.beginErr = tt.beginErr
			script.commitErr = tt.commitErr
			err := store.SaveReport(context.Background(), Report{Repo: "repo", Average: .8})
			if err == nil || !strings.Contains(err.Error(), tt.wantText) {
				t.Fatalf("SaveReport() error = %v, want text %q", err, tt.wantText)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("SaveReport() error = %v, want wrapped %v", err, tt.wantErr)
			}
			assertSQLTestScript(t, script)
		})
	}
}

func TestPostgresSaveReportPropagatesChildInsertFailures(t *testing.T) {
	wantErr := errors.New("scripted PostgreSQL failure")
	prefix := []sqlTestStep{
		sqlQuery("INSERT INTO repositories", []string{"id"}, []driver.Value{int64(3)}),
		sqlExec("UPDATE app_statistics", 1),
		sqlQuery("INSERT INTO reports", []string{"id"}, []driver.Value{int64(3)}),
		sqlExec("DELETE FROM report_checks", 0),
	}
	childInserts := []struct {
		name     string
		previous []sqlTestStep
		failed   sqlTestStep
		wantText string
	}{
		{name: "check rows", failed: sqlTestStep{op: "exec", contains: "INSERT INTO report_checks", err: wantErr}, wantText: "insert checks"},
		{name: "file rows", previous: []sqlTestStep{sqlExec("INSERT INTO report_checks", 0)}, failed: sqlTestStep{op: "exec", contains: "INSERT INTO report_check_files", err: wantErr}, wantText: "insert file summaries"},
		{name: "error rows", previous: []sqlTestStep{sqlExec("INSERT INTO report_checks", 0), sqlExec("INSERT INTO report_check_files", 0)}, failed: sqlTestStep{op: "exec", contains: "INSERT INTO report_check_errors", err: wantErr}, wantText: "insert file errors"},
	}
	for _, tc := range childInserts {
		t.Run(tc.name, func(t *testing.T) {
			steps := append(append([]sqlTestStep(nil), prefix...), tc.previous...)
			steps = append(steps, tc.failed)
			store, script := newSQLTestStore(t, steps...)
			report := Report{Repo: "repo", Checks: []check.Score{{
				Name:          "check",
				FileSummaries: []check.FileSummary{{Filename: "main.go", Errors: []check.Error{{LineNumber: 1, ErrorString: "failure"}}}},
			}}}
			err := store.SaveReport(context.Background(), report)
			if !errors.Is(err, wantErr) || !strings.Contains(errorString(err), tc.wantText) {
				t.Fatalf("SaveReport() error = %v, want %q wrapping %v", err, tc.wantText, wantErr)
			}
			assertSQLTestScript(t, script)
		})
	}
}

func TestPostgresAddRecentFailurePaths(t *testing.T) {
	wantErr := errors.New("scripted PostgreSQL failure")
	tests := []struct {
		name     string
		steps    []sqlTestStep
		beginErr error
		wantErr  error
		wantText string
	}{
		{name: "begin", beginErr: wantErr, wantErr: wantErr, wantText: "begin recent repository update"},
		{name: "find repository", steps: []sqlTestStep{{op: "query", contains: "SELECT repository_id FROM repositories", err: wantErr}}, wantErr: wantErr, wantText: "find recent repository"},
		{name: "lock recent table", steps: []sqlTestStep{sqlQuery("SELECT repository_id FROM repositories", []string{"id"}, []driver.Value{int64(1)}), {op: "exec", contains: "LOCK TABLE recent_repositories", err: wantErr}}, wantErr: wantErr, wantText: "lock recent repositories"},
		{name: "insert recent", steps: []sqlTestStep{sqlQuery("SELECT repository_id FROM repositories", []string{"id"}, []driver.Value{int64(1)}), sqlExec("LOCK TABLE recent_repositories", 0), {op: "exec", contains: "INSERT INTO recent_repositories", err: wantErr}}, wantErr: wantErr, wantText: "add recent repository"},
		{name: "trim recents", steps: []sqlTestStep{sqlQuery("SELECT repository_id FROM repositories", []string{"id"}, []driver.Value{int64(1)}), sqlExec("LOCK TABLE recent_repositories", 0), sqlExec("INSERT INTO recent_repositories", 0), {op: "exec", contains: "DELETE FROM recent_repositories", err: wantErr}}, wantErr: wantErr, wantText: "trim recent repositories"},
		{name: "commit", steps: []sqlTestStep{sqlQuery("SELECT repository_id FROM repositories", []string{"id"}, []driver.Value{int64(1)}), sqlExec("LOCK TABLE recent_repositories", 0), sqlExec("INSERT INTO recent_repositories", 0), sqlExec("DELETE FROM recent_repositories", 0)}, wantErr: wantErr, wantText: "commit recent repository update"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, script := newSQLTestStore(t, tt.steps...)
			script.beginErr = tt.beginErr
			if tt.name == "commit" {
				script.commitErr = wantErr
			}
			err := store.AddRecent(context.Background(), "repo")
			if err == nil || !strings.Contains(err.Error(), tt.wantText) {
				t.Fatalf("AddRecent() error = %v, want text %q", err, tt.wantText)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("AddRecent() error = %v, want wrapped %v", err, tt.wantErr)
			}
			assertSQLTestScript(t, script)
		})
	}
}

func TestPostgresReadMethodsReportQueryAndScanErrors(t *testing.T) {
	wantErr := errors.New("scripted PostgreSQL failure")
	t.Run("recent query", func(t *testing.T) {
		store, script := newSQLTestStore(t, sqlTestStep{op: "query", contains: "FROM recent_repositories AS recent", err: wantErr})
		if _, err := store.RecentRepos(context.Background()); !errors.Is(err, wantErr) || !strings.Contains(err.Error(), "read recent repositories") {
			t.Fatalf("RecentRepos() error = %v", err)
		}
		assertSQLTestScript(t, script)
	})
	t.Run("recent scan", func(t *testing.T) {
		store, script := newSQLTestStore(t, sqlQuery("FROM recent_repositories AS recent", []string{"repo", "extra"}, []driver.Value{"repo", "unexpected"}))
		if _, err := store.RecentRepos(context.Background()); err == nil || !strings.Contains(err.Error(), "scan recent repository") {
			t.Fatalf("RecentRepos() error = %v", err)
		}
		assertSQLTestScript(t, script)
	})
	t.Run("recent rows", func(t *testing.T) {
		store, script := newSQLTestStore(t, sqlTestStep{op: "query", contains: "FROM recent_repositories AS recent", columns: []string{"repo"}, rowsErr: wantErr})
		if _, err := store.RecentRepos(context.Background()); !errors.Is(err, wantErr) || !strings.Contains(err.Error(), "read recent repositories") {
			t.Fatalf("RecentRepos() error = %v", err)
		}
		assertSQLTestScript(t, script)
	})
	for _, tc := range []struct {
		name     string
		steps    []sqlTestStep
		beginErr error
		wantText string
	}{
		{name: "high score begin", beginErr: wantErr, wantText: "begin high scores read"},
		{name: "count query", steps: []sqlTestStep{{op: "query", contains: "SELECT repository_count", err: wantErr}}, wantText: "read repository count"},
		{name: "score query", steps: []sqlTestStep{sqlQuery("SELECT repository_count", []string{"count"}, []driver.Value{int64(1)}), {op: "query", contains: "FROM reports AS p", err: wantErr}}, wantText: "read high scores"},
		{name: "score scan", steps: []sqlTestStep{sqlQuery("SELECT repository_count", []string{"count"}, []driver.Value{int64(1)}), sqlQuery("FROM reports AS p", []string{"repo", "extra"}, []driver.Value{"repo", "unexpected"})}, wantText: "scan high score"},
		{name: "score rows", steps: []sqlTestStep{sqlQuery("SELECT repository_count", []string{"count"}, []driver.Value{int64(1)}), {op: "query", contains: "FROM reports AS p", columns: []string{"repo"}, rowsErr: wantErr}}, wantText: "read high scores"},
		{name: "score close", steps: []sqlTestStep{sqlQuery("SELECT repository_count", []string{"count"}, []driver.Value{int64(1)}), {op: "query", contains: "FROM reports AS p", columns: []string{"repo"}, closeErr: wantErr}}, wantText: "read high scores"},
		{name: "high score commit", steps: []sqlTestStep{sqlQuery("SELECT repository_count", []string{"count"}, []driver.Value{int64(1)}), sqlQuery("FROM reports AS p", []string{"repo", "score", "files"})}, wantText: "commit high scores read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, script := newSQLTestStore(t, tc.steps...)
			script.beginErr = tc.beginErr
			if tc.name == "high score commit" {
				script.commitErr = wantErr
			}
			_, _, err := store.HighScores(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.wantText) {
				t.Fatalf("HighScores() error = %v, want text %q", err, tc.wantText)
			}
			if tc.name != "score scan" && tc.name != "score close" && tc.name != "high score commit" && !errors.Is(err, wantErr) {
				t.Fatalf("HighScores() error = %v, want wrapped %v", err, wantErr)
			}
			assertSQLTestScript(t, script)
		})
	}
}

func TestPostgresDeleteRepositoryFailurePaths(t *testing.T) {
	wantErr := errors.New("scripted PostgreSQL failure")
	tests := []struct {
		name      string
		steps     []sqlTestStep
		beginErr  error
		commitErr error
		wantText  string
	}{
		{name: "begin", beginErr: wantErr, wantText: "begin repository deletion"},
		{name: "delete", steps: []sqlTestStep{{op: "exec", contains: "DELETE FROM repositories", err: wantErr}}, wantText: "delete repository"},
		{name: "delete rows affected", steps: []sqlTestStep{{op: "exec", contains: "DELETE FROM repositories", affectedErr: wantErr}}, wantText: "check repository deletion"},
		{name: "missing delete commit", steps: []sqlTestStep{sqlExec("DELETE FROM repositories", 0)}, commitErr: wantErr, wantText: "commit repository deletion"},
		{name: "decrement count", steps: []sqlTestStep{sqlExec("DELETE FROM repositories", 1), {op: "exec", contains: "UPDATE app_statistics", err: wantErr}}, wantText: "decrement repository count"},
		{name: "count rows affected error", steps: []sqlTestStep{sqlExec("DELETE FROM repositories", 1), {op: "exec", contains: "UPDATE app_statistics", affectedErr: wantErr}}, wantText: "check repository count update"},
		{name: "inconsistent count", steps: []sqlTestStep{sqlExec("DELETE FROM repositories", 1), sqlExec("UPDATE app_statistics", 0)}, wantText: "missing or inconsistent"},
		{name: "delete commit", steps: []sqlTestStep{sqlExec("DELETE FROM repositories", 1), sqlExec("UPDATE app_statistics", 1)}, commitErr: wantErr, wantText: "commit repository deletion"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, script := newSQLTestStore(t, tt.steps...)
			script.beginErr = tt.beginErr
			script.commitErr = tt.commitErr
			_, err := store.DeleteRepository(context.Background(), "repo")
			if err == nil || !strings.Contains(err.Error(), tt.wantText) {
				t.Fatalf("DeleteRepository() error = %v, want text %q", err, tt.wantText)
			}
			if tt.name != "inconsistent count" && !errors.Is(err, wantErr) {
				t.Fatalf("DeleteRepository() error = %v, want wrapped %v", err, wantErr)
			}
			assertSQLTestScript(t, script)
		})
	}
}

func TestPostgresDuplicatePathsReportReadErrors(t *testing.T) {
	wantErr := errors.New("scripted PostgreSQL failure")
	t.Run("query", func(t *testing.T) {
		store, script := newSQLTestStore(t, sqlTestStep{op: "query", contains: "WHERE duplicate.module_path", err: wantErr})
		if _, err := store.DuplicateRepositoryPaths(context.Background()); !errors.Is(err, wantErr) || !strings.Contains(err.Error(), "find case-duplicate repositories") {
			t.Fatalf("DuplicateRepositoryPaths() error = %v", err)
		}
		assertSQLTestScript(t, script)
	})
	t.Run("scan", func(t *testing.T) {
		store, script := newSQLTestStore(t, sqlQuery("WHERE duplicate.module_path", []string{"repo", "extra"}, []driver.Value{"repo", "unexpected"}))
		if _, err := store.DuplicateRepositoryPaths(context.Background()); err == nil || !strings.Contains(err.Error(), "scan duplicate repository") {
			t.Fatalf("DuplicateRepositoryPaths() error = %v", err)
		}
		assertSQLTestScript(t, script)
	})
	t.Run("rows", func(t *testing.T) {
		store, script := newSQLTestStore(t, sqlTestStep{op: "query", contains: "WHERE duplicate.module_path", columns: []string{"repo"}, rowsErr: wantErr})
		if _, err := store.DuplicateRepositoryPaths(context.Background()); !errors.Is(err, wantErr) || !strings.Contains(err.Error(), "read duplicate repositories") {
			t.Fatalf("DuplicateRepositoryPaths() error = %v", err)
		}
		assertSQLTestScript(t, script)
	})
}

func TestInsertRowsRejectsMalformedRowsAndPropagatesExecErrors(t *testing.T) {
	t.Run("malformed row", func(t *testing.T) {
		store, script := newSQLTestStore(t)
		tx, err := store.db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		err = insertRows(context.Background(), tx, "test_rows", []string{"id", "name"}, [][]any{{int64(1)}})
		if err == nil || !strings.Contains(err.Error(), "row has 1 values, want 2") {
			t.Fatalf("insertRows() error = %v", err)
		}
		_ = tx.Rollback()
		assertSQLTestScript(t, script)
	})
	t.Run("exec", func(t *testing.T) {
		wantErr := errors.New("insert failed")
		store, script := newSQLTestStore(t, sqlTestStep{op: "exec", contains: "INSERT INTO test_rows", err: wantErr})
		tx, err := store.db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		err = insertRows(context.Background(), tx, "test_rows", []string{"id"}, [][]any{{int64(1)}})
		if !errors.Is(err, wantErr) {
			t.Fatalf("insertRows() error = %v, want %v", err, wantErr)
		}
		_ = tx.Rollback()
		assertSQLTestScript(t, script)
	})
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
