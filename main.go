package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Constants.
const (
	nanosecondMod           = 1000000
	handshakeTimeout        = 10 * time.Second
	pingTimeout             = 5 * time.Second
	maxRetries              = 5
	baseDelaySeconds        = 1
	maxDelaySeconds         = 30
	backoffFactor           = 2.0
	healthCheckInterval     = 30 * time.Second
	defaultPollInterval     = 300
	complexityThreshold     = 15
	httpReadTimeout         = 15 * time.Second
	httpWriteTimeout        = 15 * time.Second
	httpIdleTimeout         = 60 * time.Second
	fahrenheitOffset        = 32
	celsiusConversionFactor = 5.0 / 9.0
)

// IntelliCenter API structures.
type IntelliCenterRequest struct {
	MessageID  string        `json:"messageID"`
	Command    string        `json:"command"`
	Condition  string        `json:"condition"`
	ObjectList []ObjectQuery `json:"objectList"`
}

type ObjectQuery struct {
	ObjName string   `json:"objnam"`
	Keys    []string `json:"keys"`
}

type IntelliCenterResponse struct {
	Command    string       `json:"command"`
	MessageID  string       `json:"messageID"`
	Response   string       `json:"response"`
	ObjectList []ObjectData `json:"objectList"`
}

type ObjectData struct {
	Params  map[string]string `json:"params"`
	ObjName string            `json:"objnam"`
}

// Prometheus metrics.
var (
	poolTemperature = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "water_temperature_celsius",
			Help: "Current water temperature in Celsius",
		},
		[]string{"body", "name"},
	)

	airTemperature = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "air_temperature_celsius",
			Help: "Current outdoor air temperature in Celsius",
		},
		[]string{"sensor", "name"},
	)

	connectionFailure = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "intellicenter_connection_failure",
			Help: "1 if there was a connection failure in the last refresh, 0 if successful",
		},
	)

	lastRefreshTimestamp = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "intellicenter_last_refresh_timestamp_seconds",
			Help: "Unix timestamp of the last successful data refresh",
		},
	)

	pumpRPM = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "pump_rpm",
			Help: "Current pump speed in revolutions per minute",
		},
		[]string{"pump", "name"},
	)

	circuitStatus = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "circuit_status",
			Help: "Circuit on/off status (1=on, 0=off)",
		},
		[]string{"circuit", "name", "type"},
	)
)

type PoolMonitor struct {
	lastHealthCheck   time.Time
	lastRefresh       time.Time
	conn              *websocket.Conn
	bodyHeatingStatus map[string]bool      // Track which bodies are actively heating
	pendingRequests   map[string]time.Time // Track messageID -> request time
	intelliCenterURL  string
	retryConfig       RetryConfig
	connected         bool
	debugMode         bool // Enable enhanced debugging
}

type RetryConfig struct {
	MaxRetries      int
	BaseDelay       time.Duration
	MaxDelay        time.Duration
	BackoffFactor   float64
	HealthCheckRate time.Duration
}

func NewPoolMonitor(intelliCenterIP, intelliCenterPort string, debugMode bool) *PoolMonitor {
	return &PoolMonitor{
		intelliCenterURL: fmt.Sprintf("ws://%s", net.JoinHostPort(intelliCenterIP, intelliCenterPort)),
		retryConfig: RetryConfig{
			MaxRetries:      maxRetries,
			BaseDelay:       baseDelaySeconds * time.Second,
			MaxDelay:        maxDelaySeconds * time.Second,
			BackoffFactor:   backoffFactor,
			HealthCheckRate: healthCheckInterval,
		},
		connected:         false,
		bodyHeatingStatus: make(map[string]bool),
		pendingRequests:   make(map[string]time.Time),
		debugMode:         debugMode,
	}
}

func (pm *PoolMonitor) Connect(ctx context.Context) error {
	return pm.ConnectWithRetry(ctx)
}

