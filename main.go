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

// Version information set at build time.
var version = "dev"

// Constants.
const (
	nanosecondMod       = 1000000
	handshakeTimeout    = 10 * time.Second
	pingTimeout         = 5 * time.Second
	maxRetries          = 5
	baseDelaySeconds    = 1
	maxDelaySeconds     = 30
	backoffFactor       = 2.0
	healthCheckInterval = 30 * time.Second
	defaultPollInterval = 60
	complexityThreshold = 15
	httpReadTimeout     = 15 * time.Second
	httpWriteTimeout    = 15 * time.Second
	httpIdleTimeout     = 60 * time.Second

	// Listen mode polling interval (rapid polling for change detection).
	listenModePollInterval = 2

	// Circuit status constants.
	statusOn = "ON"

	// Boolean string constants.
	trueString = "true"

	// Thermal status constants.
	thermalStatusOff      = 0
	thermalStatusHeating  = 1
	thermalStatusIdle     = 2
	thermalStatusCooling  = 3
	htModeOff             = 0
	htModeHeating         = 1
	htModeHeatPumpHeating = 4
	htModeHeatPumpCooling = 9
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
		[]string{"circuit", "name", "subtyp"},
	)

	thermalStatus = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "thermal_status",
			Help: "Thermal equipment operational status derived from IntelliCenter HTMODE+HTSRC " +
				"(0=off, 1=heating, 2=idle, 3=cooling). Note: 'idle' is pentameter's interpretation " +
				"of HTMODE=0+assigned heater, not an IntelliCenter native status.",
		},
		[]string{"heater", "name", "subtyp"},
	)

	thermalLowSetpoint = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "thermal_low_setpoint_fahrenheit",
			Help: "Heating target temperature in Fahrenheit (turn on heating when temp drops below this)",
		},
		[]string{"heater", "name", "subtyp"},
	)

	thermalHighSetpoint = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "thermal_high_setpoint_fahrenheit",
			Help: "Cooling target temperature in Fahrenheit (turn on cooling when temp rises above this)",
		},
		[]string{"heater", "name", "subtyp"},
	)

	featureStatus = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "feature_status",
			Help: "Feature on/off status (1=on, 0=off)",
		},
		[]string{"feature", "name", "subtyp"},
	)
)

type PoolMonitor struct {
	lastHealthCheck   time.Time
	lastRefresh       time.Time
	conn              *websocket.Conn
	bodyHeatingStatus map[string]bool           // Track which bodies are actively heating
	referencedHeaters map[string]BodyHeaterInfo // Track body-to-heater assignments
	pendingRequests   map[string]time.Time      // Track messageID -> request time
	featureConfig     map[string]string         // Track feature objnam -> SHOMNU for visibility
	previousState     *EquipmentState           // Previous state for change detection
	intelliCenterURL  string
	retryConfig       RetryConfig
	connected         bool
	debugMode         bool // Enable enhanced debugging
	listenMode        bool // Enable live event logging mode
}

// EquipmentState tracks the current state of all equipment for change detection.
type EquipmentState struct {
	WaterTemps      map[string]float64 // body -> temperature
	PumpRPMs        map[string]float64 // pump -> RPM
	Circuits        map[string]string  // circuit -> ON/OFF
	Thermals        map[string]int     // heater -> status (0=off, 1=heating, 2=idle, 3=cooling)
	Features        map[string]string  // feature -> ON/OFF
	UnknownEquip    map[string]string  // objnam -> "OBJTYP:STATUS" for equipment not otherwise tracked
	ParseErrors     map[string]bool    // Track parse errors we've already logged
	SkippedFeatures map[string]bool    // Track skipped features we've already logged
	AirTemp         float64
}

type BodyHeaterInfo struct {
	BodyName  string
	BodyObj   string
	HeaterObj string
	HTMode    int
	Temp      float64
	LoTemp    float64
	HiTemp    float64
}

type RetryConfig struct {
	MaxRetries      int
	BaseDelay       time.Duration
	MaxDelay        time.Duration
	BackoffFactor   float64
	HealthCheckRate time.Duration
}

func NewPoolMonitor(intelliCenterIP, intelliCenterPort string, debugMode, listenMode bool) *PoolMonitor {
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
		referencedHeaters: make(map[string]BodyHeaterInfo),
		pendingRequests:   make(map[string]time.Time),
		featureConfig:     make(map[string]string),
		previousState:     nil,
		debugMode:         debugMode,
		listenMode:        listenMode,
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

		conn, resp, err := dialer.DialContext(ctx, websocketURL.String(), nil)
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
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

	// Get thermal equipment status
	pm.logIfNotListeningf("DEBUG: About to call getThermalStatus()")
	if err := pm.getThermalStatus(); err != nil {
		return fmt.Errorf("failed to get thermal status: %w", err)
	}
	pm.logIfNotListeningf("DEBUG: getThermalStatus() completed successfully")

	// In listen mode, query ALL objects to discover unknown equipment
	if pm.listenMode {
		if err := pm.getAllObjects(); err != nil {
			// Don't fail the whole poll if this fails, just log it
			pm.logIfNotListeningf("Warning: failed to get all objects: %v", err)
		}
	}

	return nil
}

