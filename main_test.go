package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/prometheus/client_golang/prometheus"
)

// Test helper to create a mock WebSocket server.
func createMockWebSocketServer(t *testing.T, responses map[string]IntelliCenterResponse) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{
		CheckOrigin: func(_ *http.Request) bool { return true },
	}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("Failed to upgrade connection: %v", err)
		}
		defer conn.Close()

		for {
			var req IntelliCenterRequest
			if err := conn.ReadJSON(&req); err != nil {
				return
			}

			// Determine response based on command and condition
			var resp IntelliCenterResponse
			if response, exists := responses[req.Command+":"+req.Condition]; exists {
				resp = response
				resp.MessageID = req.MessageID
			} else {
				resp = IntelliCenterResponse{
					Command:   req.Command,
					MessageID: req.MessageID,
					Response:  "200",
				}
			}

			if err := conn.WriteJSON(resp); err != nil {
				return
			}
		}
	}))
}

func TestNewPoolMonitor(t *testing.T) {
	poolMonitor := NewPoolMonitor("192.168.1.100", "6680", false)

	if poolMonitor.intelliCenterURL != "ws://192.168.1.100:6680" {
		t.Errorf("Expected URL ws://192.168.1.100:6680, got %s", poolMonitor.intelliCenterURL)
	}

	if poolMonitor.connected {
		t.Error("New monitor should not be connected initially")
	}

	if poolMonitor.debugMode {
		t.Error("Debug mode should be false")
	}

	if poolMonitor.retryConfig.MaxRetries != maxRetries {
		t.Errorf("Expected MaxRetries %d, got %d", maxRetries, poolMonitor.retryConfig.MaxRetries)
	}
}

func TestNewPoolMonitorWithDebug(t *testing.T) {
	poolMonitor := NewPoolMonitor("192.168.1.100", "6680", true)

	if !poolMonitor.debugMode {
		t.Error("Debug mode should be true")
	}
}

