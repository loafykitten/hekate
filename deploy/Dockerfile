# syntax=docker/dockerfile:1.7
# The deployable image: Go service + built PWA. No secrets and no character assets; those are
# mounted at runtime (see deploy/compose.example.yaml).

FROM node:24-trixie-slim AS web
WORKDIR /src/app
COPY app/package.json app/package-lock.json ./
RUN npm ci
COPY app/ ./
RUN npm run build

FROM golang:1.27-trixie AS service
WORKDIR /src/service
COPY service/go.mod service/go.sum ./
RUN go mod download
COPY service/ ./
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILT_AT=unknown
# cgo stays on: speech-to-text (sherpa-onnx) links a native library.
RUN pkg=github.com/loafykitten/hekate/service/internal/buildinfo && \
    go build -trimpath \
      -ldflags "-s -w -X $pkg.Version=$VERSION -X $pkg.Commit=$COMMIT -X $pkg.BuiltAt=$BUILT_AT" \
      -o /out/hekate ./cmd/hekate

FROM debian:trixie-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates tzdata \
 && rm -rf /var/lib/apt/lists/*
COPY --from=service /out/hekate /usr/local/bin/hekate
COPY --from=web /src/app/dist /srv/web
ENV HEKATE_ADDR=127.0.0.1:3200 \
    HEKATE_DATA_DIR=/data \
    HEKATE_WEB_DIR=/srv/web \
    HEKATE_ASSETS_DIR=/character
# Run as the host user that owns the data volume (compose sets `user:`); this is the fallback.
USER 65532:65532
VOLUME /data
ENTRYPOINT ["/usr/local/bin/hekate"]
