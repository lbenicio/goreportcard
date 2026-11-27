package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/lbenicio/goreportcard/src/storage"
)

type startupTestStore struct {
	closed   bool
	closeErr error
}

func (*startupTestStore) GetReport(context.Context, string) (storage.Report, error) {
	return storage.Report{}, nil
}

func (*startupTestStore) SaveReport(context.Context, storage.Report) error { return nil }
func (*startupTestStore) AddRecent(context.Context, string) error          { return nil }
func (*startupTestStore) RecentRepos(context.Context) ([]string, error)    { return nil, nil }
func (*startupTestStore) HighScores(context.Context) (int64, []storage.HighScore, error) {
	return 0, nil, nil
}
func (s *startupTestStore) Close() error {
	s.closed = true
	return s.closeErr
}

func configureApplicationTest(t *testing.T) *startupTestStore {
	t.Helper()
	t.Chdir(t.TempDir())
	t.Setenv("DATABASE_URL", "postgres://unit-test")
	t.Setenv("REDIS_URL", "")

	oldParseFlags, oldFatal := parseFlags, fatal
	oldOpenDatabase, oldOpenRedis, oldConfigureRedisAnalysisJobs := openDatabaseStore, openRedisStore, configureRedisAnalysisJobs
	oldLoadAssets, oldListenAndServe := loadAssets, listenAndServe
	oldAddr := *addr

	store := &startupTestStore{}
	parseFlags = func() {}
	fatal = func(...any) {}
	openDatabaseStore = func(context.Context, string) (closeableStore, error) {
		return store, nil
	}
	openRedisStore = func(context.Context, storage.Store, string) (closeableStore, error) {
		return store, nil
	}
	configureRedisAnalysisJobs = func(context.Context, string, storage.Store) (func() error, error) {
		return func() error { return nil }, nil
	}
	listenAndServe = func(string, http.Handler) error { return nil }
	*addr = "127.0.0.1:0"

	t.Cleanup(func() {
		parseFlags, fatal = oldParseFlags, oldFatal
		openDatabaseStore, openRedisStore, configureRedisAnalysisJobs = oldOpenDatabase, oldOpenRedis, oldConfigureRedisAnalysisJobs
		loadAssets, listenAndServe = oldLoadAssets, oldListenAndServe
		*addr = oldAddr
	})
	return store
}

func TestMakeHandlerRejectsEmptyRepositoryPath(t *testing.T) {
	called := false
	handler := makeHandler(nil, "report", func(http.ResponseWriter, *http.Request, storage.Store, string) {
		called = true
	})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/report/", nil))

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
	if !strings.Contains(response.Body.String(), "Please enter a repository") {
		t.Fatalf("response body = %q, want repository validation message", response.Body.String())
	}
	if called {
		t.Fatal("empty repository path invoked the report handler")
	}
}

