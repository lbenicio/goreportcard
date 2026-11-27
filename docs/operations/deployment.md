# Deployment

## Container image

The root `Dockerfile` builds the server in a Go build stage and runs it as a nonroot user in a Distroless runtime. Static assets are embedded in the binary. The image includes Go, Git, `unzip`, `wc`, and analyzer tools used when processing repositories.

Configure `DATABASE_URL` and, for multi-instance deployments, `REDIS_URL` as runtime environment variables. Set `HTTP_ADDR` if the container should listen on a different address. Do not copy credentials into the image or source repository. The container exposes port 8000 by default.

## Production workflow

The `Deploy production` workflow validates the published release and Docker Hub image manifest, then connects to the host over SSH. It writes a temporary Compose override for the release image and waits for the configured service to become ready.

Configure the `production` GitHub environment with:

| Type | Name | Purpose |
|---|---|---|
| Variable | `PRODUCTION_HOST` | Production host name or address. |
| Variable | `PRODUCTION_SSH_PORT` | SSH port; defaults to `22`. |
| Variable | `PRODUCTION_USER` | SSH account used by deployment. |
| Variable | `PRODUCTION_DEPLOY_PATH` | Absolute remote path containing the Compose project. |
| Variable | `PRODUCTION_COMPOSE_FILE` | Compose file path, absolute or relative to the deploy path. |
| Variable | `PRODUCTION_COMPOSE_SERVICE` | Service name; defaults to `goreportcard`. |
| Secret | `PRODUCTION_SSH_PRIVATE_KEY` | Private key for the SSH connection. |
| Secret | `PRODUCTION_KNOWN_HOSTS` | Pinned SSH host-key entry for the production host. |

The workflow also uses the repository's `DOCKER_USER` and `DOCKER_PAT` secrets to verify and pull the published Docker Hub image. See [Release process](releases.md) for its triggers and artifacts.

## Local container build

```sh
docker build -t goreportcard:local .
docker run --rm -p 8000:8000 \
  -e DATABASE_URL='postgresql://user:password@host.docker.internal:5432/goreportcard?sslmode=disable' \
  goreportcard:local
```

The database host name depends on the container runtime and network. Create the database before launching the service. For local development without a container, see [Installation](../getting-started/installation.md).
