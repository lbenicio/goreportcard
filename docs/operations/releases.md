# Release process

The release workflow is manually started from the `main` branch. It reads the newest versioned section in `CHANGELOG.md`; add a SemVer heading and release notes before starting a release. The workflow refuses an already existing tag or GitHub release.

## Build and publish steps

1. Build and push native Linux container images for amd64 and arm64 to Docker Hub and GHCR.
2. Cross-compile the CLI for Linux, macOS, and Windows on amd64 and arm64.
3. Package Linux/macOS binaries as `.tar.gz` archives and Windows binaries as `.zip` archives. Each archive includes `LICENSE.txt`.
4. Generate `SHA256SUMS` for the six CLI archives.
5. Publish multi-platform container tags and create a GitHub release titled `v<version>` with the CLI archives and checksums attached.

Release CLI binaries and the server container have the version embedded at build time. The container is tagged with the bare SemVer value and `latest`; architecture-specific images are assembled into a manifest. The GitHub release tag uses the bare SemVer value.

## Production deployment

After a stable release is published, the `Deploy production` workflow verifies the release and its Docker Hub manifest before updating the configured Compose service. Its SSH and Compose settings are documented in [Deployment](deployment.md).

## Changelog format

The release workflow expects a heading such as `## [1.2.5] - YYYY-MM-DD` and nonempty notes below it. Keep unreleased notes under `## [Unreleased]`; move release notes into the version heading before starting a release.