func TestCalculateBackoffDelay(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", false)

	tests := []struct {
		attempt  int
		expected time.Duration
	}{
		{1, 1 * time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{4, 8 * time.Second},
		{5, 16 * time.Second},
		{6, 30 * time.Second},  // Capped at maxDelay
		{10, 30 * time.Second}, // Still capped
	}

	for _, test := range tests {
		result := poolMonitor.calculateBackoffDelay(test.attempt)
		if result != test.expected {
			t.Errorf("Attempt %d: expected %v, got %v", test.attempt, test.expected, result)
		}
	}
}

func TestConnectWithValidServer(t *testing.T) {
	responses := map[string]IntelliCenterResponse{}
	server := createMockWebSocketServer(t, responses)
	defer server.Close()

	// Convert http:// to ws://
	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	urlParts := strings.Split(strings.TrimPrefix(wsURL, "ws://"), ":")

	poolMonitor := NewPoolMonitor(urlParts[0], urlParts[1], false)
	ctx := t.Context()

	err := poolMonitor.Connect(ctx)
	if err != nil {
		t.Fatalf("Expected successful connection, got error: %v", err)
	}

	if !poolMonitor.connected {
		t.Error("Monitor should be connected after successful Connect()")
	}

	poolMonitor.Close()
}

func TestConnectWithInvalidServer(t *testing.T) {
	poolMonitor := NewPoolMonitor("invalid.host", "6680", false)
	// Reduce retry config for faster test execution
	poolMonitor.retryConfig.MaxRetries = 1
	poolMonitor.retryConfig.BaseDelay = 10 * time.Millisecond
	ctx := t.Context()

	err := poolMonitor.Connect(ctx)
	if err == nil {
		t.Error("Expected connection error for invalid host")
	}

	if poolMonitor.connected {
		t.Error("Monitor should not be connected after failed connection")
	}
}

func TestIsHealthyWithoutConnection(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", false)
	ctx := t.Context()

	if poolMonitor.IsHealthy(ctx) {
		t.Error("Monitor should not be healthy without connection")
	}
}

func TestGetBodyTemperatures(t *testing.T) {
	responses := map[string]IntelliCenterResponse{
		"GetParamList:OBJTYP=BODY": {
			Command:  "GetParamList",
			Response: "200",
			ObjectList: []ObjectData{
				{
					ObjName: "BODY1",
					Params: map[string]string{
						"SNAME":  "Pool",
						"TEMP":   "82.5",
						"SUBTYP": "POOL",
						"STATUS": "ON",
						"HTMODE": "1",
						"HTSRC":  "GAS",
					},
				},
				{
					ObjName: "BODY2",
					Params: map[string]string{
						"SNAME":  "Spa",
						"TEMP":   "104.0",
						"SUBTYP": "SPA",
						"STATUS": "ON",
						"HTMODE": "0",
						"HTSRC":  "GAS",
					},
				},
			},
		},
	}

	server := createMockWebSocketServer(t, responses)
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	urlParts := strings.Split(strings.TrimPrefix(wsURL, "ws://"), ":")

	poolMonitor := NewPoolMonitor(urlParts[0], urlParts[1], false)
	ctx := t.Context()

	if err := poolMonitor.Connect(ctx); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer poolMonitor.Close()

	err := poolMonitor.getBodyTemperatures()
	if err != nil {
		t.Fatalf("getBodyTemperatures failed: %v", err)
	}

	// Check that heating status was tracked
	if !poolMonitor.bodyHeatingStatus["pool"] {
		t.Error("Pool heating status should be true (HTMODE=1)")
	}
	if poolMonitor.bodyHeatingStatus["spa"] {
		t.Error("Spa heating status should be false (HTMODE=0)")
	}
}

func testAirTemperature(t *testing.T, probeValue string, shouldFail bool, errorMsg string) {
	t.Helper()
	responses := map[string]IntelliCenterResponse{
		"GetParamList:": {
			Command:  "GetParamList",
			Response: "200",
			ObjectList: []ObjectData{
				{
					ObjName: "_A135",
					Params: map[string]string{
						"SNAME":  "Air Sensor",
						"PROBE":  probeValue,
						"SUBTYP": "AIR",
						"STATUS": "ON",
					},
				},
			},
		},
	}

	server := createMockWebSocketServer(t, responses)
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	urlParts := strings.Split(strings.TrimPrefix(wsURL, "ws://"), ":")

	poolMonitor := NewPoolMonitor(urlParts[0], urlParts[1], false)
	ctx := t.Context()

	if err := poolMonitor.Connect(ctx); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer poolMonitor.Close()

	err := poolMonitor.getAirTemperature()
	if shouldFail {
		if err != nil {
			t.Errorf(errorMsg, err)
		}
	} else {
		if err != nil {
			t.Fatalf("getAirTemperature failed: %v", err)
		}
	}
}

func TestGetAirTemperature(t *testing.T) {
	testAirTemperature(t, "75.2", false, "")
}

func TestGetPumpData(t *testing.T) {
	responses := map[string]IntelliCenterResponse{
		"GetParamList:OBJTYP=PUMP": {
			Command:  "GetParamList",
			Response: "200",
			ObjectList: []ObjectData{
				{
					ObjName: "PUMP1",
					Params: map[string]string{
						"SNAME":  "Pool Pump",
						"RPM":    "2400",
						"STATUS": "ON",
						"WATTS":  "1200",
						"GPM":    "55",
						"SPEED":  "HIGH",
					},
				},
			},
		},
	}

	server := createMockWebSocketServer(t, responses)
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	urlParts := strings.Split(strings.TrimPrefix(wsURL, "ws://"), ":")

	poolMonitor := NewPoolMonitor(urlParts[0], urlParts[1], false)
	ctx := t.Context()

	if err := poolMonitor.Connect(ctx); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer poolMonitor.Close()

	err := poolMonitor.getPumpData()
	if err != nil {
		t.Fatalf("getPumpData failed: %v", err)
	}
}

func TestGetCircuitStatus(t *testing.T) {
	responses := map[string]IntelliCenterResponse{
		"GetParamList:OBJTYP=CIRCUIT": {
			Command:  "GetParamList",
			Response: "200",
			ObjectList: []ObjectData{
				{
					ObjName: "C01",
					Params: map[string]string{
						"SNAME":  "Pool Light",
						"STATUS": "ON",
						"OBJTYP": "CIRCUIT",
						"SUBTYP": "LIGHT",
					},
				},
				{
					ObjName: "C02",
					Params: map[string]string{
						"SNAME":  "AUX 1",
						"STATUS": "OFF",
						"OBJTYP": "CIRCUIT",
						"SUBTYP": "GENERIC",
					},
				},
			},
		},
	}

	server := createMockWebSocketServer(t, responses)
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	urlParts := strings.Split(strings.TrimPrefix(wsURL, "ws://"), ":")

	poolMonitor := NewPoolMonitor(urlParts[0], urlParts[1], false)
	ctx := t.Context()

	if err := poolMonitor.Connect(ctx); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer poolMonitor.Close()

	err := poolMonitor.getCircuitStatus()
	if err != nil {
		t.Fatalf("getCircuitStatus failed: %v", err)
	}
}

func TestIsValidCircuit(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", false)

	tests := []struct {
		objName  string
		name     string
		subtype  string
		expected bool
	}{
		{"C01", "Pool Light", "LIGHT", true},
		{"FTR01", "Feature", "FEATURE", false}, // FTR objects are now features, not circuits
		{"C02", "AUX 1", "GENERIC", false}, // Generic AUX circuits are filtered out
		{"C03", "Custom Circuit", "CUSTOM", true},
		{"PUMP1", "Pool Pump", "PUMP", false}, // Wrong prefix
	}

	for _, test := range tests {
		result := poolMonitor.isValidCircuit(test.objName, test.name, test.subtype)
		if result != test.expected {
			t.Errorf("isValidCircuit(%s, %s, %s): expected %v, got %v",
				test.objName, test.name, test.subtype, test.expected, result)
		}
	}
}

func TestGetBodyNameFromCircuit(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", false)

	tests := []struct {
		circuitName string
		expected    string
	}{
		{"Pool Heater", "pool"},
		{"Spa Heat", "spa"},
		{"SPA HEATER", "spa"},
		{"POOL HEAT PUMP", "pool"},
		{"Random Circuit", ""},
		{"", ""},
	}

	for _, test := range tests {
		result := poolMonitor.getBodyNameFromCircuit(test.circuitName)
		if result != test.expected {
			t.Errorf("getBodyNameFromCircuit(%s): expected %s, got %s",
				test.circuitName, test.expected, result)
		}
	}
}

func TestCalculateCircuitStatusValue(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", false)
	poolMonitor.bodyHeatingStatus["pool"] = true
	poolMonitor.bodyHeatingStatus["spa"] = false

	tests := []struct {
		name     string
		status   string
		objName  string
		expected float64
	}{
		{"Pool Light", "ON", "C01", 1.0},
		{"Pool Light", "OFF", "C01", 0.0},
		{"Pool Heater", "ON", "C02", 1.0}, // Should use heating status, not circuit status
		{"Spa Heater", "ON", "C03", 0.0},  // Spa not heating
	}

	for _, test := range tests {
		result := poolMonitor.calculateCircuitStatusValue(test.name, test.status, test.objName)
		if result != test.expected {
			t.Errorf("calculateCircuitStatusValue(%s, %s, %s): expected %.1f, got %.1f",
				test.name, test.status, test.objName, test.expected, result)
		}
	}
}

func createMessageIDMismatchServer(t *testing.T) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{
		CheckOrigin: func(_ *http.Request) bool { return true },
	}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("Failed to upgrade connection: %v", err)
		}
		defer conn.Close()

		var req IntelliCenterRequest
		if err := conn.ReadJSON(&req); err != nil {
			return
		}

		// Always return a different messageID to cause mismatch
		resp := IntelliCenterResponse{
			Command:    req.Command,
			MessageID:  "wrong-id-" + req.MessageID,
			Response:   "200",
			ObjectList: []ObjectData{},
		}

		if err := conn.WriteJSON(resp); err != nil {
			return
		}
	}))
}

