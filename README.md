# Traefik Datadog Sidecar

A Go-based sidecar container that provides Datadog APM integration for Traefik, achieving feature parity with nginx-datadog-module.

## Why This Sidecar?

Traefik's native Datadog integration cannot set custom `resource_name` for APM traces. This results in generic resource names like "GET" instead of hostnames (e.g., `api.example.com`).

This sidecar parses Traefik access logs and sends properly formatted traces and metrics to Datadog, with hostname-based resource names.

## Features

- **APM Traces** with hostname as resource name (not generic HTTP method)
- **Metrics** matching nginx-datadog-module format:
  - `trace.traefik.request` - APM latency (p50-p99)
  - `trace.traefik.request.hits` - Request count
  - `trace.traefik.request.duration` - Latency histogram
  - `trace.traefik.request.errors` - Error count
  - `trace.traefik.request.apdex` - Apdex score
- **Full tag support**: `peer.hostname`, `resource_name`, `http.status_code`, `env`, `service`, `version`

## Architecture

```
┌─────────────────────────────────────────────────────┐
│  Traefik Pod                                        │
├─────────────────────────────────────────────────────┤
│  ┌─────────────┐         ┌───────────────────────┐ │
│  │  Traefik    │──JSON──▶│ /var/log/traefik/     │ │
│  │  (main)     │  logs   │ access.log            │ │
│  └─────────────┘         │ (shared volume)       │ │
│                          └───────────┬───────────┘ │
│                                      │             │
│                          ┌───────────▼───────────┐ │
│                          │  datadog-sidecar      │ │
│                          │  (this container)     │ │
│                          └───────────┬───────────┘ │
│                                      │             │
│                          ┌───────────▼───────────┐ │
│                          │  Datadog Agent        │ │
│                          │  APM:8126 DogStatsD:  │ │
│                          │  8127                 │ │
│                          └───────────────────────┘ │
└─────────────────────────────────────────────────────┘
```

## Configuration

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `DOGSTATSD_ADDRESS` | `datadog-apm.datadog.svc:8127` | DogStatsD endpoint |
| `SERVICE_NAME` | `traefik` | Service name in Datadog |
| `ENVIRONMENT` | `staging` | Environment tag |
| `VERSION` | `3.6.7` | Version tag |
| `LOG_FILE` | `/var/log/traefik/access.log` | Traefik access log path |

### Traefik Access Log Format

Traefik must be configured to write JSON access logs:

```yaml
accessLog:
  filePath: /var/log/traefik/access.log
  format: json
  fields:
    defaultMode: keep
```

## Deployment

### Build

```bash
docker buildx build --platform linux/amd64 -t your-registry/traefik-datadog-sidecar:v2.0.2 .
docker push your-registry/traefik-datadog-sidecar:v2.0.2
```

### Helm Values Example

```yaml
deployment:
  additionalVolumes:
    - name: traefik-logs
      emptyDir: {}
  additionalContainers:
    - name: datadog-sidecar
      image: your-registry/traefik-datadog-sidecar:v2.0.2
      env:
        - name: DOGSTATSD_ADDRESS
          value: "datadog-apm.datadog.svc:8127"
        - name: SERVICE_NAME
          value: "traefik-production"
        - name: ENVIRONMENT
          value: "production"
      volumeMounts:
        - name: traefik-logs
          mountPath: /var/log/traefik
          readOnly: true
      resources:
        limits:
          memory: 64Mi
        requests:
          cpu: 50m
          memory: 64Mi
```

## Metrics Comparison with nginx

| nginx Metric | traefik Metric |
|--------------|----------------|
| `trace.nginx.request` | `trace.traefik.request` |
| `trace.nginx.request.hits` | `trace.traefik.request.hits` |
| `trace.nginx.request.duration` | `trace.traefik.request.duration` |
| `trace.nginx.request.errors` | `trace.traefik.request.errors` |
| `trace.nginx.request.apdex` | `trace.traefik.request.apdex` |

## License

Vhico Putra
