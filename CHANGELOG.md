# Changelog

## [Unreleased]

## [1.2.0] - 2026-11-09

### Added
- Support MySQL and MariaDB alongside PostgreSQL, selected through the `DATABASE_URL` scheme, with migrations and importer/maintenance-tool support.
- Add extensive edge-case test coverage across analysis, downloads, handlers, database storage, and maintenance tools.
- Add CI and release workflow summaries with check results, image publication status, and release details.

### Changed
- Restrict CI token permissions to read-only and scope release workflow permissions to publishing needs.
- Improve error handling and cleanup across application startup, repository downloads, Badger imports, and maintenance tools.

### Fixed
- Correct Go source-file detection in license analysis.

## [1.1.2] - 2026-11-08

### Added
- Add a manually triggered GitHub Actions release workflow that checks for existing tags and releases, publishes container images to Docker Hub and GHCR, and creates GitHub releases from the changelog.
- Add contributor guidelines, a code of conduct, and a security reporting policy.

### Changed
- Build amd64 and arm64 images on native runners and publish multi-platform version and `latest` tags; publish GHCR images under the repository name.
- Update the Go module and project import paths to `github.com/lbenicio/goreportcard`.
- Upgrade the PostgreSQL driver `pgx` to v5.9.2.

## [1.1.1] - 2026-11-08

### Added
- Add optional Redis report caching through `REDIS_URL`, with a five-minute TTL, PostgreSQL as the source of truth, and PostgreSQL fallback when Redis is unavailable.
- Add Codecov coverage reporting and project status badges for the report, coverage, and license.

### Changed
- Install `staticcheck` with the development tools, limit Codecov runs to relevant branches and changes, and rename the CodeQL workflow to `codeql.yml`.
- Refactor `GoTool` and its package/diagnostic helpers to keep analyzer functions below the gocyclo warning threshold.

## [1.1.0] - 2026-11-08

### Added
- Replace the local Badger runtime store with PostgreSQL using typed, normalized tables for reports, checks, file summaries, and errors.
- Add versioned PostgreSQL schema migrations and a read-only Badger-to-PostgreSQL importer.

### Changed
- Configure the web app with `DATABASE_URL`; derive the leaderboard from current relational report data and maintain the repository count transactionally.
- Update the database maintenance utility to operate on PostgreSQL.

## [1.0.7] - 2026-11-07

### Fixed
- Generate README badges and report links with the current `goreportcard.lbenicio.dev` host instead of the retired `goreportcard.com` host.

## [1.0.6] - 2026-11-07

### Changed
- Remove sponsor, supporter, and Patreon promotions from the UI, and update repository links and maintainer credits for the current maintainer.

## [1.0.5] - 2026-11-06

### Fixed
- Ignore Go module download progress lines when parsing `go vet` output, preserving real diagnostics without reporting false analyzer errors.

## [1.0.4] - 2026-11-06

### Added
- Run repository analyses asynchronously with request deduplication and report-page polling, avoiding gateway timeouts on large workspaces.

### Fixed
- Skip package checks for directories with no Go files active under the current build constraints.
- Escape server error text in alert templates so repository-controlled diagnostics are not rendered as HTML.

## [1.0.3] - 2026-11-06

### Added
- Analyze public GitHub repositories when they are not available from the Go module proxy.
- Support Go workspaces with multiple nested modules by running package checks from each module root.

## [1.0.2] - 2026-11-06

### Changed
- Remove the shutdown notice banner from the homepage.

## [1.0.0] - 2026-11-06

### Security
- Validate module paths and versions before using them in proxy requests and cache paths, and restrict module cleanup to the repository cache.
- Limit GitHub Actions token permissions in the CI and CodeQL workflows.
- Extend CodeQL analysis to cover GitHub Actions workflows as well as Go code.

### Changed
- Replace `gometalinter` with individual Go analysis tools and update their installation and formatting scripts.
- Refresh Go module dependencies and vendored code, including `golang.org/x/net` from 0.38.0 to 0.55.0.
- Update CI actions and use the Go 1.27 toolchain; refresh test fixtures for Go 1.27 `go vet` behavior.

### Fixed
- Improve error handling when reading repository-count metadata from Badger.
