package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dgraph-io/badger/v2"
	"github.com/lbenicio/goreportcard/storage"
)

type importTestStore struct {
	reports       []storage.Report
	saveErr       error
	recent        []string
	addRecentErrs map[string]error
	closeCalls    int
	closeErr      error
}

func (*importTestStore) GetReport(context.Context, string) (storage.Report, error) {
	return storage.Report{}, nil
}

func (s *importTestStore) SaveReport(_ context.Context, report storage.Report) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.reports = append(s.reports, report)
	return nil
}

func (s *importTestStore) AddRecent(_ context.Context, repo string) error {
	s.recent = append(s.recent, repo)
	return s.addRecentErrs[repo]
}
func (s *importTestStore) Close() error {
	s.closeCalls++
	return s.closeErr
}
func (*importTestStore) RecentRepos(context.Context) ([]string, error) {
	return nil, nil
}
func (*importTestStore) HighScores(context.Context) (int64, []storage.HighScore, error) {
	return 0, nil, nil
}

func openImportTestBadger(t *testing.T) *badger.DB {
	t.Helper()
	db, err := badger.Open(badger.DefaultOptions(filepath.Join(t.TempDir(), "badger")).WithLogger(nil))
	if err != nil {
		t.Fatalf("open test Badger database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close test Badger database: %v", err)
		}
	})
	return db
}

func putImportTestValue(t *testing.T, db *badger.DB, key string, value []byte) {
	t.Helper()
	if err := db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(key), value)
	}); err != nil {
		t.Fatalf("write test Badger value %q: %v", key, err)
	}
}

func TestImportReportsSavesPrefixedReportsUsingKeyRepository(t *testing.T) {
	db := openImportTestBadger(t)
	original := storage.Report{Repo: "stale-value", Average: 0.91, Files: 12}
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	putImportTestValue(t, db, "repos-github.com/example/one", encoded)
	putImportTestValue(t, db, "unrelated", []byte("not a report"))
	putImportTestValue(t, db, "repos-codeberg.org/example/two", []byte(`{"average":0.75,"files":4}`))

	store := &importTestStore{}
	count, err := importReports(context.Background(), db, store)
	if err != nil {
		t.Fatalf("import reports: %v", err)
	}
	if count != 2 || len(store.reports) != 2 {
		t.Fatalf("import count = %d and saved reports = %d, want 2 each", count, len(store.reports))
	}
	got := make(map[string]storage.Report, len(store.reports))
	for _, report := range store.reports {
		got[report.Repo] = report
	}
	if report, ok := got["github.com/example/one"]; !ok || report.Average != original.Average || report.Files != original.Files {
		t.Fatalf("first imported report = %+v, want repository from key and original fields", report)
	}
	if report, ok := got["codeberg.org/example/two"]; !ok || report.Average != 0.75 || report.Files != 4 {
		t.Fatalf("second imported report = %+v, want repository from key and decoded fields", report)
	}
}

func TestImportReportsRejectsEmptyRepositoryAndInvalidJSON(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  string
		data []byte
		want string
	}{
		{name: "empty repository suffix", key: "repos-", data: []byte(`{}`), want: "unexpected Badger report key"},
		{name: "invalid JSON", key: "repos-github.com/example/broken", data: []byte(`{`), want: "decode Badger report"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openImportTestBadger(t)
			putImportTestValue(t, db, tc.key, tc.data)
			store := &importTestStore{}

			count, err := importReports(context.Background(), db, store)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("import error = %v, want error containing %q", err, tc.want)
			}
			if count != 0 || len(store.reports) != 0 {
				t.Fatalf("failed import returned count %d and saved %d reports, want 0", count, len(store.reports))
			}
		})
	}
}

func TestImportReportsWrapsStoreError(t *testing.T) {
	db := openImportTestBadger(t)
	putImportTestValue(t, db, "repos-github.com/example/one", []byte(`{}`))
	saveErr := errors.New("storage unavailable")
	store := &importTestStore{saveErr: saveErr}

	count, err := importReports(context.Background(), db, store)
	if count != 0 || !errors.Is(err, saveErr) {
		t.Fatalf("import returned count %d and error %v, want count 0 wrapping storage error", count, err)
	}
	if !strings.Contains(err.Error(), `import report "github.com/example/one"`) {
		t.Fatalf("import error = %q, want repository context", err)
	}
}

func TestRunValidatesRequiredArgumentsBeforeOpeningExternalServices(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		url  string
		want string
	}{
		{name: "Badger path required", url: "postgres://unused", want: "-badger-path is required"},
		{name: "database URL required", args: []string{"-badger-path=unused"}, want: "DATABASE_URL is required"},
		{name: "blank database URL rejected before connection", args: []string{"-badger-path=unused"}, url: "  ", want: "DATABASE_URL is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DATABASE_URL", tc.url)
			configureImportRun(t, tc.args...)

			err := run()
			if err == nil || err.Error() != tc.want {
				t.Fatalf("run error = %v, want %q", err, tc.want)
			}
		})
	}
}

