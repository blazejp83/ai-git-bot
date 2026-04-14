# =============================================================================
# AI Git Bot — Multi-stage Dockerfile
#
# Two targets:
#   docker build -f Dockerfile.go --target minimal .   → ~30MB  (review only)
#   docker build -f Dockerfile.go --target full .       → ~1.2GB (review + agent with build tools)
#   docker build -f Dockerfile.go .                     → full (default)
# =============================================================================

# ---------------------------------------------------------------------------
# Build stage — compile the Go binary
# ---------------------------------------------------------------------------
FROM golang:1.25-alpine AS build
RUN apk add --no-cache gcc musl-dev
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -ldflags="-s -w" -o /server ./cmd/server

# ---------------------------------------------------------------------------
# Minimal stage — review-only, no build tools (~30MB)
# ---------------------------------------------------------------------------
FROM alpine:3.21 AS minimal
RUN apk add --no-cache ca-certificates git && \
    addgroup -g 1000 app && adduser -u 1000 -G app -D app
COPY --from=build /server /server
COPY migrations/ /migrations/
COPY prompts/ /prompts/
COPY web/ /web/
USER app
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s CMD wget -q -O- http://localhost:8080/healthz || exit 1
ENTRYPOINT ["/server"]

# ---------------------------------------------------------------------------
# Full stage — agent with polyglot build tools for code validation
# ---------------------------------------------------------------------------
FROM alpine:3.21 AS full

# Base utilities
RUN apk add --no-cache \
    ca-certificates curl wget git bash \
    # Java (Maven + Gradle)
    openjdk21-jdk maven \
    # Node.js / npm / TypeScript
    nodejs npm \
    # Python
    python3 py3-pip python3-dev \
    # Go
    go \
    # C / C++
    gcc g++ musl-dev make cmake \
    # Ruby
    ruby ruby-bundler \
    # .NET (if available)
    # dotnet8-sdk \
    && \
    # Create app user
    addgroup -g 1000 app && adduser -u 1000 -G app -D app && \
    mkdir -p /home/app/.cache && chown -R app:app /home/app

# Install Gradle (not in Alpine repos)
ARG GRADLE_VERSION=8.12
RUN wget -q "https://services.gradle.org/distributions/gradle-${GRADLE_VERSION}-bin.zip" -O /tmp/gradle.zip && \
    unzip -q /tmp/gradle.zip -d /opt && \
    ln -s /opt/gradle-${GRADLE_VERSION}/bin/gradle /usr/local/bin/gradle && \
    rm /tmp/gradle.zip

# Install Rust for the app user
USER app
RUN curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y --profile minimal
USER root

# Install global npm packages useful for validation
RUN npm install -g typescript pnpm yarn 2>/dev/null || true

# Environment
ENV JAVA_HOME=/usr/lib/jvm/java-21-openjdk
ENV GOPATH=/home/app/go
ENV PATH="/home/app/.cargo/bin:/home/app/go/bin:/usr/lib/go/bin:/usr/lib/jvm/java-21-openjdk/bin:${PATH}"

# Copy application
COPY --from=build /server /server
COPY migrations/ /migrations/
COPY prompts/ /prompts/
COPY web/ /web/

# Ensure app user can write to working directories
RUN mkdir -p /data && chown app:app /data

USER app
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -q -O- http://localhost:8080/healthz || exit 1

ENTRYPOINT ["/server"]
