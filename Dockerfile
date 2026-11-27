FROM golang:1.27.3-trixie AS build

RUN apt-get update \
    && apt-get install --no-install-recommends -y ca-certificates coreutils git unzip \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src

COPY go.mod go.sum ./
COPY scripts/install.sh ./scripts/install.sh
RUN GOBIN=/out/bin ./scripts/install.sh

COPY src/ ./src/

RUN mkdir -p /out/repo-cache/_repos/src/github.com \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bin/goreportcard ./src

# The application runs git, unzip, wc, Go, and analyzer tools for submitted repositories.
# Copy only the system executables and shared libraries those runtime commands need.
RUN set -eu; \
    runtime=/out/runtime; \
    git_exec_path="$(git --exec-path)"; \
    mkdir -p "$runtime/usr/bin" "$runtime$git_exec_path"; \
    cp /usr/bin/git /usr/bin/unzip /usr/bin/wc "$runtime/usr/bin/"; \
    cp -a "$git_exec_path/." "$runtime$git_exec_path/"; \
    mkdir -p "$runtime/usr/share/git-core"; \
    cp -a /usr/share/git-core/templates "$runtime/usr/share/git-core/"; \
    { \
        for binary in /usr/bin/git /usr/bin/unzip /usr/bin/wc; do \
            ldd "$binary" 2>/dev/null || true; \
        done; \
        find "$git_exec_path" -type f -executable -exec sh -c 'for binary do ldd "$binary" 2>/dev/null || true; done' sh {} +; \
    } | awk '$2 == "=>" && $3 ~ /^\// { print $3 } $1 ~ /^\// { print $1 }' | sort -u | \
        while IFS= read -r library; do \
            mkdir -p "$runtime$(dirname "$library")"; \
            cp -L "$library" "$runtime$library"; \
        done

FROM gcr.io/distroless/cc-debian13:nonroot

# The runtime includes Go analysis tools but no C compiler.
ENV GOROOT=/usr/local/go \
    GOCACHE=/tmp/go-build-cache \
    GOMODCACHE=/tmp/go-mod-cache \
    HOME=/tmp \
    GIT_EXEC_PATH=/usr/lib/git-core \
    CGO_ENABLED=0 \
    PATH=/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin

COPY --from=build /usr/local/go /usr/local/go
COPY --from=build /out/bin/ /usr/local/bin/
COPY --from=build /out/runtime/ /

WORKDIR /app
COPY --from=build --chown=65532:65532 /out/repo-cache/ /app/

USER 65532:65532
EXPOSE 8000
ENTRYPOINT ["/usr/local/bin/goreportcard"]