func configureImportRun(t *testing.T, args ...string) {
	t.Helper()
	originalCommandLine := flag.CommandLine
	originalArgs := os.Args
	flag.CommandLine = flag.NewFlagSet("import-badger-test", flag.ContinueOnError)
	os.Args = append([]string{"import-badger"}, args...)
	t.Cleanup(func() {
		flag.CommandLine = originalCommandLine
		os.Args = originalArgs
	})
}

func TestReadLegacyRecentWrapsClosedDatabaseError(t *testing.T) {
	db, err := badger.Open(badger.DefaultOptions(filepath.Join(t.TempDir(), "badger")).WithLogger(nil))
	if err != nil {
		t.Fatalf("open test Badger database: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close test Badger database: %v", err)
	}

	items, err := readLegacyRecent(db)
	if err == nil || !strings.Contains(err.Error(), "read legacy recent repositories") {
		t.Fatalf("read error = %v, want contextual closed-database error", err)
	}
	if items != nil {
		t.Fatalf("recent entries = %+v, want nil after database error", items)
	}
}

func TestReadLegacyRecentTransactionPropagatesUnexpectedGetError(t *testing.T) {
	getErr := errors.New("Badger read failed")
	getter := failingLegacyRecentGetter{err: getErr}
	var recent []legacyRecentItem

	err := readLegacyRecentTransaction(&getter, &recent)
	if !errors.Is(err, getErr) {
		t.Fatalf("recent transaction error = %v, want %v", err, getErr)
	}
	if getter.key != "recent" {
		t.Fatalf("requested key = %q, want recent", getter.key)
	}
	if recent != nil {
		t.Fatalf("recent values = %+v, want nil on read failure", recent)
	}
}

type failingLegacyRecentGetter struct {
	err error
	key string
}

func (g *failingLegacyRecentGetter) Get(key []byte) (*badger.Item, error) {
	g.key = string(key)
	return nil, g.err
}

func TestRunImportsReportsAndRecentRepositoriesThroughInjectedStore(t *testing.T) {
	preserveImporterState(t)
	recentJSON, err := json.Marshal([]legacyRecentItem{{Repo: "missing-report"}, {Repo: "github.com/example/imported"}})
	if err != nil {
		t.Fatal(err)
	}
	path := createRunBadger(t, map[string][]byte{
		"repos-github.com/example/imported": []byte(`{"average":0.95,"files":8}`),
		"recent":                            recentJSON,
	})
	store := &importTestStore{addRecentErrs: map[string]error{"missing-report": storage.ErrRepositoryNotFound}}
	openPostgresStore = func(_ context.Context, databaseURL string) (importerStore, error) {
		if databaseURL != "postgres://test" {
			t.Errorf("database URL = %q, want postgres://test", databaseURL)
		}
		return store, nil
	}
	t.Setenv("DATABASE_URL", "postgres://test")
	configureImportRun(t, "-badger-path="+path)

	output := captureImporterLog(t, main)
	if store.closeCalls != 1 {
		t.Fatalf("store close calls = %d, want 1", store.closeCalls)
	}
	if len(store.reports) != 1 || store.reports[0].Repo != "github.com/example/imported" || store.reports[0].Files != 8 {
		t.Fatalf("imported reports = %+v, want the report keyed by repository path", store.reports)
	}
	if !reflect.DeepEqual(store.recent, []string{"missing-report", "github.com/example/imported"}) {
		t.Fatalf("recent calls = %v, want both legacy entries", store.recent)
	}
	for _, want := range []string{
		`Skipping recent repository "missing-report" because it has no imported report`,
		"Imported 1 reports and 1 recent repository entries",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("import log %q does not contain %q", output, want)
		}
	}
}

func TestRunPropagatesPostgresAndBadgerOpenErrors(t *testing.T) {
	for _, tc := range []struct {
		name      string
		pgErr     error
		badgerErr error
		want      string
	}{
		{name: "PostgreSQL open", pgErr: errors.New("postgres unavailable"), want: "postgres unavailable"},
		{name: "Badger open", badgerErr: errors.New("Badger unavailable"), want: "open legacy Badger database: Badger unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			preserveImporterState(t)
			store := &importTestStore{}
			openPostgresStore = func(context.Context, string) (importerStore, error) {
				if tc.pgErr != nil {
					return nil, tc.pgErr
				}
				return store, nil
			}
			if tc.badgerErr != nil {
				openLegacyBadger = func(string) (*badger.DB, error) { return nil, tc.badgerErr }
			}
			t.Setenv("DATABASE_URL", "postgres://test")
			configureImportRun(t, "-badger-path=unused")

			err := run()
			if err == nil || err.Error() != tc.want {
				t.Fatalf("run error = %v, want %q", err, tc.want)
			}
			if tc.pgErr == nil && store.closeCalls != 1 {
				t.Fatalf("store close calls = %d, want 1", store.closeCalls)
			}
		})
	}
}

