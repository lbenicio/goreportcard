package download

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	testProxyModule  = "github.com/user/module"
	testProxyVersion = "v0.1.0"
)

func newTestProxyClient(url string) *ProxyClient {
	client := NewProxyClient(url)
	return &client
}

func makeTestModuleArchive(t *testing.T) []byte {
	t.Helper()

	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	file, err := writer.Create(testProxyModule + "@" + testProxyVersion + "/go.mod")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(file, "module "+testProxyModule+"\n"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func testProxyArchiveHandler(archive []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + testProxyModule + "/@latest":
			_, _ = io.WriteString(w, `{"Version":"`+testProxyVersion+`"}`)
		case "/" + testProxyModule + "/@v/" + testProxyVersion + ".zip":
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}
}

func installTestCommand(t *testing.T, name, script string) {
	t.Helper()

	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func writeTruncatedHTTPResponse(t *testing.T, w http.ResponseWriter) {
	t.Helper()

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		t.Error("HTTP test server does not support hijacking")
		return
	}
	conn, _, err := hijacker.Hijack()
	if err != nil {
		t.Errorf("hijack HTTP connection: %v", err)
		return
	}
	_, writeErr := io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 64\r\nConnection: close\r\n\r\npartial body")
	if writeErr != nil {
		t.Errorf("write truncated HTTP response: %v", writeErr)
	}
	if err := conn.Close(); err != nil {
		t.Errorf("close truncated HTTP response: %v", err)
	}
}

func TestDownloadUsesProxyWhenModuleIsAvailable(t *testing.T) {
	t.Chdir(t.TempDir())
	archive := makeTestModuleArchive(t)
	server := httptest.NewServer(testProxyArchiveHandler(archive))
	defer server.Close()

	downloaded, err := newTestProxyClient(server.URL).Download(testProxyModule)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := downloaded.Version, testProxyVersion; got != want {
		t.Errorf("Download version = %q, want %q", got, want)
	}
	goMod, err := os.ReadFile(filepath.Join(downloaded.Dir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(goMod), "module "+testProxyModule+"\n"; got != want {
		t.Errorf("downloaded go.mod = %q, want %q", got, want)
	}
	if err := downloaded.Cleanup(); err != nil {
		t.Fatal(err)
	}
}

func TestDownloadDoesNotFallbackForNonGitHubNotFound(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	_, err := newTestProxyClient(server.URL).Download("example.com/user/module")
	var responseErr *proxyResponseError
	if !errors.As(err, &responseErr) || responseErr.StatusCode != http.StatusNotFound {
		t.Fatalf("Download error = %v, want proxy HTTP 404 without fallback", err)
	}
}

func TestDownloadReportsGitHubFallbackFailure(t *testing.T) {
	t.Chdir(t.TempDir())
	installFakeGit(t, "clone-error", "0123456789abcdef0123456789abcdef01234567")
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	_, err := newTestProxyClient(server.URL).Download("github.com/user/module")
	if err == nil || !strings.Contains(err.Error(), "GitHub source fallback failed") || !strings.Contains(err.Error(), "GitHub clone failed") {
		t.Fatalf("Download error = %v, want proxy 404 and GitHub fallback failure", err)
	}
	entries, readErr := os.ReadDir(reposDir)
	if readErr != nil {
		t.Fatalf("read cache after failed fallback: %v", readErr)
	}
	if len(entries) != 0 {
		t.Errorf("failed fallback left temporary cache entries: %v", entries)
	}
}

func TestDownloadGitHubRepositoryReportsTemporaryDirectoryCreationFailure(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(reposDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(reposDir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chmod(reposDir, 0o700); err != nil {
			t.Errorf("restore cache permissions: %v", err)
		}
	}()

	if _, err := downloadGitHubRepository("github.com/user/module"); err == nil {
		t.Fatal("downloadGitHubRepository succeeded with a read-only cache directory")
	}
}

func TestLatestVersionErrorPaths(t *testing.T) {
	t.Run("invalid module path", func(t *testing.T) {
		if _, err := newTestProxyClient("http://unused.invalid").LatestVersion("../../outside"); err == nil || !strings.Contains(err.Error(), "invalid module path") {
			t.Fatalf("LatestVersion error = %v, want invalid module path", err)
		}
	})

	t.Run("request construction error", func(t *testing.T) {
		if _, err := newTestProxyClient("http://[::1").LatestVersion(testProxyModule); err == nil {
			t.Fatal("LatestVersion succeeded with a malformed proxy URL")
		}
	})

	t.Run("non-200 response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer server.Close()

		_, err := newTestProxyClient(server.URL).LatestVersion(testProxyModule)
		var responseErr *proxyResponseError
		if !errors.As(err, &responseErr) || responseErr.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("LatestVersion error = %v, want HTTP 503 response error", err)
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "not JSON")
		}))
		defer server.Close()

		if _, err := newTestProxyClient(server.URL).LatestVersion(testProxyModule); err == nil {
			t.Fatal("LatestVersion accepted an invalid JSON response")
		}
	})
}