func (pm *PoolMonitor) ConnectWithRetry(ctx context.Context) error {
	var lastErr error

	for attempt := 0; attempt <= pm.retryConfig.MaxRetries; attempt++ {
		if attempt > 0 {
			delay := pm.calculateBackoffDelay(attempt)
			log.Printf("Connection attempt %d/%d failed, retrying in %v: %v",
				attempt, pm.retryConfig.MaxRetries, delay, lastErr)

			select {
			case <-ctx.Done():
				return fmt.Errorf("context canceled during retry delay: %w", ctx.Err())
			case <-time.After(delay):
			}
		}

		websocketURL, err := url.Parse(pm.intelliCenterURL)
		if err != nil {
			return fmt.Errorf("failed to parse URL: %w", err)
		}

		dialer := websocket.DefaultDialer
		dialer.HandshakeTimeout = handshakeTimeout

		conn, _, err := dialer.DialContext(ctx, websocketURL.String(), nil)
		if err != nil {
			lastErr = err
			continue
		}

		pm.conn = conn
		pm.connected = true
		pm.lastHealthCheck = time.Now()
		log.Printf("Connected to IntelliCenter at %s (attempt %d/%d)",
			pm.intelliCenterURL, attempt+1, pm.retryConfig.MaxRetries+1)
		return nil
	}

	pm.connected = false
	return fmt.Errorf("failed to connect after %d attempts: %w", pm.retryConfig.MaxRetries+1, lastErr)
}

func (pm *PoolMonitor) calculateBackoffDelay(attempt int) time.Duration {
	delay := float64(pm.retryConfig.BaseDelay) * math.Pow(pm.retryConfig.BackoffFactor, float64(attempt-1))
	if delay > float64(pm.retryConfig.MaxDelay) {
		delay = float64(pm.retryConfig.MaxDelay)
	}
	return time.Duration(delay)
}

func (pm *PoolMonitor) validateResponse(messageID string) {
	// Remove from pending requests
	delete(pm.pendingRequests, messageID)
}

func (pm *PoolMonitor) GetTemperatures(_ context.Context) error {
	if pm.conn == nil {
		return fmt.Errorf("not connected to IntelliCenter")
	}

	// Get body temperatures
	if err := pm.getBodyTemperatures(); err != nil {
		return fmt.Errorf("failed to get body temperatures: %w", err)
	}

	// Get air temperature
	if err := pm.getAirTemperature(); err != nil {
		return fmt.Errorf("failed to get air temperature: %w", err)
	}

	// Get pump data
	if err := pm.getPumpData(); err != nil {
		return fmt.Errorf("failed to get pump data: %w", err)
	}

	// Get circuit status
	if err := pm.getCircuitStatus(); err != nil {
		return fmt.Errorf("failed to get circuit status: %w", err)
	}

	return nil
}

