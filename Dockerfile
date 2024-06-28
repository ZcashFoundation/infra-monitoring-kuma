# syntax=docker/dockerfile:1
# ===================== Create base stage =====================
ARG NODE_VERSION=lts
ARG UPTIME_KUMA_VERSION=1.23.13
ARG LITESTREAM_VERSION=0.3.13
ARG WORK_DIR=/app
FROM louislam/uptime-kuma:${UPTIME_KUMA_VERSION} AS base

ARG PORT=3001
ARG WORK_DIR

ENV WORK_DIR=${WORK_DIR}

WORKDIR ${WORK_DIR}

# ==== App specific variables

ENV UPTIME_KUMA_IS_CONTAINER=1
ENV UPTIME_KUMA_VERSION=${UPTIME_KUMA_VERSION}
ENV LITESTREAM_VERSION=${LITESTREAM_VERSION}

# ===================== App Runner Stage =====================
FROM base AS runner

RUN apt-get update --no-install-recommends \
    && apt-get install -y ca-certificates iputils-ping wget \
    && rm -rf /var/lib/apt/lists/*

# Copy all necessary files
COPY --from=litestream/litestream:${LITESTREAM_VERSION} /usr/local/bin/litestream /usr/local/bin/litestream

USER node

EXPOSE ${PORT}

ENV PORT ${PORT}
ENV HOSTNAME 0.0.0.0

HEALTHCHECK --interval=60s --timeout=30s --start-period=180s --retries=5 CMD extra/healthcheck

# Copy Litestream configuration file & startup script.
COPY etc/litestream.yml /etc/litestream.yml
COPY scripts/run.sh /scripts/run.sh

CMD [ "/scripts/run.sh" ]
