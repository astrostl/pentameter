package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math"
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

// IntelliCenter API structures
type IntelliCenterRequest struct {
	MessageID  string     `json:"messageID"`
	Command    string     `json:"command"`
	Condition  string     `json:"condition"`
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
	ObjName string            `json:"objnam"`
	Params  map[string]string `json:"params"`
}

// Prometheus metrics
var (
	poolTemperature = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "water_temperature_fahrenheit",
			Help: "Current water temperature in Fahrenheit",
		},
		[]string{"body", "name"},
	)
	
	airTemperature = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "air_temperature_fahrenheit",
			Help: "Current outdoor air temperature in Fahrenheit",
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
	intelliCenterURL string
	conn             *websocket.Conn
	retryConfig      RetryConfig
	lastHealthCheck  time.Time
	connected        bool
	lastRefresh      time.Time
	bodyHeatingStatus map[string]bool // Track which bodies are actively heating
	pendingRequests  map[string]time.Time // Track messageID -> request time
	debugMode        bool // Enable enhanced debugging
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
		intelliCenterURL: fmt.Sprintf("ws://%s:%s", intelliCenterIP, intelliCenterPort),
		retryConfig: RetryConfig{
			MaxRetries:      5,
			BaseDelay:       1 * time.Second,
			MaxDelay:        30 * time.Second,
			BackoffFactor:   2.0,
			HealthCheckRate: 30 * time.Second,
		},
		connected: false,
		bodyHeatingStatus: make(map[string]bool),
		pendingRequests: make(map[string]time.Time),
		debugMode: debugMode,
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
				return ctx.Err()
			case <-time.After(delay):
			}
		}

		u, err := url.Parse(pm.intelliCenterURL)
		if err != nil {
			return fmt.Errorf("failed to parse URL: %w", err)
		}

		dialer := websocket.DefaultDialer
		dialer.HandshakeTimeout = 10 * time.Second
		
		conn, _, err := dialer.DialContext(ctx, u.String(), nil)
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

func (pm *PoolMonitor) GetTemperatures(ctx context.Context) error {
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
	messageID := fmt.Sprintf("body-temp-%d-%d", time.Now().Unix(), time.Now().Nanosecond()%1000000)
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
			temp, err := strconv.ParseFloat(tempStr, 64)
			if err != nil {
				log.Printf("Failed to parse temperature %s for %s: %v", tempStr, name, err)
				continue
			}

			poolTemperature.WithLabelValues(subtype, name).Set(temp)
			log.Printf("Updated temperature: %s (%s) = %.1f°F (Status: %s)", name, subtype, temp, status)
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
	messageID := fmt.Sprintf("air-temp-%d-%d", time.Now().Unix(), time.Now().Nanosecond()%1000000)
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
			temp, err := strconv.ParseFloat(tempStr, 64)
			if err != nil {
				log.Printf("Failed to parse air temperature %s for %s: %v", tempStr, name, err)
				continue
			}

			airTemperature.WithLabelValues(subtype, name).Set(temp)
			log.Printf("Updated air temperature: %s (%s) = %.1f°F (Status: %s)", name, subtype, temp, status)
		}
	}

	return nil
}