func (pm *PoolMonitor) getBodyTemperatures() error {
	// Create GetParamList request for body temperatures and heater status
	messageID := fmt.Sprintf("body-temp-%d-%d", time.Now().Unix(), time.Now().Nanosecond()%nanosecondMod)
	sentTime := time.Now()

	req := IntelliCenterRequest{
		MessageID: messageID,
		Command:   "GetParamList",
		Condition: "OBJTYP=BODY",
		ObjectList: []ObjectQuery{
			{
				ObjName: "INCR",
				Keys:    []string{"SNAME", "STATUS", "TEMP", "SUBTYP", "HTMODE", "HTSRC"},
			},
		},
	}

	// Track pending request
	pm.pendingRequests[messageID] = sentTime
	if pm.debugMode {
		log.Printf("DEBUG: Sending body temp request with messageID: %s", messageID)
	}

	// Send request
	if err := pm.conn.WriteJSON(req); err != nil {
		delete(pm.pendingRequests, messageID)
		return fmt.Errorf("failed to send request: %w", err)
	}

	// Read response
	var resp IntelliCenterResponse
	if err := pm.conn.ReadJSON(&resp); err != nil {
		delete(pm.pendingRequests, messageID)
		return fmt.Errorf("failed to read response: %w", err)
	}

	if pm.debugMode {
		log.Printf("DEBUG: Received body temp response with messageID: %s (sent: %s)", resp.MessageID, messageID)
	}

	// Validate response correlation
	if resp.MessageID != messageID {
		delete(pm.pendingRequests, messageID)
		pm.connected = false // Mark as disconnected to trigger reconnection
		log.Printf("ERROR: Body temp messageID mismatch! Sent: %s, Received: %s - FORCING RECONNECTION", messageID, resp.MessageID)
		return fmt.Errorf("messageID mismatch: sent %s, received %s - forcing reconnection", messageID, resp.MessageID)
	}

	// Clean up pending request
	pm.validateResponse(messageID)
	if pm.debugMode {
		log.Printf("DEBUG: Body temp messageID correlation successful: %s", messageID)
	}

	if resp.Response != "200" {
		return fmt.Errorf("API request failed with response: %s", resp.Response)
	}

	// Update Prometheus metrics
	for _, obj := range resp.ObjectList {
		name := obj.Params["SNAME"]
		tempStr := obj.Params["TEMP"]
		subtype := obj.Params["SUBTYP"]
		status := obj.Params["STATUS"]
		htmodeStr := obj.Params["HTMODE"]

		if tempStr != "" && name != "" {
			tempFahrenheit, err := strconv.ParseFloat(tempStr, 64)
			if err != nil {
				log.Printf("Failed to parse temperature %s for %s: %v", tempStr, name, err)
				continue
			}

			// Convert from Fahrenheit to Celsius
			tempCelsius := (tempFahrenheit - fahrenheitOffset) * celsiusConversionFactor
			poolTemperature.WithLabelValues(subtype, name).Set(tempCelsius)
			log.Printf("Updated temperature: %s (%s) = %.1f°F (Status: %s)", name, subtype, tempFahrenheit, status)
		}

		// Track body heating status for later circuit lookup
		if htmodeStr != "" && name != "" {
			htmode, err := strconv.Atoi(htmodeStr)
			if err != nil {
				log.Printf("Failed to parse HTMODE %s for %s: %v", htmodeStr, name, err)
			} else {
				// HTMODE >= 1 means heater is on (1=actively heating, 2=on but not heating)
				pm.bodyHeatingStatus[strings.ToLower(name)] = htmode >= 1
				log.Printf("Updated body heating status: %s (%s) HTMODE=%d [%v]", name, obj.ObjName, htmode, htmode >= 1)
			}
		}
	}

	return nil
}

func (pm *PoolMonitor) getAirTemperature() error {
	// Create GetParamList request for air sensor
	messageID := fmt.Sprintf("air-temp-%d-%d", time.Now().Unix(), time.Now().Nanosecond()%nanosecondMod)
	sentTime := time.Now()

	req := IntelliCenterRequest{
		MessageID: messageID,
		Command:   "GetParamList",
		Condition: "",
		ObjectList: []ObjectQuery{
			{
				ObjName: "_A135",
				Keys:    []string{"SNAME", "STATUS", "PROBE", "SUBTYP"},
			},
		},
	}

	// Track pending request
	pm.pendingRequests[messageID] = sentTime

	// Send request
	if err := pm.conn.WriteJSON(req); err != nil {
		delete(pm.pendingRequests, messageID)
		return fmt.Errorf("failed to send air temp request: %w", err)
	}

	// Read response
	var resp IntelliCenterResponse
	if err := pm.conn.ReadJSON(&resp); err != nil {
		delete(pm.pendingRequests, messageID)
		return fmt.Errorf("failed to read air temp response: %w", err)
	}

	// Validate response correlation
	if resp.MessageID != messageID {
		delete(pm.pendingRequests, messageID)
		pm.connected = false
		return fmt.Errorf("air temp messageID mismatch: sent %s, received %s - forcing reconnection", messageID, resp.MessageID)
	}

	// Clean up pending request
	pm.validateResponse(messageID)

	if resp.Response != "200" {
		return fmt.Errorf("air temp API request failed with response: %s", resp.Response)
	}

	// Update Prometheus metrics
	for _, obj := range resp.ObjectList {
		name := obj.Params["SNAME"]
		tempStr := obj.Params["PROBE"]
		subtype := obj.Params["SUBTYP"]
		status := obj.Params["STATUS"]

		if tempStr != "" && name != "" {
			tempFahrenheit, err := strconv.ParseFloat(tempStr, 64)
			if err != nil {
				log.Printf("Failed to parse air temperature %s for %s: %v", tempStr, name, err)
				continue
			}

			// Convert from Fahrenheit to Celsius
			tempCelsius := (tempFahrenheit - fahrenheitOffset) * celsiusConversionFactor
			airTemperature.WithLabelValues(subtype, name).Set(tempCelsius)
			log.Printf("Updated air temperature: %s (%s) = %.1f°F (Status: %s)", name, subtype, tempFahrenheit, status)
		}
	}

	return nil
}

