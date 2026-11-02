package download

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/mod/module"
)

const (
	reposDir = "_repos/src"
)

type moduleVersion struct {
	Version string
}

// ProxyClient is a client for the module proxy
type ProxyClient struct {
	URL string
}

// NewProxyClient returns a new ProxyClient
func NewProxyClient(url string) ProxyClient {
	return ProxyClient{URL: url}
}

func (c *ProxyClient) latestURL(escapedModule string) string {
	return fmt.Sprintf("%s/%s/@latest", c.URL, escapedModule)
}

func (c *ProxyClient) zipURL(escapedModule, escapedVersion string) string {
	return fmt.Sprintf("%s/%s/@v/%s.zip", c.URL, escapedModule, escapedVersion)
}

func (c *ProxyClient) modURL(escapedModule, escapedVersion string) string {
	return fmt.Sprintf("%s/%s/@v/%s.mod", c.URL, escapedModule, escapedVersion)
}

// ModuleName gets the name of a module from the proxy
func (c *ProxyClient) ModuleName(path string) (string, error) {
	escapedPath, err := module.EscapePath(path)
	if err != nil {
		return "", fmt.Errorf("invalid module path: %w", err)
	}

	ver, err := c.LatestVersion(path)
	if err != nil {
		return "", err
	}
	if err := module.Check(path, ver); err != nil {
		return "", fmt.Errorf("invalid module version: %w", err)
	}
	escapedVersion, err := module.EscapeVersion(ver)
	if err != nil {
		return "", fmt.Errorf("invalid module version: %w", err)
	}

	u := c.modURL(escapedPath, escapedVersion)
	resp, err := http.Get(u)
	if err != nil {
		return "", err
	}

	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("could not get module name from %s", u)
	}

	sp := strings.Split(string(b), "\n")
	if len(sp) == 0 {
		return "", fmt.Errorf("empty go.mod")
	}

	mn := strings.Fields(sp[0])
	if len(mn) != 2 {
		return "", fmt.Errorf("invalid go.mod: %s", mn)
	}

	if err := module.CheckPath(mn[1]); err != nil {
		return "", fmt.Errorf("invalid module path in go.mod: %w", err)
	}

	return mn[1], nil
}

// LatestVersion gets the latest module version from the proxy
func (c *ProxyClient) LatestVersion(path string) (string, error) {
	escapedPath, err := module.EscapePath(path)
	if err != nil {
		return "", fmt.Errorf("invalid module path: %w", err)
	}
	u := c.latestURL(escapedPath)
	resp, err := http.Get(u)
	if err != nil {
		return "", err
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("could not get latest module version from %s", u)
	}

	var mv moduleVersion

	err = json.NewDecoder(resp.Body).Decode(&mv)
	if err != nil {
		return "", err
	}

	return mv.Version, nil
}

// DownloadedModule describes a module unpacked into the repository cache.
type DownloadedModule struct {
	Version string
	Dir     string
	path    string
}

// Cleanup removes the downloaded module without allowing the path to escape the repository cache.
func (d DownloadedModule) Cleanup() error {
	if d.path == "" {
		return nil
	}
	return removeModuleDir(d.path)
}

func removeModuleDir(path string) (err error) {
	if !filepath.IsLocal(path) {
		return fmt.Errorf("invalid module cleanup path")
	}

	root, err := os.OpenRoot(reposDir)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := root.Close(); err == nil {
			err = closeErr
		}
	}()

	return root.RemoveAll(path)
}

// ProxyDownload downloads a module from the Go module proxy into the repository cache.
func (c *ProxyClient) ProxyDownload(path string) (downloaded DownloadedModule, retErr error) {
	escapedPath, err := module.EscapePath(path)
	if err != nil {
		return DownloadedModule{}, fmt.Errorf("invalid module path: %w", err)
	}

	ver, err := c.LatestVersion(path)
	if err != nil {
		return DownloadedModule{}, err
	}
	if err := module.Check(path, ver); err != nil {
		return DownloadedModule{}, fmt.Errorf("invalid module version: %w", err)
	}
	escapedVersion, err := module.EscapeVersion(ver)
	if err != nil {
		return DownloadedModule{}, fmt.Errorf("invalid module version: %w", err)
	}
	moduleDir := escapedPath + "@" + escapedVersion
	if !filepath.IsLocal(moduleDir) {
		return DownloadedModule{}, fmt.Errorf("invalid module download path")
	}

	resp, err := http.Get(c.zipURL(escapedPath, escapedVersion))
	if err != nil {
		return DownloadedModule{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return DownloadedModule{}, fmt.Errorf("status %d", resp.StatusCode)
	}

	if err := os.MkdirAll(reposDir, 0o755); err != nil {
		return DownloadedModule{}, err
	}

	zipFile, err := os.CreateTemp(reposDir, "module-*.zip")
	if err != nil {
		return DownloadedModule{}, err
	}
	zipPath := zipFile.Name()
	zipClosed := false
	zipRemoved := false
	defer func() {
		if !zipClosed {
			if err := zipFile.Close(); retErr == nil && err != nil {
				retErr = err
			}
		}
		if !zipRemoved {
			if err := os.Remove(zipPath); retErr == nil && !os.IsNotExist(err) && err != nil {
				retErr = err
			}
		}
	}()

	if _, err := io.Copy(zipFile, resp.Body); err != nil {
		return DownloadedModule{}, err
	}
	if err := zipFile.Close(); err != nil {
		return DownloadedModule{}, err
	}
	zipClosed = true

	keepModule := false
	defer func() {
		if !keepModule {
			if err := removeModuleDir(moduleDir); retErr == nil && err != nil {
				retErr = err
			}
		}
	}()

	cmd := exec.Command("unzip", "-o", zipPath, "-d", reposDir)
	if err := cmd.Run(); err != nil {
		return DownloadedModule{}, err
	}

	if err := os.Remove(zipPath); err != nil {
		return DownloadedModule{}, err
	}
	zipRemoved = true

	keepModule = true
	return DownloadedModule{
		Version: ver,
		Dir:     filepath.Join(reposDir, moduleDir),
		path:    moduleDir,
	}, nil
}
