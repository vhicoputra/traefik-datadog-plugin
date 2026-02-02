package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace/tracer"
)

type AccessLog struct {
	StartUTC            string `json:"StartUTC"`
	StartLocal          string `json:"StartLocal"`
	Duration            int64  `json:"Duration"`
	ClientHost          string `json:"ClientHost"`
	RequestHost         string `json:"RequestHost"`
	RequestAddr         string `json:"RequestAddr"`
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
		ServiceName:      getEnv("SERVICE_NAME", "traefik-cfs-staging-echo"),
		Environment:      getEnv("ENVIRONMENT", "staging"),
		Version:          getEnv("VERSION", "3.6.7"),
		LogFile:          getEnv("LOG_FILE", "/var/log/traefik/access.log"),
		ApdexThreshold:   0.5,
	}

	agentAddr := strings.Replace(cfg.DogStatsDAddress, ":8127", ":8126", 1)
	tracer.Start(
		tracer.WithAgentAddr(agentAddr),
		tracer.WithService(cfg.ServiceName),
		tracer.WithEnv(cfg.Environment),
		tracer.WithServiceVersion(cfg.Version),
		tracer.WithDebugMode(false),
	)
	defer tracer.Stop()

	addr, err := net.ResolveUDPAddr("udp", cfg.DogStatsDAddress)
	if err != nil {
		log.Fatalf("Failed to resolve DogStatsD address: %v", err)
	}

	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		log.Fatalf("Failed to connect to DogStatsD: %v", err)
	}
	defer conn.Close()

	if cfg.LogFile == "-" {
		log.Printf("Starting Datadog sidecar - reading from stdin")
		scanner := bufio.NewScanner(os.Stdin)
		buf := make([]byte, 0, 256*1024)
		scanner.Buffer(buf, 512*1024)

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

			processLogLine(conn, cfg, &accessLog)
		}

		if err := scanner.Err(); err != nil {
			log.Fatalf("Error reading stdin: %v", err)
		}
	} else {
		log.Printf("Starting Datadog sidecar - reading from %s", cfg.LogFile)
		tailFile(cfg.LogFile, conn, cfg)
	}
}

func tailFile(filename string, conn *net.UDPConn, cfg *Config) {
	var file *os.File
	var err error
	var lastSize int64

	for {
		file, err = os.Open(filename)
		if err != nil {
			log.Printf("Waiting for log file (will retry in 5s): %v", err)
			time.Sleep(5 * time.Second)
			continue
		}

		info, err := file.Stat()
		if err != nil {
			log.Printf("Failed to stat file: %v", err)
			file.Close()
			time.Sleep(5 * time.Second)
			continue
		}
		lastSize = info.Size()

		_, err = file.Seek(0, io.SeekEnd)
		if err != nil {
			log.Printf("Seek failed: %v", err)
			file.Close()
			time.Sleep(5 * time.Second)
			continue
		}

		log.Printf("Tailing log file from position %d, waiting for new lines...", lastSize)
		break
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	var partialLine string

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				if line != "" {
					partialLine += line
				}

				info, statErr := file.Stat()
				if statErr != nil {
					log.Printf("Failed to stat file, reopening: %v", statErr)
					file.Close()
					time.Sleep(1 * time.Second)
					file, err = os.Open(filename)
					if err != nil {
						log.Printf("Failed to reopen file: %v", err)
						time.Sleep(5 * time.Second)
						continue
					}
					reader = bufio.NewReader(file)
					lastSize = 0
					continue
				}

				currentSize := info.Size()
				if currentSize < lastSize {
					log.Printf("Log file was truncated (size %d -> %d), reopening from start", lastSize, currentSize)
					file.Close()
					file, err = os.Open(filename)
					if err != nil {
						log.Printf("Failed to reopen file: %v", err)
						time.Sleep(5 * time.Second)
						continue
					}
					reader = bufio.NewReader(file)
					lastSize = 0
					partialLine = ""
					continue
				}
				lastSize = currentSize

				time.Sleep(100 * time.Millisecond)
				continue
			}

			log.Printf("Error reading file: %v, reopening...", err)
			file.Close()
			time.Sleep(1 * time.Second)
			file, err = os.Open(filename)
			if err != nil {
				log.Printf("Failed to reopen file: %v", err)
				time.Sleep(5 * time.Second)
				continue
			}
			reader = bufio.NewReader(file)
			lastSize = 0
			partialLine = ""
			continue
		}

		fullLine := partialLine + line
		partialLine = ""

		fullLine = strings.TrimSpace(fullLine)
		if fullLine == "" {
			continue
		}

		var accessLog AccessLog
		if err := json.Unmarshal([]byte(fullLine), &accessLog); err != nil {
			log.Printf("Failed to parse access log line: %v (first 80 chars: %q)", err, truncate(fullLine, 80))
			continue
		}

		processLogLine(conn, cfg, &accessLog)
	}
}

