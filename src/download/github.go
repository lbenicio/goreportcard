package download

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var githubCloneTimeout = 10 * time.Minute

var githubMkdirAll = os.MkdirAll

func parseGitHubRepository(path string) (owner, repo string, err error) {
	path = strings.TrimSpace(Clean(path))
	path = strings.TrimSuffix(path, "/")
	path = strings.TrimSuffix(path, ".git")
	parts := strings.Split(path, "/")
	if len(parts) != 3 || !strings.EqualFold(parts[0], "github.com") {
		return "", "", fmt.Errorf("not a GitHub repository root")
	}
	if !validGitHubPathSegment(parts[1]) || !validGitHubPathSegment(parts[2]) {
		return "", "", fmt.Errorf("invalid GitHub repository path")
	}
	return parts[1], parts[2], nil
}

func validGitHubPathSegment(segment string) bool {
	if segment == "" || segment == "." || segment == ".." {
		return false
	}
	for _, r := range segment {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func downloadGitHubRepository(path string) (downloaded DownloadedModule, retErr error) {
	owner, repo, err := parseGitHubRepository(path)
	if err != nil {
		return DownloadedModule{}, err
	}
	if err := githubMkdirAll(reposDir, 0o755); err != nil {
		return DownloadedModule{}, err
	}

	tempRoot, err := os.MkdirTemp(reposDir, "github-")
	if err != nil {
		return DownloadedModule{}, err
	}
	tempRootName := filepath.Base(tempRoot)
	keep := false
	defer func() {
		removeModuleDirUnlessKept(tempRootName, keep, &retErr)
	}()

	checkoutParent := filepath.Join(tempRoot, "github.com", owner)
	if err := githubMkdirAll(checkoutParent, 0o755); err != nil {
		return DownloadedModule{}, err
	}
	checkoutDir := filepath.Join(checkoutParent, repo)
	repositoryURL := fmt.Sprintf("https://github.com/%s/%s.git", owner, repo)

	ctx, cancel := context.WithTimeout(context.Background(), githubCloneTimeout)
	defer cancel()

	clone := exec.CommandContext(ctx,
		"git",
		"-c", "core.hooksPath=/dev/null",
		"-c", "credential.helper=",
		"-c", "protocol.file.allow=never",
		"-c", "core.symlinks=false",
		"clone",
		"--depth=1",
		"--single-branch",
		"--no-tags",
		"--no-recurse-submodules",
		"--",
		repositoryURL,
		checkoutDir,
	)
	clone.Env = githubGitEnvironment()
	output, err := clone.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return DownloadedModule{}, fmt.Errorf("GitHub clone timed out: %w", ctx.Err())
		}
		return DownloadedModule{}, fmt.Errorf("GitHub clone failed: %w: %s", err, strings.TrimSpace(string(output)))
	}

	revision := exec.CommandContext(ctx, "git", "-C", checkoutDir, "rev-parse", "--verify", "HEAD^{commit}")
	revision.Env = githubGitEnvironment()
	output, err = revision.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return DownloadedModule{}, fmt.Errorf("reading GitHub revision timed out: %w", ctx.Err())
		}
		return DownloadedModule{}, fmt.Errorf("could not read GitHub revision: %w: %s", err, strings.TrimSpace(string(output)))
	}
	commit := strings.TrimSpace(string(output))
	if !validCommitHash(commit) {
		return DownloadedModule{}, fmt.Errorf("GitHub returned an invalid commit ID")
	}

	versionedDir := filepath.Join(checkoutParent, repo+"@"+commit)
	if err := os.Rename(checkoutDir, versionedDir); err != nil {
		return DownloadedModule{}, fmt.Errorf("could not prepare GitHub checkout: %w", err)
	}

	keep = true
	return DownloadedModule{
		Version: commit,
		Dir:     versionedDir,
		path:    tempRootName,
	}, nil
}

func githubGitEnvironment() []string {
	const (
		gitConfigPrefix = "GIT_CONFIG_"
		gitDirectory    = "GIT_DIR="
		gitWorkTree     = "GIT_WORK_TREE="
	)

	env := make([]string, 0, len(os.Environ())+4)
	for _, value := range os.Environ() {
		if strings.HasPrefix(value, gitConfigPrefix) || strings.HasPrefix(value, gitDirectory) || strings.HasPrefix(value, gitWorkTree) {
			continue
		}
		env = append(env, value)
	}
	return append(env,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_LFS_SKIP_SMUDGE=1",
	)
}

func validCommitHash(commit string) bool {
	if len(commit) != 40 && len(commit) != 64 {
		return false
	}
	_, err := hex.DecodeString(commit)
	return err == nil
}
