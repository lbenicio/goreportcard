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
