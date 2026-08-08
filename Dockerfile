# syntax=docker/dockerfile:1

# The interface is built first, into the directory the Go binary embeds. Node
# exists only in this stage: the published image is distroless with no runtime,
# no shell, and no package manager, so a self-hoster still runs one command and
# never installs a toolchain.
FROM node:22-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY web/ ./
# The Go source, because several client tests read it directly rather than
# copying it: the rule engine checks itself against the Go package's own
# fixtures, and the role matrix is compared against roles.go and authn.go. Those
# guards exist to fail when the two sides drift, so they have to run here rather
# than skip for want of a file. Text only, and the stage is discarded.
COPY internal/ /internal/
# Tests before build, deliberately. The client rule engine and the Go one are
# checked against the same fixtures here, so an image cannot be produced in which
# the browser and the server disagree about which questions were required.
RUN npm run test && npm run build

FROM golang:1.26-alpine AS build
WORKDIR /src

# Dependencies change far less often than source, so they get their own layer.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
# Overwrites the committed placeholder. Without this the image would compile and
# serve a page telling the operator to build the interface -- which is exactly
# the failure this ordering exists to prevent.
COPY --from=web /internal/webui/dist ./internal/webui/dist

ARG VERSION=dev
ARG COMMIT=unknown
# CGO off produces a static binary that runs on distroless with no libc at all.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
      -o /out/collectr ./cmd/collectr && \
    CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
      -o /out/collectr-worker ./cmd/collectr-worker

# Created in the build stage so the final image carries a /data/files owned by
# the unprivileged user. Docker initialises a named volume from the image's
# directory, permissions included; without this the volume arrives owned by root
# and every upload fails on a fresh install -- with the process having no shell,
# and no way to fix it from inside.
RUN mkdir -p /out/data/files && chown -R 65532:65532 /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/collectr /collectr
COPY --from=build /out/collectr-worker /collectr-worker
COPY --from=build --chown=65532:65532 /out/data /data

# Runs unprivileged, and has no shell for an attacker to reach even if it did.
USER nonroot:nonroot
EXPOSE 8080

# CMD, not ENTRYPOINT: Compose's `command:` replaces CMD but is only *appended*
# to an ENTRYPOINT. With an entrypoint the worker would exec
# `/collectr /collectr-worker`, and Go's flag package stops parsing at the first
# positional argument -- so every service would have silently run the API server
# with its flags ignored.
CMD ["/collectr"]
