FROM golang:1.21-alpine AS builder

WORKDIR /build

COPY go.mod ./
RUN go mod download
COPY . .

RUN mkdir -p /output && \
    cp -r . /output/

FROM alpine:latest

WORKDIR /plugins

COPY --from=builder /output/ ./
RUN chmod -R 644 ./*.go ./.traefik.yml 2>/dev/null || true
