# syntax=docker/dockerfile:1
#
# Production image of Steward, the Haxurus fork of YAGPDB, deployed on VPS01 by
# ops/steward-deploy (docs/DEPLOYMENT.md). The upstream image in yagpdb_docker/ is
# left untouched. Build from the repository root:
#   docker build -f deploy/Dockerfile --build-arg STEWARD_VERSION=$(git describe --tags) .

FROM golang:1.26-alpine3.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Steward branding on the copied sources only (deploy/rebrand.sed).
ARG STEWARD_SUPPORT_URL=
RUN STEWARD_SUPPORT_URL="${STEWARD_SUPPORT_URL}" sh deploy/rebrand.sh /src
ARG STEWARD_VERSION=dev
RUN GOEXPERIMENT=jsonv2 CGO_ENABLED=0 go build -trimpath \
      -ldflags "-X github.com/botlabs-gg/yagpdb/v2/common.VERSION=${STEWARD_VERSION}" \
      -o /out/steward ./cmd/yagpdb

FROM alpine:3.24 AS runtime
# ffmpeg transcodes soundboard uploads, tzdata serves timezone conversions.
RUN apk add --no-cache ca-certificates tzdata ffmpeg \
 && addgroup -S -g 10001 steward \
 && adduser -S -D -H -u 10001 -G steward -h /data steward \
 && install -d -o 10001 -g 10001 -m 750 /data

COPY --from=build /out/steward /app/steward
COPY --chmod=0755 deploy/runtime/entrypoint.sh /app/entrypoint.sh
# MIT license of YAGPDB, shipped with the software.
COPY LICENSE /app/LICENSE

# Deployment bundle: steward-deploy installs these files on the VPS from the image it
# deploys, so the Compose configuration always matches the running release.
COPY deploy/docker-compose.prod.yml /opt/steward-deploy/docker-compose.yml
COPY deploy/steward.env.example /opt/steward-deploy/steward.env.example
COPY deploy/runtime/nginx.conf /opt/steward-deploy/runtime/nginx.conf
COPY --chmod=0755 deploy/runtime/db-init.sh /opt/steward-deploy/runtime/db-init.sh

# The bot reads and writes relative to its working directory (soundboard files,
# safebrowsing database, access log, blog posts): /data is the writable volume.
WORKDIR /data
USER 10001:10001
EXPOSE 5000
ENTRYPOINT ["/app/entrypoint.sh"]
CMD ["-all", "-https=false", "-exthttps=true"]
