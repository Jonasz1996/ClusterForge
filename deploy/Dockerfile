# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM node:22-alpine AS web
WORKDIR /src/web
RUN corepack enable
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

# Go cross-compileert; zo draait de build voor arm64 niet onder emulatie.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS server
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/out/ ./internal/webui/dist/
ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH
# Op een LXC met weinig geheugen: CF_BUILD_GOFLAGS en CF_BUILD_GOGC in deploy/.env
# (zie Bijwerken in docs/install-proxmox-lxc.md). Leeg = de standaard van Go.
ARG GOFLAGS=
ARG GOGC=
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/clusterforge-server ./cmd/clusterforge-server
# De server biedt de agent aan om te downloaden, voor beide architecturen.
RUN for arch in amd64 arm64; do \
      CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/agents/cf-agent-linux-$arch ./cmd/cf-agent && \
      (cd /out/agents && sha256sum cf-agent-linux-$arch > cf-agent-linux-$arch.sha256); \
    done
# De map die de server met de scannercontainer deelt (aanmeldbestand).
RUN mkdir -p /out/scanner

# De scannercontainer (fase 27, profiel scanner in docker-compose.yml): een
# cf-agent met nmap op Debian 13. Hij meldt zich aan met het bestand dat
# "Scannercontainer koppelen" in de gedeelde map zet, en scant alleen binnen
# de eigen scopes. Bouwen: --target scanner.
FROM debian:13-slim AS scanner
ARG TARGETARCH
RUN apt-get update \
 && apt-get install -y --no-install-recommends nmap ca-certificates \
 && rm -rf /var/lib/apt/lists/*
COPY --from=server /out/agents/cf-agent-linux-${TARGETARCH} /usr/local/bin/cf-agent
ENTRYPOINT ["/usr/local/bin/cf-agent"]
CMD ["run"]

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=server /out/clusterforge-server /usr/local/bin/clusterforge-server
COPY --from=server /out/agents/ /usr/share/clusterforge/agents/
COPY --from=server --chown=65532:65532 /out/scanner /var/lib/clusterforge/scanner
# 8080: webinterface en API (achter je reverse proxy). 4222: NATS voor de agents.
EXPOSE 8080 4222
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/clusterforge-server"]
CMD ["serve"]
