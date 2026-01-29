package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type AccessLog struct {
	StartUTC            string `json:"StartUTC"`
	StartLocal          string `json:"StartLocal"`
	Duration            int64  `json:"Duration"`
	ClientHost          string `json:"ClientHost"`
	RequestHost         string `json:"RequestHost"`
	RequestAddr         string `json:"RequestAddr"` // Traefik sometimes uses this for host
	RequestMethod       string `json:"RequestMethod"`
	RequestPath         string `json:"RequestPath"`
	RequestProtocol     string `json:"RequestProtocol"`
	RequestScheme       string `json:"RequestScheme"`
	DownstreamStatus    int    `json:"DownstreamStatus"`
	OriginStatus        int    `json:"OriginStatus"`
	RouterName          string `json:"RouterName"`
	ServiceName         string `json:"ServiceName"`
}

type Config struct {
	DogStatsDAddress string
	OTLPEndpoint     string
	ServiceName      string
	Environment      string
	Version          string
	LogFile          string
	ApdexThreshold   float64
}

var onceLogProcessed sync.Once

func main() {
	cfg := &Config{
		DogStatsDAddress: getEnv("DOGSTATSD_ADDRESS", "datadog-apm.datadog.svc:8127"),
		OTLPEndpoint:     fmt.Sprintf("http://%s/v1/traces", strings.Replace(getEnv("DOGSTATSD_ADDRESS", "datadog-apm.datadog.svc:8127"), ":8127", ":4318", 1)),
		ServiceName:      getEnv("SERVICE_NAME", "traefik-cfs-staging-echo"),
		Environment:      getEnv("ENVIRONMENT", "staging"),
		Version:          getEnv("VERSION", "3.6.7"),
		LogFile:          getEnv("LOG_FILE", "/var/log/traefik/access.log"),
		ApdexThreshold:   0.5,
	}

	// Connect to DogStatsD
	addr, err := net.ResolveUDPAddr("udp", cfg.DogStatsDAddress)
	if err != nil {
		log.Fatalf("Failed to resolve DogStatsD address: %v", err)
	}

	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		log.Fatalf("Failed to connect to DogStatsD: %v", err)
	}
	defer conn.Close()

	// HTTP client for OTLP
	otlpClient := &http.Client{
		Timeout: 10 * time.Second,
	}

	// Support reading from stdin (when LOG_FILE is "-") or from file
	if cfg.LogFile == "-" {
		log.Printf("Starting Datadog sidecar - reading from stdin")
		scanner := bufio.NewScanner(os.Stdin)
		// Increase buffer size for long log lines (default 64KB may be too small under load)
		buf := make([]byte, 0, 256*1024)
		scanner.Buffer(buf, 512*1024)

		// Read from stdin line by line
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}

			var accessLog AccessLog
			if err := json.Unmarshal([]byte(line), &accessLog); err != nil {
				log.Printf("Failed to parse access log line: %v (first 80 chars: %q)", err, truncate(line, 80))
				continue
			}

			processLogLine(conn, otlpClient, cfg, &accessLog)
		}

		if err := scanner.Err(); err != nil {
			log.Fatalf("Error reading stdin: %v", err)
		}
	} else {
		log.Printf("Starting Datadog sidecar - reading from %s", cfg.LogFile)
		// Tail the log file continuously: keep file open and read new lines as they're appended.
		for {
			file, err := os.Open(cfg.LogFile)
			if err != nil {
				log.Printf("Waiting for log file (will retry): %v", err)
				time.Sleep(5 * time.Second)
				continue
			}

			// Seek to end to skip existing content and only process new lines
			_, err = file.Seek(0, 2)
			if err != nil {
				log.Printf("Seek failed: %v", err)
				file.Close()
				time.Sleep(5 * time.Second)
				continue
			}

			scanner := bufio.NewScanner(file)
			// Increase buffer size for long log lines (default 64KB may be too small under load)
			buf := make([]byte, 0, 256*1024)
			scanner.Buffer(buf, 512*1024)

			for {
				if scanner.Scan() {
					line := strings.TrimSpace(scanner.Text())
					if line == "" {
						continue
					}

					var accessLog AccessLog
					if err := json.Unmarshal([]byte(line), &accessLog); err != nil {
						log.Printf("Failed to parse access log line: %v (first 80 chars: %q)", err, truncate(line, 80))
						continue
					}

					processLogLine(conn, otlpClient, cfg, &accessLog)
					continue
				}

				// EOF or error: don't close file so we can read newly appended data
				if err := scanner.Err(); err != nil {
					log.Printf("Error reading log: %v", err)
					file.Close()
					break
				}

				// At EOF - sleep and retry; file stays open so we see new lines
				time.Sleep(100 * time.Millisecond)
			}
		}
	}
}