func (pm *PoolMonitor) getPumpData() error {
	resp, responseTime, err := pm.requestPumpData()
	if err != nil {
		return err
	}

	// Update Prometheus metrics
	for _, obj := range resp.ObjectList {
		if err := pm.processPumpObject(obj, responseTime); err != nil {
			log.Printf("Failed to process pump object %s: %v", obj.ObjName, err)
		}
	}

	return nil
}

func (pm *PoolMonitor) getCircuitStatus() error {
	resp, err := pm.requestCircuitData()
	if err != nil {
		return err
	}

	// Update Prometheus metrics
	for _, obj := range resp.ObjectList {
		pm.processCircuitObject(obj)
	}

	return nil
}

func (pm *PoolMonitor) requestCircuitData() (*IntelliCenterResponse, error) {
	messageID := fmt.Sprintf("circuit-status-%d-%d", time.Now().Unix(), time.Now().Nanosecond()%nanosecondMod)
	sentTime := time.Now()

	req := IntelliCenterRequest{
		MessageID: messageID,
		Command:   "GetParamList",
		Condition: "OBJTYP=CIRCUIT",
		ObjectList: []ObjectQuery{
			{
				ObjName: "INCR",
				Keys:    []string{"SNAME", "STATUS", "OBJTYP", "SUBTYP"},
			},
		},
	}

	pm.pendingRequests[messageID] = sentTime

	if err := pm.conn.WriteJSON(req); err != nil {
		delete(pm.pendingRequests, messageID)
		return nil, fmt.Errorf("failed to send circuit status request: %w", err)
	}

	var resp IntelliCenterResponse
	if err := pm.conn.ReadJSON(&resp); err != nil {
		delete(pm.pendingRequests, messageID)
		return nil, fmt.Errorf("failed to read circuit status response: %w", err)
	}

	if resp.MessageID != messageID {
		delete(pm.pendingRequests, messageID)
		pm.connected = false
		return nil, fmt.Errorf("circuit status messageID mismatch: sent %s, received %s - forcing reconnection", messageID, resp.MessageID)
	}

	pm.validateResponse(messageID)

	if resp.Response != "200" {
		return nil, fmt.Errorf("circuit status API request failed with response: %s", resp.Response)
	}

	return &resp, nil
}

func (pm *PoolMonitor) processCircuitObject(obj ObjectData) {
	name := obj.Params["SNAME"]
	status := obj.Params["STATUS"]
	subtype := obj.Params["SUBTYP"]

	if !pm.isValidCircuit(obj.ObjName, name, subtype) {
		return
	}

	if name == "" || status == "" {
		return
	}

	statusValue := pm.calculateCircuitStatusValue(name, status, obj.ObjName)
	circuitStatus.WithLabelValues(obj.ObjName, name, subtype).Set(statusValue)
}

func (pm *PoolMonitor) isValidCircuit(objName, name, subtype string) bool {
	hasValidPrefix := strings.HasPrefix(objName, "C") || strings.HasPrefix(objName, "FTR")
	isGenericAux := strings.HasPrefix(objName, "C") && strings.HasPrefix(name, "AUX ") && subtype == "GENERIC"
	return hasValidPrefix && !isGenericAux
}

