FROM golang:1.21-alpine AS builder

WORKDIR /build
COPY main.go go.mod ./
RUN CGO_ENABLED=0 GOOS=linux go build -o datadog-sidecar main.go

FROM alpine:latest
RUN apk add --no-cache ca-certificates
COPY --from=builder /build/datadog-sidecar /usr/local/bin/datadog-sidecar
ENTRYPOINT ["/usr/local/bin/datadog-sidecar"]
