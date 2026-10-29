FROM --platform=$BUILDPLATFORM golang:1.24.13-alpine3.22 AS builder

ARG VERSION
ARG GIT_COMMIT
ARG GIT_DATE

RUN apk add --no-cache make gcc musl-dev linux-headers git jq bash just

COPY . /app

WORKDIR /app/gas-oracle

RUN go mod download

ARG TARGETOS TARGETARCH

RUN GITCOMMIT="${GIT_COMMIT:-$(git rev-parse HEAD 2>/dev/null || true)}" && \
    GITDATE="${GIT_DATE:-$(git show -s --format='%ct' 2>/dev/null || true)}" && \
    export CGO_ENABLED=0 && \
    if [ -n "${VERSION:-}" ]; then \
      make gas-oracle VERSION="$VERSION" GITCOMMIT="$GITCOMMIT" GITDATE="$GITDATE" GOOS=$TARGETOS GOARCH=$TARGETARCH; \
    else \
      make gas-oracle GITCOMMIT="$GITCOMMIT" GITDATE="$GITDATE" GOOS=$TARGETOS GOARCH=$TARGETARCH; \
    fi

# Opt-in runtime: docker build --target alpine ...
FROM alpine:3.22 AS alpine

RUN apk add --no-cache ca-certificates jq curl
COPY --from=builder /app/gas-oracle/bin/gas-oracle /usr/local/bin/

WORKDIR /usr/local/bin/
ENTRYPOINT ["gas-oracle"]

# Default runtime: must stay the last stage, builds without --target use it.
# cgr.dev/chainguard/static:latest
FROM cgr.dev/chainguard/static@sha256:fe55470f22d3259488d9d3739168d8f04da67755f0b69382bc26eda4a7d3d327 AS chainguard

COPY --from=builder /app/gas-oracle/bin/gas-oracle /usr/local/bin/

WORKDIR /usr/local/bin/
ENTRYPOINT ["gas-oracle"]