func (pm *PoolMonitor) calculateCircuitStatusValue(name, status, objName string) float64 {
	isHeaterCircuit := strings.Contains(strings.ToLower(name), "heat")

	if isHeaterCircuit {
		return pm.getHeaterCircuitStatus(name, objName)
	}

	return pm.getRegularCircuitStatus(name, status, objName)
}

func (pm *PoolMonitor) getHeaterCircuitStatus(name, objName string) float64 {
	bodyName := pm.getBodyNameFromCircuit(name)
	statusValue := 0.0

	if bodyName != "" && pm.bodyHeatingStatus[bodyName] {
		statusValue = 1.0
	}

	log.Printf("Updated heater circuit status: %s (%s) = [%.0f] (Body: %s, Heating: %v)",
		name, objName, statusValue, bodyName, pm.bodyHeatingStatus[bodyName])

	return statusValue
}

func (pm *PoolMonitor) getBodyNameFromCircuit(name string) string {
	lowerName := strings.ToLower(name)
	if strings.Contains(lowerName, "spa") {
		return "spa"
	}
	if strings.Contains(lowerName, "pool") {
		return "pool"
	}
	return ""
}

func (pm *PoolMonitor) getRegularCircuitStatus(name, status, objName string) float64 {
	statusValue := 0.0
	if status == "ON" {
		statusValue = 1.0
	}

	log.Printf("Updated circuit status: %s (%s) = %s [%.0f]", name, objName, status, statusValue)
	return statusValue
}

func (pm *PoolMonitor) requestPumpData() (*IntelliCenterResponse, time.Duration, error) {
	messageID := fmt.Sprintf("pump-data-%d-%d", time.Now().Unix(), time.Now().Nanosecond()%nanosecondMod)
	sentTime := time.Now()

	req := IntelliCenterRequest{
		MessageID: messageID,
		Command:   "GetParamList",
		Condition: "OBJTYP=PUMP",
		ObjectList: []ObjectQuery{
			{
				ObjName: "INCR",
				Keys:    []string{"SNAME", "STATUS", "RPM", "WATTS", "GPM", "SPEED"},
			},
		},
	}

	pm.pendingRequests[messageID] = sentTime
	if pm.debugMode {
		log.Printf("DEBUG: Sending pump data request with messageID: %s", messageID)
	}

	if err := pm.conn.WriteJSON(req); err != nil {
		delete(pm.pendingRequests, messageID)
		return nil, 0, fmt.Errorf("failed to send pump data request: %w", err)
	}

	var resp IntelliCenterResponse
	if err := pm.conn.ReadJSON(&resp); err != nil {
		delete(pm.pendingRequests, messageID)
		return nil, 0, fmt.Errorf("failed to read pump data response: %w", err)
	}

	responseTime := time.Since(sentTime)
	if pm.debugMode {
		log.Printf("DEBUG: Received pump data response with messageID: %s (sent: %s) in %v", resp.MessageID, messageID, responseTime)
	}

	if resp.MessageID != messageID {
		delete(pm.pendingRequests, messageID)
		pm.connected = false
		log.Printf("ERROR: Pump data messageID mismatch! Sent: %s, Received: %s - FORCING RECONNECTION", messageID, resp.MessageID)
		return nil, 0, fmt.Errorf("pump data messageID mismatch: sent %s, received %s - forcing reconnection", messageID, resp.MessageID)
	}

	pm.validateResponse(messageID)

	if pm.debugMode && responseTime < 5*time.Millisecond {
		log.Printf("WARNING: Suspiciously fast pump data response (%v) - possible cached data", responseTime)
	}

	if pm.debugMode {
		log.Printf("DEBUG: Pump data messageID correlation successful: %s", messageID)
	}

	if resp.Response != "200" {
		return nil, 0, fmt.Errorf("pump data API request failed with response: %s", resp.Response)
	}

	if pm.debugMode {
		log.Printf("DEBUG: Raw pump response data: %+v", resp.ObjectList)
	}

	return &resp, responseTime, nil
}