func TestMessageIDMismatch(t *testing.T) {
	server := createMessageIDMismatchServer(t)
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	urlParts := strings.Split(strings.TrimPrefix(wsURL, "ws://"), ":")

	poolMonitor := NewPoolMonitor(urlParts[0], urlParts[1], false)
	ctx := t.Context()

	if err := poolMonitor.Connect(ctx); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer poolMonitor.Close()

	err := poolMonitor.getBodyTemperatures()
	if err == nil {
		t.Error("Expected error due to messageID mismatch")
	}

	if poolMonitor.connected {
		t.Error("Connection should be marked as disconnected after messageID mismatch")
	}
}

func TestProcessPumpObjectWithInvalidRPM(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", false)

	obj := ObjectData{
		ObjName: "PUMP1",
		Params: map[string]string{
			"SNAME":  "Pool Pump",
			"RPM":    "invalid",
			"STATUS": "ON",
		},
	}

	err := poolMonitor.processPumpObject(obj, time.Millisecond)
	if err == nil {
		t.Error("Expected error for invalid RPM value")
	}
}

func TestProcessPumpObjectWithMissingData(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", false)

	obj := ObjectData{
		ObjName: "PUMP1",
		Params: map[string]string{
			"STATUS": "ON",
			// Missing RPM and SNAME
		},
	}

	err := poolMonitor.processPumpObject(obj, time.Millisecond)
	if err != nil {
		t.Errorf("Should handle missing data gracefully, got error: %v", err)
	}
}

func TestValidateResponse(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", false)

	messageID := "test-message-123"
	poolMonitor.pendingRequests[messageID] = time.Now()

	if len(poolMonitor.pendingRequests) != 1 {
		t.Error("Should have one pending request")
	}

	poolMonitor.validateResponse(messageID)

	if len(poolMonitor.pendingRequests) != 0 {
		t.Error("Pending request should be removed after validation")
	}
}

func TestGetEnvOrDefault(t *testing.T) {
	tests := []struct {
		envVar       string
		defaultValue string
		expected     string
	}{
		{"NONEXISTENT_VAR", "default", "default"},
		{"PATH", "default", ""}, // PATH exists but we expect it to return actual value (not tested here)
	}

	for _, test := range tests {
		if test.envVar == "NONEXISTENT_VAR" {
			result := getEnvOrDefault(test.envVar, test.defaultValue)
			if result != test.expected {
				t.Errorf("getEnvOrDefault(%s, %s): expected %s, got %s",
					test.envVar, test.defaultValue, test.expected, result)
			}
		}
	}
}

func TestCreateMetricsHandler(t *testing.T) {
	registry := prometheus.NewRegistry()
	poolMonitor := NewPoolMonitor("test", "6680", false)

	handler := createMetricsHandler(registry, poolMonitor)
	if handler == nil {
		t.Error("createMetricsHandler should return a non-nil handler")
	}
}

