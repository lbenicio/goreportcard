# Changelog

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
