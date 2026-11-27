package main

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDirSizeCountsFilesRecursively(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "nested", "deeper"), 0755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		filepath.Join(root, "root.txt"):                     "abc",
		filepath.Join(root, "nested", "deeper", "file.txt"): "12345",
	} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	size, err := DirSize(root)
	if err != nil {
		t.Fatalf("DirSize: %v", err)
	}
	if size != 8 {
		t.Fatalf("directory size = %d, want 8", size)
	}
}

func TestDirSizeReturnsZeroForEmptyDirectory(t *testing.T) {
	size, err := DirSize(t.TempDir())
	if err != nil {
		t.Fatalf("DirSize: %v", err)
	}
	if size != 0 {
		t.Fatalf("empty directory size = %d, want 0", size)
	}
}

func TestMainRealModeDeletesOnlyExpiredOrSmallRepositoriesInTempWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name        string
		fileSize    int64
		old         bool
		wantDeleted bool
	}{
		{name: "expired repository", fileSize: 1, old: true, wantDeleted: true},
		{name: "small repository", fileSize: 1, wantDeleted: true},
		{name: "size threshold is exclusive", fileSize: 20 * 1000 * 1000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workDir := t.TempDir()
			originalDir, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chdir(workDir); err != nil {
				t.Fatalf("change to test working directory: %v", err)
			}
			t.Cleanup(func() {
				if err := os.Chdir(originalDir); err != nil {
					t.Errorf("restore working directory: %v", err)
				}
			})

			repoDir := filepath.Join("_repos", "src", "github.com", "example", "project")
			if err := os.MkdirAll(repoDir, 0755); err != nil {
				t.Fatal(err)
			}
			file, err := os.Create(filepath.Join(repoDir, "payload"))
			if err != nil {
				t.Fatal(err)
			}
			if err := file.Truncate(tc.fileSize); err != nil {
				file.Close()
				t.Fatalf("set test repository size: %v", err)
			}
			if err := file.Close(); err != nil {
				t.Fatalf("close test repository file: %v", err)
			}
			if tc.old {
				old := time.Now().Add(-31 * 24 * time.Hour)
				if err := os.Chtimes(filepath.Dir(repoDir), old, old); err != nil {
					t.Fatalf("set repository owner directory age: %v", err)
				}
			}

			originalReal := *real
			*real = true
			t.Cleanup(func() { *real = originalReal })

			main()

			_, err = os.Stat(repoDir)
			if tc.wantDeleted && !os.IsNotExist(err) {
				t.Fatalf("eligible repository stat error = %v, want repository removed", err)
			}
			if !tc.wantDeleted && err != nil {
				t.Fatalf("repository at size threshold was removed: %v", err)
			}
		})
	}
}

func TestMainDryRunPreservesSmallRepository(t *testing.T) {
	workDir := t.TempDir()
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("change to test working directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	repoDir := filepath.Join("_repos", "src", "github.com", "example", "project")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("small"), 0600); err != nil {
		t.Fatal(err)
	}
	ignoredRepo := filepath.Join("_repos", "src", "gitlab.com", "example", "project")
	if err := os.MkdirAll(ignoredRepo, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ignoredRepo, "README.md"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-31 * 24 * time.Hour)
	if err := os.Chtimes(filepath.Dir(repoDir), old, old); err != nil {
		t.Fatalf("set repository owner directory age: %v", err)
	}

	originalReal := *real
	*real = false
	t.Cleanup(func() { *real = originalReal })
	originalWriter := log.Writer()
	var output bytes.Buffer
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(originalWriter) })

	main()

	if _, err := os.Stat(filepath.Join(repoDir, "README.md")); err != nil {
		t.Fatalf("dry run changed repository contents: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ignoredRepo, "README.md")); err != nil {
		t.Fatalf("non-GitHub repository was changed: %v", err)
	}
	for _, want := range []string{
		"Would delete _repos/src/github.com/example/project (repo is old)",
		"Would delete _repos/src/github.com/example/project (dir size < 20M)",
	} {
		if !bytes.Contains(output.Bytes(), []byte(want)) {
			t.Errorf("dry-run output = %q, want it to contain %q", output.String(), want)
		}
	}
}