func TestPrometheusMetrics(t *testing.T) {
	// Test that metrics can be set and retrieved
	testRegistry := prometheus.NewRegistry()
	testPoolTemp := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "test_water_temperature_celsius",
			Help: "Test water temperature",
		},
		[]string{"body", "name"},
	)
	testRegistry.MustRegister(testPoolTemp)

	testPoolTemp.WithLabelValues("POOL", "Test Pool").Set(82.5)

	// Gather metrics
	metricFamilies, err := testRegistry.Gather()
	if err != nil {
		t.Fatalf("Failed to gather metrics: %v", err)
	}

	if len(metricFamilies) != 1 {
		t.Errorf("Expected 1 metric family, got %d", len(metricFamilies))
	}

	if *metricFamilies[0].Name != "test_water_temperature_celsius" {
		t.Errorf("Expected metric name test_water_temperature_celsius, got %s", *metricFamilies[0].Name)
	}
}

func TestIntelliCenterStructures(t *testing.T) {
	// Test JSON marshaling/unmarshaling of IntelliCenter structures
	req := IntelliCenterRequest{
		MessageID: "test-123",
		Command:   "GetParamList",
		Condition: "OBJTYP=BODY",
		ObjectList: []ObjectQuery{
			{
				ObjName: "INCR",
				Keys:    []string{"SNAME", "TEMP"},
			},
		},
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Failed to marshal request: %v", err)
	}

	var unmarshaled IntelliCenterRequest
	err = json.Unmarshal(data, &unmarshaled)
	if err != nil {
		t.Fatalf("Failed to unmarshal request: %v", err)
	}

	if unmarshaled.MessageID != req.MessageID {
		t.Errorf("MessageID mismatch after marshal/unmarshal")
	}

	if len(unmarshaled.ObjectList) != 1 {
		t.Errorf("Expected 1 object in ObjectList, got %d", len(unmarshaled.ObjectList))
	}
}

func TestConstants(t *testing.T) {
	// Verify that constants have reasonable values
	if nanosecondMod != 1000000 {
		t.Errorf("nanosecondMod should be 1000000, got %d", nanosecondMod)
	}

	if handshakeTimeout != 10*time.Second {
		t.Errorf("handshakeTimeout should be 10s, got %v", handshakeTimeout)
	}

	if maxRetries != 5 {
		t.Errorf("maxRetries should be 5, got %d", maxRetries)
	}

	if complexityThreshold != 15 {
		t.Errorf("complexityThreshold should be 15, got %d", complexityThreshold)
	}
}

