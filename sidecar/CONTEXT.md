# Context Prompt for Traefik Datadog Integration

## Objective
Replace ingress-nginx with Traefik while maintaining **identical Datadog APM and metrics behavior**.

## Current Status: v2.0.1 ✅ WORKING

### What's Been Achieved
1. **Resource names show hostnames** - APM Resources tab displays `api-dummy-cfs-traefik.mekari.io` instead of generic "GET"
2. **Metrics working** - `trace.traefik.request.hits`, `trace.traefik.request.duration`, etc.
3. **Traces captured** - With durations, status codes, methods
4. **Latency monitoring** - p50, p75, p90, p95, p99 available in Service Summary
5. **Error tracking** - By HTTP status code (429, 499, 500, etc.)
6. **All 14 tag keys present** - peer.hostname, resource_name, env, service, version, http.status_code, http.method, etc.

### Validated with Load Test
```bash
oha --no-tui -n 200 -c 50 -z 10s https://api-dummy-cfs-traefik.mekari.io/health
```
- 8,543 requests sent → Datadog shows 8.58k hits ✅ Match!
- 7,653 errors (429 rate limit) → Datadog shows 7.68k errors ✅ Match!

### Bug Fixed in v2.0.1
**Problem:** v2.0.0 had a file tailing bug - `bufio.Scanner` doesn't work for tailing files. Once it hits EOF, it never recovers.

**Fix:** Replaced with `bufio.Reader` and `ReadString('\n')` that properly handles EOF and continues reading new lines.

## Architecture

**Pod contains 3 containers:**
- **Traefik** (main): Writes JSON access logs to `/var/log/traefik/access.log`
- **log-forwarder** (busybox): Tails logs to stdout → Promtail → Loki (for Grafana)
- **datadog-sidecar** (Go): Reads logs → sends metrics (DogStatsD 8127) + traces (APM 8126)

**Image:** `imageregistry-shared-alpha-registry.ap-southeast-5.cr.aliyuncs.com/mekariengineering/traefik-datadog-sidecar:v2.0.1`

## Key Files

**Sidecar Code:**
- `main.go` - Main logic with proper file tailing
- `Dockerfile` - Multi-stage build
- `build.sh` - Build script with `--platform linux/amd64`

**Deployment:**
- `/Users/mekari/Documents/MEKARI/deployment-argocd/cfs/traefik/values-staging.yaml`

## Potentially Missing (vs nginx)

Based on earlier nginx screenshots, these tags may still be missing:
- `span.kind` (client/internal)
- `synthetics` (true/false)

These can be added in a future iteration if needed.

## Build & Deploy

```bash
cd /Users/mekari/Documents/MEKARI/traefik-datadog-plugin/sidecar
./build.sh v2.0.2  # or next version
docker push imageregistry-shared-alpha-registry.ap-southeast-5.cr.aliyuncs.com/mekariengineering/traefik-datadog-sidecar:v2.0.2
# Update values-staging.yaml image tag
argocd app sync traefik-cfs-staging-echo
```

## Next Steps (When User Returns)

User will test:
1. Creating monitors/alerts based on metrics
2. Query compatibility with existing nginx dashboards
3. Production deployment

**When user reports back, focus on:**
- Any missing tags needed for dashboards/monitors
- Any gaps compared to nginx APM behavior
- Production readiness

## Quick Start for New Session

```
I'm continuing work on Traefik Datadog integration. v2.0.1 is deployed and working.
Here's what I need help with: [describe issue or next task]
```