func (pm *PoolMonitor) getBodyTemperatures() error {
	resp, err := pm.requestBodyTemperatures()
	if err != nil {
		return err
	}

	// Update Prometheus metrics and collect heater assignments
	referencedHeaters := make(map[string]BodyHeaterInfo)

	for _, obj := range resp.ObjectList {
		pm.processBodyObject(obj, referencedHeaters)
	}

	// Store referenced heaters for heater status processing
	pm.referencedHeaters = referencedHeaters

	return nil
}

func (pm *PoolMonitor) requestBodyTemperatures() (*IntelliCenterResponse, error) {
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
				Keys:    []string{"SNAME", "STATUS", "TEMP", "SUBTYP", "HTMODE", "HTSRC", "LOTMP", "HITMP"},
			},
		},
	}

	// Track pending request
	pm.pendingRequests[messageID] = sentTime
	if pm.debugMode {
		pm.logIfNotListeningf("DEBUG: Sending body temp request with messageID: %s", messageID)
	}

	// Send request
	if err := pm.conn.WriteJSON(req); err != nil {
		delete(pm.pendingRequests, messageID)
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	// Read response
	var resp IntelliCenterResponse
	if err := pm.conn.ReadJSON(&resp); err != nil {
		delete(pm.pendingRequests, messageID)
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if pm.debugMode {
		pm.logIfNotListeningf("DEBUG: Received body temp response with messageID: %s (sent: %s)", resp.MessageID, messageID)
	}

	// Validate response correlation
	if resp.MessageID != messageID {
		delete(pm.pendingRequests, messageID)
		pm.connected = false // Mark as disconnected to trigger reconnection
		log.Printf("ERROR: Body temp messageID mismatch! Sent: %s, Received: %s - FORCING RECONNECTION", messageID, resp.MessageID)
		return nil, fmt.Errorf("messageID mismatch: sent %s, received %s - forcing reconnection", messageID, resp.MessageID)
	}

	// Clean up pending request
	pm.validateResponse(messageID)
	if pm.debugMode {
		pm.logIfNotListeningf("DEBUG: Body temp messageID correlation successful: %s", messageID)
	}

	if resp.Response != "200" {
		return nil, fmt.Errorf("API request failed with response: %s", resp.Response)
	}

	return &resp, nil
}

func (pm *PoolMonitor) processBodyObject(obj ObjectData, referencedHeaters map[string]BodyHeaterInfo) {
	name := obj.Params["SNAME"]
	tempStr := obj.Params["TEMP"]
	subtype := obj.Params["SUBTYP"]
	status := obj.Params["STATUS"]
	htmodeStr := obj.Params["HTMODE"]
	htsrc := obj.Params["HTSRC"]
	lotmpStr := obj.Params["LOTMP"]
	hitmpStr := obj.Params["HITMP"]

	pm.processBodyTemperature(name, tempStr, subtype, status)
	pm.processBodyHeatingStatus(name, htmodeStr, obj.ObjName)
	pm.processHeaterAssignment(name, tempStr, htmodeStr, htsrc, lotmpStr, hitmpStr, obj.ObjName, referencedHeaters)
}

func (pm *PoolMonitor) processBodyTemperature(name, tempStr, subtype, status string) {
	if tempStr == "" || name == "" {
		return
	}

	tempFahrenheit, err := strconv.ParseFloat(tempStr, 64)
	if err != nil {
		// Only log parse errors once in listen mode
		errorKey := fmt.Sprintf("temp-parse-%s", name)
		if pm.listenMode && pm.previousState != nil {
			if !pm.previousState.ParseErrors[errorKey] {
				log.Printf("Failed to parse temperature %s for %s: %v", tempStr, name, err)
				pm.previousState.ParseErrors[errorKey] = true
			}
		} else if !pm.listenMode {
			log.Printf("Failed to parse temperature %s for %s: %v", tempStr, name, err)
		}
		return
	}

	// Store temperature in Fahrenheit as per project standard
	poolTemperature.WithLabelValues(subtype, name).Set(tempFahrenheit)
	pm.trackWaterTemp(name, tempFahrenheit)
	pm.logIfNotListeningf("Updated temperature: %s (%s) = %.1f°F (Status: %s)", name, subtype, tempFahrenheit, status)
}

func (pm *PoolMonitor) processBodyHeatingStatus(name, htmodeStr, objName string) {
	if htmodeStr == "" || name == "" {
		return
	}

	htmode, err := strconv.Atoi(htmodeStr)
	if err != nil {
		log.Printf("Failed to parse HTMODE %s for %s: %v", htmodeStr, name, err)
		return
	}

	// HTMODE >= 1 means heater is on (1=actively heating, 2=on but not heating)
	pm.bodyHeatingStatus[strings.ToLower(name)] = htmode >= 1
	pm.logIfNotListeningf("Updated body heating status: %s (%s) HTMODE=%d [%v]", name, objName, htmode, htmode >= 1)
}

func (pm *PoolMonitor) processHeaterAssignment(
	name, tempStr, htmodeStr, htsrc, lotmpStr, hitmpStr, objName string,
	referencedHeaters map[string]BodyHeaterInfo,
) {
	if htsrc == "" || htsrc == "00000" || name == "" {
		return
	}

	// Parse temperature setpoints
	temp, _ := strconv.ParseFloat(tempStr, 64)
	lotmp, _ := strconv.ParseFloat(lotmpStr, 64)
	hitmp, _ := strconv.ParseFloat(hitmpStr, 64)
	htmode, _ := strconv.Atoi(htmodeStr)

	referencedHeaters[htsrc] = BodyHeaterInfo{
		BodyName:  name,
		BodyObj:   objName,
		HeaterObj: htsrc,
		HTMode:    htmode,
		Temp:      temp,
		LoTemp:    lotmp,
		HiTemp:    hitmp,
	}

	if pm.debugMode {
		pm.logIfNotListeningf("DEBUG: Body %s (%s) references heater %s with HTMODE=%d", name, objName, htsrc, htmode)
	}
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

			// Store temperature in Fahrenheit as per project standard
			airTemperature.WithLabelValues(subtype, name).Set(tempFahrenheit)
			pm.trackAirTemp(tempFahrenheit)
			pm.logIfNotListeningf("Updated air temperature: %s (%s) = %.1f°F (Status: %s)", name, subtype, tempFahrenheit, status)
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
	pm.logIfNotListeningf("DEBUG: getCircuitStatus() starting")
	resp, err := pm.requestCircuitData()
	if err != nil {
		return err
	}

	// Update Prometheus metrics
	for _, obj := range resp.ObjectList {
		pm.processCircuitObject(obj)
	}

	pm.logIfNotListeningf("DEBUG: getCircuitStatus() completed successfully")
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

	if name == "" || status == "" {
		return
	}

	// Separate features (FTR) from circuits (C)
	if strings.HasPrefix(obj.ObjName, "FTR") {
		pm.processFeatureObject(obj, name, status, subtype)
	} else if pm.isValidCircuit(obj.ObjName, name, subtype) {
		statusValue := pm.calculateCircuitStatusValue(name, status, obj.ObjName)
		circuitStatus.WithLabelValues(obj.ObjName, name, subtype).Set(statusValue)
		pm.trackCircuit(name, status)
	}
}

func (pm *PoolMonitor) isValidCircuit(objName, name, subtype string) bool {
	hasValidPrefix := strings.HasPrefix(objName, "C")
	isGenericAux := strings.HasPrefix(objName, "C") && strings.HasPrefix(name, "AUX ") && subtype == "GENERIC"
	return hasValidPrefix && !isGenericAux
}

func (pm *PoolMonitor) processFeatureObject(obj ObjectData, name, status, subtype string) {
	// Check if feature should be shown based on IntelliCenter's "Show as Feature" setting
	shomnu, exists := pm.featureConfig[obj.ObjName]
	if !exists || strings.HasSuffix(shomnu, "w") {
		// Feature should be shown - continue to processing
		pm.processVisibleFeature(obj, name, status, subtype)
		return
	}

	// Feature hidden - log skip message
	pm.logSkippedFeature(name, obj.ObjName, shomnu)
}

func (pm *PoolMonitor) logSkippedFeature(name, objName, shomnu string) {
	// Only log skipped features once in listen mode
	if pm.listenMode && pm.previousState != nil {
		if !pm.previousState.SkippedFeatures[objName] {
			log.Printf("Skipping feature with 'Show as Feature: NO': %s (%s) SHOMNU=%s", name, objName, shomnu)
			pm.previousState.SkippedFeatures[objName] = true
		}
		return
	}

	if !pm.listenMode {
		log.Printf("Skipping feature with 'Show as Feature: NO': %s (%s) SHOMNU=%s", name, objName, shomnu)
	}
}

func (pm *PoolMonitor) processVisibleFeature(obj ObjectData, name, status, subtype string) {
	// Calculate feature status value
	statusValue := 0.0
	if status == statusOn {
		statusValue = 1.0
	}

	// Update Prometheus metric using IntelliCenter's SUBTYP
	featureStatus.WithLabelValues(obj.ObjName, name, subtype).Set(statusValue)
	pm.trackFeature(name, status)

	pm.logIfNotListeningf("Updated feature status: %s (%s) = %s [%.0f]", name, obj.ObjName, status, statusValue)
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

	pm.logIfNotListeningf("Updated heater circuit status: %s (%s) = [%.0f] (Body: %s, Heating: %v)",
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
	if status == statusOn {
		statusValue = 1.0
	}

	pm.logIfNotListeningf("Updated circuit status: %s (%s) = %s [%.0f]", name, objName, status, statusValue)
	return statusValue
}

func (pm *PoolMonitor) getThermalStatus() error {
	// Process all heaters, not just referenced ones
	pm.logIfNotListeningf("DEBUG: getThermalStatus() called - Processing all thermal equipment")
	if pm.debugMode {
		pm.logIfNotListeningf("DEBUG: Debug mode is enabled, referenced heaters: %d", len(pm.referencedHeaters))
	}

	// Query all heaters, not just referenced ones

	// Query heater details for all heaters
	messageID := fmt.Sprintf("heater-status-%d-%d", time.Now().Unix(), time.Now().Nanosecond()%nanosecondMod)
	sentTime := time.Now()

	req := IntelliCenterRequest{
		MessageID: messageID,
		Command:   "GetParamList",
		Condition: "OBJTYP=HEATER",
		ObjectList: []ObjectQuery{
			{
				ObjName: "INCR",
				Keys:    []string{"SNAME", "STATUS", "SUBTYP", "OBJTYP"},
			},
		},
	}

	pm.pendingRequests[messageID] = sentTime
	if pm.debugMode {
		pm.logIfNotListeningf("DEBUG: Sending thermal status request for all thermal devices")
	}

	if err := pm.conn.WriteJSON(req); err != nil {
		delete(pm.pendingRequests, messageID)
		return fmt.Errorf("failed to send thermal status request: %w", err)
	}

	var resp IntelliCenterResponse
	if err := pm.conn.ReadJSON(&resp); err != nil {
		delete(pm.pendingRequests, messageID)
		return fmt.Errorf("failed to read thermal status response: %w", err)
	}

	if resp.MessageID != messageID {
		delete(pm.pendingRequests, messageID)
		pm.connected = false
		return fmt.Errorf("thermal status messageID mismatch: sent %s, received %s - forcing reconnection", messageID, resp.MessageID)
	}

	pm.validateResponse(messageID)

	if resp.Response != "200" {
		return fmt.Errorf("thermal status API request failed with response: %s", resp.Response)
	}

	// Process heater data and update metrics
	for _, obj := range resp.ObjectList {
		pm.processHeaterObject(obj)
	}

	return nil
}

func (pm *PoolMonitor) processHeaterObject(obj ObjectData) {
	name := obj.Params["SNAME"]
	subtype := obj.Params["SUBTYP"]
	status := obj.Params["STATUS"]

	if name == "" || subtype == "" {
		return
	}

	var heaterStatusValue int
	var statusDescription string

	// Check if this heater is referenced by a body
	bodyInfo, isReferenced := pm.referencedHeaters[obj.ObjName]
	if isReferenced {
		// Use body operational data for referenced heaters
		heaterStatusValue = pm.calculateHeaterStatus(&bodyInfo, subtype)
		statusDescription = fmt.Sprintf("%s (Body: %s, HTMODE: %d)",
			pm.getStatusDescription(heaterStatusValue), bodyInfo.BodyName, bodyInfo.HTMode)
	} else {
		// For non-referenced heaters, determine status by name matching with body heating status
		heaterStatusValue = pm.calculateHeaterStatusFromName(name, status)
		statusDescription = fmt.Sprintf("%s (Non-referenced, inferred from body status)",
			pm.getStatusDescription(heaterStatusValue))
	}

	// Update Prometheus metric
	thermalStatus.WithLabelValues(obj.ObjName, name, subtype).Set(float64(heaterStatusValue))
	pm.trackThermal(name, heaterStatusValue)

	// Handle temperature setpoints
	pm.updateThermalSetpoints(obj.ObjName, name, subtype, isReferenced, &bodyInfo, heaterStatusValue)

	pm.logIfNotListeningf("Updated thermal status: %s (%s) = %d [%s]",
		name, obj.ObjName, heaterStatusValue, statusDescription)
}

func (pm *PoolMonitor) updateThermalSetpoints(objName, name, subtype string, isReferenced bool, bodyInfo *BodyHeaterInfo, heaterStatusValue int) {
	// Always show heatpoint for referenced heaters
	if isReferenced {
		thermalLowSetpoint.WithLabelValues(objName, name, subtype).Set(bodyInfo.LoTemp)
	} else {
		// Remove low setpoint metric when not referenced
		thermalLowSetpoint.DeleteLabelValues(objName, name, subtype)
	}

	// Only show coolpoint if realistic temperature (< 100°F) and relevant state
	if isReferenced && bodyInfo.HiTemp < 100 && (heaterStatusValue == 3 || heaterStatusValue == 2) { // Cooling or Idle with realistic setpoint
		thermalHighSetpoint.WithLabelValues(objName, name, subtype).Set(bodyInfo.HiTemp)
	} else {
		// Remove high setpoint metric when >= 100°F, not cooling/idle, or not referenced
		thermalHighSetpoint.DeleteLabelValues(objName, name, subtype)
	}
}

func (pm *PoolMonitor) calculateHeaterStatus(bodyInfo *BodyHeaterInfo, _ string) int {
	switch bodyInfo.HTMode {
	case htModeOff:
		// When heater is off, determine if it's idle (within setpoints) or off (outside setpoints)
		if bodyInfo.Temp >= bodyInfo.LoTemp && bodyInfo.Temp <= bodyInfo.HiTemp {
			return thermalStatusIdle // Idle (heater assigned, temperature within setpoints)
		}
		return thermalStatusOff // Off (temperature outside setpoints, heater not needed)
	case htModeHeating:
		return thermalStatusHeating // Heating (traditional gas heater)
	case htModeHeatPumpHeating:
		return thermalStatusHeating // Heating (heat pump heating mode)
	case htModeHeatPumpCooling:
		return thermalStatusCooling // Cooling (heat pump cooling mode)
	default:
		return thermalStatusOff // Unknown mode, treat as off
	}
}

func (pm *PoolMonitor) calculateHeaterStatusFromName(heaterName, status string) int {
	// For non-referenced heaters, try to match with body heating status
	// Look for body names that might be associated with this heater
	heaterNameLower := strings.ToLower(heaterName)

	// Check if any body is currently heating and matches this heater's name
	for bodyName, isHeating := range pm.bodyHeatingStatus {
		if strings.Contains(heaterNameLower, bodyName) || strings.Contains(bodyName, heaterNameLower) {
			if isHeating {
				return thermalStatusHeating // Heating
			}
			return thermalStatusOff // Off
		}
	}

	// If no body match found, use the heater's own status if available
	if status == statusOn {
		return thermalStatusHeating // Heating
	}

	return thermalStatusOff // Off
}

func (pm *PoolMonitor) getStatusDescription(status int) string {
	switch status {
	case 0:
		return "off"
	case 1:
		return "heating"
	case thermalStatusIdle:
		return "idle"
	case thermalStatusCooling:
		return "cooling"
	default:
		return "unknown"
	}
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
		pm.logIfNotListeningf("DEBUG: Sending pump data request with messageID: %s", messageID)
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
		pm.logIfNotListeningf("DEBUG: Received pump data response with messageID: %s (sent: %s) in %v", resp.MessageID, messageID, responseTime)
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
		pm.logIfNotListeningf("DEBUG: Pump data messageID correlation successful: %s", messageID)
	}

	if resp.Response != "200" {
		return nil, 0, fmt.Errorf("pump data API request failed with response: %s", resp.Response)
	}

	if pm.debugMode {
		pm.logIfNotListeningf("DEBUG: Raw pump response data: %+v", resp.ObjectList)
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
	pm.trackPumpRPM(name, rpm)
	pm.logPumpUpdate(name, obj.ObjName, rpm, status, responseTime)
	return nil
}

func (pm *PoolMonitor) logPumpUpdate(name, objName string, rpm float64, status string, responseTime time.Duration) {
	if pm.debugMode {
		pm.logIfNotListeningf("Updated pump RPM: %s (%s) = %.0f RPM (Status: %s) [ResponseTime: %v]", name, objName, rpm, status, responseTime)
	} else {
		pm.logIfNotListeningf("Updated pump RPM: %s (%s) = %.0f RPM (Status: %s)", name, objName, rpm, status)
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
	} else if err := pm.LoadFeatureConfiguration(ctx); err != nil {
		log.Printf("Failed to load feature configuration: %v", err)
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

func (pm *PoolMonitor) logIfNotListeningf(format string, v ...interface{}) {
	if !pm.listenMode {
		log.Printf(format, v...)
	}
}

func (pm *PoolMonitor) LoadFeatureConfiguration(_ context.Context) error {
	messageID := fmt.Sprintf("config-%d-%d", time.Now().Unix(), time.Now().Nanosecond()%nanosecondMod)

	request := map[string]interface{}{
		"command":   "GetQuery",
		"queryName": "GetConfiguration",
		"arguments": "",
		"messageID": messageID,
	}

	pm.pendingRequests[messageID] = time.Now()
	if err := pm.conn.WriteJSON(request); err != nil {
		delete(pm.pendingRequests, messageID)
		return fmt.Errorf("failed to send configuration request: %w", err)
	}

	// Read response
	var resp map[string]interface{}
	if err := pm.conn.ReadJSON(&resp); err != nil {
		delete(pm.pendingRequests, messageID)
		return fmt.Errorf("failed to read configuration response: %w", err)
	}

	// Validate response correlation
	if respMessageID, ok := resp["messageID"].(string); !ok || respMessageID != messageID {
		delete(pm.pendingRequests, messageID)
		return fmt.Errorf("messageID mismatch: sent %s, received %v", messageID, resp["messageID"])
	}

	delete(pm.pendingRequests, messageID)

	// Parse configuration data
	pm.parseFeatureConfiguration(resp)

	return nil
}

func (pm *PoolMonitor) parseFeatureConfiguration(resp map[string]interface{}) {
	answer, ok := resp["answer"].([]interface{})
	if !ok {
		return
	}

	for _, item := range answer {
		pm.processConfigurationItem(item)
	}
}

func (pm *PoolMonitor) processConfigurationItem(item interface{}) {
	obj, objOK := item.(map[string]interface{})
	if !objOK {
		return
	}

	objName, nameOK := obj["objnam"].(string)
	if !nameOK || !strings.HasPrefix(objName, "FTR") {
		return
	}

	params, paramsOK := obj["params"].(map[string]interface{})
	if !paramsOK {
		return
	}

	shomnu, shomnuOK := params["SHOMNU"].(string)
	if !shomnuOK {
		return
	}

	pm.featureConfig[objName] = shomnu
	log.Printf("Loaded feature config: %s -> %s", objName, shomnu)
}

func (pm *PoolMonitor) initializeState() {
	pm.previousState = &EquipmentState{
		WaterTemps:      make(map[string]float64),
		PumpRPMs:        make(map[string]float64),
		Circuits:        make(map[string]string),
		Thermals:        make(map[string]int),
		Features:        make(map[string]string),
		UnknownEquip:    make(map[string]string),
		ParseErrors:     make(map[string]bool),
		SkippedFeatures: make(map[string]bool),
	}
}

func (pm *PoolMonitor) trackWaterTemp(name string, temp float64) {
	if !pm.listenMode {
		return
	}
	if pm.previousState == nil {
		pm.initializeState()
	}

	prevTemp, exists := pm.previousState.WaterTemps[name]
	if !exists {
		// First time seeing this equipment
		log.Printf("EVENT: %s temperature detected: %.1f°F", name, temp)
	} else if prevTemp != temp {
		// Value changed
		log.Printf("EVENT: %s temperature changed: %.1f°F → %.1f°F", name, prevTemp, temp)
	}
	pm.previousState.WaterTemps[name] = temp
}

func (pm *PoolMonitor) trackAirTemp(temp float64) {
	if !pm.listenMode {
		return
	}
	if pm.previousState == nil {
		pm.initializeState()
	}

	if pm.previousState.AirTemp == 0 {
		// First time seeing air temp
		log.Printf("EVENT: Air temperature detected: %.1f°F", temp)
	} else if pm.previousState.AirTemp != temp {
		// Temperature changed
		log.Printf("EVENT: Air temperature changed: %.1f°F → %.1f°F", pm.previousState.AirTemp, temp)
	}
	pm.previousState.AirTemp = temp
}

func (pm *PoolMonitor) trackPumpRPM(name string, rpm float64) {
	if !pm.listenMode {
		return
	}
	if pm.previousState == nil {
		pm.initializeState()
	}

	prevRPM, exists := pm.previousState.PumpRPMs[name]
	if !exists {
		// First time seeing this pump
		log.Printf("EVENT: %s detected: %.0f RPM", name, rpm)
	} else if prevRPM != rpm {
		// RPM changed
		log.Printf("EVENT: %s RPM changed: %.0f → %.0f", name, prevRPM, rpm)
	}
	pm.previousState.PumpRPMs[name] = rpm
}

func (pm *PoolMonitor) trackCircuit(name, status string) {
	if !pm.listenMode {
		return
	}
	if pm.previousState == nil {
		pm.initializeState()
	}

	prevStatus, exists := pm.previousState.Circuits[name]
	if !exists {
		// First time seeing this circuit
		log.Printf("EVENT: %s detected: %s", name, status)
	} else if prevStatus != status {
		// Status changed
		log.Printf("EVENT: %s turned %s", name, status)
	}
	pm.previousState.Circuits[name] = status
}

func (pm *PoolMonitor) trackThermal(name string, status int) {
	if !pm.listenMode {
		return
	}
	if pm.previousState == nil {
		pm.initializeState()
	}

	prevStatus, exists := pm.previousState.Thermals[name]
	if !exists {
		// First time seeing this thermal equipment
		log.Printf("EVENT: %s detected: %s", name, pm.getStatusDescription(status))
	} else if prevStatus != status {
		// Status changed
		log.Printf("EVENT: %s status changed: %s → %s", name,
			pm.getStatusDescription(prevStatus), pm.getStatusDescription(status))
	}
	pm.previousState.Thermals[name] = status
}

func (pm *PoolMonitor) trackFeature(name, status string) {
	if !pm.listenMode {
		return
	}
	if pm.previousState == nil {
		pm.initializeState()
	}

	prevStatus, exists := pm.previousState.Features[name]
	if !exists {
		// First time seeing this feature
		log.Printf("EVENT: %s detected: %s", name, status)
	} else if prevStatus != status {
		// Status changed
		log.Printf("EVENT: %s turned %s", name, status)
	}
	pm.previousState.Features[name] = status
}

func (pm *PoolMonitor) getAllObjects() error {
	messageID := fmt.Sprintf("all-objects-%d-%d", time.Now().Unix(), time.Now().Nanosecond()%nanosecondMod)

	req := IntelliCenterRequest{
		MessageID: messageID,
		Command:   "GetParamList",
		Condition: "", // No filter - get everything
		ObjectList: []ObjectQuery{
			{
				ObjName: "INCR",
				Keys:    []string{"SNAME", "STATUS", "OBJTYP", "SUBTYP"},
			},
		},
	}

	pm.pendingRequests[messageID] = time.Now()

	if err := pm.conn.WriteJSON(req); err != nil {
		delete(pm.pendingRequests, messageID)
		return fmt.Errorf("failed to send all objects request: %w", err)
	}

	var resp IntelliCenterResponse
	if err := pm.conn.ReadJSON(&resp); err != nil {
		delete(pm.pendingRequests, messageID)
		return fmt.Errorf("failed to read all objects response: %w", err)
	}

	if resp.MessageID != messageID {
		delete(pm.pendingRequests, messageID)
		pm.connected = false
		return fmt.Errorf("all objects messageID mismatch: sent %s, received %s", messageID, resp.MessageID)
	}

	pm.validateResponse(messageID)

	if resp.Response != "200" {
		return fmt.Errorf("all objects API request failed with response: %s", resp.Response)
	}

	// Process all objects and track unknown ones
	for _, obj := range resp.ObjectList {
		pm.trackUnknownEquipment(obj)
	}

	return nil
}

func (pm *PoolMonitor) trackUnknownEquipment(obj ObjectData) {
	if !pm.listenMode || pm.previousState == nil {
		return
	}

	objType := obj.Params["OBJTYP"]
	name := obj.Params["SNAME"]
	status := obj.Params["STATUS"]
	subtype := obj.Params["SUBTYP"]

	// Skip if already handled by specific equipment types
	switch objType {
	case "BODY", "PUMP", "CIRCUIT", "HEATER":
		return // Already tracked by specific handlers
	case "":
		return // No object type, skip
	}

	// Skip internal/system objects
	if strings.HasPrefix(obj.ObjName, "_") || strings.HasPrefix(obj.ObjName, "X") {
		return
	}

	// Build a tracking key with meaningful info
	trackingValue := fmt.Sprintf("%s:%s", objType, status)
	if subtype != "" {
		trackingValue = fmt.Sprintf("%s/%s:%s", objType, subtype, status)
	}

	prevValue, exists := pm.previousState.UnknownEquip[obj.ObjName]

	// Log equipment changes with appropriate format
	if !exists {
		pm.logUnknownEquipmentDetected(name, obj.ObjName, objType, status)
	} else if prevValue != trackingValue {
		pm.logUnknownEquipmentChanged(name, obj.ObjName, prevValue, trackingValue)
	}

	pm.previousState.UnknownEquip[obj.ObjName] = trackingValue
}

func (pm *PoolMonitor) logUnknownEquipmentDetected(name, objName, objType, status string) {
	if name != "" {
		log.Printf("EVENT: Unknown equipment detected - %s (%s) type=%s status=%s", name, objName, objType, status)
		return
	}
	log.Printf("EVENT: Unknown equipment detected - %s type=%s status=%s", objName, objType, status)
}

func (pm *PoolMonitor) logUnknownEquipmentChanged(name, objName, prevValue, trackingValue string) {
	if name != "" {
		log.Printf("EVENT: Unknown equipment changed - %s (%s) %s → %s", name, objName, prevValue, trackingValue)
		return
	}
	log.Printf("EVENT: Unknown equipment changed - %s %s → %s", objName, prevValue, trackingValue)
}

func createMetricsHandler(registry *prometheus.Registry, _ *PoolMonitor) http.Handler {
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
}

type appConfig struct {
	intelliCenterIP   string
	intelliCenterPort string
	httpPort          string
	debugMode         bool
	listenMode        bool
	pollInterval      time.Duration
}

func parseCommandLineFlags() *appConfig {
	intelliCenterIP := flag.String("ic-ip", getEnvOrDefault("PENTAMETER_IC_IP", ""),
		"IntelliCenter IP address (optional, will auto-discover if not provided, env: PENTAMETER_IC_IP)")
	intelliCenterPort := flag.String("ic-port", getEnvOrDefault("PENTAMETER_IC_PORT", "6680"), "IntelliCenter WebSocket port (env: PENTAMETER_IC_PORT)")
	httpPort := flag.String("http-port", getEnvOrDefault("PENTAMETER_HTTP_PORT", "8080"), "HTTP server port for metrics (env: PENTAMETER_HTTP_PORT)")
	debugMode := flag.Bool("debug", getEnvOrDefault("PENTAMETER_DEBUG", "false") == trueString, "Enable enhanced debugging (env: PENTAMETER_DEBUG)")
	listenMode := flag.Bool("listen", getEnvOrDefault("PENTAMETER_LISTEN", "false") == trueString,
		"Enable live event logging mode (rapid polling, log changes only) (env: PENTAMETER_LISTEN)")
	pollIntervalSeconds := flag.Int("interval", func() int {
		if env := os.Getenv("PENTAMETER_INTERVAL"); env != "" {
			if val, err := strconv.Atoi(env); err == nil {
				return val
			}
		}
		return defaultPollInterval
	}(), "Temperature polling interval in seconds (env: PENTAMETER_INTERVAL)")
	showVersion := flag.Bool("version", false, "Show version information")
	discoverOnly := flag.Bool("discover", false, "Discover IntelliCenter IP address and exit")
	flag.Parse()

	if *showVersion {
		log.Printf("pentameter %s", version)
		os.Exit(0)
	}

	if *discoverOnly {
		log.Println("Discovering IntelliCenter...")
		log.Println("Searching for IntelliCenter on network (up to 60 seconds). Press Ctrl-C to cancel.")
		ip, err := DiscoverIntelliCenter(true) // verbose mode for --discover
		if err != nil {
			log.Fatalf("Discovery failed: %v", err)
		}
		log.Printf("IntelliCenter discovered at: %s", ip)
		os.Exit(0)
	}

	pollInterval := time.Duration(*pollIntervalSeconds) * time.Second
	if *listenMode && *pollIntervalSeconds == defaultPollInterval {
		pollInterval = listenModePollInterval * time.Second
	}

	// If no IP provided, attempt auto-discovery
	if *intelliCenterIP == "" {
		log.Println("No IP address provided, attempting auto-discovery...")
		log.Println("Searching for IntelliCenter on network (up to 60 seconds). Press Ctrl-C to cancel.")
		discoveredIP, err := DiscoverIntelliCenter(false) // quiet mode for normal startup
		if err != nil {
			log.Fatalf("Auto-discovery failed: %v\nPlease provide IP address using --ic-ip flag or PENTAMETER_IC_IP environment variable", err)
		}
		*intelliCenterIP = discoveredIP
		log.Printf("Auto-discovered IntelliCenter at: %s", *intelliCenterIP)
	}

	return &appConfig{
		intelliCenterIP:   *intelliCenterIP,
		intelliCenterPort: *intelliCenterPort,
		httpPort:          *httpPort,
		debugMode:         *debugMode,
		listenMode:        *listenMode,
		pollInterval:      pollInterval,
	}
}

func logStartupMessage(cfg *appConfig) {
	log.Printf("Starting pool monitor for IntelliCenter at %s:%s", cfg.intelliCenterIP, cfg.intelliCenterPort)
	if cfg.listenMode {
		log.Printf("Listen mode enabled - logging equipment changes only")
		log.Printf("Polling interval: %v (rapid polling for change detection)", cfg.pollInterval)
	} else {
		log.Printf("HTTP server will run on port %s", cfg.httpPort)
		log.Printf("Polling interval: %v", cfg.pollInterval)
	}
	if cfg.debugMode {
		log.Printf("Enhanced debugging enabled")
	}
}

func createPrometheusRegistry() *prometheus.Registry {
	registry := prometheus.NewRegistry()
	registry.MustRegister(poolTemperature)
	registry.MustRegister(airTemperature)
	registry.MustRegister(connectionFailure)
	registry.MustRegister(lastRefreshTimestamp)
	registry.MustRegister(pumpRPM)
	registry.MustRegister(circuitStatus)
	registry.MustRegister(thermalStatus)
	registry.MustRegister(thermalLowSetpoint)
	registry.MustRegister(thermalHighSetpoint)
	registry.MustRegister(featureStatus)
	return registry
}

func setupHTTPEndpoints(registry *prometheus.Registry, monitor *PoolMonitor, httpPort string) {
	http.Handle("/metrics", createMetricsHandler(registry, monitor))
	http.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("OK")); err != nil {
			log.Printf("Failed to write health check response: %v", err)
		}
	})

	serverAddr := ":" + httpPort
	log.Printf("Starting Prometheus metrics server on %s", serverAddr)
	log.Printf("Metrics available at http://localhost:%s/metrics", httpPort)
	startServer(serverAddr)
}

func main() {
	cfg := parseCommandLineFlags()
	logStartupMessage(cfg)

	registry := createPrometheusRegistry()
	monitor := NewPoolMonitor(cfg.intelliCenterIP, cfg.intelliCenterPort, cfg.debugMode, cfg.listenMode)
	ctx := context.Background()

	if err := monitor.Connect(ctx); err != nil {
		log.Fatalf("Failed to connect to IntelliCenter: %v", err)
	}
	defer func() {
		if err := monitor.Close(); err != nil {
			log.Printf("Error closing monitor: %v", err)
		}
	}()

	if cfg.listenMode {
		log.Println("Starting live event monitoring...")
		monitor.StartTemperaturePolling(ctx, cfg.pollInterval)
		return
	}

	go monitor.StartTemperaturePolling(ctx, cfg.pollInterval)
	setupHTTPEndpoints(registry, monitor, cfg.httpPort)
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
