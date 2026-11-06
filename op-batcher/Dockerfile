FROM --platform=$BUILDPLATFORM golang:1.24.13-alpine3.22 AS builder

ARG VERSION
ARG GIT_COMMIT
ARG GIT_DATE

RUN apk add --no-cache make gcc musl-dev linux-headers git jq bash just

# build op-batcher with the shared go.mod & go.sum files
COPY . /app

WORKDIR /app/op-batcher

RUN go mod download

ARG TARGETOS TARGETARCH

RUN GITCOMMIT="${GIT_COMMIT:-$(git rev-parse HEAD 2>/dev/null || true)}" && \
    GITDATE="${GIT_DATE:-$(git show -s --format='%ct' 2>/dev/null || true)}" && \
    if [ -n "${VERSION:-}" ]; then \
      just GITCOMMIT="$GITCOMMIT" GITDATE="$GITDATE" VERSION="$VERSION" GOOS=$TARGETOS GOARCH=$TARGETARCH op-batcher; \
    else \
      just GITCOMMIT="$GITCOMMIT" GITDATE="$GITDATE" GOOS=$TARGETOS GOARCH=$TARGETARCH op-batcher; \
    fi

# Opt-in runtime: docker build --target alpine ...
FROM alpine:3.22 AS alpine

COPY --from=builder /app/op-batcher/bin/op-batcher /usr/local/bin

CMD ["op-batcher"]

# Default runtime: must stay the last stage, builds without --target use it.
# cgr.dev/chainguard/static:latest
FROM cgr.dev/chainguard/static@sha256:fe55470f22d3259488d9d3739168d8f04da67755f0b69382bc26eda4a7d3d327 AS chainguard

COPY --from=builder /app/op-batcher/bin/op-batcher /usr/local/bin/

CMD ["op-batcher"]