func processLogLine(conn *net.UDPConn, cfg *Config, accessLog *AccessLog) {
	hostname := accessLog.RequestHost
	if hostname == "" {
		hostname = accessLog.RequestAddr
	}
	if hostname == "" {
		hostname = "unknown"
	}

	statusCode := accessLog.DownstreamStatus
	if statusCode == 0 {
		statusCode = accessLog.OriginStatus
	}
	statusCodeStr := strconv.Itoa(statusCode)

	durationMs := float64(accessLog.Duration) / 1e6

	isError := statusCode >= 400

	apdex := 0.0
	if durationMs <= cfg.ApdexThreshold*1000 {
		apdex = 1.0
	} else if durationMs <= cfg.ApdexThreshold*4000 {
		apdex = 0.5
	}

	tags := []string{
		fmt.Sprintf("peer.hostname:%s", sanitizeTagValue(hostname)),
		fmt.Sprintf("http.status_code:%s", statusCodeStr),
		fmt.Sprintf("resource_name:%s", sanitizeTagValue(hostname)),
		fmt.Sprintf("http.method:%s", sanitizeTagValue(accessLog.RequestMethod)),
		fmt.Sprintf("service:%s", sanitizeTagValue(cfg.ServiceName)),
		fmt.Sprintf("env:%s", sanitizeTagValue(cfg.Environment)),
		fmt.Sprintf("version:%s", sanitizeTagValue(cfg.Version)),
	}

	onceLogProcessed.Do(func() {
		log.Printf("First access log line processed, sending metrics/traces to Datadog (hostname=%s)", hostname)
	})

	sendMetrics(conn, statusCodeStr, durationMs, isError, apdex, tags)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("sendTraceNative panic recovered: %v", r)
			}
		}()
		sendTraceNative(cfg, accessLog, hostname, statusCode, durationMs)
	}()
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func sanitizeTagValue(s string) string {
	s = strings.ReplaceAll(s, ",", "_")
	s = strings.ReplaceAll(s, "|", "_")
	s = strings.ReplaceAll(s, "\n", "_")
	return s
}

func sendMetrics(conn *net.UDPConn, statusCode string, durationMs float64, isError bool, apdex float64, tags []string) {
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

func sendTraceNative(cfg *Config, accessLog *AccessLog, hostname string, statusCode int, durationMs float64) {
	startTime := time.Now().Add(-time.Duration(durationMs) * time.Millisecond)

	span := tracer.StartSpan(
		"traefik.request",
		tracer.ResourceName(hostname),
		tracer.SpanType("web"),
		tracer.StartTime(startTime),
	)
	defer span.Finish()

	span.SetTag("http.method", accessLog.RequestMethod)
	span.SetTag("http.url", accessLog.RequestPath)
	span.SetTag("http.status_code", statusCode)
	span.SetTag("http.host", hostname)
	span.SetTag("peer.hostname", hostname)

	if statusCode >= 400 {
		span.SetTag("error", true)
		if statusCode >= 500 {
			span.SetTag("error.type", "server_error")
		} else {
			span.SetTag("error.type", "client_error")
		}
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