func (pm *PoolMonitor) getPumpData() error {
	// Create GetParamList request for pump data
	messageID := fmt.Sprintf("pump-data-%d-%d", time.Now().Unix(), time.Now().Nanosecond()%1000000)
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

	// Track pending request
	pm.pendingRequests[messageID] = sentTime
	if pm.debugMode {
		log.Printf("DEBUG: Sending pump data request with messageID: %s", messageID)
	}

	// Send request
	if err := pm.conn.WriteJSON(req); err != nil {
		delete(pm.pendingRequests, messageID)
		return fmt.Errorf("failed to send pump data request: %w", err)
	}

	// Read response
	var resp IntelliCenterResponse
	if err := pm.conn.ReadJSON(&resp); err != nil {
		delete(pm.pendingRequests, messageID)
		return fmt.Errorf("failed to read pump data response: %w", err)
	}

	responseTime := time.Since(sentTime)
	if pm.debugMode {
		log.Printf("DEBUG: Received pump data response with messageID: %s (sent: %s) in %v", resp.MessageID, messageID, responseTime)
	}

	// Validate response correlation
	if resp.MessageID != messageID {
		delete(pm.pendingRequests, messageID)
		pm.connected = false
		log.Printf("ERROR: Pump data messageID mismatch! Sent: %s, Received: %s - FORCING RECONNECTION", messageID, resp.MessageID)
		return fmt.Errorf("pump data messageID mismatch: sent %s, received %s - forcing reconnection", messageID, resp.MessageID)
	}

	// Clean up pending request
	pm.validateResponse(messageID)
	
	// Check for suspiciously fast responses (potential cached data)
	if pm.debugMode && responseTime < 5*time.Millisecond {
		log.Printf("WARNING: Suspiciously fast pump data response (%v) - possible cached data", responseTime)
	}
	
	if pm.debugMode {
		log.Printf("DEBUG: Pump data messageID correlation successful: %s", messageID)
	}

	if resp.Response != "200" {
		return fmt.Errorf("pump data API request failed with response: %s", resp.Response)
	}

	// Log raw response for debugging
	if pm.debugMode {
		log.Printf("DEBUG: Raw pump response data: %+v", resp.ObjectList)
	}
	
	// Update Prometheus metrics
	for _, obj := range resp.ObjectList {
		name := obj.Params["SNAME"]
		rpmStr := obj.Params["RPM"]
		status := obj.Params["STATUS"]

		if rpmStr != "" && name != "" {
			rpm, err := strconv.ParseFloat(rpmStr, 64)
			if err != nil {
				log.Printf("Failed to parse RPM %s for pump %s: %v", rpmStr, name, err)
				continue
			}

			pumpRPM.WithLabelValues(obj.ObjName, name).Set(rpm)
			if pm.debugMode {
				log.Printf("Updated pump RPM: %s (%s) = %.0f RPM (Status: %s) [ResponseTime: %v]", name, obj.ObjName, rpm, status, responseTime)
			} else {
				log.Printf("Updated pump RPM: %s (%s) = %.0f RPM (Status: %s)", name, obj.ObjName, rpm, status)
			}
		}
	}

	return nil
}

func (pm *PoolMonitor) getCircuitStatus() error {
	// Create GetParamList request for circuit status
	messageID := fmt.Sprintf("circuit-status-%d-%d", time.Now().Unix(), time.Now().Nanosecond()%1000000)
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

	// Track pending request
	pm.pendingRequests[messageID] = sentTime

	// Send request
	if err := pm.conn.WriteJSON(req); err != nil {
		delete(pm.pendingRequests, messageID)
		return fmt.Errorf("failed to send circuit status request: %w", err)
	}

	// Read response
	var resp IntelliCenterResponse
	if err := pm.conn.ReadJSON(&resp); err != nil {
		delete(pm.pendingRequests, messageID)
		return fmt.Errorf("failed to read circuit status response: %w", err)
	}

	// Validate response correlation
	if resp.MessageID != messageID {
		delete(pm.pendingRequests, messageID)
		pm.connected = false
		return fmt.Errorf("circuit status messageID mismatch: sent %s, received %s - forcing reconnection", messageID, resp.MessageID)
	}

	// Clean up pending request
	pm.validateResponse(messageID)

	if resp.Response != "200" {
		return fmt.Errorf("circuit status API request failed with response: %s", resp.Response)
	}

	// Update Prometheus metrics
	for _, obj := range resp.ObjectList {
		name := obj.Params["SNAME"]
		status := obj.Params["STATUS"]
		subtype := obj.Params["SUBTYP"]

		// Only include C-prefixed (core circuits) and FTR-prefixed (features) circuits
		// Exclude AUX circuits with GENERIC type as they're usually unused
		isValidCircuit := (strings.HasPrefix(obj.ObjName, "C") || strings.HasPrefix(obj.ObjName, "FTR")) &&
			!(strings.HasPrefix(obj.ObjName, "C") && strings.HasPrefix(name, "AUX ") && subtype == "GENERIC")
		
		if name != "" && status != "" && isValidCircuit {
			var statusValue float64
			
			// Check if this is a heater-related circuit
			isHeaterCircuit := strings.Contains(strings.ToLower(name), "heat")
			
			if isHeaterCircuit {
				// For heater circuits, use the body heating status instead of circuit status
				bodyName := ""
				if strings.Contains(strings.ToLower(name), "spa") {
					bodyName = "spa"
				} else if strings.Contains(strings.ToLower(name), "pool") {
					bodyName = "pool"
				}
				
				if bodyName != "" && pm.bodyHeatingStatus[bodyName] {
					statusValue = 1
				} else {
					statusValue = 0
				}
				log.Printf("Updated heater circuit status: %s (%s) = [%.0f] (Body: %s, Heating: %v)", name, obj.ObjName, statusValue, bodyName, pm.bodyHeatingStatus[bodyName])
			} else {
				// For non-heater circuits, use regular ON/OFF status
				if status == "ON" {
					statusValue = 1
				} else {
					statusValue = 0
				}
				log.Printf("Updated circuit status: %s (%s) = %s [%.0f] (Type: %s)", name, obj.ObjName, status, statusValue, subtype)
			}

			circuitStatus.WithLabelValues(obj.ObjName, name, subtype).Set(statusValue)
		}
	}

	return nil
}