func TestModuleNameRejectsInvalidProxyMetadata(t *testing.T) {
	t.Run("invalid module path", func(t *testing.T) {
		if _, err := newTestProxyClient("http://unused.invalid").ModuleName("../../outside"); err == nil || !strings.Contains(err.Error(), "invalid module path") {
			t.Fatalf("ModuleName error = %v, want invalid module path", err)
		}
	})

	t.Run("latest-version request error", func(t *testing.T) {
		server := httptest.NewServer(http.NotFoundHandler())
		defer server.Close()

		_, err := newTestProxyClient(server.URL).ModuleName(testProxyModule)
		var responseErr *proxyResponseError
		if !errors.As(err, &responseErr) || responseErr.StatusCode != http.StatusNotFound {
			t.Fatalf("ModuleName error = %v, want latest-version HTTP 404", err)
		}
	})

	tests := []struct {
		name          string
		latestVersion string
		modStatus     int
		modBody       string
		wantErr       string
	}{
		{name: "invalid latest version", latestVersion: "v2.0.0", wantErr: "invalid module version"},
		{name: "non-200 go.mod response", latestVersion: testProxyVersion, modStatus: http.StatusNotFound, wantErr: "HTTP 404"},
		{name: "malformed go.mod declaration", latestVersion: testProxyVersion, modBody: "not a module declaration\n", wantErr: "invalid go.mod"},
		{name: "invalid declared module path", latestVersion: testProxyVersion, modBody: "module ../outside\n", wantErr: "invalid module path in go.mod"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/"+testProxyModule+"/@latest" {
					_, _ = io.WriteString(w, `{"Version":"`+tt.latestVersion+`"}`)
					return
				}
				if tt.modStatus != 0 {
					w.WriteHeader(tt.modStatus)
					return
				}
				_, _ = io.WriteString(w, tt.modBody)
			}))
			defer server.Close()

			if _, err := newTestProxyClient(server.URL).ModuleName(testProxyModule); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ModuleName error = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestModuleNameReportsGoModRequestAndReadErrors(t *testing.T) {
	tests := []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request)
	}{
		{
			name: "request error after redirect",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/"+testProxyModule+"/@latest" {
					_, _ = io.WriteString(w, `{"Version":"`+testProxyVersion+`"}`)
					return
				}
				http.Redirect(w, r, "http://[", http.StatusFound)
			},
		},
		{
			name: "truncated response body",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/"+testProxyModule+"/@latest" {
					_, _ = io.WriteString(w, `{"Version":"`+testProxyVersion+`"}`)
					return
				}
				writeTruncatedHTTPResponse(t, w)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(tt.handler))
			defer server.Close()

			if _, err := newTestProxyClient(server.URL).ModuleName(testProxyModule); err == nil {
				t.Fatal("ModuleName succeeded despite a go.mod request/read failure")
			}
		})
	}
}

func TestProxyDownloadPropagatesLatestVersionError(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	_, err := newTestProxyClient(server.URL).ProxyDownload(testProxyModule)
	var responseErr *proxyResponseError
	if !errors.As(err, &responseErr) || responseErr.StatusCode != http.StatusNotFound {
		t.Fatalf("ProxyDownload error = %v, want latest-version HTTP 404", err)
	}
}

