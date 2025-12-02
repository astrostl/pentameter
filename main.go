package main

import (
	"context"
	"encoding/json"
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
	"sync"
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
	minPollInterval     = 5
	complexityThreshold = 15
	httpReadTimeout     = 15 * time.Second
	httpWriteTimeout    = 15 * time.Second
	httpIdleTimeout     = 60 * time.Second

	// Listen mode polling interval (catches equipment that doesn't push).
	listenModePollInterval = 10

	// Re-discovery failure threshold (number of consecutive failures before attempting re-discovery).
	defaultFailureThreshold = 3

	// Maximum number of unsolicited messages to skip when waiting for a response.
	maxUnsolicitedMessages = 10

	// Read timeout for waiting for response (allows time to receive and skip push notifications).
	responseReadTimeout = 30 * time.Second

	// Circuit status constants.
	statusOn = "ON"

	// Circuit/feature status metric values.
	circuitStatusOff             = 0.0
	circuitStatusOn              = 1.0
	circuitStatusFreezeProtected = 2.0

	// Status description strings.
	statusDescOff    = "OFF"
	statusDescOn     = "ON"
	statusDescFreeze = "FREEZE"

	// Boolean string constants.
	trueString = "true"

	// Object type constants.
	objTypeBody    = "BODY"
	objTypeCircuit = "CIRCUIT"
	objTypePump    = "PUMP"
	objTypeHeater  = "HEATER"
	objTypeCircGrp = "CIRCGRP"

	// Reconnect retry delay.
	reconnectRetryDelay = 5 * time.Second

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
			Help: "Circuit status (0=off, 1=on, 2=freeze protection active)",
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
			Help: "Feature status (0=off, 1=on, 2=freeze protection active)",
		},
		[]string{"feature", "name", "subtyp"},
	)
)

type PoolMonitor struct {
	lastHealthCheck        time.Time
	lastRefresh            time.Time
	conn                   *websocket.Conn
	bodyHeatingStatus      map[string]bool           // Track which bodies are actively heating
	referencedHeaters      map[string]BodyHeaterInfo // Track body-to-heater assignments
	pendingRequests        map[string]time.Time      // Track messageID -> request time
	featureConfig          map[string]string         // Track feature objnam -> SHOMNU for visibility
	circuitFreezeConfig    map[string]bool           // Track circuit objnam -> freeze protection enabled
	previousState          *EquipmentState           // Previous state for change detection
	intelliCenterURL       string
	intelliCenterIP        string // Store IP separately for re-discovery
	intelliCenterPort      string // Store port for URL reconstruction
	retryConfig            RetryConfig
	consecutiveFailures    int        // Track consecutive connection failures for re-discovery
	failureThreshold       int        // Number of failures before attempting re-discovery
	mu                     sync.Mutex // Protects concurrent access in listen mode
	connected              bool
	listenMode             bool // Enable live event logging mode
	initialPollDone        bool // Track if initial poll completed (suppresses "detected" logs after first poll)
	freezeProtectionActive bool // Track if freeze protection is currently active
	inRediscoveryMode      bool // Currently attempting re-discovery
	disableAutoRediscovery bool // Disable automatic re-discovery (for testing)
}

// CircGrpState tracks the state of a circuit group member.
type CircGrpState struct {
	Active  string // ACT: ON/OFF
	Use     string // USE: color/mode (e.g., "White", "Blue")
	Circuit string // CIRCUIT: referenced circuit ID (e.g., "C0003")
	Parent  string // PARENT: parent group ID (e.g., "GRP01")
}

