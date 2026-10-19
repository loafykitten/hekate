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
# sherpa-onnx-go-linux ships its shared libraries inside the Go module and links the binary with
# an rpath into the module cache, which the runtime image doesn't have. Collect them here (the
# version follows go.mod; the directory is named after the CPU architecture of this build).
RUN dir="$(go list -m -f '{{.Dir}}' github.com/k2-fsa/sherpa-onnx-go-linux)/lib/$(uname -m)-unknown-linux-gnu" && \
    mkdir -p /out/lib && cp "$dir"/*.so /out/lib/ && chmod 0644 /out/lib/*.so && \
    test -f /out/lib/libsherpa-onnx-c-api.so && test -f /out/lib/libonnxruntime.so

FROM debian:trixie-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates tzdata libstdc++6 \
 && rm -rf /var/lib/apt/lists/*
# The speech-to-text libraries (see the service stage). /usr/local/lib is on the loader's default
# search path, and ldconfig refreshes its cache, so the binary's dead rpath doesn't matter.
COPY --from=service /out/lib/ /usr/local/lib/
RUN ldconfig
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
