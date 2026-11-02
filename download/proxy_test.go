package download

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModuleName(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := r.URL
		switch u.Path {
		case "/github.com/user/module/@latest":
			fmt.Fprintf(w, `{"Version":"v0.1.0","Time":"2019-08-07T08:30:46Z"}`)
			return
		case "/github.com/user/module/@v/v0.1.0.mod":
			fmt.Fprintf(w, `module github.com/user/module`)
			return
		}
	}))
	defer ts.Close()

	c := NewProxyClient(ts.URL)

	got, err := c.ModuleName("github.com/user/module")
	if err != nil {
		t.Fatal(err)
	}

	want := "github.com/user/module"
	if got != want {
		t.Errorf("got module name = %q, want %q", got, want)
	}
}

func TestLatestVersion(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"Version":"v0.1.0","Time":"2019-08-07T08:30:46Z"}`)
	}))
	defer ts.Close()

	c := NewProxyClient(ts.URL)

	got, err := c.LatestVersion("github.com/user/module")
	if err != nil {
		t.Fatal(err)
	}

	want := "v0.1.0"
	if got != want {
		t.Errorf("got latest version = %q, want %q", got, want)
	}
}

func TestProxyDownloadRejectsInvalidModulePath(t *testing.T) {
	c := NewProxyClient("http://unused.invalid")
	if _, err := c.ProxyDownload("../../outside"); err == nil {
		t.Fatal("ProxyDownload accepted a path with parent-directory components")
	}
}

func TestDownloadedModuleCleanupCannotEscapeCache(t *testing.T) {
	t.Chdir(t.TempDir())

	outsideFile := filepath.Join("outside", "keep")
	if err := os.MkdirAll(filepath.Dir(outsideFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outsideFile, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	downloaded := DownloadedModule{path: "../../outside"}
	if err := downloaded.Cleanup(); err == nil {
		t.Fatal("Cleanup accepted a path outside the repository cache")
	}
	if _, err := os.Stat(outsideFile); err != nil {
		t.Fatalf("cleanup removed or could not access the outside file: %v", err)
	}
}

func TestProxyDownloadRejectsInvalidVersion(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"Version":"../../outside"}`)
	}))
	defer ts.Close()

	client := NewProxyClient(ts.URL)
	if _, err := client.ProxyDownload("github.com/user/module"); err == nil || !strings.Contains(err.Error(), "invalid module version") {
		t.Fatalf("ProxyDownload error = %v, want invalid module version error", err)
	}
}

func TestProxyDownloadExtractsModule(t *testing.T) {
	t.Chdir(t.TempDir())

	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	file, err := writer.Create("github.com/user/module@v0.1.0/go.mod")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(file, "module github.com/user/module\n\ngo 1.27.3\n"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/github.com/user/module/@latest":
			fmt.Fprint(w, `{"Version":"v0.1.0"}`)
		case "/github.com/user/module/@v/v0.1.0.zip":
			_, _ = w.Write(archive.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	client := NewProxyClient(ts.URL)
	downloaded, err := client.ProxyDownload("github.com/user/module")
	if err != nil {
		t.Fatal(err)
	}
	if downloaded.Version != "v0.1.0" {
		t.Errorf("downloaded.Version = %q, want %q", downloaded.Version, "v0.1.0")
	}
	if got, want := downloaded.Dir, filepath.Join(reposDir, "github.com/user/module@v0.1.0"); got != want {
		t.Errorf("downloaded.Dir = %q, want %q", got, want)
	}

	goMod, err := os.ReadFile(filepath.Join(downloaded.Dir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(goMod), "module github.com/user/module\n\ngo 1.27.3\n"; got != want {
		t.Errorf("downloaded go.mod = %q, want %q", got, want)
	}

	if err := downloaded.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(downloaded.Dir); !os.IsNotExist(err) {
		t.Errorf("downloaded module directory still exists or could not be checked: %v", err)
	}
}