// EquipmentState tracks the current state of all equipment for change detection.
type EquipmentState struct {
	WaterTemps      map[string]float64      // body -> temperature
	PumpRPMs        map[string]float64      // pump -> RPM
	Circuits        map[string]string       // circuit -> ON/OFF
	Thermals        map[string]int          // heater -> status (0=off, 1=heating, 2=idle, 3=cooling)
	Features        map[string]string       // feature -> ON/OFF
	CircGrps        map[string]CircGrpState // circgrp objnam -> state
	UnknownEquip    map[string]string       // objnam -> "OBJTYP:STATUS" for equipment not otherwise tracked
	ParseErrors     map[string]bool         // Track parse errors we've already logged
	SkippedFeatures map[string]bool         // Track skipped features we've already logged
	AirTemp         float64
	PollChangeCount int // Count changes detected during current poll
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

func NewPoolMonitor(intelliCenterIP, intelliCenterPort string, listenMode bool) *PoolMonitor {
	return &PoolMonitor{
		intelliCenterURL:  fmt.Sprintf("ws://%s", net.JoinHostPort(intelliCenterIP, intelliCenterPort)),
		intelliCenterIP:   intelliCenterIP,
		intelliCenterPort: intelliCenterPort,
		retryConfig: RetryConfig{
			MaxRetries:      maxRetries,
			BaseDelay:       baseDelaySeconds * time.Second,
			MaxDelay:        maxDelaySeconds * time.Second,
			BackoffFactor:   backoffFactor,
			HealthCheckRate: healthCheckInterval,
		},
		connected:              false,
		bodyHeatingStatus:      make(map[string]bool),
		referencedHeaters:      make(map[string]BodyHeaterInfo),
		pendingRequests:        make(map[string]time.Time),
		featureConfig:          make(map[string]string),
		circuitFreezeConfig:    make(map[string]bool),
		previousState:          nil,
		listenMode:             listenMode,
		freezeProtectionActive: false,
		consecutiveFailures:    0,
		failureThreshold:       defaultFailureThreshold,
		inRediscoveryMode:      false,
		disableAutoRediscovery: false,
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

// readResponseWithPushHandling reads from the WebSocket, skipping any unsolicited
// push notifications until we receive the response matching our messageID.
// Push notifications from IntelliCenter have their own messageIDs and are sent
// when equipment state changes. We log these in listen/debug mode.
func (pm *PoolMonitor) readResponseWithPushHandling(expectedMessageID string) (*IntelliCenterResponse, error) {
	// Set read deadline to avoid hanging forever
	if err := pm.conn.SetReadDeadline(time.Now().Add(responseReadTimeout)); err != nil {
		return nil, fmt.Errorf("failed to set read deadline: %w", err)
	}
	defer func() {
		// Clear the deadline after we're done
		_ = pm.conn.SetReadDeadline(time.Time{})
	}()

	for i := 0; i < maxUnsolicitedMessages; i++ {
		var resp IntelliCenterResponse
		if err := pm.conn.ReadJSON(&resp); err != nil {
			return nil, fmt.Errorf("failed to read response: %w", err)
		}

		// Check if this is our expected response
		if resp.MessageID == expectedMessageID {
			return &resp, nil
		}

		// This is an unsolicited push notification from IntelliCenter
		// Log it in listen mode for visibility
		if pm.listenMode {
			pm.logPushNotification(&resp)
		}
		// Continue reading to get our actual response
	}

	return nil, fmt.Errorf("exceeded maximum unsolicited messages (%d) while waiting for response %s",
		maxUnsolicitedMessages, expectedMessageID)
}

// logPushNotification logs details about an unsolicited push notification from IntelliCenter.
// This is called during polling when we receive a push while waiting for our response.
// Since the listen loop handles pushes properly, we just skip it here.
func (pm *PoolMonitor) logPushNotification(_ *IntelliCenterResponse) {
	// Don't log - the listen loop will handle this push notification properly
	// We just skip it here to avoid duplicate/incomplete logging
}

// readGenericResponseWithPushHandling reads from the WebSocket, skipping any unsolicited
// push notifications until we receive the response matching our messageID.
// This variant handles generic map responses (used by GetQuery/GetConfiguration).
func (pm *PoolMonitor) readGenericResponseWithPushHandling(expectedMessageID string) (map[string]interface{}, error) {
	// Set read deadline to avoid hanging forever
	if err := pm.conn.SetReadDeadline(time.Now().Add(responseReadTimeout)); err != nil {
		return nil, fmt.Errorf("failed to set read deadline: %w", err)
	}
	defer func() {
		// Clear the deadline after we're done
		_ = pm.conn.SetReadDeadline(time.Time{})
	}()

	for i := 0; i < maxUnsolicitedMessages; i++ {
		var resp map[string]interface{}
		if err := pm.conn.ReadJSON(&resp); err != nil {
			return nil, fmt.Errorf("failed to read response: %w", err)
		}

		// Check if this is our expected response
		if msgID, ok := resp["messageID"].(string); ok && msgID == expectedMessageID {
			return resp, nil
		}

		// This is an unsolicited push notification from IntelliCenter
		// Log it in listen mode for visibility
		if pm.listenMode {
			pm.logGenericPushNotification(resp)
		}
		// Continue reading to get our actual response
	}

	return nil, fmt.Errorf("exceeded maximum unsolicited messages (%d) while waiting for response %s",
		maxUnsolicitedMessages, expectedMessageID)
}

// logGenericPushNotification logs details about an unsolicited push notification (generic format).
// This is called during polling when we receive a push while waiting for our response.
// Since the listen loop handles pushes properly, we just skip it here.
func (pm *PoolMonitor) logGenericPushNotification(_ map[string]interface{}) {
	// Don't log - the listen loop will handle this push notification properly
}

// StartEventListener runs a hybrid listen mode.
// It listens for real-time push notifications AND polls periodically to catch
// equipment types that IntelliCenter doesn't push (like pump RPM changes).
func (pm *PoolMonitor) StartEventListener(ctx context.Context, pollInterval time.Duration) {
	// Initialize state tracking
	pm.initializeState()

	// Do one initial poll to establish baseline state
	log.Println("Fetching initial equipment state...")
	if err := pm.GetTemperatures(ctx); err != nil {
		log.Printf("Warning: initial state fetch failed: %v", err)
	}
	pm.initialPollDone = true
	log.Println("Listening for real-time changes (Ctrl+C to stop)...")

	// Create a separate poller with its own connection
	poller := &PoolMonitor{
		intelliCenterIP:     pm.intelliCenterIP,
		intelliCenterPort:   pm.intelliCenterPort,
		intelliCenterURL:    pm.intelliCenterURL,
		retryConfig:         pm.retryConfig,
		listenMode:          pm.listenMode,
		initialPollDone:     true,             // Initial poll already done by main monitor
		previousState:       pm.previousState, // Share state for change detection
		bodyHeatingStatus:   pm.bodyHeatingStatus,
		referencedHeaters:   pm.referencedHeaters,
		pendingRequests:     make(map[string]time.Time),
		featureConfig:       pm.featureConfig,
		circuitFreezeConfig: pm.circuitFreezeConfig,
	}

	// Start poller in background with its own connection
	go pm.pollLoop(ctx, poller, pollInterval)

	// Listen for push notifications in foreground using main connection
	pm.listenLoop(ctx)
}

// pollLoop polls periodically to catch equipment that doesn't push.
// Uses a separate connection to avoid conflicts with the listen loop.
func (pm *PoolMonitor) pollLoop(ctx context.Context, poller *PoolMonitor, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Connect the poller
	if err := poller.EnsureConnected(ctx); err != nil {
		log.Printf("Poller connection failed: %v", err)
		return
	}

	for {
		select {
		case <-ctx.Done():
			if poller.conn != nil {
				_ = poller.conn.Close()
			}
			return
		case <-ticker.C:
			pm.mu.Lock()
			pm.previousState.PollChangeCount = 0
			err := poller.GetTemperatures(ctx)
			changes := pm.previousState.PollChangeCount
			pm.mu.Unlock()
			if err != nil {
				log.Printf("Poll error: %v", err)
				// Try to reconnect poller
				if err := poller.EnsureConnected(ctx); err != nil {
					log.Printf("Poller reconnection failed: %v", err)
				}
			} else if changes == 0 {
				log.Println("POLL: [no changes]")
			}
		}
	}
}

// listenLoop listens for push notifications.
func (pm *PoolMonitor) listenLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			log.Println("Event listener stopped")
			return
		default:
			var rawMsg map[string]interface{}
			if err := pm.conn.ReadJSON(&rawMsg); err != nil {
				log.Printf("Connection error: %v", err)
				if err := pm.EnsureConnected(ctx); err != nil {
					log.Printf("Reconnection failed: %v", err)
					time.Sleep(reconnectRetryDelay)
				} else {
					// Reconnected - reset state to get full report on next poll
					pm.mu.Lock()
					pm.initialPollDone = false
					pm.previousState = nil
					pm.initializeState()
					log.Println("Reconnected - fetching full equipment state...")
					if err := pm.GetTemperatures(ctx); err != nil {
						log.Printf("Warning: state fetch failed: %v", err)
					}
					pm.initialPollDone = true
					pm.mu.Unlock()
				}
				continue
			}

			pm.mu.Lock()
			pm.processRawPushNotification(rawMsg)
			pm.mu.Unlock()
		}
	}
}