func TestProxyDownloadReportsArchiveRequestAndExtractionFailures(t *testing.T) {
	archive := makeTestModuleArchive(t)
	tests := []struct {
		name       string
		zipHandler func(http.ResponseWriter, *http.Request)
		unzip      string
		wantErr    string
	}{
		{
			name: "archive request error after redirect",
			zipHandler: func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "http://[", http.StatusFound)
			},
			wantErr: "Get",
		},
		{
			name: "non-200 archive response",
			zipHandler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
			},
			wantErr: "HTTP 503",
		},
		{
			name: "truncated archive response",
			zipHandler: func(w http.ResponseWriter, _ *http.Request) {
				writeTruncatedHTTPResponse(t, w)
			},
			wantErr: "unexpected EOF",
		},
		{
			name: "unzip command failure",
			zipHandler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write(archive)
			},
			unzip:   "exit 1\n",
			wantErr: "exit status 1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			if tt.unzip != "" {
				installTestCommand(t, "unzip", tt.unzip)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/"+testProxyModule+"/@latest" {
					_, _ = io.WriteString(w, `{"Version":"`+testProxyVersion+`"}`)
					return
				}
				if r.URL.Path == "/"+testProxyModule+"/@v/"+testProxyVersion+".zip" {
					tt.zipHandler(w, r)
					return
				}
				http.NotFound(w, r)
			}))
			defer server.Close()

			_, err := newTestProxyClient(server.URL).ProxyDownload(testProxyModule)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ProxyDownload error = %v, want error containing %q", err, tt.wantErr)
			}
			if entries, readErr := os.ReadDir(reposDir); readErr == nil && len(entries) != 0 {
				t.Errorf("failed download left temporary cache entries: %v", entries)
			}
		})
	}
}

func TestProxyDownloadReportsCacheCreationFailures(t *testing.T) {
	archive := makeTestModuleArchive(t)

	t.Run("cache root is a file", func(t *testing.T) {
		t.Chdir(t.TempDir())
		if err := os.WriteFile("_repos", []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(testProxyArchiveHandler(archive))
		defer server.Close()

		if _, err := newTestProxyClient(server.URL).ProxyDownload(testProxyModule); err == nil {
			t.Fatal("ProxyDownload succeeded when the cache root was a file")
		}
	})

	t.Run("cache directory is read-only", func(t *testing.T) {
		t.Chdir(t.TempDir())
		if err := os.MkdirAll(reposDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(reposDir, 0o500); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := os.Chmod(reposDir, 0o700); err != nil {
				t.Errorf("restore cache permissions: %v", err)
			}
		}()

		server := httptest.NewServer(testProxyArchiveHandler(archive))
		defer server.Close()

		if _, err := newTestProxyClient(server.URL).ProxyDownload(testProxyModule); err == nil {
			t.Fatal("ProxyDownload succeeded with a read-only cache directory")
		}
	})
}

func TestProxyDownloadReportsArchiveRemovalFailure(t *testing.T) {
	t.Chdir(t.TempDir())
	archive := makeTestModuleArchive(t)
	installTestCommand(t, "unzip", `rm -f "$2" || exit 1
mkdir "$2" || exit 1
printf marker > "$2/marker"
exit 0
`)
	server := httptest.NewServer(testProxyArchiveHandler(archive))
	defer server.Close()

	_, err := newTestProxyClient(server.URL).ProxyDownload(testProxyModule)
	if err == nil || !strings.Contains(err.Error(), "remove") {
		t.Fatalf("ProxyDownload error = %v, want temporary archive removal error", err)
	}
}

func TestDownloadedModuleCleanupWithoutCachePathIsNoop(t *testing.T) {
	if err := (DownloadedModule{Version: "v1.0.0", Dir: "not-cached"}).Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v, want nil for a module without a cache path", err)
	}
}

func TestDownloadedModuleCleanupReportsMissingCacheRoot(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := (DownloadedModule{path: "module@v1.0.0"}).Cleanup(); err == nil {
		t.Fatal("Cleanup succeeded when the repository cache root does not exist")
	}
}