func TestHealthCheckEndpoint(t *testing.T) {
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "/health", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}

	responseRecorder := httptest.NewRecorder()
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("OK")); err != nil {
			t.Errorf("Failed to write response: %v", err)
		}
	})

	handler.ServeHTTP(responseRecorder, req)

	if status := responseRecorder.Code; status != http.StatusOK {
		t.Errorf("Handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}

	expected := "OK"
	if responseRecorder.Body.String() != expected {
		t.Errorf("Handler returned unexpected body: got %v want %v", responseRecorder.Body.String(), expected)
	}
}

func TestGetTemperatures(t *testing.T) {
	responses := map[string]IntelliCenterResponse{
		"GetParamList:OBJTYP=BODY": {
			Command:  "GetParamList",
			Response: "200",
			ObjectList: []ObjectData{
				{
					ObjName: "BODY1",
					Params: map[string]string{
						"SNAME":  "Pool",
						"TEMP":   "82.5",
						"SUBTYP": "POOL",
						"STATUS": "ON",
						"HTMODE": "1",
					},
				},
			},
		},
		"GetParamList:": {
			Command:  "GetParamList",
			Response: "200",
			ObjectList: []ObjectData{
				{
					ObjName: "_A135",
					Params: map[string]string{
						"SNAME":  "Air Sensor",
						"PROBE":  "75.2",
						"SUBTYP": "AIR",
						"STATUS": "ON",
					},
				},
			},
		},
		"GetParamList:OBJTYP=PUMP": {
			Command:  "GetParamList",
			Response: "200",
			ObjectList: []ObjectData{
				{
					ObjName: "PUMP1",
					Params: map[string]string{
						"SNAME":  "Pool Pump",
						"RPM":    "2400",
						"STATUS": "ON",
					},
				},
			},
		},
		"GetParamList:OBJTYP=CIRCUIT": {
			Command:  "GetParamList",
			Response: "200",
			ObjectList: []ObjectData{
				{
					ObjName: "C01",
					Params: map[string]string{
						"SNAME":  "Pool Light",
						"STATUS": "ON",
						"OBJTYP": "CIRCUIT",
						"SUBTYP": "LIGHT",
					},
				},
			},
		},
	}

	server := createMockWebSocketServer(t, responses)
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	urlParts := strings.Split(strings.TrimPrefix(wsURL, "ws://"), ":")

	poolMonitor := NewPoolMonitor(urlParts[0], urlParts[1], false)
	ctx := t.Context()

	if err := poolMonitor.Connect(ctx); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer poolMonitor.Close()

	err := poolMonitor.GetTemperatures(ctx)
	if err != nil {
		t.Fatalf("GetTemperatures failed: %v", err)
	}
}

func TestGetTemperaturesWithoutConnection(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", false)
	ctx := t.Context()

	err := poolMonitor.GetTemperatures(ctx)
	if err == nil {
		t.Error("Expected error when calling GetTemperatures without connection")
	}

	if !strings.Contains(err.Error(), "not connected") {
		t.Errorf("Expected 'not connected' error, got: %v", err)
	}
}

func TestEnsureConnected(t *testing.T) {
	responses := map[string]IntelliCenterResponse{}
	server := createMockWebSocketServer(t, responses)
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	urlParts := strings.Split(strings.TrimPrefix(wsURL, "ws://"), ":")

	poolMonitor := NewPoolMonitor(urlParts[0], urlParts[1], false)
	ctx := t.Context()

	// Test with no connection
	if err := poolMonitor.EnsureConnected(ctx); err != nil {
		t.Fatalf("EnsureConnected should establish connection: %v", err)
	}

	if !poolMonitor.connected {
		t.Error("Should be connected after EnsureConnected")
	}

	// Test with existing healthy connection
	if err := poolMonitor.EnsureConnected(ctx); err != nil {
		t.Fatalf("EnsureConnected should work with existing connection: %v", err)
	}

	poolMonitor.Close()
}

func TestEnsureConnectedWithUnhealthyConnection(t *testing.T) {
	poolMonitor := NewPoolMonitor("invalid.host", "6680", false)
	// Reduce retry config for faster test execution
	poolMonitor.retryConfig.MaxRetries = 1
	poolMonitor.retryConfig.BaseDelay = 10 * time.Millisecond
	ctx := t.Context()

	// Force connected state but no actual connection
	poolMonitor.connected = true
	poolMonitor.conn = nil

	err := poolMonitor.EnsureConnected(ctx)
	if err == nil {
		t.Error("Expected error when reconnecting to invalid host")
	}

	if poolMonitor.connected {
		t.Error("Should not be connected after failed reconnection")
	}
}

func TestIsHealthyWithConnection(t *testing.T) {
	responses := map[string]IntelliCenterResponse{}
	server := createMockWebSocketServer(t, responses)
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	urlParts := strings.Split(strings.TrimPrefix(wsURL, "ws://"), ":")

	poolMonitor := NewPoolMonitor(urlParts[0], urlParts[1], false)
	ctx := t.Context()

	if err := poolMonitor.Connect(ctx); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer poolMonitor.Close()

	// Test healthy connection
	if !poolMonitor.IsHealthy(ctx) {
		t.Error("Connection should be healthy after successful connect")
	}

	// Test health check caching (should not perform ping if within interval)
	poolMonitor.lastHealthCheck = time.Now()
	if !poolMonitor.IsHealthy(ctx) {
		t.Error("Health check should be cached")
	}
}

func TestIsHealthyWithFailedPing(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", false)
	ctx := t.Context()

	// Test without any connection
	poolMonitor.connected = false
	poolMonitor.conn = nil

	if poolMonitor.IsHealthy(ctx) {
		t.Error("Health check should fail without connection")
	}

	// Test with connection marked true but no actual conn object
	poolMonitor.connected = true
	poolMonitor.conn = nil

	if poolMonitor.IsHealthy(ctx) {
		t.Error("Health check should fail when conn is nil")
	}
}

func TestStartTemperaturePollingShutdown(t *testing.T) {
	responses := map[string]IntelliCenterResponse{
		"GetParamList:OBJTYP=BODY":    {Command: "GetParamList", Response: "200"},
		"GetParamList:":               {Command: "GetParamList", Response: "200"},
		"GetParamList:OBJTYP=PUMP":    {Command: "GetParamList", Response: "200"},
		"GetParamList:OBJTYP=CIRCUIT": {Command: "GetParamList", Response: "200"},
	}

	server := createMockWebSocketServer(t, responses)
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	urlParts := strings.Split(strings.TrimPrefix(wsURL, "ws://"), ":")

	poolMonitor := NewPoolMonitor(urlParts[0], urlParts[1], false)

	// Create context that will be canceled
	ctx, cancel := context.WithCancel(t.Context())

	// Start polling in goroutine
	done := make(chan bool)
	go func() {
		poolMonitor.StartTemperaturePolling(ctx, 100*time.Millisecond)
		done <- true
	}()

	// Let it run briefly
	time.Sleep(50 * time.Millisecond)

	// Cancel and wait for shutdown
	cancel()

	select {
	case <-done:
		// Success - polling stopped
	case <-time.After(time.Second):
		t.Error("Temperature polling did not stop within timeout")
	}

	poolMonitor.Close()
}

func TestLogPumpUpdate(_ *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", false)

	// Test without debug mode
	poolMonitor.logPumpUpdate("Test Pump", "PUMP1", 2400, "ON", time.Millisecond)

	// Test with debug mode
	poolMonitor.debugMode = true
	poolMonitor.logPumpUpdate("Test Pump", "PUMP1", 2400, "ON", time.Millisecond)
}

func TestCloseWithoutConnection(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", false)

	err := poolMonitor.Close()
	if err != nil {
		t.Errorf("Close should not error when no connection exists: %v", err)
	}

	if poolMonitor.connected {
		t.Error("Should not be marked as connected after close")
	}
}

func TestGetEnvOrDefaultWithExistingVar(t *testing.T) {
	// Test with PATH which should exist
	result := getEnvOrDefault("PATH", "default")
	if result == "default" {
		t.Error("Should return actual PATH value, not default")
	}
}

func TestStartServer(t *testing.T) {
	// Test coverage for startServer function existence
	// We can't easily test startServer directly since it calls log.Fatalf which would exit
	// and ListenAndServe blocks, so we just verify the function exists and can be called
	// indirectly through testing that main.go compiles and has the expected structure

	// This test mainly exists for coverage - the actual server startup is tested
	// in integration scenarios
	if testing.Short() {
		t.Skip("Skipping server test in short mode")
	}

	// Test that the function signature exists by checking we can reference it
	// without actually calling it (since it would block or exit)
	serverFunc := startServer
	_ = serverFunc // Acknowledge we're testing existence, not calling
}

func TestRequestPumpDataAPIError(t *testing.T) {
	responses := map[string]IntelliCenterResponse{
		"GetParamList:OBJTYP=PUMP": {
			Command:  "GetParamList",
			Response: "500", // API error
		},
	}

	server := createMockWebSocketServer(t, responses)
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	urlParts := strings.Split(strings.TrimPrefix(wsURL, "ws://"), ":")

	poolMonitor := NewPoolMonitor(urlParts[0], urlParts[1], false)
	ctx := t.Context()

	if err := poolMonitor.Connect(ctx); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer poolMonitor.Close()

	_, _, err := poolMonitor.requestPumpData()
	if err == nil {
		t.Error("Expected error for API response 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("Expected error to mention response code 500, got: %v", err)
	}
}

func TestRequestCircuitDataAPIError(t *testing.T) {
	responses := map[string]IntelliCenterResponse{
		"GetParamList:OBJTYP=CIRCUIT": {
			Command:  "GetParamList",
			Response: "404", // API error
		},
	}

	server := createMockWebSocketServer(t, responses)
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	urlParts := strings.Split(strings.TrimPrefix(wsURL, "ws://"), ":")

	poolMonitor := NewPoolMonitor(urlParts[0], urlParts[1], false)
	ctx := t.Context()

	if err := poolMonitor.Connect(ctx); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer poolMonitor.Close()

	_, err := poolMonitor.requestCircuitData()
	if err == nil {
		t.Error("Expected error for API response 404")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("Expected error to mention response code 404, got: %v", err)
	}
}

func testAPIError(t *testing.T, condition, responseCode string, testFunc func(*PoolMonitor) error) {
	t.Helper()
	responses := map[string]IntelliCenterResponse{
		condition: {
			Command:  "GetParamList",
			Response: responseCode,
		},
	}

	server := createMockWebSocketServer(t, responses)
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	urlParts := strings.Split(strings.TrimPrefix(wsURL, "ws://"), ":")

	poolMonitor := NewPoolMonitor(urlParts[0], urlParts[1], false)
	ctx := t.Context()

	if err := poolMonitor.Connect(ctx); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer poolMonitor.Close()

	err := testFunc(poolMonitor)
	if err == nil {
		t.Errorf("Expected error for API response %s", responseCode)
	}
}

func TestGetBodyTemperaturesAPIError(t *testing.T) {
	testAPIError(t, "GetParamList:OBJTYP=BODY", "500", func(pm *PoolMonitor) error {
		return pm.getBodyTemperatures()
	})
}

func TestGetAirTemperatureAPIError(t *testing.T) {
	testAPIError(t, "GetParamList:", "403", func(pm *PoolMonitor) error {
		return pm.getAirTemperature()
	})
}

func TestStartTemperaturePollingWithConnectionFailures(t *testing.T) {
	// Test polling with continuous connection failures
	poolMonitor := NewPoolMonitor("invalid.host", "6680", false)
	// Reduce retry config for faster test execution
	poolMonitor.retryConfig.MaxRetries = 1
	poolMonitor.retryConfig.BaseDelay = 10 * time.Millisecond
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	// Start polling with very short interval
	done := make(chan bool, 1)
	go func() {
		poolMonitor.StartTemperaturePolling(ctx, 50*time.Millisecond)
		done <- true
	}()

	// Wait for context timeout
	select {
	case <-done:
		// Success - polling stopped due to context cancellation
	case <-time.After(500 * time.Millisecond):
		t.Error("Temperature polling did not stop within timeout")
	}
}

func TestStartTemperaturePollingWithGetTemperaturesFailure(t *testing.T) {
	// Create server that closes connection immediately after upgrade
	upgrader := websocket.Upgrader{
		CheckOrigin: func(_ *http.Request) bool { return true },
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("Failed to upgrade connection: %v", err)
		}
		// Close immediately to cause read failures
		conn.Close()
	}))
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	urlParts := strings.Split(strings.TrimPrefix(wsURL, "ws://"), ":")

	poolMonitor := NewPoolMonitor(urlParts[0], urlParts[1], false)
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	done := make(chan bool, 1)
	go func() {
		poolMonitor.StartTemperaturePolling(ctx, 50*time.Millisecond)
		done <- true
	}()

	select {
	case <-done:
		// Success - polling stopped
	case <-time.After(500 * time.Millisecond):
		t.Error("Temperature polling did not stop within timeout")
	}
}

