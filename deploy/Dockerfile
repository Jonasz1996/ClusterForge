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
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/clusterforge-server ./cmd/clusterforge-server
# De server biedt de agent aan om te downloaden, voor beide architecturen.
RUN for arch in amd64 arm64; do \
      CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/agents/cf-agent-linux-$arch ./cmd/cf-agent && \
      (cd /out/agents && sha256sum cf-agent-linux-$arch > cf-agent-linux-$arch.sha256); \
    done

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=server /out/clusterforge-server /usr/local/bin/clusterforge-server
COPY --from=server /out/agents/ /usr/share/clusterforge/agents/
# 8080: webinterface en API (achter je reverse proxy). 4222: NATS voor de agents.
EXPOSE 8080 4222
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/clusterforge-server"]
CMD ["serve"]
