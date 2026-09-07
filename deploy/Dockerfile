# syntax=docker/dockerfile:1.7
FROM --platform=$BUILDPLATFORM node:26.4.0-bookworm-slim AS web
RUN npm install --global pnpm@11.25.0
WORKDIR /src/web
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

FROM --platform=$BUILDPLATFORM golang:1.27.1-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY --from=web /src/web/dist/ ./internal/webassets/dist/
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" -o /out/herdrx-server ./cmd/herdrx-server
RUN mkdir -p /out/data && chmod 0700 /out/data

FROM gcr.io/distroless/static-debian12:nonroot
ARG VERSION=dev
ARG REVISION=local
LABEL org.opencontainers.image.title="herdrx" \
      org.opencontainers.image.description="Herdr 多主机 Web 终端工作台" \
      org.opencontainers.image.source="https://github.com/riba2534/herdrx" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}"
COPY --from=build /out/herdrx-server /app/herdrx-server
COPY LICENSE THIRD_PARTY_NOTICES.md sbom.cdx.json /usr/share/doc/herdrx/
COPY --from=build --chown=nonroot:nonroot /out/data/ /data/
USER nonroot:nonroot
EXPOSE 8080
ENV HERDRX_ADDR=0.0.0.0:8080 HERDRX_DATA_DIR=/data HERDRX_COOKIE_SECURE=false
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 CMD ["/app/herdrx-server", "healthcheck"]
ENTRYPOINT ["/app/herdrx-server"]
