# Traefik Datadog Plugin

Custom Traefik plugin that provides comprehensive Datadog integration, similar to the Nginx Datadog module.

## Features

- **Metrics**: Sends detailed metrics to DogStatsD
  - `trace.traefik.request.hits` - Total request count
  - `trace.traefik.request.hits.by_http_status` - Request count by HTTP status
  - `trace.traefik.request.duration` - Request duration histogram
  - `trace.traefik.request.duration.by_http_status` - Duration by HTTP status
  - `trace.traefik.request.errors` - Error count
  - `trace.traefik.request.errors.by_http_status` - Errors by HTTP status
  - `trace.traefik.request.apdex` - Apdex score

- **Traces**: Sends traces to Datadog APM via OTLP
  - Proper `resource_name` set to hostname (not HTTP method)
  - All required tags: `peer.hostname`, `http.status_code`, `service`, `env`, `version`

## Architecture

This plugin uses Traefik's Yaegi plugin system, which allows Go plugins to be loaded dynamically without compilation.

## Building

### Option 1: Yaegi Plugin (Recommended for Phase 1)

The plugin is written in pure Go using standard library, making it compatible with Yaegi:

```bash
docker build -t imageregistry-shared-alpha-registry-vpc.ap-southeast-5.cr.aliyuncs.com/mekariengineering/traefik-datadog-plugin:latest .
docker push imageregistry-shared-alpha-registry-vpc.ap-southeast-5.cr.aliyuncs.com/mekariengineering/traefik-datadog-plugin:latest
```

### Option 2: Compiled Plugin (Future)

For compiled plugins, uncomment the build commands in the Dockerfile and build as a shared library.

## Configuration

### Helm Values

The plugin is configured via `values-staging.yaml`:

```yaml
datadogPlugin:
  enabled: true
  namespace: traefik
  dogstatsdAddress: "datadog-apm.datadog.svc:8127"
  otlpEndpoint: "http://datadog-apm.datadog.svc:4318/v1/traces"
  serviceName: "traefik-cfs-staging"
  environment: "staging"
  version: "3.6.5"
  sampleRate: 1.0
  apdexThreshold: 0.5
```

### Traefik Plugin Configuration

The plugin is registered in Traefik's experimental plugins:

```yaml
experimental:
  plugins:
    datadog:
      moduleName: github.com/mekari/traefik-datadog-plugin
      version: v1.0.0
```

### Middleware Usage

Apply the middleware to your Ingress resources:

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: my-ingress
  annotations:
    traefik.ingress.kubernetes.io/middlewares: traefik-datadog-plugin@kubernetescrd
spec:
  # ... ingress spec
```

Or apply globally via Traefik's default middleware chain.

## Deployment

1. **Build and push the plugin image**:
   ```bash
   docker build -t imageregistry-shared-alpha-registry-vpc.ap-southeast-5.cr.aliyuncs.com/mekariengineering/traefik-datadog-plugin:latest .
   docker push imageregistry-shared-alpha-registry-vpc.ap-southeast-5.cr.aliyuncs.com/mekariengineering/traefik-datadog-plugin:latest
   ```

2. **Update Helm values** in `values-staging.yaml`:
   - Ensure `datadogPlugin.enabled: true`
   - Verify Datadog endpoints are correct
   - Set service name and environment

3. **Deploy via Helm/ArgoCD**:
   ```bash
   helm upgrade --install traefik . -f values-staging.yaml
   ```

4. **Verify plugin loading**:
   ```bash
   kubectl logs -n traefik deployment/traefik-cfs-staging | grep -i plugin
   ```

5. **Check metrics in Datadog**:
   - Look for metrics prefixed with `trace.traefik.request.*`
   - Verify tags include `peer.hostname`, `http.status_code`, etc.

## Troubleshooting

### Plugin Not Loading

1. Check Traefik logs for plugin errors:
   ```bash
   kubectl logs -n traefik deployment/traefik-cfs-staging
   ```

2. Verify plugin configuration in values:
   ```bash
   helm get values traefik -n traefik
   ```

3. Check initContainer logs:
   ```bash
   kubectl logs -n traefik <pod-name> -c plugin-loader
   ```

### Metrics Not Appearing

1. Verify DogStatsD endpoint is reachable:
   ```bash
   kubectl exec -n traefik <pod-name> -- nc -u -v datadog-apm.datadog.svc 8127
   ```

2. Check plugin is applied to routes:
   ```bash
   kubectl get middleware -n traefik
   ```

3. Verify Datadog Agent is receiving metrics:
   ```bash
   kubectl logs -n datadog <datadog-agent-pod> | grep traefik
   ```

### Traces Not Appearing

1. Verify OTLP endpoint:
   ```bash
   kubectl exec -n traefik <pod-name> -- curl -v http://datadog-apm.datadog.svc:4318/v1/traces
   ```

2. Check trace format in plugin logs (if debug enabled)

3. Verify Datadog APM is configured correctly

## Development

### Local Testing

1. Run Traefik locally with plugin:
   ```bash
   traefik --experimental.plugins.datadog.moduleName=github.com/mekari/traefik-datadog-plugin --experimental.plugins.datadog.version=v1.0.0
   ```

2. Test with curl:
   ```bash
   curl -H "Host: test.example.com" http://localhost:8000/
   ```

3. Check metrics in Datadog

### Adding New Metrics

Edit `main.go` and add new metric calls in the `sendMetrics` function:

```go
p.statsd.Count("request.new_metric", 1, tags, 1.0)
```

## Phase 1 (POC) Status

✅ Basic plugin structure
✅ DogStatsD metrics integration
✅ OTLP trace integration
✅ Helm chart integration
✅ Middleware template
⏳ Production hardening (Phase 3)

## Phase 2 (Enhancement) - TODO

- [ ] Match Nginx's exact metric names and structure
- [ ] Improve OTLP trace format
- [ ] Add more detailed tags
- [ ] Performance optimization

## Phase 3 (Production) - TODO

- [ ] Comprehensive error handling
- [ ] Metrics batching
- [ ] Connection pooling
- [ ] Retry logic
- [ ] Circuit breakers
- [ ] Comprehensive testing

## License

Internal use only - Mekari Engineering
