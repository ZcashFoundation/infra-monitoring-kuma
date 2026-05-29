# syntax=docker/dockerfile:1
ARG UPTIME_KUMA_VERSION=2.3.2
FROM louislam/uptime-kuma:${UPTIME_KUMA_VERSION} AS runner

ARG PORT=3001
ENV PORT=${PORT}
ENV UPTIME_KUMA_IS_CONTAINER=1

EXPOSE ${PORT}

# The upstream uptime-kuma image is designed to run as root: it manages its own
# data directory and child processes, and a non-root USER breaks startup. We
# inherit that here intentionally, so skip the non-root-user policy.
# checkov:skip=CKV_DOCKER_3:upstream uptime-kuma image runs as root by design

# Reuse Uptime Kuma's built-in healthcheck (same as the upstream image).
HEALTHCHECK --interval=60s --timeout=30s --start-period=180s --retries=5 \
    CMD extra/healthcheck