func processLogLine(conn *net.UDPConn, otlpClient *http.Client, cfg *Config, accessLog *AccessLog) {
	// Extract hostname (matches Nginx behavior); Traefik may use RequestHost or RequestAddr
	hostname := accessLog.RequestHost
	if hostname == "" {
		hostname = accessLog.RequestAddr
	}
	if hostname == "" {
		hostname = "unknown"
	}

	// Get status code
	statusCode := accessLog.DownstreamStatus
	if statusCode == 0 {
		statusCode = accessLog.OriginStatus
	}
	statusCodeStr := strconv.Itoa(statusCode)

	// Calculate duration in milliseconds
	durationMs := float64(accessLog.Duration) / 1e6

	// Determine if error
	isError := statusCode >= 400

	// Calculate Apdex (matching Nginx threshold of 0.5s)
	apdex := 0.0
	if durationMs <= cfg.ApdexThreshold*1000 {
		apdex = 1.0
	} else if durationMs <= cfg.ApdexThreshold*4000 {
		apdex = 0.5
	}

	// Prepare tags (matching Nginx format exactly). Sanitize values so commas/pipes don't break DogStatsD.
	tags := []string{
		fmt.Sprintf("peer.hostname:%s", sanitizeTagValue(hostname)),
		fmt.Sprintf("http.status_code:%s", statusCodeStr),
		fmt.Sprintf("resource_name:%s", sanitizeTagValue(hostname)),
		fmt.Sprintf("http.method:%s", sanitizeTagValue(accessLog.RequestMethod)),
		fmt.Sprintf("service:%s", sanitizeTagValue(cfg.ServiceName)),
		fmt.Sprintf("env:%s", sanitizeTagValue(cfg.Environment)),
		fmt.Sprintf("version:%s", sanitizeTagValue(cfg.Version)),
	}

	// Log once when first line is processed (confirms sidecar is reading and parsing)
	onceLogProcessed.Do(func() {
		log.Printf("First access log line processed, sending metrics/traces to Datadog (hostname=%s)", hostname)
	})

	// Send metrics (matching Nginx metric names exactly)
	sendMetrics(conn, statusCodeStr, durationMs, isError, apdex, tags)

	// Send trace with correct resource_name (recover panic so sidecar keeps running)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("sendTrace panic recovered: %v", r)
			}
		}()
		sendTrace(otlpClient, cfg.OTLPEndpoint, hostname, accessLog.RequestMethod, statusCode, durationMs, accessLog.RequestPath, cfg)
	}()
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// sanitizeTagValue replaces characters that break DogStatsD tag format (comma, pipe, colon in value).
func sanitizeTagValue(s string) string {
	s = strings.ReplaceAll(s, ",", "_")
	s = strings.ReplaceAll(s, "|", "_")
	s = strings.ReplaceAll(s, "\n", "_")
	return s
}

func sendMetrics(conn *net.UDPConn, statusCode string, durationMs float64, isError bool, apdex float64, tags []string) {
	// Match Nginx metric names exactly (percentile distribution metric reverted)
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
		payload := []byte(metric + "\n")
		if _, err := conn.Write(payload); err != nil {
			log.Printf("Failed to send metric to DogStatsD: %v (metric=%s)", err, truncate(metric, 80))
		}
	}
}

func sendTrace(client *http.Client, endpoint, hostname, method string, statusCode int, durationMs float64, url string, cfg *Config) {
	traceID := fmt.Sprintf("%032x", time.Now().UnixNano())
	spanID := fmt.Sprintf("%016x", time.Now().UnixNano())

	startTime := time.Now()
	startNano := startTime.UnixNano()
	endNano := startNano + int64(durationMs*1e6)

	// Use http.route with hostname to help Datadog APM show hostname instead of just "GET"
	// Datadog derives resource name from http.method + http.route, so setting http.route to hostname
	// should make the resource appear as "GET api-dummy-cfs-traefik.mekari.io" or similar
	httpRoute := hostname

	tracePayload := map[string]interface{}{
		"resourceSpans": []map[string]interface{}{
			{
				"resource": map[string]interface{}{
					"attributes": []map[string]interface{}{
						{"key": "service.name", "value": map[string]interface{}{"stringValue": cfg.ServiceName}},
						{"key": "service.version", "value": map[string]interface{}{"stringValue": cfg.Version}},
						{"key": "deployment.environment", "value": map[string]interface{}{"stringValue": cfg.Environment}},
					},
				},
				"scopeSpans": []map[string]interface{}{
					{
						"spans": []map[string]interface{}{
							{
								"traceId":           traceID,
								"spanId":            spanID,
								"name":              fmt.Sprintf("%s %s", method, httpRoute), // Use "METHOD hostname" format
								"kind":              1,
								"startTimeUnixNano": startNano,
								"endTimeUnixNano":   endNano,
								"attributes": []map[string]interface{}{
									{"key": "http.method", "value": map[string]interface{}{"stringValue": method}},
									{"key": "http.route", "value": map[string]interface{}{"stringValue": httpRoute}}, // Set http.route to hostname
									{"key": "http.url", "value": map[string]interface{}{"stringValue": url}},
									{"key": "peer.hostname", "value": map[string]interface{}{"stringValue": hostname}},
									{"key": "resource_name", "value": map[string]interface{}{"stringValue": hostname}},
									{"key": "http.status_code", "value": map[string]interface{}{"intValue": strconv.Itoa(statusCode)}},
									{"key": "http.request.duration", "value": map[string]interface{}{"doubleValue": durationMs}},
									{"key": "service", "value": map[string]interface{}{"stringValue": cfg.ServiceName}},
									{"key": "env", "value": map[string]interface{}{"stringValue": cfg.Environment}},
									{"key": "version", "value": map[string]interface{}{"stringValue": cfg.Version}},
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
		return
	}

	req, err := http.NewRequest("POST", endpoint, bytes.NewBuffer(jsonData))
	if err != nil {
		return
	}

	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