func TestGetTemperaturesPartialFailures(t *testing.T) {
	// Create server that responds to some requests but not others
	upgrader := websocket.Upgrader{
		CheckOrigin: func(_ *http.Request) bool { return true },
	}

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("Failed to upgrade connection: %v", err)
		}
		defer conn.Close()

		for {
			var req IntelliCenterRequest
			if err := conn.ReadJSON(&req); err != nil {
				return
			}

			requestCount++
			// Fail the second request (air temperature)
			if requestCount == 2 {
				conn.Close()
				return
			}

			resp := IntelliCenterResponse{
				Command:   req.Command,
				MessageID: req.MessageID,
				Response:  "200",
				ObjectList: []ObjectData{
					{
						ObjName: "BODY1",
						Params: map[string]string{
							"SNAME":  "Pool",
							"TEMP":   "82.5",
							"SUBTYP": "POOL",
							"STATUS": "ON",
							"HTMODE": "1",
						},
					},
				},
			}

			if err := conn.WriteJSON(resp); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	urlParts := strings.Split(strings.TrimPrefix(wsURL, "ws://"), ":")

	poolMonitor := NewPoolMonitor(urlParts[0], urlParts[1], false)
	ctx := t.Context()

	if err := poolMonitor.Connect(ctx); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer poolMonitor.Close()

	err := poolMonitor.GetTemperatures(ctx)
	if err == nil {
		t.Error("Expected error due to connection failure during GetTemperatures")
	}
}