func (pm *PoolMonitor) IsHealthy(ctx context.Context) bool {
	if pm.conn == nil || !pm.connected {
		return false
	}

	// Check if it's time for a health check
	if time.Since(pm.lastHealthCheck) < pm.retryConfig.HealthCheckRate {
		return pm.connected
	}

	// Perform health check by trying to write a ping
	if err := pm.conn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(5*time.Second)); err != nil {
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
	pm.Close()
	return pm.ConnectWithRetry(ctx)
}

func (pm *PoolMonitor) Close() error {
	pm.connected = false
	if pm.conn != nil {
		return pm.conn.Close()
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

func createMetricsHandler(registry *prometheus.Registry, monitor *PoolMonitor) http.Handler {
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
}

func main() {
	// Command line flags with environment variable fallbacks
	var intelliCenterIP = flag.String("ic-ip", getEnvOrDefault("PENTAMETER_IC_IP", ""), "IntelliCenter IP address (required, env: PENTAMETER_IC_IP)")
	var intelliCenterPort = flag.String("ic-port", getEnvOrDefault("PENTAMETER_IC_PORT", "6680"), "IntelliCenter WebSocket port (env: PENTAMETER_IC_PORT)")
	var httpPort = flag.String("http-port", getEnvOrDefault("PENTAMETER_HTTP_PORT", "8080"), "HTTP server port for metrics (env: PENTAMETER_HTTP_PORT)")
	var debugMode = flag.Bool("debug", getEnvOrDefault("PENTAMETER_DEBUG", "false") == "true", "Enable enhanced debugging (env: PENTAMETER_DEBUG)")
	var pollIntervalSeconds = flag.Int("interval", func() int {
		if env := os.Getenv("PENTAMETER_INTERVAL"); env != "" {
			if val, err := strconv.Atoi(env); err == nil {
				return val
			}
		}
		return 300
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
	defer monitor.Close()

	// Start temperature polling in background
	go monitor.StartTemperaturePolling(ctx, pollInterval)

	// Setup Prometheus metrics endpoint with custom registry and timestamp
	http.Handle("/metrics", createMetricsHandler(registry, monitor))

	// Setup health check endpoint
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	// Start HTTP server
	serverAddr := ":" + *httpPort
	log.Printf("Starting Prometheus metrics server on %s", serverAddr)
	log.Printf("Metrics available at http://localhost:%s/metrics", *httpPort)
	log.Fatal(http.ListenAndServe(serverAddr, nil))
}