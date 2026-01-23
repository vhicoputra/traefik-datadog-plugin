package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DogStatsDAddress string `json:"dogstatsdAddress,omitempty"`
	APMAddress       string `json:"apmAddress,omitempty"`
	OTLPEndpoint     string `json:"otlpEndpoint,omitempty"`
	ServiceName      string `json:"serviceName,omitempty"`
	Environment      string `json:"environment,omitempty"`
	Version          string `json:"version,omitempty"`
	SampleRate       float64 `json:"sampleRate,omitempty"`
	ApdexThreshold   float64 `json:"apdexThreshold,omitempty"`
}

type DatadogPlugin struct {
	config         *Config
	statsdConn     *net.UDPConn
	otlpClient     *http.Client
	next           http.Handler
	name           string
	apdexThreshold float64
}

func New(ctx context.Context, next http.Handler, config map[string]interface{}, name string) (http.Handler, error) {
	cfg := &Config{
		DogStatsDAddress: getEnv("DD_AGENT_HOST", "datadog-apm.datadog.svc") + ":8127",
		APMAddress:       getEnv("DD_AGENT_HOST", "datadog-apm.datadog.svc") + ":8126",
		OTLPEndpoint:     "http://" + getEnv("DD_AGENT_HOST", "datadog-apm.datadog.svc") + ":4318/v1/traces",
		ServiceName:      getEnv("DD_SERVICE", "traefik"),
		Environment:      getEnv("DD_ENV", "staging"),
		Version:          getEnv("TRAEFIK_VERSION", "3.6.5"),
		SampleRate:       1.0,
		ApdexThreshold:   0.5,
	}

	if config != nil {
		if addr, ok := config["dogstatsdAddress"].(string); ok && addr != "" {
			cfg.DogStatsDAddress = addr
		}
		if addr, ok := config["apmAddress"].(string); ok && addr != "" {
			cfg.APMAddress = addr
		}
		if endpoint, ok := config["otlpEndpoint"].(string); ok && endpoint != "" {
			cfg.OTLPEndpoint = endpoint
		}
		if svc, ok := config["serviceName"].(string); ok && svc != "" {
			cfg.ServiceName = svc
		}
		if env, ok := config["environment"].(string); ok && env != "" {
			cfg.Environment = env
		}
		if ver, ok := config["version"].(string); ok && ver != "" {
			cfg.Version = ver
		}
		if rate, ok := config["sampleRate"].(float64); ok {
			cfg.SampleRate = rate
		}
		if threshold, ok := config["apdexThreshold"].(float64); ok {
			cfg.ApdexThreshold = threshold
		}
	}

	addr, err := net.ResolveUDPAddr("udp", cfg.DogStatsDAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve DogStatsD address: %w", err)
	}

	statsdConn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return nil, fmt.Errorf("failed to create DogStatsD connection: %w", err)
	}

	otlpClient := &http.Client{
		Timeout: 5 * time.Second,
	}

	plugin := &DatadogPlugin{
		config:         cfg,
		statsdConn:     statsdConn,
		otlpClient:     otlpClient,
		next:           next,
		name:           name,
		apdexThreshold: cfg.ApdexThreshold,
	}

	return plugin, nil
}

func (p *DatadogPlugin) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	startTime := time.Now()

	hostname := req.Host
	if hostname == "" {
		hostname = req.Header.Get("Host")
	}
	if hostname == "" {
		hostname = "unknown"
	}

	method := req.Method

	wrappedRW := &responseWriter{
		ResponseWriter: rw,
		statusCode:     http.StatusOK,
	}

	p.next.ServeHTTP(wrappedRW, req)

	duration := time.Since(startTime)
	durationMs := float64(duration.Nanoseconds()) / 1e6

	statusCode := wrappedRW.statusCode
	statusCodeStr := strconv.Itoa(statusCode)

	isError := statusCode >= 400

	apdex := 0.0
	if duration.Seconds() <= p.apdexThreshold {
		apdex = 1.0
	} else if duration.Seconds() <= p.apdexThreshold*4 {
		apdex = 0.5
	}

	tags := []string{
		fmt.Sprintf("peer.hostname:%s", hostname),
		fmt.Sprintf("http.status_code:%s", statusCodeStr),
		fmt.Sprintf("resource_name:%s", hostname),
		fmt.Sprintf("http.method:%s", method),
		fmt.Sprintf("service:%s", p.config.ServiceName),
		fmt.Sprintf("env:%s", p.config.Environment),
		fmt.Sprintf("version:%s", p.config.Version),
	}

	go func() {
		p.sendMetrics(hostname, method, statusCodeStr, durationMs, isError, apdex, tags)
		p.sendTrace(hostname, method, statusCode, startTime, durationMs, req.URL.String())
	}()
}