// processRawPushNotification handles raw JSON push notifications.
// Logs everything received, then processes known types.
func (pm *PoolMonitor) processRawPushNotification(msg map[string]interface{}) {
	objectList, ok := msg["objectList"].([]interface{})
	if !ok || len(objectList) == 0 {
		pm.logRawMessage(msg)
		return
	}

	for _, item := range objectList {
		pm.processObjectListItem(item)
	}
}

func (pm *PoolMonitor) logRawMessage(msg map[string]interface{}) {
	jsonBytes, err := json.Marshal(msg)
	if err != nil {
		log.Printf("RAW: [marshal error: %v]", err)
		return
	}
	log.Printf("RAW: %s", string(jsonBytes))
}

func (pm *PoolMonitor) processObjectListItem(item interface{}) {
	itemMap, ok := item.(map[string]interface{})
	if !ok {
		return
	}

	changes, ok := itemMap["changes"].([]interface{})
	if !ok {
		pm.logRawMessage(itemMap)
		return
	}

	for _, change := range changes {
		pm.processChangeItem(change)
	}
}

func (pm *PoolMonitor) processChangeItem(change interface{}) {
	changeMap, ok := change.(map[string]interface{})
	if !ok {
		return
	}

	objnam, _ := changeMap["objnam"].(string)
	paramsRaw, ok := changeMap["params"].(map[string]interface{})
	if !ok {
		return
	}

	obj := pm.convertToObjectData(objnam, paramsRaw)
	pm.processPushObject(obj)
}

func (pm *PoolMonitor) convertToObjectData(objnam string, paramsRaw map[string]interface{}) ObjectData {
	params := make(map[string]string)
	for k, v := range paramsRaw {
		if s, ok := v.(string); ok {
			params[k] = s
		} else {
			params[k] = fmt.Sprintf("%v", v)
		}
	}
	return ObjectData{
		ObjName: objnam,
		Params:  params,
	}
}

// processPushObject routes a push notification to the appropriate handler.
// Uses the same processing functions as polling mode, then logs a human-readable summary.
func (pm *PoolMonitor) processPushObject(obj ObjectData) {
	objType := obj.Params["OBJTYP"]
	name := obj.Params["SNAME"]
	if name == "" {
		name = obj.ObjName
	}

	// Use the same processing functions as polling mode, then log the change.
	switch objType {
	case objTypeBody:
		pm.handleBodyPush(obj, name)
	case objTypePump:
		pm.handlePumpPush(obj, name)
	case objTypeCircuit:
		pm.handleCircuitPush(obj, name)
	case objTypeHeater:
		pm.handleHeaterPush(obj, name)
	case objTypeCircGrp:
		pm.handleCircGrpPush(obj)
	default:
		pm.handleUnknownPush(obj)
	}
}

func (pm *PoolMonitor) handleBodyPush(obj ObjectData, name string) {
	referencedHeaters := make(map[string]BodyHeaterInfo)
	pm.processBodyObject(obj, referencedHeaters)
	for k, v := range referencedHeaters {
		pm.referencedHeaters[k] = v
	}
	log.Printf("PUSH: %s temp=%s°F setpoint=%s°F htmode=%s status=%s",
		name, obj.Params["TEMP"], obj.Params["SETPT"], obj.Params["HTMODE"], obj.Params["STATUS"])
}

func (pm *PoolMonitor) handlePumpPush(obj ObjectData, name string) {
	if err := pm.processPumpObject(obj, 0); err != nil {
		log.Printf("PUSH: %s pump error: %v", name, err)
	} else {
		log.Printf("PUSH: %s rpm=%s watts=%s status=%s",
			name, obj.Params["RPM"], obj.Params["PWR"], obj.Params["STATUS"])
	}
}

func (pm *PoolMonitor) handleCircuitPush(obj ObjectData, name string) {
	pm.processCircuitObject(obj)
	log.Printf("PUSH: %s status=%s", name, obj.Params["STATUS"])
}

func (pm *PoolMonitor) handleHeaterPush(obj ObjectData, name string) {
	pm.processHeaterObject(obj)
	log.Printf("PUSH: %s status=%s mode=%s", name, obj.Params["STATUS"], obj.Params["MODE"])
}