func TestDownloadGitHubRepositoryReportsCheckoutParentCreationFailure(t *testing.T) {
	t.Chdir(t.TempDir())
	originalMkdirAll := githubMkdirAll
	t.Cleanup(func() { githubMkdirAll = originalMkdirAll })
	calls := 0
	wantErr := errors.New("checkout parent creation failed")
	githubMkdirAll = func(path string, perm os.FileMode) error {
		calls++
		if calls == 2 {
			return wantErr
		}
		return originalMkdirAll(path, perm)
	}

	if _, err := downloadGitHubRepository("github.com/user/module"); !errors.Is(err, wantErr) {
		t.Fatalf("downloadGitHubRepository error = %v, want %v", err, wantErr)
	}
	entries, err := os.ReadDir(reposDir)
	if err != nil {
		t.Fatalf("read cache after checkout-parent failure: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("checkout-parent failure left temporary entries: %v", entries)
	}
}

func setGitHubCloneTimeout(t *testing.T, timeout time.Duration) {
	t.Helper()
	originalTimeout := githubCloneTimeout
	githubCloneTimeout = timeout
	t.Cleanup(func() { githubCloneTimeout = originalTimeout })
}

func TestDownloadGitHubRepositoryReportsCloneTimeouts(t *testing.T) {
	tests := []struct {
		name    string
		script  string
		wantErr string
	}{
		{
			name:    "clone",
			script:  "while :; do :; done\n",
			wantErr: "GitHub clone timed out",
		},
		{
			name: "revision",
			script: `command=""
for arg do
  if [ "$arg" = "clone" ]; then command=clone; fi
done
if [ "$command" = "clone" ]; then
  for target do :; done
  mkdir -p "$target" || exit 1
  exit 0
fi
if [ "$1" = "-C" ]; then
  while :; do :; done
fi
exit 1
`,
			wantErr: "reading GitHub revision timed out",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			setGitHubCloneTimeout(t, time.Second)
			installTestCommand(t, "git", tt.script)

			_, err := downloadGitHubRepository("github.com/user/module")
			if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("downloadGitHubRepository error = %v, want deadline error containing %q", err, tt.wantErr)
			}
			entries, readErr := os.ReadDir(reposDir)
			if readErr != nil {
				t.Fatalf("read cache after timeout: %v", readErr)
			}
			if len(entries) != 0 {
				t.Errorf("timed-out download left temporary entries: %v", entries)
			}
		})
	}
}

func TestModuleNameReportsEscapeVersionError(t *testing.T) {
	wantErr := errors.New("version escaping failed")
	originalEscape := escapeModuleVersion
	escapeModuleVersion = func(string) (string, error) { return "", wantErr }
	t.Cleanup(func() { escapeModuleVersion = originalEscape })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/@latest") {
			_, _ = io.WriteString(w, `{"Version":"`+testProxyVersion+`"}`)
			return
		}
		t.Errorf("unexpected proxy request %s", r.URL.Path)
	}))
	defer server.Close()

	_, err := newTestProxyClient(server.URL).ModuleName(testProxyModule)
	if !errors.Is(err, wantErr) || !strings.Contains(err.Error(), "invalid module version") {
		t.Fatalf("ModuleName error = %v, want wrapped escape error", err)
	}
}

func TestModuleNameHandlesEmptySplitResult(t *testing.T) {
	originalSplit := splitModuleFile
	splitModuleFile = func(string, string) []string { return nil }
	t.Cleanup(func() { splitModuleFile = originalSplit })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/@latest") {
			_, _ = io.WriteString(w, `{"Version":"`+testProxyVersion+`"}`)
			return
		}
		_, _ = io.WriteString(w, "module "+testProxyModule+"\n")
	}))
	defer server.Close()

	if _, err := newTestProxyClient(server.URL).ModuleName(testProxyModule); err == nil || err.Error() != "empty go.mod" {
		t.Fatalf("ModuleName error = %v, want empty go.mod", err)
	}
}

func TestProxyDownloadReportsEscapeVersionError(t *testing.T) {
	wantErr := errors.New("version escaping failed")
	originalEscape := escapeModuleVersion
	escapeModuleVersion = func(string) (string, error) { return "", wantErr }
	t.Cleanup(func() { escapeModuleVersion = originalEscape })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/@latest") {
			_, _ = io.WriteString(w, `{"Version":"`+testProxyVersion+`"}`)
			return
		}
		t.Errorf("unexpected proxy request %s", r.URL.Path)
	}))
	defer server.Close()

	_, err := newTestProxyClient(server.URL).ProxyDownload(testProxyModule)
	if !errors.Is(err, wantErr) || !strings.Contains(err.Error(), "invalid module version") {
		t.Fatalf("ProxyDownload error = %v, want wrapped escape error", err)
	}
}

