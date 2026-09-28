# CulpOS — production image built from source
#
#   docker build -t culpos .
#
# Builds behind a TLS-intercepting proxy can supply its CA without baking it
# into the image:
#   docker build --secret id=build_ca,src=/path/to/ca.crt -t culpos .
#
# Stages: web applications (Node) → server (Go) → dart-sass → runtime

############################
# 1. Web applications
############################
FROM node:22-bookworm AS webapps

ARG VERSION=culpos

RUN --mount=type=secret,id=build_ca,required=false \
    if [ -s /run/secrets/build_ca ]; then \
      cp /run/secrets/build_ca /usr/local/share/ca-certificates/build-ca.crt && update-ca-certificates; \
    fi
ENV NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt \
    YARN_CAFILE=/etc/ssl/certs/ca-certificates.crt \
    YARN_CACHE_FOLDER=/tmp/yarn-cache \
    BUILD_VERSION=${VERSION}

WORKDIR /src
COPY lib ./lib

RUN cd lib/js && yarn install --frozen-lockfile --non-interactive && yarn link && yarn build \
 && cd ../vue && yarn install --frozen-lockfile --non-interactive && yarn link @cortezaproject/corteza-js && yarn build && yarn link \
 && rm -rf /tmp/yarn-cache

COPY client ./client

RUN set -e; mkdir -p /webapp; \
    for app in one compose admin workflow reporter privacy; do \
      cd /src/client/web/$app; \
      yarn install --frozen-lockfile --non-interactive; \
      yarn link @cortezaproject/corteza-js @cortezaproject/corteza-vue; \
      yarn build; \
      if [ "$app" = "one" ]; then cp -r dist/. /webapp/; else mkdir -p /webapp/$app && cp -r dist/. /webapp/$app/; fi; \
      rm -rf node_modules dist /tmp/yarn-cache; \
    done; \
    find /webapp -name '*.map' -delete

############################
# 2. Server
############################
FROM golang:1.24-bookworm AS server

ARG VERSION=culpos
WORKDIR /src
COPY server ./server
COPY locale ./locale

RUN cd server/pkg/locale && rm -rf src/en && cp -r ../../../locale/en ./src/
RUN cd server && CGO_ENABLED=1 GOFLAGS=-mod=vendor go build -trimpath \
      -ldflags "-s -w -X github.com/cortezaproject/corteza/server/pkg/version.Version=${VERSION}" \
      -o /out/culpos-server ./cmd/corteza

############################
# 3. dart-sass (theme compilation at runtime)
############################
FROM debian:bookworm-slim AS sass

ARG SASS_VERSION=1.69.5
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl && rm -rf /var/lib/apt/lists/*
RUN --mount=type=secret,id=build_ca,required=false \
    CURL_CA=""; if [ -s /run/secrets/build_ca ]; then CURL_CA="--cacert /run/secrets/build_ca"; fi; \
    curl -fsSL $CURL_CA "https://github.com/sass/dart-sass/releases/download/${SASS_VERSION}/dart-sass-${SASS_VERSION}-linux-x64.tar.gz" \
      | tar -xz -C /opt

############################
# 4. Runtime
############################
FROM debian:bookworm-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl tzdata \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --system --home /culpos --shell /usr/sbin/nologin culpos

WORKDIR /culpos

COPY --from=sass /opt/dart-sass /opt/dart-sass
COPY --from=server /out/culpos-server ./bin/culpos-server
COPY --from=webapps /webapp ./webapp
COPY server/provision ./provision
COPY LICENSE NOTICE ./

RUN mkdir -p /data && chown -R culpos:culpos /data

ENV ENVIRONMENT="production" \
    STORAGE_PATH="/data" \
    HTTP_ADDR="0.0.0.0:8080" \
    HTTP_WEBAPP_ENABLED="true" \
    HTTP_WEBAPP_BASE_DIR="/culpos/webapp" \
    HTTP_WEBAPP_LIST="admin,compose,workflow,reporter,privacy" \
    HTTP_SERVER_WEB_CONSOLE_ENABLED="false" \
    PROVISION_PATH="/culpos/provision/*" \
    PATH="/opt/dart-sass:/culpos/bin:${PATH}"

USER culpos
VOLUME /data
EXPOSE 8080

HEALTHCHECK --interval=30s --start-period=120s --timeout=10s --retries=3 \
    CMD curl --silent --fail http://127.0.0.1:8080/health || exit 1

ENTRYPOINT ["/culpos/bin/culpos-server"]
CMD ["serve-api"]