func TestMainStartsAppAndRegistersHandlers(t *testing.T) {
	store := configureApplicationTest(t)
	var app http.Handler
	called := false
	listenAndServe = func(address string, handler http.Handler) error {
		called = true
		if address != "127.0.0.1:0" {
			t.Errorf("listen address = %q, want %q", address, "127.0.0.1:0")
		}
		app = handler
		return nil
	}

	main()

	if !called || app == nil {
		t.Fatal("main did not pass a handler to the HTTP server")
	}
	if !store.closed {
		t.Fatal("main did not close the database store after the server returned")
	}

	aboutResponse := httptest.NewRecorder()
	app.ServeHTTP(aboutResponse, httptest.NewRequest(http.MethodGet, "/about/", nil))
	if aboutResponse.Code != http.StatusOK {
		t.Fatalf("about status = %d, want %d: %s", aboutResponse.Code, http.StatusOK, aboutResponse.Body.String())
	}

	reportResponse := httptest.NewRecorder()
	app.ServeHTTP(reportResponse, httptest.NewRequest(http.MethodGet, "/report/", nil))
	if reportResponse.Code != http.StatusBadRequest {
		t.Fatalf("empty report path status = %d, want %d", reportResponse.Code, http.StatusBadRequest)
	}

	metricsResponse := httptest.NewRecorder()
	app.ServeHTTP(metricsResponse, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if metricsResponse.Code != http.StatusOK || !strings.Contains(metricsResponse.Body.String(), `response_time_ms_count{endpoint="/about/"} 1`) {
		t.Fatalf("metrics response status/body = %d/%q, want one /about/ observation", metricsResponse.Code, metricsResponse.Body.String())
	}
}

func TestRunReturnsStartupAndServerErrorsAndClosesStore(t *testing.T) {
	t.Run("database open error", func(t *testing.T) {
		configureApplicationTest(t)
		wantErr := errors.New("database unavailable")
		openDatabaseStore = func(context.Context, string) (closeableStore, error) {
			return nil, wantErr
		}
		if err := run(); !errors.Is(err, wantErr) || !strings.Contains(err.Error(), "could not open database") {
			t.Fatalf("run error = %v, want wrapped database error", err)
		}
	})

	t.Run("Redis open error closes database", func(t *testing.T) {
		store := configureApplicationTest(t)
		t.Setenv("REDIS_URL", "redis://unit-test")
		wantErr := errors.New("redis unavailable")
		openRedisStore = func(context.Context, storage.Store, string) (closeableStore, error) {
			return nil, wantErr
		}
		if err := run(); !errors.Is(err, wantErr) || !strings.Contains(err.Error(), "could not configure Redis cache") {
			t.Fatalf("run error = %v, want wrapped Redis error", err)
		}
		if !store.closed {
			t.Fatal("database store was not closed after Redis setup failed")
		}
	})

	t.Run("asset loading error closes database", func(t *testing.T) {
		store := configureApplicationTest(t)
		wantErr := errors.New("assets unavailable")
		loadAssets = func() (fs.FS, error) { return nil, wantErr }
		if err := run(); !errors.Is(err, wantErr) {
			t.Fatalf("run error = %v, want asset loading error", err)
		}
		if !store.closed {
			t.Fatal("database store was not closed after asset loading failed")
		}
	})

	t.Run("HTTP server error closes database", func(t *testing.T) {
		store := configureApplicationTest(t)
		wantErr := errors.New("listen failed")
		listenAndServe = func(string, http.Handler) error { return wantErr }
		if err := run(); !errors.Is(err, wantErr) {
			t.Fatalf("run error = %v, want HTTP server error", err)
		}
		if !store.closed {
			t.Fatal("database store was not closed after the server returned an error")
		}
	})
}

func TestMainReportsStartupError(t *testing.T) {
	configureApplicationTest(t)
	wantErr := errors.New("database unavailable")
	openDatabaseStore = func(context.Context, string) (closeableStore, error) {
		return nil, wantErr
	}
	var fatalArgs []any
	fatal = func(args ...any) { fatalArgs = args }

	main()

	if len(fatalArgs) != 1 || !strings.Contains(fatalArgs[0].(error).Error(), "could not open database") {
		t.Fatalf("fatal arguments = %#v, want the database startup error", fatalArgs)
	}
}

func TestMainPrintsVersionWithoutStartingService(t *testing.T) {
	store := configureApplicationTest(t)
	oldShowVersion, oldVersion := *showVersion, version
	t.Cleanup(func() { *showVersion, version = oldShowVersion, oldVersion })
	*showVersion = true
	version = "1.2.5-test"

	output := captureMainStdout(t, main)
	if want := "goreportcard version 1.2.5-test\n"; output != want {
		t.Fatalf("version output = %q, want %q", output, want)
	}
	if store.closed {
		t.Fatal("version command started and closed the database")
	}
}

func TestMainReportsInvalidLoggingConfiguration(t *testing.T) {
	configureApplicationTest(t)
	oldShowVersion, oldLogLevel := *showVersion, *logLevel
	t.Cleanup(func() { *showVersion, *logLevel = oldShowVersion, oldLogLevel })
	*showVersion = false
	*logLevel = "trace"
	var fatalArgs []any
	fatal = func(args ...any) { fatalArgs = args }

	main()
	if len(fatalArgs) != 1 || !strings.Contains(fatalArgs[0].(error).Error(), "invalid LOG_LEVEL") {
		t.Fatalf("fatal arguments = %#v, want invalid logging configuration", fatalArgs)
	}
}

func TestEnvOrDefault(t *testing.T) {
	t.Setenv("GOREPORTCARD_TEST_VALUE", "  configured  ")
	if got := envOrDefault("GOREPORTCARD_TEST_VALUE", "fallback"); got != "configured" {
		t.Fatalf("envOrDefault(configured) = %q, want configured", got)
	}
	t.Setenv("GOREPORTCARD_TEST_VALUE", "  ")
	if got := envOrDefault("GOREPORTCARD_TEST_VALUE", "fallback"); got != "fallback" {
		t.Fatalf("envOrDefault(blank) = %q, want fallback", got)
	}
}

func TestRunReturnsRedisAnalysisConfigurationError(t *testing.T) {
	store := configureApplicationTest(t)
	t.Setenv("REDIS_URL", "redis://unit-test")
	wantErr := errors.New("analysis Redis unavailable")
	configureRedisAnalysisJobs = func(context.Context, string, storage.Store) (func() error, error) {
		return nil, wantErr
	}

	if err := run(); !errors.Is(err, wantErr) || !strings.Contains(err.Error(), "could not configure Redis analysis jobs") {
		t.Fatalf("run error = %v, want wrapped analysis Redis error", err)
	}
	if !store.closed {
		t.Fatal("database store was not closed after Redis analysis setup failed")
	}
}

func TestRunLogsRedisAnalysisCloseError(t *testing.T) {
	configureApplicationTest(t)
	t.Setenv("REDIS_URL", "redis://unit-test")
	wantErr := errors.New("analysis worker close failed")
	configureRedisAnalysisJobs = func(context.Context, string, storage.Store) (func() error, error) {
		return func() error { return wantErr }, nil
	}

	var output bytes.Buffer
	oldOutput, oldFlags := log.Writer(), log.Flags()
	log.SetOutput(&output)
	t.Cleanup(func() {
		log.SetOutput(oldOutput)
		log.SetFlags(oldFlags)
	})

	if err := run(); err != nil {
		t.Fatalf("run() error = %v, want nil", err)
	}
	if !strings.Contains(output.String(), wantErr.Error()) {
		t.Fatalf("log output = %q, want analysis worker close error", output.String())
	}
}

func captureMainStdout(t *testing.T, run func()) string {
	t.Helper()
	originalStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	t.Cleanup(func() {
		os.Stdout = originalStdout
		_ = reader.Close()
		_ = writer.Close()
	})
	run()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = originalStdout
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(output)
}

func TestRunHandlesRepositoryDirectoryAndRedisCloseErrors(t *testing.T) {
	t.Run("repository directory creation error", func(t *testing.T) {
		t.Chdir(t.TempDir())
		if err := os.WriteFile("_repos", []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := run(); err == nil || !strings.Contains(err.Error(), "could not create repos dir") {
			t.Fatalf("run error = %v, want repository directory error", err)
		}
	})

	t.Run("Redis close error is logged", func(t *testing.T) {
		configureApplicationTest(t)
		t.Setenv("REDIS_URL", "redis://unit-test")
		redisErr := errors.New("redis close failed")
		redisStore := &startupTestStore{closeErr: redisErr}
		openRedisStore = func(context.Context, storage.Store, string) (closeableStore, error) {
			return redisStore, nil
		}
		var output bytes.Buffer
		originalWriter := log.Writer()
		log.SetOutput(&output)
		t.Cleanup(func() { log.SetOutput(originalWriter) })

		if err := run(); err != nil {
			t.Fatalf("run error = %v, want nil", err)
		}
		if !redisStore.closed || !strings.Contains(output.String(), redisErr.Error()) {
			t.Fatalf("Redis close state/log = %t/%q, want close error logged", redisStore.closed, output.String())
		}
	})
}

func TestDefaultStoreAdaptersRejectInvalidConfiguration(t *testing.T) {
	if _, err := openDatabaseStore(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "DATABASE_URL is required") {
		t.Fatalf("default database opener error = %v, want missing DATABASE_URL error", err)
	}
	if _, err := openRedisStore(context.Background(), nil, "not-a-redis-url"); err == nil || !strings.Contains(err.Error(), "parse REDIS_URL") {
		t.Fatalf("default Redis opener error = %v, want malformed REDIS_URL error", err)
	}
}

func TestDefaultStorageOpenersRejectInvalidConfigurationWithoutConnecting(t *testing.T) {
	t.Run("missing database URL", func(t *testing.T) {
		configureApplicationTest(t)
		t.Setenv("DATABASE_URL", "")
		openDatabaseStore = func(ctx context.Context, dsn string) (closeableStore, error) {
			return storage.OpenDatabase(ctx, dsn)
		}
		if err := run(); err == nil || !strings.Contains(err.Error(), "DATABASE_URL is required") {
			t.Fatalf("run error = %v, want missing DATABASE_URL error", err)
		}
	})

	t.Run("malformed Redis URL", func(t *testing.T) {
		configureApplicationTest(t)
		t.Setenv("REDIS_URL", "not-a-redis-url")
		openRedisStore = func(ctx context.Context, store storage.Store, redisURL string) (closeableStore, error) {
			return storage.OpenRedisCache(ctx, store, redisURL)
		}
		if err := run(); err == nil || !strings.Contains(err.Error(), "could not configure Redis cache") {
			t.Fatalf("run error = %v, want Redis URL error", err)
		}
	})
}
