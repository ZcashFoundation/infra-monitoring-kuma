# syntax=docker/dockerfile:1
ARG UPTIME_KUMA_VERSION=2.3.2
FROM louislam/uptime-kuma:${UPTIME_KUMA_VERSION} AS runner

ARG PORT=3001
ENV PORT=${PORT}
ENV UPTIME_KUMA_IS_CONTAINER=1

EXPOSE ${PORT}

# Run as the image's built-in node user (uid 1000). Upstream pre-chowns
# /app/data to node, and 2.3.2 starts clean as non-root (verified: HTTP up,
# extra/healthcheck OK, DB written). Cloud Run's in-memory volume is a tmpfs
# writable by any uid, so the /app/data mount keeps working.
USER node

# Reuse Uptime Kuma's built-in healthcheck (same as the upstream image).
HEALTHCHECK --interval=60s --timeout=30s --start-period=180s --retries=5 \
    CMD extra/healthcheck