func (pm *PoolMonitor) processPumpObject(obj ObjectData, responseTime time.Duration) error {
	name := obj.Params["SNAME"]
	rpmStr := obj.Params["RPM"]
	status := obj.Params["STATUS"]

	if rpmStr == "" || name == "" {
		return nil
	}

	rpm, err := strconv.ParseFloat(rpmStr, 64)
	if err != nil {
		log.Printf("Failed to parse RPM %s for pump %s: %v", rpmStr, name, err)
		return fmt.Errorf("failed to parse RPM %s for pump %s: %w", rpmStr, name, err)
	}

	pumpRPM.WithLabelValues(obj.ObjName, name).Set(rpm)
	pm.logPumpUpdate(name, obj.ObjName, rpm, status, responseTime)
	return nil
}

func (pm *PoolMonitor) logPumpUpdate(name, objName string, rpm float64, status string, responseTime time.Duration) {
	if pm.debugMode {
		log.Printf("Updated pump RPM: %s (%s) = %.0f RPM (Status: %s) [ResponseTime: %v]", name, objName, rpm, status, responseTime)
	} else {
		log.Printf("Updated pump RPM: %s (%s) = %.0f RPM (Status: %s)", name, objName, rpm, status)
	}
}

func (pm *PoolMonitor) IsHealthy(_ context.Context) bool {
	if pm.conn == nil || !pm.connected {
		return false
	}

	// Check if it's time for a health check
	if time.Since(pm.lastHealthCheck) < pm.retryConfig.HealthCheckRate {
		return pm.connected
	}

	// Perform health check by trying to write a ping
	if err := pm.conn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(pingTimeout)); err != nil {
		log.Printf("Health check failed: %v", err)
		pm.connected = false
		return false
	}

	pm.lastHealthCheck = time.Now()
	return true
}

func (pm *PoolMonitor) EnsureConnected(ctx context.Context) error {
	if pm.IsHealthy(ctx) {
		return nil
	}

	log.Println("Connection unhealthy, attempting to reconnect...")
	if err := pm.Close(); err != nil {
		log.Printf("Warning: failed to close connection: %v", err)
	}
	return pm.ConnectWithRetry(ctx)
}

func (pm *PoolMonitor) Close() error {
	pm.connected = false
	if pm.conn != nil {
		if err := pm.conn.Close(); err != nil {
			return fmt.Errorf("failed to close WebSocket connection: %w", err)
		}
	}
	return nil
}

func (pm *PoolMonitor) StartTemperaturePolling(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Get initial reading with connection check
	if err := pm.EnsureConnected(ctx); err != nil {
		log.Printf("Failed to establish initial connection: %v", err)
	} else if err := pm.GetTemperatures(ctx); err != nil {
		log.Printf("Failed to get initial temperatures: %v", err)
	} else {
		pm.lastRefresh = time.Now()
		lastRefreshTimestamp.Set(float64(pm.lastRefresh.Unix()))
	}

	for {
		select {
		case <-ctx.Done():
			log.Println("Temperature polling stopped")
			return
		case <-ticker.C:
			// Ensure connection is healthy before attempting to get temperatures
			if err := pm.EnsureConnected(ctx); err != nil {
				log.Printf("Failed to ensure connection: %v", err)
				connectionFailure.Set(1)
				continue
			}

			if err := pm.GetTemperatures(ctx); err != nil {
				log.Printf("Failed to get temperatures: %v", err)
				// Mark connection as unhealthy for next iteration
				pm.connected = false
				connectionFailure.Set(1)
			} else {
				pm.lastRefresh = time.Now()
				connectionFailure.Set(0)
				lastRefreshTimestamp.Set(float64(pm.lastRefresh.Unix()))
			}
		}
	}
}

func getEnvOrDefault(envVar, defaultValue string) string {
	if value := os.Getenv(envVar); value != "" {
		return value
	}
	return defaultValue
}

