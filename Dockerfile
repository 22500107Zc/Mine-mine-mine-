# CulpOS — production image built from source
#
#   docker build -t culpos .
#
# Stages: web applications (Node) → server (Go) → runtime (Debian slim)

############################
# 1. Web applications
############################
FROM node:22-bookworm AS webapps

WORKDIR /src
COPY lib ./lib
COPY client ./client

RUN cd lib/js && yarn --frozen-lockfile --non-interactive && yarn link && yarn build \
 && cd ../vue && yarn --frozen-lockfile --non-interactive && yarn link @cortezaproject/corteza-js && yarn build && yarn link

RUN set -e; mkdir -p /webapp; \
    for app in one compose admin workflow reporter privacy; do \
      cd /src/client/web/$app; \
      yarn --frozen-lockfile --non-interactive; \
      yarn link @cortezaproject/corteza-js @cortezaproject/corteza-vue; \
      yarn build; \
      if [ "$app" = "one" ]; then cp -r dist/. /webapp/; else mkdir -p /webapp/$app && cp -r dist/. /webapp/$app/; fi; \
    done

############################
# 2. Server
############################
FROM golang:1.24-bookworm AS server

ARG VERSION=culpos
WORKDIR /src
COPY server ./server
COPY locale ./locale

RUN cd server/pkg/locale && rm -rf src/en && cp -r ../../../locale/en ./src/
RUN cd server && CGO_ENABLED=0 go build -mod=vendor -trimpath \
      -ldflags "-s -w -X github.com/cortezaproject/corteza/server/pkg/version.Version=${VERSION}" \
      -o /out/culpos-server ./cmd/corteza

############################
# 3. Runtime
############################
FROM debian:bookworm-slim

ARG SASS_VERSION=1.69.5

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl \
 && curl -sSL "https://github.com/sass/dart-sass/releases/download/${SASS_VERSION}/dart-sass-${SASS_VERSION}-linux-x64.tar.gz" | tar -xz -C /opt \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --system --home /culpos --shell /usr/sbin/nologin culpos

WORKDIR /culpos

COPY --from=server /out/culpos-server ./bin/culpos-server
COPY --from=webapps /webapp ./webapp
COPY server/provision ./provision
COPY LICENSE NOTICE ./

RUN mkdir -p /data && chown -R culpos:culpos /data /culpos

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

HEALTHCHECK --interval=30s --start-period=90s --timeout=10s --retries=3 \
    CMD curl --silent --fail http://127.0.0.1:8080/healthcheck || exit 1

ENTRYPOINT ["/culpos/bin/culpos-server"]
CMD ["serve-api"]
