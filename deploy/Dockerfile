# syntax=docker/dockerfile:1
#
# Production image of the Haxurus fork, deployed on VPS01 by ops/yagpdb-deploy
# (docs/DEPLOYMENT.md). The upstream image in yagpdb_docker/ is left untouched.
# Build from the repository root:
#   docker build -f deploy/Dockerfile --build-arg YAGPDB_VERSION=$(git describe --tags) .

FROM golang:1.26-alpine3.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG YAGPDB_VERSION=dev
RUN GOEXPERIMENT=jsonv2 CGO_ENABLED=0 go build -trimpath \
      -ldflags "-X github.com/botlabs-gg/yagpdb/v2/common.VERSION=${YAGPDB_VERSION}" \
      -o /out/yagpdb ./cmd/yagpdb

FROM alpine:3.24 AS runtime
# ffmpeg transcodes soundboard uploads, tzdata serves timezone conversions.
RUN apk add --no-cache ca-certificates tzdata ffmpeg \
 && addgroup -S -g 10001 yagpdb \
 && adduser -S -D -H -u 10001 -G yagpdb -h /data yagpdb \
 && install -d -o 10001 -g 10001 -m 750 /data

COPY --from=build /out/yagpdb /app/yagpdb
COPY cmd/yagpdb/posts /app/posts
COPY --chmod=0755 deploy/runtime/entrypoint.sh /app/entrypoint.sh

# Deployment bundle: yagpdb-deploy installs these files on the VPS from the image it
# deploys, so the Compose configuration always matches the running release.
COPY deploy/docker-compose.prod.yml /opt/yagpdb-deploy/docker-compose.yml
COPY deploy/yagpdb.env.example /opt/yagpdb-deploy/yagpdb.env.example
COPY deploy/runtime/nginx.conf /opt/yagpdb-deploy/runtime/nginx.conf
COPY --chmod=0755 deploy/runtime/db-init.sh /opt/yagpdb-deploy/runtime/db-init.sh

# YAGPDB reads and writes relative to its working directory (soundboard files,
# safebrowsing database, access log, blog posts): /data is the writable volume.
WORKDIR /data
USER 10001:10001
EXPOSE 5000
ENTRYPOINT ["/app/entrypoint.sh"]
CMD ["-all", "-https=false", "-exthttps=true"]