type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func (p *DatadogPlugin) sendMetrics(hostname, method, statusCode string, durationMs float64, isError bool, apdex float64, tags []string) {
	metrics := []string{
		fmt.Sprintf("trace.traefik.request.hits:1|c|#%s", strings.Join(tags, ",")),
		fmt.Sprintf("trace.traefik.request.hits.by_http_status:1|c|#%s,status:%s", strings.Join(tags, ","), statusCode),
		fmt.Sprintf("trace.traefik.request.duration:%.2f|h|#%s", durationMs, strings.Join(tags, ",")),
		fmt.Sprintf("trace.traefik.request.duration.by_http_status:%.2f|h|#%s,status:%s", durationMs, strings.Join(tags, ","), statusCode),
		fmt.Sprintf("trace.traefik.request.apdex:%.2f|g|#%s", apdex, strings.Join(tags, ",")),
	}

	if isError {
		metrics = append(metrics,
			fmt.Sprintf("trace.traefik.request.errors:1|c|#%s", strings.Join(tags, ",")),
			fmt.Sprintf("trace.traefik.request.errors.by_http_status:1|c|#%s,status:%s", strings.Join(tags, ","), statusCode),
		)
	}

	for _, metric := range metrics {
		_, err := p.statsdConn.Write([]byte(metric + "\n"))
		if err != nil {
			log.Printf("Failed to send metric: %v", err)
		}
	}
}

func (p *DatadogPlugin) sendTrace(hostname, method string, statusCode int, startTime time.Time, durationMs float64, url string) {
	traceID := generateID()
	spanID := generateID()

	startNano := startTime.UnixNano()
	endNano := startNano + int64(durationMs*1e6)

	tracePayload := map[string]interface{}{
		"resourceSpans": []map[string]interface{}{
			{
				"resource": map[string]interface{}{
					"attributes": []map[string]interface{}{
						{"key": "service.name", "value": map[string]interface{}{"stringValue": p.config.ServiceName}},
						{"key": "service.version", "value": map[string]interface{}{"stringValue": p.config.Version}},
						{"key": "deployment.environment", "value": map[string]interface{}{"stringValue": p.config.Environment}},
					},
				},
				"scopeSpans": []map[string]interface{}{
					{
						"spans": []map[string]interface{}{
							{
								"traceId":           traceID,
								"spanId":            spanID,
								"name":              hostname,
								"kind":              1,
								"startTimeUnixNano": startNano,
								"endTimeUnixNano":   endNano,
								"attributes": []map[string]interface{}{
									{"key": "http.method", "value": map[string]interface{}{"stringValue": method}},
									{"key": "http.url", "value": map[string]interface{}{"stringValue": url}},
									{"key": "peer.hostname", "value": map[string]interface{}{"stringValue": hostname}},
									{"key": "resource_name", "value": map[string]interface{}{"stringValue": hostname}},
									{"key": "http.status_code", "value": map[string]interface{}{"intValue": strconv.Itoa(statusCode)}},
									{"key": "http.request.duration", "value": map[string]interface{}{"doubleValue": durationMs}},
									{"key": "service", "value": map[string]interface{}{"stringValue": p.config.ServiceName}},
									{"key": "env", "value": map[string]interface{}{"stringValue": p.config.Environment}},
									{"key": "version", "value": map[string]interface{}{"stringValue": p.config.Version}},
								},
								"status": map[string]interface{}{
									"code": 0,
								},
							},
						},
					},
				},
			},
		},
	}

	jsonData, err := json.Marshal(tracePayload)
	if err != nil {
		log.Printf("Failed to marshal trace: %v", err)
		return
	}

	req, err := http.NewRequest("POST", p.config.OTLPEndpoint, bytes.NewBuffer(jsonData))
	if err != nil {
		log.Printf("Failed to create trace request: %v", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	resp, err := p.otlpClient.Do(req)
	if err != nil {
		log.Printf("Failed to send trace: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("OTLP endpoint returned non-OK status: %d", resp.StatusCode)
	}
}

func generateID() string {
	nanos := time.Now().UnixNano()
	return fmt.Sprintf("%032x", nanos)
}
