package download

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownloadFallsBackToGitHubForUnpublishedModule(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"

	fakeBin := t.TempDir()
	fakeGit := "#!/bin/sh\n" +
		"for arg do\n  if [ \"$arg\" = \"clone\" ]; then command=clone; fi\ndone\n" +
		"if [ \"$command\" = \"clone\" ]; then\n  for target do :; done\n  mkdir -p \"$target\"\n  printf 'package main\\n' > \"$target/main.go\"\n  exit 0\nfi\n" +
		"if [ \"$1\" = \"-C\" ]; then printf '%s\\n' '" + commit + "'; exit 0; fi\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "git"), []byte(fakeGit), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer proxy.Close()

	client := NewProxyClient(proxy.URL)
	downloaded, err := client.Download("github.com/lbenicio/kubernetes-dashboard")
	if err != nil {
		t.Fatal(err)
	}
	if downloaded.Version != commit {
		t.Errorf("downloaded.Version = %q, want %q", downloaded.Version, commit)
	}
	if !strings.Contains(downloaded.Dir, "kubernetes-dashboard@"+commit) {
		t.Errorf("downloaded.Dir = %q, want commit-qualified GitHub checkout", downloaded.Dir)
	}
	if _, err := os.Stat(filepath.Join(downloaded.Dir, "main.go")); err != nil {
		t.Fatalf("downloaded source file missing: %v", err)
	}
	if err := downloaded.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(downloaded.Dir); !os.IsNotExist(err) {
		t.Errorf("GitHub checkout still exists or could not be checked: %v", err)
	}
}

func TestDownloadDoesNotFallBackForProxyServerErrors(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer proxy.Close()

	client := NewProxyClient(proxy.URL)
	_, err := client.Download("github.com/lbenicio/kubernetes-dashboard")
	if err == nil || !strings.Contains(err.Error(), "HTTP 503") || strings.Contains(err.Error(), "GitHub source fallback") {
		t.Fatalf("Download error = %v, want proxy HTTP 503 without GitHub fallback", err)
	}
}

func TestParseGitHubRepository(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		{
			name:      "import path",
			path:      "github.com/lbenicio/kubernetes-dashboard",
			wantOwner: "lbenicio",
			wantRepo:  "kubernetes-dashboard",
		},
		{
			name:      "GitHub URL with git suffix",
			path:      "https://github.com/lbenicio/kubernetes-dashboard.git",
			wantOwner: "lbenicio",
			wantRepo:  "kubernetes-dashboard",
		},
		{
			name:      "URL with credentials is normalized without using them",
			path:      "https://user@github.com/lbenicio/kubernetes-dashboard",
			wantOwner: "lbenicio",
			wantRepo:  "kubernetes-dashboard",
		},
		{
			name:    "non-GitHub host",
			path:    "github.com.example/lbenicio/kubernetes-dashboard",
			wantErr: true,
		},
		{
			name:    "nested repository path",
			path:    "github.com/lbenicio/kubernetes-dashboard/tree/main",
			wantErr: true,
		},
		{
			name:    "path traversal segment",
			path:    "github.com/../kubernetes-dashboard",
			wantErr: true,
		},
		{
			name:    "invalid path characters",
			path:    "github.com/lbenicio/kubernetes-dashboard?ref=main",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo, err := parseGitHubRepository(tt.path)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseGitHubRepository(%q) error = %v, wantErr %t", tt.path, err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if owner != tt.wantOwner || repo != tt.wantRepo {
				t.Errorf("parseGitHubRepository(%q) = (%q, %q), want (%q, %q)", tt.path, owner, repo, tt.wantOwner, tt.wantRepo)
			}
		})
	}
}

