# syntax=docker/dockerfile:1
# ===================== Create base stage =====================
ARG NODE_VERSION=lts
ARG UPTIME_KUMA_VERSION=nightly2
ARG APP_HOME=/app
FROM louislam/uptime-kuma:${UPTIME_KUMA_VERSION} AS base

ARG PORT=3001
ARG APP_HOME

ENV APP_HOME=${APP_HOME}

WORKDIR ${APP_HOME}

# ==== App specific variables

ENV UPTIME_KUMA_IS_CONTAINER=1
ARG DATA_DIR=./data/
ENV DATA_DIR=${DATA_DIR}
ENV DB_PATH=${DATA_DIR}kuma.db

# ===================== App Runner Stage =====================
FROM base AS runner

# Copy all necessary files
COPY --from=litestream/litestream:0.3.13 /usr/local/bin/litestream /usr/local/bin/litestream

# Create data directory (although this will likely be mounted too) as some services won't mount it.
RUN mkdir -p "${DATA_DIR}"

EXPOSE ${PORT}

ENV PORT=${PORT}

HEALTHCHECK --interval=60s --timeout=30s --start-period=180s --retries=5 CMD extra/healthcheck

# Copy Litestream configuration file & startup script.
COPY etc/litestream.yml /etc/litestream.yml
COPY scripts/run.sh /scripts/run.sh

USER node

CMD [ "/scripts/run.sh" ]
