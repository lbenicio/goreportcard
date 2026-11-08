package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"reflect"
	"testing"
)

type repositoryAdminStoreStub struct {
	duplicates    []string
	duplicateErr  error
	deleteResults map[string]bool
	deleteErrors  map[string]error
	deleted       []string
	closeCalls    int
}

func (s *repositoryAdminStoreStub) DeleteRepository(_ context.Context, repo string) (bool, error) {
	s.deleted = append(s.deleted, repo)
	return s.deleteResults[repo], s.deleteErrors[repo]
}

func (s *repositoryAdminStoreStub) DuplicateRepositoryPaths(context.Context) ([]string, error) {
	return s.duplicates, s.duplicateErr
}

func (s *repositoryAdminStoreStub) Close() error {
	s.closeCalls++
	return nil
}

func preserveDBToolState(t *testing.T) {
	t.Helper()
	oldDeleteRepo, oldRemoveDuplicates, oldDryRun := *deleteRepoName, *removeDuplicatesFlag, *dryRun
	oldOpen, oldFatal := openRepositoryStore, fatalLog
	*deleteRepoName, *removeDuplicatesFlag, *dryRun = "", false, false
	t.Cleanup(func() {
		*deleteRepoName, *removeDuplicatesFlag, *dryRun = oldDeleteRepo, oldRemoveDuplicates, oldDryRun
		openRepositoryStore, fatalLog = oldOpen, oldFatal
	})
}

func stubRepositoryStore(t *testing.T, store repositoryAdminStore, openErr error) *int {
	t.Helper()
	calls := 0
	openRepositoryStore = func(context.Context, string) (repositoryAdminStore, error) {
		calls++
		return store, openErr
	}
	return &calls
}

func captureDBToolLog(t *testing.T, run func()) string {
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

func TestRunRequiresDatabaseURLBeforeOpeningStore(t *testing.T) {
	preserveDBToolState(t)
	t.Setenv("DATABASE_URL", "")
	calls := stubRepositoryStore(t, &repositoryAdminStoreStub{}, nil)

	err := run()
	if err == nil || err.Error() != "DATABASE_URL is required" {
		t.Fatalf("run error = %v, want DATABASE_URL required error", err)
	}
	if *calls != 0 {
		t.Fatalf("store opener called %d times without DATABASE_URL, want 0", *calls)
	}
}

func TestRunRejectsWhitespaceDatabaseURLBeforeConnecting(t *testing.T) {
	preserveDBToolState(t)
	t.Setenv("DATABASE_URL", "  \t")

	err := run()
	if err == nil || err.Error() != "DATABASE_URL is required" {
		t.Fatalf("run error = %v, want whitespace DATABASE_URL to be rejected before connecting", err)
	}
}

func TestRunPropagatesStoreOpenError(t *testing.T) {
	preserveDBToolState(t)
	t.Setenv("DATABASE_URL", "postgres://test")
	openErr := errors.New("store unavailable")
	calls := stubRepositoryStore(t, nil, openErr)

	err := run()
	if !errors.Is(err, openErr) {
		t.Fatalf("run error = %v, want wrapped store-open error", err)
	}
	if *calls != 1 {
		t.Fatalf("store opener called %d times, want 1", *calls)
	}
}

func TestRunExecutesRequestedDryRunActionsAndClosesStore(t *testing.T) {
	preserveDBToolState(t)
	t.Setenv("DATABASE_URL", "postgres://test")
	store := &repositoryAdminStoreStub{duplicates: []string{"github.com/example/duplicate"}}
	calls := stubRepositoryStore(t, store, nil)
	*deleteRepoName = "github.com/example/requested"
	*removeDuplicatesFlag = true
	*dryRun = true

	output := captureDBToolLog(t, func() {
		if err := run(); err != nil {
			t.Fatalf("run dry-run actions: %v", err)
		}
	})
	if *calls != 1 || store.closeCalls != 1 {
		t.Fatalf("open calls = %d, close calls = %d; want 1 each", *calls, store.closeCalls)
	}
	if len(store.deleted) != 0 {
		t.Fatalf("dry run deleted repositories: %v", store.deleted)
	}
	for _, repo := range []string{"github.com/example/requested", "github.com/example/duplicate"} {
		if !bytes.Contains([]byte(output), []byte("would delete "+repo+"\n")) {
			t.Errorf("dry-run log %q does not mention %q", output, repo)
		}
	}
}

func TestRunClosesStoreWhenNoActionIsRequested(t *testing.T) {
	preserveDBToolState(t)
	t.Setenv("DATABASE_URL", "postgres://test")
	store := &repositoryAdminStoreStub{}
	stubRepositoryStore(t, store, nil)

	if err := run(); err != nil {
		t.Fatalf("run with no action: %v", err)
	}
	if store.closeCalls != 1 {
		t.Fatalf("store close calls = %d, want 1", store.closeCalls)
	}
}

func TestRunPropagatesActionErrorsAndClosesStore(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*repositoryAdminStoreStub)
		flags func()
		want  error
	}{
		{
			name: "single repository deletion",
			setup: func(store *repositoryAdminStoreStub) {
				store.deleteErrors = map[string]error{"repo": errors.New("delete failed")}
			},
			flags: func() { *deleteRepoName = "repo" },
			want:  errors.New("delete failed"),
		},
		{
			name:  "duplicate listing",
			setup: func(store *repositoryAdminStoreStub) { store.duplicateErr = errors.New("list failed") },
			flags: func() { *removeDuplicatesFlag = true },
			want:  errors.New("list failed"),
		},
		{
			name: "duplicate deletion",
			setup: func(store *repositoryAdminStoreStub) {
				store.duplicates = []string{"duplicate"}
				store.deleteErrors = map[string]error{"duplicate": errors.New("duplicate delete failed")}
			},
			flags: func() { *removeDuplicatesFlag = true },
			want:  errors.New("duplicate delete failed"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			preserveDBToolState(t)
			t.Setenv("DATABASE_URL", "postgres://test")
			store := &repositoryAdminStoreStub{}
			tc.setup(store)
			stubRepositoryStore(t, store, nil)
			tc.flags()

			err := run()
			if err == nil || err.Error() != tc.want.Error() {
				t.Fatalf("run error = %v, want %q", err, tc.want)
			}
			if store.closeCalls != 1 {
				t.Fatalf("store close calls = %d, want 1", store.closeCalls)
			}
		})
	}
}