func TestRunPropagatesReportRecentReadAndAddErrors(t *testing.T) {
	tests := []struct {
		name    string
		values  map[string][]byte
		addErrs map[string]error
		wantErr string
	}{
		{
			name: "report decoding",
			values: map[string][]byte{
				"repos-github.com/example/broken": []byte(`{`),
			},
			wantErr: "decode Badger report",
		},
		{
			name: "recent decoding",
			values: map[string][]byte{
				"recent": []byte(`{`),
			},
			wantErr: "read legacy recent repositories",
		},
		{
			name: "recent store update",
			values: map[string][]byte{
				"recent": []byte(`[{"Repo":"github.com/example/repo"}]`),
			},
			addErrs: map[string]error{"github.com/example/repo": errors.New("recent update failed")},
			wantErr: `import recent repository "github.com/example/repo"`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			preserveImporterState(t)
			path := createRunBadger(t, tc.values)
			store := &importTestStore{addRecentErrs: tc.addErrs}
			openPostgresStore = func(context.Context, string) (importerStore, error) { return store, nil }
			t.Setenv("DATABASE_URL", "postgres://test")
			configureImportRun(t, "-badger-path="+path)

			err := run()
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("run error = %v, want error containing %q", err, tc.wantErr)
			}
			if store.closeCalls != 1 {
				t.Fatalf("store close calls = %d, want 1", store.closeCalls)
			}
		})
	}
}

func TestMainLogsFatalRunError(t *testing.T) {
	preserveImporterState(t)
	openErr := errors.New("postgres unavailable")
	openPostgresStore = func(context.Context, string) (importerStore, error) { return nil, openErr }
	t.Setenv("DATABASE_URL", "postgres://test")
	configureImportRun(t, "-badger-path=unused")
	var fatalMessage string
	fatalLog = func(args ...interface{}) { fatalMessage = fmt.Sprint(args...) }

	main()
	if fatalMessage != openErr.Error() {
		t.Fatalf("fatal message = %q, want %q", fatalMessage, openErr)
	}
}

func preserveImporterState(t *testing.T) {
	t.Helper()
	oldPostgresOpener, oldBadgerOpener, oldFatalLog := openPostgresStore, openLegacyBadger, fatalLog
	t.Cleanup(func() {
		openPostgresStore, openLegacyBadger, fatalLog = oldPostgresOpener, oldBadgerOpener, oldFatalLog
	})
}

func createRunBadger(t *testing.T, values map[string][]byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legacy")
	db, err := badger.Open(badger.DefaultOptions(path).WithLogger(nil))
	if err != nil {
		t.Fatalf("open fixture Badger database: %v", err)
	}
	for key, value := range values {
		if err := db.Update(func(txn *badger.Txn) error { return txn.Set([]byte(key), value) }); err != nil {
			db.Close()
			t.Fatalf("write fixture Badger value %q: %v", key, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture Badger database: %v", err)
	}
	return path
}

func captureImporterLog(t *testing.T, run func()) string {
	t.Helper()
	originalWriter := log.Writer()
	originalFlags := log.Flags()
	originalPrefix := log.Prefix()
	var output bytes.Buffer
	log.SetOutput(&output)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(originalWriter)
		log.SetFlags(originalFlags)
		log.SetPrefix(originalPrefix)
	})
	run()
	return output.String()
}

func TestReadLegacyRecentHandlesMissingValidAndInvalidValues(t *testing.T) {
	db := openImportTestBadger(t)
	items, err := readLegacyRecent(db)
	if err != nil || items != nil {
		t.Fatalf("missing recent entry = (%v, %v), want (nil, nil)", items, err)
	}

	want := []legacyRecentItem{{Repo: "github.com/example/one"}, {Repo: "codeberg.org/example/two"}}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	putImportTestValue(t, db, "recent", encoded)
	items, err = readLegacyRecent(db)
	if err != nil {
		t.Fatalf("read valid recent entry: %v", err)
	}
	if !reflect.DeepEqual(items, want) {
		t.Fatalf("recent entries = %+v, want %+v", items, want)
	}

	putImportTestValue(t, db, "recent", []byte(`{`))
	items, err = readLegacyRecent(db)
	if err == nil || !strings.Contains(err.Error(), "read legacy recent repositories") {
		t.Fatalf("invalid recent error = %v, want contextual decode error", err)
	}
	if items != nil {
		t.Fatalf("invalid recent entries = %+v, want nil", items)
	}
}