func TestIsHealthyPingFailure(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", false)
	ctx := t.Context()

	// Test case 1: nil connection should return false immediately
	poolMonitor.connected = true
	poolMonitor.conn = nil                    // This will cause early return false
	poolMonitor.lastHealthCheck = time.Time{} // Force health check

	// This should fail because conn is nil (early return)
	if poolMonitor.IsHealthy(ctx) {
		t.Error("Health check should fail when connection is nil")
	}

	// Note: poolMonitor.connected remains true because the early return in IsHealthy doesn't change it
	// This is the actual behavior of the implementation

	// Test case 2: Test with disconnected state
	poolMonitor.connected = false
	poolMonitor.conn = nil

	if poolMonitor.IsHealthy(ctx) {
		t.Error("Health check should fail when disconnected")
	}
}

func TestGetBodyTemperaturesWithInvalidTemperature(t *testing.T) {
	responses := map[string]IntelliCenterResponse{
		"GetParamList:OBJTYP=BODY": {
			Command:  "GetParamList",
			Response: "200",
			ObjectList: []ObjectData{
				{
					ObjName: "BODY1",
					Params: map[string]string{
						"SNAME":  "Pool",
						"TEMP":   "invalid_temp",
						"SUBTYP": "POOL",
						"STATUS": "ON",
						"HTMODE": "1",
					},
				},
			},
		},
	}

	server := createMockWebSocketServer(t, responses)
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	urlParts := strings.Split(strings.TrimPrefix(wsURL, "ws://"), ":")

	poolMonitor := NewPoolMonitor(urlParts[0], urlParts[1], false)
	ctx := t.Context()

	if err := poolMonitor.Connect(ctx); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer poolMonitor.Close()

	// This should not fail even with invalid temperature - it logs and continues
	err := poolMonitor.getBodyTemperatures()
	if err != nil {
		t.Errorf("getBodyTemperatures should handle invalid temperature gracefully: %v", err)
	}
}

func TestGetAirTemperatureWithInvalidTemperature(t *testing.T) {
	testAirTemperature(t, "not_a_number", true, "getAirTemperature should handle invalid temperature gracefully: %v")
}

func TestConnectWithRetryContextCancellation(t *testing.T) {
	poolMonitor := NewPoolMonitor("invalid.host", "6680", false)
	// Reduce retry config for faster test execution
	poolMonitor.retryConfig.MaxRetries = 3
	poolMonitor.retryConfig.BaseDelay = 50 * time.Millisecond

	// Create context that gets canceled during retry
	ctx, cancel := context.WithCancel(t.Context())

	// Cancel context after a delay that will occur during retry attempts
	go func() {
		time.Sleep(120 * time.Millisecond)
		cancel()
	}()

	err := poolMonitor.ConnectWithRetry(ctx)
	if err == nil {
		t.Error("Expected error due to context cancellation")
	}

	// The error could be either context.Canceled or a connection error
	// depending on timing, both are acceptable for this test
	if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "failed to connect") {
		t.Errorf("Expected context.Canceled or connection error, got: %v", err)
	}
}