func createMetricsHandler(registry *prometheus.Registry, _ *PoolMonitor) http.Handler {
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
}

func main() {
	// Command line flags with environment variable fallbacks
	intelliCenterIP := flag.String("ic-ip", getEnvOrDefault("PENTAMETER_IC_IP", ""), "IntelliCenter IP address (required, env: PENTAMETER_IC_IP)")
	intelliCenterPort := flag.String("ic-port", getEnvOrDefault("PENTAMETER_IC_PORT", "6680"), "IntelliCenter WebSocket port (env: PENTAMETER_IC_PORT)")
	httpPort := flag.String("http-port", getEnvOrDefault("PENTAMETER_HTTP_PORT", "8080"), "HTTP server port for metrics (env: PENTAMETER_HTTP_PORT)")
	debugMode := flag.Bool("debug", getEnvOrDefault("PENTAMETER_DEBUG", "false") == "true", "Enable enhanced debugging (env: PENTAMETER_DEBUG)")
	pollIntervalSeconds := flag.Int("interval", func() int {
		if env := os.Getenv("PENTAMETER_INTERVAL"); env != "" {
			if val, err := strconv.Atoi(env); err == nil {
				return val
			}
		}
		return defaultPollInterval
	}(), "Temperature polling interval in seconds (env: PENTAMETER_INTERVAL)")
	flag.Parse()

	// Convert interval seconds to duration
	pollInterval := time.Duration(*pollIntervalSeconds) * time.Second

	// Validate required parameters
	if *intelliCenterIP == "" {
		log.Fatal("IntelliCenter IP address is required. Use --ic-ip flag or PENTAMETER_IC_IP environment variable")
	}

	log.Printf("Starting pool monitor for IntelliCenter at %s:%s", *intelliCenterIP, *intelliCenterPort)
	log.Printf("HTTP server will run on port %s", *httpPort)
	log.Printf("Polling interval: %v", pollInterval)
	if *debugMode {
		log.Printf("Enhanced debugging enabled")
	}

	// Create custom registry to avoid default Go metrics
	registry := prometheus.NewRegistry()
	registry.MustRegister(poolTemperature)
	registry.MustRegister(airTemperature)
	registry.MustRegister(connectionFailure)
	registry.MustRegister(lastRefreshTimestamp)
	registry.MustRegister(pumpRPM)
	registry.MustRegister(circuitStatus)

	// Create pool monitor
	monitor := NewPoolMonitor(*intelliCenterIP, *intelliCenterPort, *debugMode)

	// Create context for graceful shutdown
	ctx := context.Background()

	// Connect to IntelliCenter
	if err := monitor.Connect(ctx); err != nil {
		log.Fatalf("Failed to connect to IntelliCenter: %v", err)
	}
	defer func() {
		if err := monitor.Close(); err != nil {
			log.Printf("Error closing monitor: %v", err)
		}
	}()

	// Start temperature polling in background
	go monitor.StartTemperaturePolling(ctx, pollInterval)

	// Setup Prometheus metrics endpoint with custom registry and timestamp
	http.Handle("/metrics", createMetricsHandler(registry, monitor))

	// Setup health check endpoint
	http.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("OK")); err != nil {
			log.Printf("Failed to write health check response: %v", err)
		}
	})

	// Start HTTP server
	serverAddr := ":" + *httpPort
	log.Printf("Starting Prometheus metrics server on %s", serverAddr)
	log.Printf("Metrics available at http://localhost:%s/metrics", *httpPort)

	// Server startup in separate function to avoid exitAfterDefer issue
	startServer(serverAddr)
}

func startServer(serverAddr string) {
	server := &http.Server{
		Addr:         serverAddr,
		Handler:      nil,
		ReadTimeout:  httpReadTimeout,
		WriteTimeout: httpWriteTimeout,
		IdleTimeout:  httpIdleTimeout,
	}

	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("HTTP server failed: %v", err)
	}
}
