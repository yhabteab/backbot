FROM golang:1 AS base

WORKDIR /build

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
  go mod download

FROM base AS builder

RUN --mount=type=bind,source=.,target=.,readonly \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build \
	  -trimpath \
	  -ldflags '-s -w' \
      -tags netgo \
      -o /usr/local/bin/backbot \
      .

FROM alpine:3

# Install git required to perform git cerry-pick operations
RUN apk --no-cache add git

WORKDIR /

COPY --from=builder /usr/local/bin/backbot .

ENTRYPOINT ["/backbot"]
