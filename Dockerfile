# Fresh Breath — container image.
#
# This image compiles nothing. It unpacks a release tarball — the very same
# `freshbreath-<version>-linux-<arch>.tar.gz` that `mise run build:linux-static`
# produces and that CI attaches to the GitHub release. One build path, one
# set of bytes: if the tarball on the release page works, so does this.
#
# That's the trade: you can't `docker build .` from a bare checkout, there
# has to be a tarball in dist/ first. The mise task does both:
#
#   mise run docker:build
#   docker run -p 9009:9009 -v freshbreath-data:/data freshbreath:dev
#
# The binary is static musl, so the base image below is chosen purely for
# the *task scripts'* comfort — Fresh Breath itself would be just as happy
# on scratch. Debian gives a task a real shell, git, ssh and curl to work
# with, which is rather the point of task services.

FROM debian:bookworm-slim

# Set by buildx per platform (amd64 / arm64); "amd64" when you build by
# hand. It picks which tarball out of dist/ gets unpacked, which is the
# whole of this image's cross-architecture story — there's no compiler here
# to emulate.
ARG TARGETARCH=amd64

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates curl git openssh-client tzdata \
 && rm -rf /var/lib/apt/lists/*

# Non-root by default. A named volume mounted at /data inherits this
# ownership; a bind mount does not, so chown the host directory to 10001
# if you use one.
RUN useradd --uid 10001 --user-group --home-dir /data --create-home freshbreath

WORKDIR /app
COPY dist/*.tar.gz /tmp/dist/
# The ldd check at the end: a tarball built dynamically on some other distro
# would unpack here perfectly happily and only fail when someone runs the
# container. Ask it now, while anyone is still watching.
RUN set -eu; \
    case "$TARGETARCH" in \
      amd64) arch=x64 ;; \
      arm64) arch=arm64 ;; \
      *) echo "✗ unsupported arch $TARGETARCH"; exit 1 ;; \
    esac; \
    tar -xzf /tmp/dist/freshbreath-*-linux-"$arch".tar.gz -C /app; \
    rm -rf /tmp/dist; \
    mv /app/freshbreath /usr/local/bin/freshbreath; \
    chown -R freshbreath:freshbreath /data; \
    if ! ldd /usr/local/bin/freshbreath 2>&1 | grep -q "not a dynamic executable"; then \
      echo "✗ binary is not static — build it with: mise run build:linux-static"; exit 1; \
    fi

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
