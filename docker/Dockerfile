# syntax=docker/dockerfile:1
# ===================== Create base stage =====================
ARG NODE_VERSION=lts
ARG UPTIME_KUMA_VERSION=1.23.13
ARG WORK_DIR=/app
FROM louislam/uptime-kuma:${UPTIME_KUMA_VERSION} AS base

ARG PORT=3001
ARG WORK_DIR

ENV WORK_DIR=${WORK_DIR}

WORKDIR ${WORK_DIR}

# ==== App specific variables

ENV UPTIME_KUMA_IS_CONTAINER=1

# ===================== App Runner Stage =====================
FROM base AS runner

RUN apt-get update --no-install-recommends \
    && apt-get install -y ca-certificates iputils-ping wget \
    && rm -rf /var/lib/apt/lists/*

# Copy all necessary files
COPY --from=litestream/litestream:0.3.13 /usr/local/bin/litestream /usr/local/bin/litestream

# Create data directory (although this will likely be mounted too) as some services won't mount it.
RUN mkdir -p /data

    EXPOSE ${PORT}

ENV PORT=${PORT}

HEALTHCHECK --interval=60s --timeout=30s --start-period=180s --retries=5 CMD extra/healthcheck

# Copy Litestream configuration file & startup script.
# COPY etc/litestream.yml /etc/litestream.yml
COPY scripts/run.sh /scripts/run.sh

CMD [ "/scripts/run.sh" ]
