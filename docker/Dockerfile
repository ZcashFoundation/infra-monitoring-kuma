# syntax=docker/dockerfile:1
ARG UPTIME_KUMA_VERSION=2.5.0
# The -rootless variant is upstream's supported non-root image (USER node,
# /app/data chowned to node) — the non-root contract is theirs to keep across
# upgrades, not ours.
FROM louislam/uptime-kuma:${UPTIME_KUMA_VERSION}-rootless AS runner

ARG PORT=3001
ENV PORT=${PORT}
ENV UPTIME_KUMA_IS_CONTAINER=1

EXPOSE ${PORT}

# Redundant at runtime (the -rootless base already sets it) but kept explicit:
# image scanners read the Dockerfile, not the base image, and this keeps us
# non-root even if the base tag ever changes.
USER node

# Reuse Uptime Kuma's built-in healthcheck (same as the upstream image).
HEALTHCHECK --interval=60s --timeout=30s --start-period=180s --retries=5 \
    CMD extra/healthcheck