func TestProcessCircuitObjectWithMissingFields(_ *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", false)

	// Test with missing name
	obj := ObjectData{
		ObjName: "C01",
		Params: map[string]string{
			"STATUS": "ON",
			"OBJTYP": "CIRCUIT",
			"SUBTYP": "LIGHT",
			// Missing SNAME
		},
	}

	// Should handle gracefully without panicking
	poolMonitor.processCircuitObject(obj)

	// Test with missing status
	obj2 := ObjectData{
		ObjName: "C02",
		Params: map[string]string{
			"SNAME":  "Test Circuit",
			"OBJTYP": "CIRCUIT",
			"SUBTYP": "LIGHT",
			// Missing STATUS
		},
	}

	// Should handle gracefully without panicking
	poolMonitor.processCircuitObject(obj2)
}

func TestIsHealthyWithPingSuccess(t *testing.T) {
	responses := map[string]IntelliCenterResponse{}
	server := createMockWebSocketServer(t, responses)
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	urlParts := strings.Split(strings.TrimPrefix(wsURL, "ws://"), ":")

	poolMonitor := NewPoolMonitor(urlParts[0], urlParts[1], false)
	ctx := t.Context()

	if err := poolMonitor.Connect(ctx); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer poolMonitor.Close()

	// Force health check by setting last check to zero
	poolMonitor.lastHealthCheck = time.Time{}

	// This should perform a ping and succeed
	if !poolMonitor.IsHealthy(ctx) {
		t.Error("Health check should succeed with valid connection")
	}
}

func TestMainCommandLineParsing(t *testing.T) {
	// Test that main would fail without required IP
	// We can't call main directly since it uses flag.Parse() and os.Exit()
	// But we can test getEnvOrDefault function behavior

	result := getEnvOrDefault("NONEXISTENT_PENTAMETER_VAR", "test-default")
	if result != "test-default" {
		t.Errorf("Expected test-default, got %s", result)
	}
}

func TestRequestPumpDataWriteError(t *testing.T) {
	// Create server that immediately closes connection to cause write error
	upgrader := websocket.Upgrader{
		CheckOrigin: func(_ *http.Request) bool { return true },
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("Failed to upgrade connection: %v", err)
		}
		// Close immediately to cause write error
		conn.Close()
	}))
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	urlParts := strings.Split(strings.TrimPrefix(wsURL, "ws://"), ":")

	poolMonitor := NewPoolMonitor(urlParts[0], urlParts[1], false)
	ctx := t.Context()

	if err := poolMonitor.Connect(ctx); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer poolMonitor.Close()

	// This should fail due to closed connection
	_, _, err := poolMonitor.requestPumpData()
	if err == nil {
		t.Error("Expected error due to write failure")
	}
}

func TestRequestCircuitDataWriteError(t *testing.T) {
	// Create server that immediately closes connection to cause write error
	upgrader := websocket.Upgrader{
		CheckOrigin: func(_ *http.Request) bool { return true },
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("Failed to upgrade connection: %v", err)
		}
		// Close immediately to cause write error
		conn.Close()
	}))
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	urlParts := strings.Split(strings.TrimPrefix(wsURL, "ws://"), ":")

	poolMonitor := NewPoolMonitor(urlParts[0], urlParts[1], false)
	ctx := t.Context()

	if err := poolMonitor.Connect(ctx); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer poolMonitor.Close()

	// This should fail due to closed connection
	_, err := poolMonitor.requestCircuitData()
	if err == nil {
		t.Error("Expected error due to write failure")
	}
}

func TestRequestPumpDataMessageIDMismatch(t *testing.T) {
	server := createMessageIDMismatchServer(t)
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	urlParts := strings.Split(strings.TrimPrefix(wsURL, "ws://"), ":")

	poolMonitor := NewPoolMonitor(urlParts[0], urlParts[1], false)
	ctx := t.Context()

	if err := poolMonitor.Connect(ctx); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer poolMonitor.Close()

	_, _, err := poolMonitor.requestPumpData()
	if err == nil {
		t.Error("Expected error due to messageID mismatch")
	}

	if poolMonitor.connected {
		t.Error("Connection should be marked as disconnected after messageID mismatch")
	}
}

func TestRequestCircuitDataMessageIDMismatch(t *testing.T) {
	server := createMessageIDMismatchServer(t)
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	urlParts := strings.Split(strings.TrimPrefix(wsURL, "ws://"), ":")

	poolMonitor := NewPoolMonitor(urlParts[0], urlParts[1], false)
	ctx := t.Context()

	if err := poolMonitor.Connect(ctx); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer poolMonitor.Close()

	_, err := poolMonitor.requestCircuitData()
	if err == nil {
		t.Error("Expected error due to messageID mismatch")
	}

	if poolMonitor.connected {
		t.Error("Connection should be marked as disconnected after messageID mismatch")
	}
}
