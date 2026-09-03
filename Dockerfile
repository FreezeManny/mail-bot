# syntax=docker/dockerfile:1

FROM golang:1.23-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/mailsorter ./cmd/mailsorter

FROM alpine:3.22
# ca-certificates is required for IMAP over TLS; tzdata for local-time handling.
# Alpine ships neither by default.
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -H -u 10001 mailsorter
COPY --from=builder /out/mailsorter /usr/local/bin/mailsorter
USER mailsorter

ENTRYPOINT ["/usr/local/bin/mailsorter"]