func installFakeGit(t *testing.T, mode, commit string) {
	t.Helper()

	fakeBin := t.TempDir()
	fakeGit := `#!/bin/sh
command=""
for arg do
  if [ "$arg" = "clone" ]; then command=clone; fi
done
if [ "$command" = "clone" ]; then
  if [ "$FAKE_GIT_MODE" = "clone-error" ]; then echo "clone failed deliberately" >&2; exit 1; fi
  if [ "$FAKE_GIT_MODE" != "rename-error" ]; then
    for target do :; done
    mkdir -p "$target"
    printf 'package main' > "$target/main.go"
  fi
  exit 0
fi
if [ "$1" = "-C" ]; then
  if [ "$FAKE_GIT_MODE" = "revision-error" ]; then echo "revision failed deliberately" >&2; exit 1; fi
  printf '%s' "$FAKE_GIT_COMMIT"
  exit 0
fi
exit 2
`
	if err := os.WriteFile(filepath.Join(fakeBin, "git"), []byte(fakeGit), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_GIT_MODE", mode)
	t.Setenv("FAKE_GIT_COMMIT", commit)
}

func TestDownloadGitHubRepositoryRejectsInvalidPathBeforeCreatingCache(t *testing.T) {
	t.Chdir(t.TempDir())

	if _, err := downloadGitHubRepository("github.com/../repo"); err == nil {
		t.Fatal("downloadGitHubRepository accepted an invalid GitHub path")
	}
	if _, err := os.Stat(reposDir); !os.IsNotExist(err) {
		t.Errorf("cache directory was created for invalid path or could not be checked: %v", err)
	}
}

func TestDownloadGitHubRepositoryCacheCreationFailure(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("_repos", []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := downloadGitHubRepository("github.com/user/repo"); err == nil {
		t.Fatal("downloadGitHubRepository succeeded when the cache root was a file")
	}
}

func TestDownloadGitHubRepositoryFailuresCleanTemporaryDirectory(t *testing.T) {
	const validCommit = "0123456789abcdef0123456789abcdef01234567"
	tests := []struct {
		name       string
		mode       string
		commit     string
		wantErrMsg string
	}{
		{name: "clone failure", mode: "clone-error", commit: validCommit, wantErrMsg: "GitHub clone failed"},
		{name: "revision failure", mode: "revision-error", commit: validCommit, wantErrMsg: "could not read GitHub revision"},
		{name: "invalid commit ID", mode: "success", commit: "not-a-commit", wantErrMsg: "invalid commit ID"},
		{name: "checkout rename failure", mode: "rename-error", commit: validCommit, wantErrMsg: "could not prepare GitHub checkout"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			installFakeGit(t, tt.mode, tt.commit)

			if _, err := downloadGitHubRepository("github.com/user/repo"); err == nil || !strings.Contains(err.Error(), tt.wantErrMsg) {
				t.Fatalf("downloadGitHubRepository error = %v, want error containing %q", err, tt.wantErrMsg)
			}
			entries, err := os.ReadDir(reposDir)
			if err != nil {
				t.Fatalf("read cache directory after failed download: %v", err)
			}
			if len(entries) != 0 {
				t.Errorf("failed download left temporary entries in cache: %v", entries)
			}
		})
	}
}

func TestGitHubGitEnvironmentFiltersInheritedGitConfiguration(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "protocol.file.allow")
	t.Setenv("GIT_CONFIG_VALUE_0", "always")
	t.Setenv("GIT_DIR", "/tmp/inherited-git-dir")
	t.Setenv("GIT_WORK_TREE", "/tmp/inherited-work-tree")
	t.Setenv("DOWNLOAD_TEST_ENV", "preserved")

	values := make(map[string]string)
	for _, entry := range githubGitEnvironment() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}

	for _, key := range []string{"GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0", "GIT_DIR", "GIT_WORK_TREE"} {
		if _, ok := values[key]; ok {
			t.Errorf("githubGitEnvironment retained inherited %s", key)
		}
	}
	for key, want := range map[string]string{
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_CONFIG_GLOBAL":   "/dev/null",
		"GIT_TERMINAL_PROMPT": "0",
		"GIT_LFS_SKIP_SMUDGE": "1",
		"DOWNLOAD_TEST_ENV":   "preserved",
	} {
		if got := values[key]; got != want {
			t.Errorf("githubGitEnvironment[%q] = %q, want %q", key, got, want)
		}
	}
}

func TestValidCommitHash(t *testing.T) {
	tests := []struct {
		name   string
		commit string
		want   bool
	}{
		{name: "SHA-1", commit: "0123456789abcdef0123456789abcdef01234567", want: true},
		{name: "SHA-256", commit: strings.Repeat("a", 64), want: true},
		{name: "unsupported length", commit: "abcd", want: false},
		{name: "non-hexadecimal digits", commit: strings.Repeat("g", 40), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validCommitHash(tt.commit); got != tt.want {
				t.Errorf("validCommitHash(%q) = %t, want %t", tt.commit, got, tt.want)
			}
		})
	}
}