func (pm *PoolMonitor) handleCircGrpPush(obj ObjectData) {
	pm.trackCircGrp(obj)
	// Log with circuit group details
	parent := obj.Params["PARENT"]
	circuit := obj.Params["CIRCUIT"]
	act := obj.Params["ACT"]
	use := obj.Params["USE"]
	log.Printf("PUSH: CircGrp %s parent=%s circuit=%s act=%s use=%s",
		obj.ObjName, parent, circuit, act, use)
}

func (pm *PoolMonitor) handleUnknownPush(obj ObjectData) {
	jsonBytes, err := json.Marshal(obj.Params)
	if err != nil {
		log.Printf("PUSH: unknown %s: [marshal error: %v]", obj.ObjName, err)
		return
	}
	log.Printf("PUSH: unknown %s: %s", obj.ObjName, string(jsonBytes))
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

	// Get freeze protection status (must be before circuit status)
	if err := pm.getFreezeProtectionStatus(); err != nil {
		return fmt.Errorf("failed to get freeze protection status: %w", err)
	}

	// Get circuit status
	if err := pm.getCircuitStatus(); err != nil {
		return fmt.Errorf("failed to get circuit status: %w", err)
	}

	// Get thermal equipment status
	if err := pm.getThermalStatus(); err != nil {
		return fmt.Errorf("failed to get thermal status: %w", err)
	}

	// In listen mode, query circuit groups and ALL objects to discover unknown equipment
	if pm.listenMode {
		if err := pm.getCircuitGroups(); err != nil {
			// Don't fail the whole poll if this fails, just log it
			pm.logIfNotListeningf("Warning: failed to get circuit groups: %v", err)
		}
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

	// Send request
	if err := pm.conn.WriteJSON(req); err != nil {
		delete(pm.pendingRequests, messageID)
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	// Read response, handling any push notifications that arrive first
	resp, err := pm.readResponseWithPushHandling(messageID)
	if err != nil {
		delete(pm.pendingRequests, messageID)
		return nil, err
	}

	// Clean up pending request
	pm.validateResponse(messageID)

	if resp.Response != "200" {
		return nil, fmt.Errorf("API request failed with response: %s", resp.Response)
	}

	return resp, nil
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

	// Read response, handling any push notifications that arrive first
	resp, err := pm.readResponseWithPushHandling(messageID)
	if err != nil {
		delete(pm.pendingRequests, messageID)
		return fmt.Errorf("failed to read air temp response: %w", err)
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

func (pm *PoolMonitor) getFreezeProtectionStatus() error {
	// Query the _FEA2 object which indicates active freeze protection
	messageID := fmt.Sprintf("freeze-status-%d-%d", time.Now().Unix(), time.Now().Nanosecond()%nanosecondMod)
	sentTime := time.Now()

	req := IntelliCenterRequest{
		MessageID: messageID,
		Command:   "GetParamList",
		Condition: "OBJTYP=CIRCUIT",
		ObjectList: []ObjectQuery{
			{
				ObjName: "_FEA2",
				Keys:    []string{"SNAME", "STATUS"},
			},
		},
	}

	pm.pendingRequests[messageID] = sentTime

	if err := pm.conn.WriteJSON(req); err != nil {
		delete(pm.pendingRequests, messageID)
		return fmt.Errorf("failed to send freeze protection status request: %w", err)
	}

	// Read response, handling any push notifications that arrive first
	resp, err := pm.readResponseWithPushHandling(messageID)
	if err != nil {
		delete(pm.pendingRequests, messageID)
		return fmt.Errorf("failed to read freeze protection status response: %w", err)
	}

	pm.validateResponse(messageID)

	if resp.Response != "200" {
		return fmt.Errorf("freeze protection status API request failed with response: %s", resp.Response)
	}

	// Check _FEA2 status to determine if freeze protection is active
	pm.freezeProtectionActive = false
	for _, obj := range resp.ObjectList {
		if obj.ObjName == "_FEA2" && obj.Params["STATUS"] == statusOn {
			pm.freezeProtectionActive = true
			pm.logIfNotListeningf("Freeze protection is ACTIVE")
			break
		}
	}

	if !pm.freezeProtectionActive {
		pm.logIfNotListeningf("Freeze protection is inactive")
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
				Keys:    []string{"SNAME", "STATUS", "OBJTYP", "SUBTYP", "FREEZE"},
			},
		},
	}

	pm.pendingRequests[messageID] = sentTime

	if err := pm.conn.WriteJSON(req); err != nil {
		delete(pm.pendingRequests, messageID)
		return nil, fmt.Errorf("failed to send circuit status request: %w", err)
	}

	// Read response, handling any push notifications that arrive first
	resp, err := pm.readResponseWithPushHandling(messageID)
	if err != nil {
		delete(pm.pendingRequests, messageID)
		return nil, fmt.Errorf("failed to read circuit status response: %w", err)
	}

	pm.validateResponse(messageID)

	if resp.Response != "200" {
		return nil, fmt.Errorf("circuit status API request failed with response: %s", resp.Response)
	}

	return resp, nil
}

func (pm *PoolMonitor) processCircuitObject(obj ObjectData) {
	name := obj.Params["SNAME"]
	status := obj.Params["STATUS"]
	subtype := obj.Params["SUBTYP"]
	freezeEnabled := obj.Params["FREEZE"] == statusOn

	if name == "" || status == "" {
		return
	}

	// Separate features (FTR) from circuits (C)
	if strings.HasPrefix(obj.ObjName, "FTR") {
		pm.processFeatureObject(obj, name, status, subtype, freezeEnabled)
	} else if pm.isValidCircuit(obj.ObjName, name, subtype) {
		statusValue := pm.calculateCircuitStatusValue(name, status, obj.ObjName, freezeEnabled)
		circuitStatus.WithLabelValues(obj.ObjName, name, subtype).Set(statusValue)
		pm.trackCircuit(name, status)
	}
}

func (pm *PoolMonitor) isValidCircuit(objName, name, subtype string) bool {
	hasValidPrefix := strings.HasPrefix(objName, "C")
	isGenericAux := strings.HasPrefix(objName, "C") && strings.HasPrefix(name, "AUX ") && subtype == "GENERIC"
	return hasValidPrefix && !isGenericAux
}

func (pm *PoolMonitor) processFeatureObject(obj ObjectData, name, status, subtype string, freezeEnabled bool) {
	// Check if feature should be shown based on IntelliCenter's "Show as Feature" setting
	shomnu, exists := pm.featureConfig[obj.ObjName]
	if !exists || strings.HasSuffix(shomnu, "w") {
		// Feature should be shown - continue to processing
		pm.processVisibleFeature(obj, name, status, subtype, freezeEnabled)
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

func (pm *PoolMonitor) processVisibleFeature(obj ObjectData, name, status, subtype string, freezeEnabled bool) {
	// Calculate feature status value with freeze protection support
	statusValue := circuitStatusOff
	statusDesc := statusDescOff

	if status == statusOn {
		// Check if freeze protection is active and this feature has freeze protection enabled
		if pm.freezeProtectionActive && freezeEnabled {
			statusValue = circuitStatusFreezeProtected
			statusDesc = statusDescFreeze
		} else {
			statusValue = circuitStatusOn
			statusDesc = statusDescOn
		}
	}

	// Update Prometheus metric using IntelliCenter's SUBTYP
	featureStatus.WithLabelValues(obj.ObjName, name, subtype).Set(statusValue)
	pm.trackFeature(name, status)

	pm.logIfNotListeningf("Updated feature status: %s (%s) = %s [%.0f]", name, obj.ObjName, statusDesc, statusValue)
}

func (pm *PoolMonitor) calculateCircuitStatusValue(name, status, objName string, freezeEnabled bool) float64 {
	isHeaterCircuit := strings.Contains(strings.ToLower(name), "heat")

	if isHeaterCircuit {
		return pm.getHeaterCircuitStatus(name, objName, freezeEnabled)
	}

	return pm.getRegularCircuitStatus(name, status, objName, freezeEnabled)
}

func (pm *PoolMonitor) getHeaterCircuitStatus(name, objName string, freezeEnabled bool) float64 {
	bodyName := pm.getBodyNameFromCircuit(name)
	statusValue := circuitStatusOff
	statusDesc := statusDescOff

	if bodyName != "" && pm.bodyHeatingStatus[bodyName] {
		// Check if freeze protection is active and this circuit has freeze protection enabled
		if pm.freezeProtectionActive && freezeEnabled {
			statusValue = circuitStatusFreezeProtected
			statusDesc = statusDescFreeze
		} else {
			statusValue = circuitStatusOn
			statusDesc = statusDescOn
		}
	}

	pm.logIfNotListeningf("Updated heater circuit status: %s (%s) = %s [%.0f] (Body: %s, Heating: %v)",
		name, objName, statusDesc, statusValue, bodyName, pm.bodyHeatingStatus[bodyName])

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

func (pm *PoolMonitor) getRegularCircuitStatus(name, status, objName string, freezeEnabled bool) float64 {
	statusValue := circuitStatusOff
	statusDesc := statusDescOff

	if status == statusOn {
		// Check if freeze protection is active and this circuit has freeze protection enabled
		if pm.freezeProtectionActive && freezeEnabled {
			statusValue = circuitStatusFreezeProtected
			statusDesc = statusDescFreeze
		} else {
			statusValue = circuitStatusOn
			statusDesc = statusDescOn
		}
	}

	pm.logIfNotListeningf("Updated circuit status: %s (%s) = %s [%.0f]", name, objName, statusDesc, statusValue)
	return statusValue
}

func (pm *PoolMonitor) getThermalStatus() error {
	// Process all heaters, not just referenced ones

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

	if err := pm.conn.WriteJSON(req); err != nil {
		delete(pm.pendingRequests, messageID)
		return fmt.Errorf("failed to send thermal status request: %w", err)
	}

	// Read response, handling any push notifications that arrive first
	resp, err := pm.readResponseWithPushHandling(messageID)
	if err != nil {
		delete(pm.pendingRequests, messageID)
		return fmt.Errorf("failed to read thermal status response: %w", err)
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

func (pm *PoolMonitor) getCircuitGroups() error {
	messageID := fmt.Sprintf("circgrp-%d-%d", time.Now().Unix(), time.Now().Nanosecond()%nanosecondMod)
	sentTime := time.Now()

	req := IntelliCenterRequest{
		MessageID: messageID,
		Command:   "GetParamList",
		Condition: "OBJTYP=CIRCGRP",
		ObjectList: []ObjectQuery{
			{
				ObjName: "INCR",
				Keys:    []string{"OBJTYP", "PARENT", "CIRCUIT", "ACT", "USE", "DLY", "LISTORD", "STATIC"},
			},
		},
	}

	pm.pendingRequests[messageID] = sentTime

	if err := pm.conn.WriteJSON(req); err != nil {
		delete(pm.pendingRequests, messageID)
		return fmt.Errorf("failed to send circuit group request: %w", err)
	}

	resp, err := pm.readResponseWithPushHandling(messageID)
	if err != nil {
		delete(pm.pendingRequests, messageID)
		return fmt.Errorf("failed to read circuit group response: %w", err)
	}

	pm.validateResponse(messageID)

	if resp.Response != "200" {
		return fmt.Errorf("circuit group API request failed with response: %s", resp.Response)
	}

	// Process circuit group data
	for _, obj := range resp.ObjectList {
		pm.trackCircGrp(obj)
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

	if err := pm.conn.WriteJSON(req); err != nil {
		delete(pm.pendingRequests, messageID)
		return nil, 0, fmt.Errorf("failed to send pump data request: %w", err)
	}

	// Read response, handling any push notifications that arrive first
	resp, err := pm.readResponseWithPushHandling(messageID)
	responseTime := time.Since(sentTime)
	if err != nil {
		delete(pm.pendingRequests, messageID)
		return nil, 0, fmt.Errorf("failed to read pump data response: %w", err)
	}

	pm.validateResponse(messageID)

	if resp.Response != "200" {
		return nil, 0, fmt.Errorf("pump data API request failed with response: %s", resp.Response)
	}

	return resp, responseTime, nil
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
	pm.logIfNotListeningf("Updated pump RPM: %s (%s) = %.0f RPM (Status: %s) [ResponseTime: %v]", name, objName, rpm, status, responseTime)
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

	// Only log reconnect message if we were previously connected
	if pm.conn != nil {
		log.Println("Connection unhealthy, attempting to reconnect...")
	}
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

	pm.performInitialPolling(ctx)
	pm.runPollingLoop(ctx, ticker)
}

func (pm *PoolMonitor) performInitialPolling(ctx context.Context) {
	if err := pm.EnsureConnected(ctx); err != nil {
		log.Printf("Failed to establish initial connection: %v", err)
		return
	}

	if err := pm.LoadFeatureConfiguration(ctx); err != nil {
		log.Printf("Failed to load feature configuration: %v", err)
		return
	}

	if err := pm.GetTemperatures(ctx); err != nil {
		log.Printf("Failed to get initial temperatures: %v", err)
		return
	}

	pm.updateRefreshTimestamp()
}

func (pm *PoolMonitor) runPollingLoop(ctx context.Context, ticker *time.Ticker) {
	for {
		select {
		case <-ctx.Done():
			log.Println("Temperature polling stopped")
			return
		case <-ticker.C:
			pm.handlePollingTick(ctx)
		}
	}
}

func (pm *PoolMonitor) handlePollingTick(ctx context.Context) {
	// Check if we need to enter re-discovery mode (only if auto-discovery is enabled)
	if !pm.disableAutoRediscovery && !pm.inRediscoveryMode && pm.consecutiveFailures >= pm.failureThreshold {
		log.Printf("Connection failed %d times, entering re-discovery mode", pm.consecutiveFailures)
		pm.inRediscoveryMode = true
	}

	// If in re-discovery mode, attempt re-discovery instead of normal connection
	if pm.inRediscoveryMode {
		if pm.attemptRediscovery(ctx) {
			// Re-discovery succeeded, exit re-discovery mode and reset failure counter
			pm.inRediscoveryMode = false
			pm.consecutiveFailures = 0
			log.Printf("Re-discovery successful, resuming normal operation")
			// Fall through to attempt normal polling
		} else {
			// Re-discovery failed, stay in re-discovery mode and try again next interval
			connectionFailure.Set(1)
			return
		}
	}

	// Normal connection and polling
	if err := pm.EnsureConnected(ctx); err != nil {
		log.Printf("Failed to ensure connection: %v", err)
		pm.handlePollingError(err)
		return
	}

	if err := pm.GetTemperatures(ctx); err != nil {
		pm.handlePollingError(err)
		return
	}

	pm.handlePollingSuccess()
}

func (pm *PoolMonitor) handlePollingError(err error) {
	log.Printf("Failed to get temperatures: %v", err)
	if !pm.listenMode {
		pm.connected = false
	}
	pm.consecutiveFailures++
	connectionFailure.Set(1)
}

func (pm *PoolMonitor) handlePollingSuccess() {
	pm.updateRefreshTimestamp()
	pm.consecutiveFailures = 0   // Reset failure counter on success
	pm.inRediscoveryMode = false // Exit re-discovery mode if we were in it
	connectionFailure.Set(0)
}

func (pm *PoolMonitor) updateRefreshTimestamp() {
	pm.lastRefresh = time.Now()
	lastRefreshTimestamp.Set(float64(pm.lastRefresh.Unix()))
}

// updateIntelliCenterIP updates the IP address and reconstructs the WebSocket URL.
func (pm *PoolMonitor) updateIntelliCenterIP(newIP string) {
	pm.intelliCenterIP = newIP
	pm.intelliCenterURL = fmt.Sprintf("ws://%s", net.JoinHostPort(newIP, pm.intelliCenterPort))
	pm.connected = false // Force reconnection with new IP
}

// attemptRediscovery tries to discover the IntelliCenter via mDNS and update the IP.
// Returns true if discovery succeeded and IP was updated, false otherwise.
//
// Note: This function is not unit tested because it requires real mDNS discovery
// and an actual IntelliCenter on the network. The surrounding logic (failure counting,
// threshold detection, IP updating) is thoroughly tested. Integration testing of this
// function would require network hardware and make tests non-deterministic.
func (pm *PoolMonitor) attemptRediscovery(ctx context.Context) bool {
	log.Printf("Attempting IntelliCenter re-discovery via mDNS...")

	discoveredIP, err := DiscoverIntelliCenter(false) // non-verbose for automatic re-discovery
	if err != nil {
		log.Printf("Re-discovery failed: %v (will retry on next poll)", err)
		return false
	}

	if discoveredIP == pm.intelliCenterIP {
		log.Printf("Re-discovery found same IP (%s), connection issue may be temporary", discoveredIP)
		return false
	}

	log.Printf("Re-discovery successful! IntelliCenter found at new IP: %s (was: %s)", discoveredIP, pm.intelliCenterIP)
	pm.updateIntelliCenterIP(discoveredIP)

	// Attempt to connect with new IP
	if err := pm.Connect(ctx); err != nil {
		log.Printf("Failed to connect to re-discovered IP %s: %v", discoveredIP, err)
		return false
	}

	log.Printf("Successfully reconnected to IntelliCenter at new IP: %s", discoveredIP)
	return true
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

	// Read response, handling any push notifications that arrive first
	resp, err := pm.readGenericResponseWithPushHandling(messageID)
	if err != nil {
		delete(pm.pendingRequests, messageID)
		return fmt.Errorf("failed to read configuration response: %w", err)
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
		CircGrps:        make(map[string]CircGrpState),
		UnknownEquip:    make(map[string]string),
		ParseErrors:     make(map[string]bool),
		SkippedFeatures: make(map[string]bool),
	}
}

// logPollChangef logs a change and increments the change counter.
func (pm *PoolMonitor) logPollChangef(format string, args ...interface{}) {
	log.Printf("POLL: "+format, args...)
	pm.previousState.PollChangeCount++
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
		// First time seeing this equipment - only log on initial poll
		if !pm.initialPollDone {
			log.Printf("POLL: %s temperature detected: %.1f°F", name, temp)
		}
	} else if prevTemp != temp {
		pm.logPollChangef("%s temperature changed: %.1f°F → %.1f°F", name, prevTemp, temp)
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
		// First time seeing air temp - only log on initial poll
		if !pm.initialPollDone {
			log.Printf("POLL: Air temperature detected: %.1f°F", temp)
		}
	} else if pm.previousState.AirTemp != temp {
		pm.logPollChangef("Air temperature changed: %.1f°F → %.1f°F", pm.previousState.AirTemp, temp)
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
		// First time seeing this pump - only log on initial poll
		if !pm.initialPollDone {
			log.Printf("POLL: %s detected: %.0f RPM", name, rpm)
		}
	} else if prevRPM != rpm {
		pm.logPollChangef("%s RPM changed: %.0f → %.0f", name, prevRPM, rpm)
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
		// First time seeing this circuit - only log on initial poll
		if !pm.initialPollDone {
			log.Printf("POLL: %s detected: %s", name, status)
		}
	} else if prevStatus != status {
		pm.logPollChangef("%s turned %s", name, status)
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
		// First time seeing this thermal equipment - only log on initial poll
		if !pm.initialPollDone {
			log.Printf("POLL: %s detected: %s", name, pm.getStatusDescription(status))
		}
	} else if prevStatus != status {
		pm.logPollChangef("%s status changed: %s → %s", name,
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
		// First time seeing this feature - only log on initial poll
		if !pm.initialPollDone {
			log.Printf("POLL: %s detected: %s", name, status)
		}
	} else if prevStatus != status {
		pm.logPollChangef("%s turned %s", name, status)
	}
	pm.previousState.Features[name] = status
}

func (pm *PoolMonitor) trackCircGrp(obj ObjectData) {
	if !pm.listenMode {
		return
	}
	if pm.previousState == nil {
		pm.initializeState()
	}

	objName := obj.ObjName
	newState := CircGrpState{
		Active:  obj.Params["ACT"],
		Use:     obj.Params["USE"],
		Circuit: obj.Params["CIRCUIT"],
		Parent:  obj.Params["PARENT"],
	}

	prevState, exists := pm.previousState.CircGrps[objName]
	pm.previousState.CircGrps[objName] = newState

	if !exists {
		// First time seeing this circuit group member - only log on initial poll
		if !pm.initialPollDone {
			log.Printf("POLL: CircGrp %s detected: parent=%s circuit=%s act=%s use=%s",
				objName, newState.Parent, newState.Circuit, newState.Active, newState.Use)
		}
		return
	}

	if prevState == newState {
		return
	}

	// Log what changed
	changes := pm.buildCircGrpChanges(prevState, newState)
	if len(changes) > 0 {
		pm.logPollChangef("CircGrp %s changed: %s (parent=%s circuit=%s)",
			objName, strings.Join(changes, " "), newState.Parent, newState.Circuit)
	}
}

func (pm *PoolMonitor) buildCircGrpChanges(prevState, newState CircGrpState) []string {
	var changes []string
	if prevState.Active != newState.Active {
		changes = append(changes, fmt.Sprintf("act=%s→%s", prevState.Active, newState.Active))
	}
	if prevState.Use != newState.Use {
		changes = append(changes, fmt.Sprintf("use=%s→%s", prevState.Use, newState.Use))
	}
	return changes
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

	// Read response, handling any push notifications that arrive first
	resp, err := pm.readResponseWithPushHandling(messageID)
	if err != nil {
		delete(pm.pendingRequests, messageID)
		return fmt.Errorf("failed to read all objects response: %w", err)
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
	case "BODY", "PUMP", "CIRCUIT", "HEATER", "CIRCGRP":
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
		// Only log on initial poll
		if !pm.initialPollDone {
			pm.logUnknownEquipmentDetected(name, obj.ObjName, objType, status)
		}
	} else if prevValue != trackingValue {
		pm.logUnknownEquipmentChanged(name, obj.ObjName, prevValue, trackingValue)
	}

	pm.previousState.UnknownEquip[obj.ObjName] = trackingValue
}

func (pm *PoolMonitor) logUnknownEquipmentDetected(name, objName, objType, status string) {
	if name != "" {
		log.Printf("POLL: Unknown equipment detected - %s (%s) type=%s status=%s", name, objName, objType, status)
		return
	}
	log.Printf("POLL: Unknown equipment detected - %s type=%s status=%s", objName, objType, status)
}

func (pm *PoolMonitor) logUnknownEquipmentChanged(name, objName, prevValue, trackingValue string) {
	if name != "" {
		log.Printf("POLL: Unknown equipment changed - %s (%s) %s → %s", name, objName, prevValue, trackingValue)
		return
	}
	log.Printf("POLL: Unknown equipment changed - %s %s → %s", objName, prevValue, trackingValue)
}

func createMetricsHandler(registry *prometheus.Registry, _ *PoolMonitor) http.Handler {
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
}

type appConfig struct {
	intelliCenterIP   string
	intelliCenterPort string
	httpPort          string
	listenMode        bool
	pollInterval      time.Duration
}

type commandLineFlags struct {
	intelliCenterIP   *string
	intelliCenterPort *string
	httpPort          *string
	listenMode        *bool
	pollInterval      *int
	showVersion       *bool
	discoverOnly      *bool
}

func defineFlags() *commandLineFlags {
	return &commandLineFlags{
		intelliCenterIP: flag.String("ic-ip", getEnvOrDefault("PENTAMETER_IC_IP", ""),
			"IntelliCenter IP address (optional, will auto-discover if not provided, env: PENTAMETER_IC_IP)"),
		intelliCenterPort: flag.String("ic-port", getEnvOrDefault("PENTAMETER_IC_PORT", "6680"),
			"IntelliCenter WebSocket port (env: PENTAMETER_IC_PORT)"),
		httpPort: flag.String("http-port", getEnvOrDefault("PENTAMETER_HTTP_PORT", "8080"),
			"HTTP server port for metrics (env: PENTAMETER_HTTP_PORT)"),
		listenMode: flag.Bool("listen", getEnvOrDefault("PENTAMETER_LISTEN", "false") == trueString,
			"Enable live event logging mode (rapid polling, log changes only) (env: PENTAMETER_LISTEN)"),
		pollInterval: flag.Int("interval", getEnvIntOrDefault("PENTAMETER_INTERVAL", 0), "Temperature polling interval in seconds (env: PENTAMETER_INTERVAL)"),
		showVersion:  flag.Bool("version", false, "Show version information"),
		discoverOnly: flag.Bool("discover", false, "Discover IntelliCenter IP address and exit"),
	}
}

func getEnvIntOrDefault(envVar string, defaultValue int) int {
	if env := os.Getenv(envVar); env != "" {
		if val, err := strconv.Atoi(env); err == nil {
			return val
		}
	}
	return defaultValue
}

func handleEarlyExitFlags(flags *commandLineFlags) {
	if *flags.showVersion {
		log.Printf("pentameter %s", version)
		os.Exit(0)
	}

	if *flags.discoverOnly {
		log.Println("Discovering IntelliCenter...")
		log.Println("Searching for IntelliCenter on network (up to 60 seconds). Press Ctrl-C to cancel.")
		ip, err := DiscoverIntelliCenter(true)
		if err != nil {
			log.Fatalf("Discovery failed: %v", err)
		}
		log.Printf("IntelliCenter discovered at: %s", ip)
		os.Exit(0)
	}
}

func determinePollInterval(pollIntervalSeconds int, listenMode bool) time.Duration {
	if pollIntervalSeconds > 0 {
		if pollIntervalSeconds < minPollInterval {
			log.Printf("Warning: interval %ds is below minimum (%ds), using %ds",
				pollIntervalSeconds, minPollInterval, minPollInterval)
			return minPollInterval * time.Second
		}
		return time.Duration(pollIntervalSeconds) * time.Second
	}
	if listenMode {
		return listenModePollInterval * time.Second
	}
	return defaultPollInterval * time.Second
}

func resolveIntelliCenterIP(ip string) string {
	if ip != "" {
		return ip
	}
	log.Println("No IP address provided, attempting auto-discovery...")
	log.Println("Tip: Specify with --ic-ip flag or export PENTAMETER_IC_IP environment variable to skip discovery")
	log.Println("Searching for IntelliCenter on network (up to 60 seconds). Press Ctrl-C to cancel.")
	discoveredIP, err := DiscoverIntelliCenter(true)
	if err != nil {
		log.Fatalf("Auto-discovery failed: %v\nPlease provide IP address using --ic-ip flag or PENTAMETER_IC_IP environment variable", err)
	}
	log.Printf("Auto-discovered IntelliCenter at: %s", discoveredIP)
	return discoveredIP
}

func parseCommandLineFlags() *appConfig {
	flags := defineFlags()
	flag.Parse()

	handleEarlyExitFlags(flags)

	return &appConfig{
		intelliCenterIP:   resolveIntelliCenterIP(*flags.intelliCenterIP),
		intelliCenterPort: *flags.intelliCenterPort,
		httpPort:          *flags.httpPort,
		listenMode:        *flags.listenMode,
		pollInterval:      determinePollInterval(*flags.pollInterval, *flags.listenMode),
	}
}

func logStartupMessage(cfg *appConfig) {
	log.Printf("Starting pool monitor for IntelliCenter at %s:%s", cfg.intelliCenterIP, cfg.intelliCenterPort)
	if cfg.listenMode {
		log.Printf("Listen mode enabled - real-time push + polling every %v", cfg.pollInterval)
	} else {
		log.Printf("HTTP server will run on port %s", cfg.httpPort)
		log.Printf("Polling interval: %v", cfg.pollInterval)
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
	monitor := NewPoolMonitor(cfg.intelliCenterIP, cfg.intelliCenterPort, cfg.listenMode)
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
		monitor.StartEventListener(ctx, cfg.pollInterval)
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
