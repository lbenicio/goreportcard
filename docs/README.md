# Go Report Card Documentation

This documentation explains how to install and configure Go Report Card, how its service and storage work, how to deploy it, and how to contribute changes. Start with the section that matches what you are trying to do.

## Global index

### Getting started

Install the server or CLI, configure databases and Redis, and learn the command-line flags.

- [Getting started overview](getting-started/README.md)
- [Installation](getting-started/installation.md)
- [Configuration](getting-started/configuration.md)
- [Command-line interface](getting-started/cli.md)

### Architecture

Understand request flow, package boundaries, persistence, caching, and asynchronous analysis jobs.

- [Architecture overview](architecture/README.md)
- [System overview](architecture/system-overview.md)
- [Data model and persistence](architecture/data-model.md)
- [Analysis jobs](architecture/analysis-jobs.md)
- [Caching](architecture/caching.md)

### Operations

Build and deploy the service, prepare supported databases, configure Redis, and diagnose operational failures.

- [Operations overview](operations/README.md)
- [Deployment](operations/deployment.md)
- [Database operations](operations/database.md)
- [Redis operations](operations/redis.md)
- [Release process](operations/releases.md)
- [Troubleshooting](operations/troubleshooting.md)

### Development

Set up the repository, understand the checks and CI workflow, and add a new analyzer check.

- [Development overview](development/README.md)
- [Development workflow](development/workflow.md)
- [Testing](development/testing.md)
- [Adding a check](development/adding-checks.md)

### Project and community

Find the contributor, security, community, license, and changelog policies.

- [Project documents overview](project/README.md)
- [Contributing](project/contributing.md)
- [Security reporting](project/security.md)
- [Community standards](project/community.md)
- [License and changelog](project/license-and-changelog.md)

## Repository-level documents

These files stay at the repository root for GitHub and tooling conventions. The [project documents overview](project/README.md) summarizes each one and links to the canonical copy.

- [Project README](../README.md)
- [Changelog](../CHANGELOG.md)
- [Contributing guide](../CONTRIBUTING.md)
- [Security policy](../SECURITY.md)
- [Code of Conduct](../CODE_OF_CONDUCT.md)
- [License](../LICENSE.txt)
- [Agent instructions](../AGENTS.md)