func TestRunPropagatesFilesystemErrors(t *testing.T) {
	for _, tc := range []struct {
		name     string
		failAt   string
		failInfo bool
		failSize bool
	}{
		{name: "repository root read", failAt: "_repos/src"},
		{name: "GitHub directory read", failAt: "_repos/src/github.com"},
		{name: "owner directory read", failAt: "_repos/src/github.com/example"},
		{name: "owner information", failInfo: true},
		{name: "repository size", failSize: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			preserveCleanerState(t)
			workspace := t.TempDir()
			repoPath := filepath.Join(workspace, "_repos", "src", "github.com", "example", "project")
			if err := os.MkdirAll(repoPath, 0755); err != nil {
				t.Fatal(err)
			}
			githubEntries, err := os.ReadDir(filepath.Join(workspace, "_repos", "src"))
			if err != nil {
				t.Fatal(err)
			}
			ownerEntries, err := os.ReadDir(filepath.Join(workspace, "_repos", "src", "github.com"))
			if err != nil {
				t.Fatal(err)
			}
			repositoryEntries, err := os.ReadDir(filepath.Join(workspace, "_repos", "src", "github.com", "example"))
			if err != nil {
				t.Fatal(err)
			}
			injectedErr := errors.New("injected filesystem failure")
			readDirectory = func(path string) ([]os.DirEntry, error) {
				if path == tc.failAt {
					return nil, injectedErr
				}
				switch path {
				case "_repos/src":
					return githubEntries, nil
				case "_repos/src/github.com":
					return ownerEntries, nil
				case "_repos/src/github.com/example":
					return repositoryEntries, nil
				default:
					return nil, fmt.Errorf("unexpected directory read %q", path)
				}
			}
			if tc.failInfo {
				entryInfo = func(os.DirEntry) (os.FileInfo, error) { return nil, injectedErr }
			}
			if tc.failSize {
				entryInfo = func(os.DirEntry) (os.FileInfo, error) {
					return os.Stat(filepath.Join(workspace, "_repos", "src", "github.com", "example"))
				}
				directorySize = func(string) (int64, error) { return 0, injectedErr }
			}
			*real = false

			if err := run(); !errors.Is(err, injectedErr) {
				t.Fatalf("run error = %v, want injected filesystem error", err)
			}
		})
	}
}

func TestMainRoutesFilesystemErrorToFatalLogger(t *testing.T) {
	preserveCleanerState(t)
	injectedErr := errors.New("cannot read repositories")
	readDirectory = func(string) ([]os.DirEntry, error) { return nil, injectedErr }
	var fatalMessage string
	fatalLog = func(args ...interface{}) { fatalMessage = fmt.Sprint(args...) }

	main()
	if fatalMessage != injectedErr.Error() {
		t.Fatalf("fatal message = %q, want %q", fatalMessage, injectedErr)
	}
}

func preserveCleanerState(t *testing.T) {
	t.Helper()
	oldReal := *real
	oldReadDirectory, oldEntryInfo := readDirectory, entryInfo
	oldRemovePath, oldDirectorySize, oldFatalLog := removePath, directorySize, fatalLog
	t.Cleanup(func() {
		*real = oldReal
		readDirectory, entryInfo = oldReadDirectory, oldEntryInfo
		removePath, directorySize, fatalLog = oldRemovePath, oldDirectorySize, oldFatalLog
	})
}

func TestMainSkipsGithubEntryWhenItIsNotADirectory(t *testing.T) {
	workDir := t.TempDir()
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("change to test working directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	if err := os.MkdirAll(filepath.Join("_repos", "src"), 0755); err != nil {
		t.Fatal(err)
	}
	githubEntry := filepath.Join("_repos", "src", "github.com")
	if err := os.WriteFile(githubEntry, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}

	main()

	if contents, err := os.ReadFile(githubEntry); err != nil || string(contents) != "not a directory" {
		t.Fatalf("non-directory github.com entry = %q, %v; want original file preserved", contents, err)
	}
}