func TestProxyDownloadRejectsNonLocalEscapedPath(t *testing.T) {
	originalIsLocal := isLocalModulePath
	isLocalModulePath = func(string) bool { return false }
	t.Cleanup(func() { isLocalModulePath = originalIsLocal })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/@latest") {
			_, _ = io.WriteString(w, `{"Version":"`+testProxyVersion+`"}`)
			return
		}
		t.Errorf("unexpected proxy request %s", r.URL.Path)
	}))
	defer server.Close()

	_, err := newTestProxyClient(server.URL).ProxyDownload(testProxyModule)
	if err == nil || err.Error() != "invalid module download path" {
		t.Fatalf("ProxyDownload error = %v, want invalid module download path", err)
	}
}

func TestProxyDownloadReportsArchiveCloseFailure(t *testing.T) {
	t.Chdir(t.TempDir())
	archive := makeTestModuleArchive(t)
	wantErr := errors.New("archive close failed")
	originalClose := closeProxyZipFile
	closeProxyZipFile = func(file *os.File) error {
		_ = file.Close()
		return wantErr
	}
	t.Cleanup(func() { closeProxyZipFile = originalClose })

	server := httptest.NewServer(testProxyArchiveHandler(archive))
	defer server.Close()

	_, err := newTestProxyClient(server.URL).ProxyDownload(testProxyModule)
	if !errors.Is(err, wantErr) {
		t.Fatalf("ProxyDownload error = %v, want %v", err, wantErr)
	}
}

func TestCleanupProxyZipFilePropagatesCloseAndRemoveErrors(t *testing.T) {
	t.Run("close error", func(t *testing.T) {
		file, err := os.CreateTemp(t.TempDir(), "module-*.zip")
		if err != nil {
			t.Fatal(err)
		}
		closeErr := errors.New("close failed")
		removeErr := errors.New("remove failed")
		originalClose, originalRemove := closeProxyZipFile, removeProxyZipFile
		closeProxyZipFile = func(file *os.File) error {
			_ = file.Close()
			return closeErr
		}
		removeProxyZipFile = func(string) error { return removeErr }
		t.Cleanup(func() {
			closeProxyZipFile = originalClose
			removeProxyZipFile = originalRemove
		})

		var retErr error
		cleanupProxyZipFile(file, file.Name(), false, false, &retErr)
		if !errors.Is(retErr, closeErr) {
			t.Fatalf("cleanup error = %v, want close error %v", retErr, closeErr)
		}
	})

	t.Run("remove error", func(t *testing.T) {
		file, err := os.CreateTemp(t.TempDir(), "module-*.zip")
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		removeErr := errors.New("remove failed")
		originalRemove := removeProxyZipFile
		removeProxyZipFile = func(string) error { return removeErr }
		t.Cleanup(func() { removeProxyZipFile = originalRemove })

		var retErr error
		cleanupProxyZipFile(file, file.Name(), true, false, &retErr)
		if !errors.Is(retErr, removeErr) {
			t.Fatalf("cleanup error = %v, want remove error %v", retErr, removeErr)
		}
	})
}

func TestRemoveModuleDirUnlessKeptPropagatesCleanupError(t *testing.T) {
	var retErr error
	removeModuleDirUnlessKept("../../outside", false, &retErr)
	if retErr == nil || retErr.Error() != "invalid module cleanup path" {
		t.Fatalf("cleanup error = %v, want invalid module cleanup path", retErr)
	}

	primaryErr := errors.New("download failed")
	retErr = primaryErr
	removeModuleDirUnlessKept("../../outside", false, &retErr)
	if retErr != primaryErr {
		t.Errorf("cleanup replaced primary error: got %v, want %v", retErr, primaryErr)
	}

	retErr = nil
	removeModuleDirUnlessKept("../../outside", true, &retErr)
	if retErr != nil {
		t.Errorf("kept module cleanup changed return error to %v", retErr)
	}
}
