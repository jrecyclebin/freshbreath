# Fresh Breath — container image.
#
# The build stage runs the *same* `mise run build:linux` that ships the
# release tarballs, so the image can't drift from the zips: one build
# script, one FTS5 tag, one control-panel dist swap. We just untar the
# result into a slim runtime instead of uploading it to a release.
#
#   docker build -t freshbreath .
#   docker run -p 9009:9009 -v freshbreath-data:/data freshbreath

# ── Stage 1: build via mise ───────────────────────────────────────────
FROM debian:bookworm-slim AS build
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=none

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates curl git gcc libc6-dev nodejs zip \
 && rm -rf /var/lib/apt/lists/*

# mise brings the Go toolchain pinned in mise.toml, plus GOFLAGS (the
# sqlite_fts5 tag) — the same environment a local `mise run build:linux` gets.
RUN curl -fsSL https://mise.run | MISE_INSTALL_PATH=/usr/local/bin/mise sh
ENV PATH="/root/.local/share/mise/shims:$PATH"

WORKDIR /src
COPY mise.toml ./
RUN mise trust && mise install

COPY . .
# VERSION/COMMIT are honoured by scripts/build.sh ahead of its git-derived
# defaults, which is just as well: there's no .git in the build context.
RUN VERSION="${VERSION}" COMMIT="${COMMIT}" \
    mise run "build:$([ "$TARGETARCH" = arm64 ] && echo linux-arm64 || echo linux)"

# Unpack the release tarball we just made — binary, web/, skills/, README.
RUN mkdir -p /out && tar -xzf dist/freshbreath-*-linux-*.tar.gz -C /out

# ── Stage 2: runtime ──────────────────────────────────────────────────
FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates curl git openssh-client tzdata \
 && rm -rf /var/lib/apt/lists/*

# Non-root by default. A named volume mounted at /data inherits this
# ownership; a bind mount does not, so chown the host directory to 10001
# if you use one.
RUN useradd --uid 10001 --user-group --home-dir /data --create-home freshbreath

WORKDIR /app
COPY --from=build /out/ ./
RUN mv freshbreath /usr/local/bin/freshbreath && chown -R freshbreath:freshbreath /data

ENV FRBR_DIR=/app \
    FRBR_DATA_DIR=/data \
    FRBR_LISTEN_ADDR=:9009

USER freshbreath
EXPOSE 9009
VOLUME ["/data"]

# /env.js is served unauthenticated and exercises the config path, so a 200
# here means rather more than a bare TCP connect would. Both schemes are
# tried because FRBR_TLS_CERT/KEY can flip the server to https.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD curl -fsSk "http://127.0.0.1:9009/env.js" > /dev/null \
   || curl -fsSk "https://127.0.0.1:9009/env.js" > /dev/null || exit 1

ENTRYPOINT ["freshbreath"]