func TestDeleteRepoDryRunDoesNotCallStore(t *testing.T) {
	store := &repositoryAdminStoreStub{}
	output := captureDBToolLog(t, func() {
		if err := deleteRepo(context.Background(), store, "github.com/example/project", true); err != nil {
			t.Fatalf("dry-run delete: %v", err)
		}
	})
	if len(store.deleted) != 0 {
		t.Fatalf("dry run called DeleteRepository for %v", store.deleted)
	}
	if got, want := output, "would delete github.com/example/project\n"; got != want {
		t.Fatalf("dry-run log = %q, want %q", got, want)
	}
}

func TestDeleteRepoReportsMissingAndPropagatesErrors(t *testing.T) {
	store := &repositoryAdminStoreStub{
		deleteResults: map[string]bool{"existing": true},
		deleteErrors:  map[string]error{"broken": errors.New("delete failed")},
	}
	output := captureDBToolLog(t, func() {
		if err := deleteRepo(context.Background(), store, "missing", false); err != nil {
			t.Fatalf("delete missing repository: %v", err)
		}
		if err := deleteRepo(context.Background(), store, "existing", false); err != nil {
			t.Fatalf("delete existing repository: %v", err)
		}
		if err := deleteRepo(context.Background(), store, "broken", false); err == nil || err.Error() != "delete failed" {
			t.Fatalf("delete error = %v, want delete failed", err)
		}
	})
	if !bytes.Contains([]byte(output), []byte("repository not found: missing\n")) {
		t.Fatalf("log output %q does not report missing repository", output)
	}
	if !reflect.DeepEqual(store.deleted, []string{"missing", "existing", "broken"}) {
		t.Fatalf("delete calls = %v, want [missing existing broken]", store.deleted)
	}
}

func TestRemoveDuplicatesDeletesOnlyReportedCaseDuplicates(t *testing.T) {
	store := &repositoryAdminStoreStub{
		duplicates:    []string{"case-missing", "case-deleted"},
		deleteResults: map[string]bool{"case-deleted": true},
	}
	output := captureDBToolLog(t, func() {
		if err := removeDuplicates(context.Background(), store, false); err != nil {
			t.Fatalf("remove duplicates: %v", err)
		}
	})
	if !reflect.DeepEqual(store.deleted, []string{"case-missing", "case-deleted"}) {
		t.Fatalf("delete calls = %v, want both case duplicates", store.deleted)
	}
	if got, want := output, "Deleted case-deleted\n"; got != want {
		t.Fatalf("deletion log = %q, want %q", got, want)
	}
}

func TestRemoveDuplicatesPropagatesListAndDeleteErrors(t *testing.T) {
	listErr := errors.New("list failed")
	if err := removeDuplicates(context.Background(), &repositoryAdminStoreStub{duplicateErr: listErr}, false); !errors.Is(err, listErr) {
		t.Fatalf("duplicate-list error = %v, want %v", err, listErr)
	}
	deleteErr := errors.New("delete failed")
	store := &repositoryAdminStoreStub{
		duplicates:   []string{"duplicate"},
		deleteErrors: map[string]error{"duplicate": deleteErr},
	}
	if err := removeDuplicates(context.Background(), store, false); !errors.Is(err, deleteErr) {
		t.Fatalf("duplicate-delete error = %v, want %v", err, deleteErr)
	}
}

func TestMainReportsRunErrorThroughFatalLogger(t *testing.T) {
	preserveDBToolState(t)
	t.Setenv("DATABASE_URL", "")
	var got string
	fatalLog = func(args ...any) { got = fmt.Sprint(args...) }

	main()
	if got != "DATABASE_URL is required" {
		t.Fatalf("fatal message = %q, want DATABASE_URL required", got)
	}
}

func TestMainReturnsNormallyAfterSuccessfulRun(t *testing.T) {
	preserveDBToolState(t)
	t.Setenv("DATABASE_URL", "postgres://test")
	store := &repositoryAdminStoreStub{}
	stubRepositoryStore(t, store, nil)

	main()
	if store.closeCalls != 1 {
		t.Fatalf("store close calls = %d, want 1", store.closeCalls)
	}
}
