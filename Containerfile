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

FROM scratch

WORKDIR /

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/local/bin/backbot .

ENTRYPOINT ["/backbot"]
