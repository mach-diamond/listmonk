# syntax=docker/dockerfile:1
#
# The MachDiamond build of this fork, from source: the admin frontend, the
# visual email builder and the Go binary, with every asset stuffed into the
# binary. (The Dockerfile at the root is upstream's; it expects a binary built
# by goreleaser.)
#
#   docker buildx build --platform linux/amd64 -f deploy/Dockerfile \
#     --build-arg VERSION=v6.2.0 --build-arg COMMIT=$(git rev-parse HEAD) -t machdiamond/listmonk:<commit> .
#
# The JS and Go stages run on the build machine and cross-compile; only the
# last stage is the target platform. Node 22 (LTS) is what the frontend's
# toolchain (Vite, ESLint) is known to build with.

FROM --platform=$BUILDPLATFORM node:22.23.3-alpine3.24 AS frontend
ARG VERSION=v0.0.0
WORKDIR /src
COPY frontend/ frontend/
COPY static/ static/
RUN cd frontend/email-builder && yarn install --frozen-lockfile --network-timeout 600000 && yarn build
RUN mkdir -p frontend/public/static/email-builder && cp -r frontend/email-builder/dist/* frontend/public/static/email-builder/
# The frontend's postinstall copies the Altcha widget into static/public/static. Its prebuild lint reads
# .gitignore as its ignore file, and .dockerignore keeps every .gitignore out of the context; lint only
# looks at src/, so an empty one does.
RUN touch frontend/.gitignore && cd frontend && yarn install --frozen-lockfile --network-timeout 600000 && VUE_APP_VERSION="$VERSION" yarn build

FROM --platform=$BUILDPLATFORM golang:1.26.9-alpine3.24 AS backend
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=v0.0.0
ARG COMMIT=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go install github.com/knadh/stuffbin/stuffbin@v1.3.0
COPY . .
COPY --from=frontend /src/frontend/dist frontend/dist
COPY --from=frontend /src/static/public/static/altcha.umd.js static/public/static/altcha.umd.js
RUN CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build -trimpath -o listmonk \
      -ldflags="-s -w -X 'main.buildString=${VERSION} (#${COMMIT})' -X 'main.versionString=${VERSION}'" ./cmd \
 && stuffbin -a stuff -in listmonk -out listmonk \
      config.toml.sample schema.sql queries:/queries permissions.json \
      static/public:/public static/email-templates frontend/dist:/admin i18n:/i18n

FROM alpine:3.24.2
RUN apk --no-cache add ca-certificates tzdata \
 && adduser -D -H -u 1001 listmonk \
 && install -d -o 1001 -g 1001 /listmonk /listmonk/uploads
WORKDIR /listmonk
COPY --from=backend /src/listmonk /listmonk/listmonk
USER 1001
EXPOSE 9000
# Configuration comes from LISTMONK_* environment variables (see infra/compose.yaml), not a file.
CMD ["./listmonk", "--config", ""]
