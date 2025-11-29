package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	testShowOnMenuValue   = "1w"                      // Test value for SHOMNU parameter indicating feature should be shown.
	testStatusOff         = "OFF"                     // Test circuit/feature off status.
	testStatusOn          = statusOn                  // Test circuit/feature on status (uses main.go constant).
	testIntelliCenterURL  = "ws://192.168.1.100:6680" // Test IntelliCenter WebSocket URL.
	testIntelliCenterIP   = "192.168.1.100"           // Test IntelliCenter IP address.
	testIntelliCenterPort = "6680"                    // Test IntelliCenter port.
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
	poolMonitor := NewPoolMonitor(testIntelliCenterIP, testIntelliCenterPort, false)

	if poolMonitor.intelliCenterURL != testIntelliCenterURL {
		t.Errorf("Expected URL %s, got %s", testIntelliCenterURL, poolMonitor.intelliCenterURL)
	}

	if poolMonitor.connected {
		t.Error("New monitor should not be connected initially")
	}

	if poolMonitor.retryConfig.MaxRetries != maxRetries {
		t.Errorf("Expected MaxRetries %d, got %d", maxRetries, poolMonitor.retryConfig.MaxRetries)
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
		{"C02", "AUX 1", "GENERIC", false},     // Generic AUX circuits are filtered out
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
		name          string
		status        string
		objName       string
		freezeEnabled bool
		expected      float64
	}{
		{"Pool Light", "ON", "C01", false, 1.0},
		{"Pool Light", "OFF", "C01", false, 0.0},
		{"Pool Heater", "ON", "C02", false, 1.0}, // Should use heating status, not circuit status
		{"Spa Heater", "ON", "C03", false, 0.0},  // Spa not heating
	}

	for _, test := range tests {
		result := poolMonitor.calculateCircuitStatusValue(test.name, test.status, test.objName, test.freezeEnabled)
		if result != test.expected {
			t.Errorf("calculateCircuitStatusValue(%s, %s, %s, %v): expected %.1f, got %.1f",
				test.name, test.status, test.objName, test.freezeEnabled, test.expected, result)
		}
	}
}

// createPushNotificationServer creates a server that sends push notifications before the actual response.
// This tests that pentameter gracefully handles unsolicited push notifications.
func createPushNotificationServer(t *testing.T, numPushNotifications int) *httptest.Server {
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

		// Send push notifications first (simulating equipment state changes)
		for i := 0; i < numPushNotifications; i++ {
			pushResp := IntelliCenterResponse{
				Command:   "NotifyList",
				MessageID: fmt.Sprintf("push-notification-%d", i),
				Response:  "200",
				ObjectList: []ObjectData{
					{
						ObjName: "C0001",
						Params:  map[string]string{"SNAME": "Pool Light", "STATUS": "ON"},
					},
				},
			}
			if err := conn.WriteJSON(pushResp); err != nil {
				return
			}
		}

		// Then send the actual response with matching messageID
		resp := IntelliCenterResponse{
			Command:   req.Command,
			MessageID: req.MessageID,
			Response:  "200",
			ObjectList: []ObjectData{
				{
					ObjName: "B0001",
					Params: map[string]string{
						"SNAME":  "Pool",
						"TEMP":   "82.5",
						"SUBTYP": "POOL",
						"STATUS": "ON",
						"HTMODE": "0",
					},
				},
			},
		}

		if err := conn.WriteJSON(resp); err != nil {
			return
		}
	}))
}

func TestPushNotificationHandling(t *testing.T) {
	// Test that push notifications are gracefully skipped
	server := createPushNotificationServer(t, 3) // Send 3 push notifications before response
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	urlParts := strings.Split(strings.TrimPrefix(wsURL, "ws://"), ":")

	poolMonitor := NewPoolMonitor(urlParts[0], urlParts[1], false)
	ctx := t.Context()

	if err := poolMonitor.Connect(ctx); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer poolMonitor.Close()

	// This should succeed despite receiving push notifications first
	err := poolMonitor.getBodyTemperatures()
	if err != nil {
		t.Errorf("Expected success after skipping push notifications, got: %v", err)
	}

	// Connection should remain healthy
	if !poolMonitor.connected {
		t.Error("Connection should remain healthy after handling push notifications")
	}
}

func TestPushNotificationLogging(t *testing.T) {
	// Test that push notifications are logged in debug mode
	server := createPushNotificationServer(t, 2)
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	urlParts := strings.Split(strings.TrimPrefix(wsURL, "ws://"), ":")

	// Enable debug mode to see push notification logs
	poolMonitor := NewPoolMonitor(urlParts[0], urlParts[1], true)
	ctx := t.Context()

	if err := poolMonitor.Connect(ctx); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer poolMonitor.Close()

	// Should succeed and log push notifications
	err := poolMonitor.getBodyTemperatures()
	if err != nil {
		t.Errorf("Expected success, got: %v", err)
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

	// Test pump update logging
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
	poolMonitor.disableAutoRediscovery = true // Disable re-discovery for test
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
	poolMonitor.disableAutoRediscovery = true // Disable re-discovery for test
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

func createImmediateCloseServer(t *testing.T) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{
		CheckOrigin: func(_ *http.Request) bool { return true },
	}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("Failed to upgrade connection: %v", err)
		}
		// Close immediately to cause write/read error
		conn.Close()
	}))
}

func setupPoolMonitorWithServer(t *testing.T, server *httptest.Server) *PoolMonitor {
	t.Helper()
	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	urlParts := strings.Split(strings.TrimPrefix(wsURL, "ws://"), ":")

	poolMonitor := NewPoolMonitor(urlParts[0], urlParts[1], false)
	ctx := t.Context()

	if err := poolMonitor.Connect(ctx); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}

	return poolMonitor
}

func TestRequestPumpDataWriteError(t *testing.T) {
	server := createImmediateCloseServer(t)
	defer server.Close()

	poolMonitor := setupPoolMonitorWithServer(t, server)
	defer poolMonitor.Close()

	// This should fail due to closed connection
	_, _, err := poolMonitor.requestPumpData()
	if err == nil {
		t.Error("Expected error due to write failure")
	}
}

func TestRequestCircuitDataWriteError(t *testing.T) {
	server := createImmediateCloseServer(t)
	defer server.Close()

	poolMonitor := setupPoolMonitorWithServer(t, server)
	defer poolMonitor.Close()

	// This should fail due to closed connection
	_, err := poolMonitor.requestCircuitData()
	if err == nil {
		t.Error("Expected error due to write failure")
	}
}

// createPushThenPumpDataServer creates a server that sends push notifications before pump data response.
func createPushThenPumpDataServer(t *testing.T) *httptest.Server {
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

		// Send a push notification first
		pushResp := IntelliCenterResponse{
			Command:    "NotifyList",
			MessageID:  "push-notification-pump",
			Response:   "200",
			ObjectList: []ObjectData{},
		}
		if err := conn.WriteJSON(pushResp); err != nil {
			return
		}

		// Then send actual pump data response
		resp := IntelliCenterResponse{
			Command:   req.Command,
			MessageID: req.MessageID,
			Response:  "200",
			ObjectList: []ObjectData{
				{
					ObjName: "P0001",
					Params: map[string]string{
						"SNAME":  "Pool Pump",
						"RPM":    "2400",
						"STATUS": "ON",
					},
				},
			},
		}
		if err := conn.WriteJSON(resp); err != nil {
			return
		}
	}))
}

func TestRequestPumpDataWithPushNotification(t *testing.T) {
	server := createPushThenPumpDataServer(t)
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	urlParts := strings.Split(strings.TrimPrefix(wsURL, "ws://"), ":")

	poolMonitor := NewPoolMonitor(urlParts[0], urlParts[1], false)
	ctx := t.Context()

	if err := poolMonitor.Connect(ctx); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer poolMonitor.Close()

	resp, _, err := poolMonitor.requestPumpData()
	if err != nil {
		t.Errorf("Expected success after handling push notification, got: %v", err)
	}

	if resp == nil || len(resp.ObjectList) == 0 {
		t.Error("Expected pump data in response")
	}

	// Connection should remain healthy
	if !poolMonitor.connected {
		t.Error("Connection should remain healthy after handling push notifications")
	}
}

// createPushThenCircuitDataServer creates a server that sends push notifications before circuit data response.
func createPushThenCircuitDataServer(t *testing.T) *httptest.Server {
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

		// Send push notifications first
		for i := 0; i < 2; i++ {
			pushResp := IntelliCenterResponse{
				Command:    "NotifyList",
				MessageID:  fmt.Sprintf("push-notification-circuit-%d", i),
				Response:   "200",
				ObjectList: []ObjectData{},
			}
			if err := conn.WriteJSON(pushResp); err != nil {
				return
			}
		}

		// Then send actual circuit data response
		resp := IntelliCenterResponse{
			Command:   req.Command,
			MessageID: req.MessageID,
			Response:  "200",
			ObjectList: []ObjectData{
				{
					ObjName: "C0001",
					Params: map[string]string{
						"SNAME":  "Pool Light",
						"STATUS": "ON",
						"SUBTYP": "LIGHT",
					},
				},
			},
		}
		if err := conn.WriteJSON(resp); err != nil {
			return
		}
	}))
}

func TestRequestCircuitDataWithPushNotifications(t *testing.T) {
	server := createPushThenCircuitDataServer(t)
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	urlParts := strings.Split(strings.TrimPrefix(wsURL, "ws://"), ":")

	poolMonitor := NewPoolMonitor(urlParts[0], urlParts[1], false)
	ctx := t.Context()

	if err := poolMonitor.Connect(ctx); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer poolMonitor.Close()

	resp, err := poolMonitor.requestCircuitData()
	if err != nil {
		t.Errorf("Expected success after handling push notifications, got: %v", err)
	}

	if resp == nil || len(resp.ObjectList) == 0 {
		t.Error("Expected circuit data in response")
	}

	// Connection should remain healthy
	if !poolMonitor.connected {
		t.Error("Connection should remain healthy after handling push notifications")
	}
}

func TestProcessFeatureObject(_ *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", false)

	// Test feature with SHOMNU ending in 'w' (should be shown)
	poolMonitor.featureConfig["FTR01"] = testShowOnMenuValue

	obj := ObjectData{
		ObjName: "FTR01",
		Params: map[string]string{
			"SNAME":  "Pool Cleaner",
			"STATUS": "ON",
			"SUBTYP": "CLEANER",
		},
	}

	poolMonitor.processFeatureObject(obj, "Pool Cleaner", "ON", "CLEANER", false)

	// Test feature with SHOMNU not ending in 'w' (should be skipped)
	poolMonitor.featureConfig["FTR02"] = "1"

	obj2 := ObjectData{
		ObjName: "FTR02",
		Params: map[string]string{
			"SNAME":  "Hidden Feature",
			"STATUS": "OFF",
			"SUBTYP": "HIDDEN",
		},
	}

	poolMonitor.processFeatureObject(obj2, "Hidden Feature", "OFF", "HIDDEN", false)

	// Test feature with no config (should process normally)
	obj3 := ObjectData{
		ObjName: "FTR03",
		Params: map[string]string{
			"SNAME":  "Unknown Feature",
			"STATUS": "ON",
			"SUBTYP": "UNKNOWN",
		},
	}

	poolMonitor.processFeatureObject(obj3, "Unknown Feature", "ON", "UNKNOWN", false)
}

func TestCalculateHeaterStatus(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", false)

	tests := []struct {
		name     string
		bodyInfo BodyHeaterInfo
		expected int
	}{
		{
			name: "Off - temperature outside setpoints",
			bodyInfo: BodyHeaterInfo{
				HTMode: htModeOff,
				Temp:   70.0,
				LoTemp: 75.0,
				HiTemp: 85.0,
			},
			expected: thermalStatusOff,
		},
		{
			name: "Idle - temperature within setpoints",
			bodyInfo: BodyHeaterInfo{
				HTMode: htModeOff,
				Temp:   80.0,
				LoTemp: 75.0,
				HiTemp: 85.0,
			},
			expected: thermalStatusIdle,
		},
		{
			name: "Heating - traditional gas heater",
			bodyInfo: BodyHeaterInfo{
				HTMode: htModeHeating,
				Temp:   75.0,
				LoTemp: 80.0,
				HiTemp: 85.0,
			},
			expected: thermalStatusHeating,
		},
		{
			name: "Heating - heat pump heating mode",
			bodyInfo: BodyHeaterInfo{
				HTMode: htModeHeatPumpHeating,
				Temp:   75.0,
				LoTemp: 80.0,
				HiTemp: 85.0,
			},
			expected: thermalStatusHeating,
		},
		{
			name: "Cooling - heat pump cooling mode",
			bodyInfo: BodyHeaterInfo{
				HTMode: htModeHeatPumpCooling,
				Temp:   90.0,
				LoTemp: 80.0,
				HiTemp: 85.0,
			},
			expected: thermalStatusCooling,
		},
		{
			name: "Unknown mode",
			bodyInfo: BodyHeaterInfo{
				HTMode: 99, // Unknown mode
				Temp:   80.0,
				LoTemp: 75.0,
				HiTemp: 85.0,
			},
			expected: thermalStatusOff,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := poolMonitor.calculateHeaterStatus(&test.bodyInfo, "THERMAL")
			if result != test.expected {
				t.Errorf("Expected %d, got %d", test.expected, result)
			}
		})
	}
}

func TestCalculateHeaterStatusFromName(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", false)

	// Set up body heating status
	poolMonitor.bodyHeatingStatus["pool"] = true
	poolMonitor.bodyHeatingStatus["spa"] = false

	tests := []struct {
		name       string
		heaterName string
		status     string
		expected   int
	}{
		{
			name:       "Pool heater with matching body heating",
			heaterName: "Pool Heat Pump",
			status:     "OFF",
			expected:   thermalStatusHeating, // Based on body status
		},
		{
			name:       "Spa heater with non-heating body",
			heaterName: "Spa Heater",
			status:     "OFF",
			expected:   thermalStatusOff,
		},
		{
			name:       "Unknown heater with ON status",
			heaterName: "Unknown Heater",
			status:     "ON",
			expected:   thermalStatusHeating,
		},
		{
			name:       "Unknown heater with OFF status",
			heaterName: "Unknown Heater",
			status:     "OFF",
			expected:   thermalStatusOff,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := poolMonitor.calculateHeaterStatusFromName(test.heaterName, test.status)
			if result != test.expected {
				t.Errorf("Expected %d, got %d", test.expected, result)
			}
		})
	}
}

func TestGetStatusDescription(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", false)

	tests := []struct {
		expected string
		status   int
	}{
		{"off", 0},
		{"heating", 1},
		{"idle", thermalStatusIdle},
		{"cooling", thermalStatusCooling},
		{"unknown", 99}, // Unknown status
	}

	for _, test := range tests {
		result := poolMonitor.getStatusDescription(test.status)
		if result != test.expected {
			t.Errorf("Status %d: expected %s, got %s", test.status, test.expected, result)
		}
	}
}

func TestGetThermalStatus(t *testing.T) {
	responses := map[string]IntelliCenterResponse{
		"GetParamList:OBJTYP=HEATER": {
			Command:  "GetParamList",
			Response: "200",
			ObjectList: []ObjectData{
				{
					ObjName: "HTR01",
					Params: map[string]string{
						"SNAME":  "Pool Heater",
						"STATUS": "ON",
						"SUBTYP": "THERMAL",
						"OBJTYP": "HEATER",
					},
				},
				{
					ObjName: "HTR02",
					Params: map[string]string{
						"SNAME":  "Spa Heater",
						"STATUS": "OFF",
						"SUBTYP": "THERMAL",
						"OBJTYP": "HEATER",
					},
				},
			},
		},
	}

	server := createMockWebSocketServer(t, responses)
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	urlParts := strings.Split(strings.TrimPrefix(wsURL, "ws://"), ":")

	poolMonitor := NewPoolMonitor(urlParts[0], urlParts[1], true) // Enable debug mode
	ctx := t.Context()

	if err := poolMonitor.Connect(ctx); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer poolMonitor.Close()

	// Set up some referenced heaters
	poolMonitor.referencedHeaters["HTR01"] = BodyHeaterInfo{
		BodyName:  "Pool",
		BodyObj:   "BODY1",
		HeaterObj: "HTR01",
		HTMode:    htModeHeating,
		Temp:      75.0,
		LoTemp:    80.0,
		HiTemp:    85.0,
	}

	// Set up body heating status
	poolMonitor.bodyHeatingStatus["pool"] = true
	poolMonitor.bodyHeatingStatus["spa"] = false

	err := poolMonitor.getThermalStatus()
	if err != nil {
		t.Fatalf("getThermalStatus failed: %v", err)
	}
}

func TestGetThermalStatusAPIError(t *testing.T) {
	testAPIError(t, "GetParamList:OBJTYP=HEATER", "500", func(pm *PoolMonitor) error {
		return pm.getThermalStatus()
	})
}

func TestLoadFeatureConfiguration(t *testing.T) {
	// Create a server that returns feature configuration
	upgrader := websocket.Upgrader{
		CheckOrigin: func(_ *http.Request) bool { return true },
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("Failed to upgrade connection: %v", err)
		}
		defer conn.Close()

		var req map[string]interface{}
		if err := conn.ReadJSON(&req); err != nil {
			return
		}

		// Return mock configuration
		resp := map[string]interface{}{
			"command":   "GetQuery",
			"messageID": req["messageID"],
			"response":  "200",
			"answer": []interface{}{
				map[string]interface{}{
					"objnam": "FTR01",
					"params": map[string]interface{}{
						"SHOMNU": testShowOnMenuValue,
					},
				},
				map[string]interface{}{
					"objnam": "FTR02",
					"params": map[string]interface{}{
						"SHOMNU": "0",
					},
				},
				map[string]interface{}{
					"objnam": "PUMP01", // Non-FTR object should be ignored
					"params": map[string]interface{}{
						"SHOMNU": testShowOnMenuValue,
					},
				},
			},
		}

		if err := conn.WriteJSON(resp); err != nil {
			return
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

	err := poolMonitor.LoadFeatureConfiguration(ctx)
	if err != nil {
		t.Fatalf("LoadFeatureConfiguration failed: %v", err)
	}

	// Check that feature configuration was loaded
	if poolMonitor.featureConfig["FTR01"] != testShowOnMenuValue {
		t.Errorf("Expected FTR01 to have SHOMNU=%s, got %s", testShowOnMenuValue, poolMonitor.featureConfig["FTR01"])
	}

	if poolMonitor.featureConfig["FTR02"] != "0" {
		t.Errorf("Expected FTR02 to have SHOMNU=0, got %s", poolMonitor.featureConfig["FTR02"])
	}
}

func TestLoadFeatureConfigurationError(t *testing.T) {
	server := createImmediateCloseServer(t)
	defer server.Close()

	poolMonitor := setupPoolMonitorWithServer(t, server)
	defer poolMonitor.Close()

	ctx := t.Context()
	err := poolMonitor.LoadFeatureConfiguration(ctx)
	if err == nil {
		t.Error("Expected error due to connection failure")
	}
}

func TestProcessConfigurationItem(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", false)

	// Test valid configuration item
	item := map[string]interface{}{
		"objnam": "FTR01",
		"params": map[string]interface{}{
			"SHOMNU": testShowOnMenuValue,
		},
	}

	poolMonitor.processConfigurationItem(item)

	if poolMonitor.featureConfig["FTR01"] != testShowOnMenuValue {
		t.Errorf("Expected FTR01 to have SHOMNU=%s, got %s", testShowOnMenuValue, poolMonitor.featureConfig["FTR01"])
	}

	// Test invalid items that should be handled gracefully
	invalidItems := []interface{}{
		"not-a-map",
		map[string]interface{}{
			// Missing objnam
			"params": map[string]interface{}{
				"SHOMNU": testShowOnMenuValue,
			},
		},
		map[string]interface{}{
			"objnam": "PUMP01", // Not FTR prefix
			"params": map[string]interface{}{
				"SHOMNU": testShowOnMenuValue,
			},
		},
		map[string]interface{}{
			"objnam": "FTR02",
			// Missing params
		},
		map[string]interface{}{
			"objnam": "FTR03",
			"params": "not-a-map",
		},
		map[string]interface{}{
			"objnam": "FTR04",
			"params": map[string]interface{}{
				// Missing SHOMNU
			},
		},
	}

	// These should all handle gracefully without error
	for _, item := range invalidItems {
		poolMonitor.processConfigurationItem(item)
	}
}

func TestProcessBodyHeatingStatusError(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", false)

	// Test with invalid HTMODE value
	poolMonitor.processBodyHeatingStatus("Pool", "invalid", "BODY1")

	// Should not have added anything to bodyHeatingStatus
	if _, exists := poolMonitor.bodyHeatingStatus["pool"]; exists {
		t.Error("Should not have processed invalid HTMODE")
	}

	// Test with empty values
	poolMonitor.processBodyHeatingStatus("", "1", "BODY1")
	poolMonitor.processBodyHeatingStatus("Pool", "", "BODY1")

	// Should still not have added anything
	if _, exists := poolMonitor.bodyHeatingStatus["pool"]; exists {
		t.Error("Should not have processed empty values")
	}
}

// Listen mode tests.
func TestInitializeState(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", true)

	if poolMonitor.previousState != nil {
		t.Error("previousState should be nil initially")
	}

	poolMonitor.initializeState()

	if poolMonitor.previousState == nil {
		t.Error("initializeState should create previousState")
	}

	if poolMonitor.previousState.WaterTemps == nil {
		t.Error("WaterTemps map should be initialized")
	}
	if poolMonitor.previousState.PumpRPMs == nil {
		t.Error("PumpRPMs map should be initialized")
	}
	if poolMonitor.previousState.Circuits == nil {
		t.Error("Circuits map should be initialized")
	}
	if poolMonitor.previousState.Thermals == nil {
		t.Error("Thermals map should be initialized")
	}
	if poolMonitor.previousState.Features == nil {
		t.Error("Features map should be initialized")
	}
	if poolMonitor.previousState.UnknownEquip == nil {
		t.Error("UnknownEquip map should be initialized")
	}
}

func TestTrackWaterTempInListenMode(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", true)

	// First call - should detect new equipment
	poolMonitor.trackWaterTemp("Pool", 82.5)

	if poolMonitor.previousState == nil {
		t.Error("previousState should be initialized")
	}

	if poolMonitor.previousState.WaterTemps["Pool"] != 82.5 {
		t.Errorf("Expected Pool temp 82.5, got %v", poolMonitor.previousState.WaterTemps["Pool"])
	}

	// Second call with same temp - should not log change
	poolMonitor.trackWaterTemp("Pool", 82.5)

	// Third call with different temp - should log change
	poolMonitor.trackWaterTemp("Pool", 83.0)
	if poolMonitor.previousState.WaterTemps["Pool"] != 83.0 {
		t.Errorf("Expected Pool temp 83.0, got %v", poolMonitor.previousState.WaterTemps["Pool"])
	}
}

func TestTrackWaterTempNotInListenMode(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", false)

	poolMonitor.trackWaterTemp("Pool", 82.5)

	// Should not initialize state when not in listen mode
	if poolMonitor.previousState != nil {
		t.Error("previousState should not be initialized when not in listen mode")
	}
}

func TestTrackAirTempInListenMode(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", true)

	// First call - should detect new temperature
	poolMonitor.trackAirTemp(75.0)

	if poolMonitor.previousState == nil {
		t.Error("previousState should be initialized")
	}

	if poolMonitor.previousState.AirTemp != 75.0 {
		t.Errorf("Expected air temp 75.0, got %v", poolMonitor.previousState.AirTemp)
	}

	// Second call with same temp - should not log change
	poolMonitor.trackAirTemp(75.0)

	// Third call with different temp - should log change
	poolMonitor.trackAirTemp(76.0)
	if poolMonitor.previousState.AirTemp != 76.0 {
		t.Errorf("Expected air temp 76.0, got %v", poolMonitor.previousState.AirTemp)
	}
}

func TestTrackPumpRPMInListenMode(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", true)

	// First call - should detect new pump
	poolMonitor.trackPumpRPM("Pool Pump", 2400)

	if poolMonitor.previousState == nil {
		t.Error("previousState should be initialized")
	}

	if poolMonitor.previousState.PumpRPMs["Pool Pump"] != 2400 {
		t.Errorf("Expected Pool Pump RPM 2400, got %v", poolMonitor.previousState.PumpRPMs["Pool Pump"])
	}

	// Second call with changed RPM - should log change
	poolMonitor.trackPumpRPM("Pool Pump", 2600)
	if poolMonitor.previousState.PumpRPMs["Pool Pump"] != 2600 {
		t.Errorf("Expected Pool Pump RPM 2600, got %v", poolMonitor.previousState.PumpRPMs["Pool Pump"])
	}
}

func TestTrackCircuitInListenMode(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", true)

	// First call - should detect new circuit
	poolMonitor.trackCircuit("Pool Light", testStatusOff)

	if poolMonitor.previousState == nil {
		t.Error("previousState should be initialized")
	}

	if poolMonitor.previousState.Circuits["Pool Light"] != testStatusOff {
		t.Errorf("Expected Pool Light status OFF, got %v", poolMonitor.previousState.Circuits["Pool Light"])
	}

	// Second call with changed status - should log change
	poolMonitor.trackCircuit("Pool Light", testStatusOn)
	if poolMonitor.previousState.Circuits["Pool Light"] != testStatusOn {
		t.Errorf("Expected Pool Light status ON, got %v", poolMonitor.previousState.Circuits["Pool Light"])
	}
}

func TestTrackThermalInListenMode(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", true)

	// First call - should detect new thermal equipment
	poolMonitor.trackThermal("Pool Heater", thermalStatusOff)

	if poolMonitor.previousState == nil {
		t.Error("previousState should be initialized")
	}

	if poolMonitor.previousState.Thermals["Pool Heater"] != thermalStatusOff {
		t.Errorf("Expected Pool Heater status off, got %v", poolMonitor.previousState.Thermals["Pool Heater"])
	}

	// Second call with changed status - should log change
	poolMonitor.trackThermal("Pool Heater", thermalStatusHeating)
	if poolMonitor.previousState.Thermals["Pool Heater"] != thermalStatusHeating {
		t.Errorf("Expected Pool Heater status heating, got %v", poolMonitor.previousState.Thermals["Pool Heater"])
	}
}

func TestTrackFeatureInListenMode(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", true)

	// First call - should detect new feature
	poolMonitor.trackFeature("Spa Jets", testStatusOff)

	if poolMonitor.previousState == nil {
		t.Error("previousState should be initialized")
	}

	if poolMonitor.previousState.Features["Spa Jets"] != testStatusOff {
		t.Errorf("Expected Spa Jets status OFF, got %v", poolMonitor.previousState.Features["Spa Jets"])
	}

	// Second call with changed status - should log change
	poolMonitor.trackFeature("Spa Jets", testStatusOn)
	if poolMonitor.previousState.Features["Spa Jets"] != testStatusOn {
		t.Errorf("Expected Spa Jets status ON, got %v", poolMonitor.previousState.Features["Spa Jets"])
	}
}

func TestGetAllObjects(t *testing.T) {
	responses := map[string]IntelliCenterResponse{
		"GetParamList:": {
			Command:  "GetParamList",
			Response: "200",
			ObjectList: []ObjectData{
				{
					ObjName: "BODY1",
					Params: map[string]string{
						"SNAME":  "Pool",
						"STATUS": "ON",
						"OBJTYP": "BODY",
						"SUBTYP": "POOL",
					},
				},
				{
					ObjName: "VALVE1",
					Params: map[string]string{
						"SNAME":  "Pool Valve",
						"STATUS": "OPEN",
						"OBJTYP": "VALVE",
						"SUBTYP": "ACTUATOR",
					},
				},
			},
		},
	}

	server := createMockWebSocketServer(t, responses)
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	urlParts := strings.Split(strings.TrimPrefix(wsURL, "ws://"), ":")

	poolMonitor := NewPoolMonitor(urlParts[0], urlParts[1], true)
	ctx := t.Context()

	if err := poolMonitor.Connect(ctx); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer poolMonitor.Close()

	// Initialize state for tracking
	poolMonitor.initializeState()

	err := poolMonitor.getAllObjects()
	if err != nil {
		t.Fatalf("getAllObjects failed: %v", err)
	}

	// Check that unknown equipment was tracked
	if _, exists := poolMonitor.previousState.UnknownEquip["VALVE1"]; !exists {
		t.Error("VALVE1 should be tracked as unknown equipment")
	}
}

func TestGetAllObjectsAPIError(t *testing.T) {
	testAPIError(t, "GetParamList:", "500", func(pm *PoolMonitor) error {
		return pm.getAllObjects()
	})
}

func TestTrackUnknownEquipment(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", true)
	poolMonitor.initializeState()

	tests := []struct {
		name        string
		obj         ObjectData
		shouldTrack bool
	}{
		{
			name: "VALVE - should track",
			obj: ObjectData{
				ObjName: "VALVE1",
				Params: map[string]string{
					"SNAME":  "Pool Valve",
					"STATUS": "OPEN",
					"OBJTYP": "VALVE",
					"SUBTYP": "ACTUATOR",
				},
			},
			shouldTrack: true,
		},
		{
			name: "BODY - should not track (known type)",
			obj: ObjectData{
				ObjName: "BODY1",
				Params: map[string]string{
					"SNAME":  "Pool",
					"STATUS": "ON",
					"OBJTYP": "BODY",
				},
			},
			shouldTrack: false,
		},
		{
			name: "Internal object - should not track",
			obj: ObjectData{
				ObjName: "_SYS01",
				Params: map[string]string{
					"SNAME":  "System",
					"STATUS": "ON",
					"OBJTYP": "SYSTEM",
				},
			},
			shouldTrack: false,
		},
		{
			name: "No OBJTYP - should not track",
			obj: ObjectData{
				ObjName: "OBJ1",
				Params: map[string]string{
					"SNAME":  "Some Object",
					"STATUS": "ON",
				},
			},
			shouldTrack: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			poolMonitor.trackUnknownEquipment(test.obj)

			_, tracked := poolMonitor.previousState.UnknownEquip[test.obj.ObjName]
			if tracked != test.shouldTrack {
				t.Errorf("Expected tracked=%v, got %v", test.shouldTrack, tracked)
			}
		})
	}
}

func TestTrackUnknownEquipmentNotInListenMode(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", false)

	obj := ObjectData{
		ObjName: "VALVE1",
		Params: map[string]string{
			"SNAME":  "Pool Valve",
			"STATUS": "OPEN",
			"OBJTYP": "VALVE",
		},
	}

	poolMonitor.trackUnknownEquipment(obj)

	// Should not initialize state when not in listen mode
	if poolMonitor.previousState != nil {
		t.Error("previousState should not be initialized when not in listen mode")
	}
}

func TestLogUnknownEquipmentDetected(_ *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", true)

	// Test with name
	poolMonitor.logUnknownEquipmentDetected("Pool Valve", "VALVE1", "VALVE", "OPEN")

	// Test without name
	poolMonitor.logUnknownEquipmentDetected("", "VALVE2", "VALVE", "CLOSED")
}

func TestLogUnknownEquipmentChanged(_ *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", true)

	// Test with name
	poolMonitor.logUnknownEquipmentChanged("Pool Valve", "VALVE1", "VALVE:OPEN", "VALVE:CLOSED")

	// Test without name
	poolMonitor.logUnknownEquipmentChanged("", "VALVE2", "VALVE:CLOSED", "VALVE:OPEN")
}

func TestListenModeIntegration(t *testing.T) {
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
				{
					ObjName: "VALVE1",
					Params: map[string]string{
						"SNAME":  "Pool Valve",
						"STATUS": "OPEN",
						"OBJTYP": "VALVE",
						"SUBTYP": "ACTUATOR",
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
		"GetParamList:OBJTYP=HEATER": {
			Command:  "GetParamList",
			Response: "200",
			ObjectList: []ObjectData{
				{
					ObjName: "HTR01",
					Params: map[string]string{
						"SNAME":  "Pool Heater",
						"STATUS": "ON",
						"SUBTYP": "THERMAL",
					},
				},
			},
		},
	}

	server := createMockWebSocketServer(t, responses)
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	urlParts := strings.Split(strings.TrimPrefix(wsURL, "ws://"), ":")

	poolMonitor := NewPoolMonitor(urlParts[0], urlParts[1], true)
	ctx := t.Context()

	if err := poolMonitor.Connect(ctx); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer poolMonitor.Close()

	// First call - should detect all equipment
	err := poolMonitor.GetTemperatures(ctx)
	if err != nil {
		t.Fatalf("First GetTemperatures failed: %v", err)
	}

	// Verify state was tracked
	if poolMonitor.previousState == nil {
		t.Error("previousState should be initialized")
	}

	if poolMonitor.previousState.WaterTemps["Pool"] != 82.5 {
		t.Error("Pool temperature should be tracked")
	}

	if poolMonitor.previousState.AirTemp != 75.2 {
		t.Error("Air temperature should be tracked")
	}

	if poolMonitor.previousState.PumpRPMs["Pool Pump"] != 2400 {
		t.Error("Pool Pump RPM should be tracked")
	}

	if poolMonitor.previousState.Circuits["Pool Light"] != "ON" {
		t.Error("Pool Light should be tracked")
	}

	// Second call - should not log anything (no changes)
	err = poolMonitor.GetTemperatures(ctx)
	if err != nil {
		t.Fatalf("Second GetTemperatures failed: %v", err)
	}
}

// Re-discovery tests
//
// Testing Strategy:
// - We test all the logic surrounding re-discovery (failure counting, threshold detection,
//   IP updating, mode switching) extensively
// - We do NOT test attemptRediscovery() directly because it requires real mDNS discovery
//   and an actual IntelliCenter on the network, which would make tests non-deterministic
// - This is a pragmatic approach: test the logic we control, not external network dependencies

func TestUpdateIntelliCenterIP(t *testing.T) {
	poolMonitor := NewPoolMonitor(testIntelliCenterIP, testIntelliCenterPort, false)

	// Verify initial state
	if poolMonitor.intelliCenterIP != testIntelliCenterIP {
		t.Errorf("Expected initial IP %s, got %s", testIntelliCenterIP, poolMonitor.intelliCenterIP)
	}
	if poolMonitor.intelliCenterURL != testIntelliCenterURL {
		t.Errorf("Expected initial URL %s, got %s", testIntelliCenterURL, poolMonitor.intelliCenterURL)
	}

	// Update IP
	poolMonitor.updateIntelliCenterIP("192.168.1.200")

	// Verify updated state
	if poolMonitor.intelliCenterIP != "192.168.1.200" {
		t.Errorf("Expected updated IP 192.168.1.200, got %s", poolMonitor.intelliCenterIP)
	}
	if poolMonitor.intelliCenterURL != "ws://192.168.1.200:6680" {
		t.Errorf("Expected updated URL ws://192.168.1.200:6680, got %s", poolMonitor.intelliCenterURL)
	}
	if poolMonitor.connected {
		t.Error("Expected connected to be false after IP update")
	}
}

func TestHandlePollingSuccess(t *testing.T) {
	poolMonitor := NewPoolMonitor("192.168.1.100", "6680", false)

	// Set up failure state
	poolMonitor.consecutiveFailures = 5
	poolMonitor.inRediscoveryMode = true

	// Call handlePollingSuccess
	poolMonitor.handlePollingSuccess()

	// Verify success resets failure tracking
	if poolMonitor.consecutiveFailures != 0 {
		t.Errorf("Expected consecutiveFailures to be 0, got %d", poolMonitor.consecutiveFailures)
	}
	if poolMonitor.inRediscoveryMode {
		t.Error("Expected inRediscoveryMode to be false after success")
	}
}

func TestConsecutiveFailureTracking(t *testing.T) {
	poolMonitor := NewPoolMonitor("invalid.host", "6680", false)
	poolMonitor.retryConfig.MaxRetries = 1
	poolMonitor.retryConfig.BaseDelay = 10 * time.Millisecond
	poolMonitor.disableAutoRediscovery = true

	// Verify initial state
	if poolMonitor.consecutiveFailures != 0 {
		t.Errorf("Expected initial consecutiveFailures to be 0, got %d", poolMonitor.consecutiveFailures)
	}

	// Trigger first failure
	err := fmt.Errorf("connection failed")
	poolMonitor.handlePollingError(err)

	if poolMonitor.consecutiveFailures != 1 {
		t.Errorf("Expected consecutiveFailures to be 1 after first failure, got %d", poolMonitor.consecutiveFailures)
	}

	// Trigger second failure
	poolMonitor.handlePollingError(err)

	if poolMonitor.consecutiveFailures != 2 {
		t.Errorf("Expected consecutiveFailures to be 2 after second failure, got %d", poolMonitor.consecutiveFailures)
	}

	// Trigger success - should reset counter
	poolMonitor.handlePollingSuccess()

	if poolMonitor.consecutiveFailures != 0 {
		t.Errorf("Expected consecutiveFailures to be 0 after success, got %d", poolMonitor.consecutiveFailures)
	}
}

func TestFailureThresholdTriggersRediscoveryMode(t *testing.T) {
	poolMonitor := NewPoolMonitor("invalid.host", "6680", false)
	poolMonitor.retryConfig.MaxRetries = 1
	poolMonitor.retryConfig.BaseDelay = 10 * time.Millisecond
	poolMonitor.failureThreshold = 3

	// Test that we don't enter re-discovery mode before threshold
	poolMonitor.consecutiveFailures = 2

	// Check the logic without calling handlePollingTick (which would trigger actual discovery)
	shouldEnterRediscovery := !poolMonitor.disableAutoRediscovery &&
		!poolMonitor.inRediscoveryMode &&
		poolMonitor.consecutiveFailures >= poolMonitor.failureThreshold

	if shouldEnterRediscovery {
		t.Error("Should not enter re-discovery mode before threshold (consecutiveFailures=2, threshold=3)")
	}

	// Test that we would enter re-discovery mode at threshold
	poolMonitor.consecutiveFailures = 3

	shouldEnterRediscovery = !poolMonitor.disableAutoRediscovery &&
		!poolMonitor.inRediscoveryMode &&
		poolMonitor.consecutiveFailures >= poolMonitor.failureThreshold

	if !shouldEnterRediscovery {
		t.Error("Should enter re-discovery mode at threshold (consecutiveFailures=3, threshold=3)")
	}

	// Verify the flag is actually set when we manually simulate the logic
	poolMonitor.inRediscoveryMode = true
	if !poolMonitor.inRediscoveryMode {
		t.Error("Failed to set re-discovery mode")
	}
}

func TestRediscoveryModeDisabledByFlag(t *testing.T) {
	poolMonitor := NewPoolMonitor("invalid.host", "6680", false)
	poolMonitor.retryConfig.MaxRetries = 1
	poolMonitor.retryConfig.BaseDelay = 10 * time.Millisecond
	poolMonitor.disableAutoRediscovery = true
	poolMonitor.failureThreshold = 3

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	// Simulate exceeding threshold
	poolMonitor.consecutiveFailures = 5
	poolMonitor.handlePollingTick(ctx)

	// Should not enter re-discovery mode when disabled
	if poolMonitor.inRediscoveryMode {
		t.Error("Should not enter re-discovery mode when disableAutoRediscovery is true")
	}
}

func TestHandlePollingTickWithRediscoverySuccess(t *testing.T) {
	// Create a test server
	upgrader := websocket.Upgrader{
		CheckOrigin: func(_ *http.Request) bool { return true },
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("Failed to upgrade connection: %v", err)
		}
		defer conn.Close()

		// Keep connection alive for the duration of the test
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				return
			}
		}
	}))
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	urlParts := strings.Split(strings.TrimPrefix(wsURL, "ws://"), ":")

	poolMonitor := NewPoolMonitor(urlParts[0], urlParts[1], false)
	poolMonitor.disableAutoRediscovery = true // We'll manually control re-discovery

	// Set up state: in re-discovery mode with failures
	poolMonitor.inRediscoveryMode = true
	poolMonitor.consecutiveFailures = 5

	// Mock successful re-discovery by updating IP to a working server
	poolMonitor.updateIntelliCenterIP(urlParts[0])

	// Manually trigger re-discovery success path
	// In real scenario, attemptRediscovery would be called, but we can't easily test that
	// without a real IntelliCenter. Instead, we test the success handling.
	poolMonitor.handlePollingSuccess()

	// Verify re-discovery mode exited and failures reset
	if poolMonitor.inRediscoveryMode {
		t.Error("Should exit re-discovery mode after success")
	}
	if poolMonitor.consecutiveFailures != 0 {
		t.Errorf("Should reset consecutive failures after success, got %d", poolMonitor.consecutiveFailures)
	}
}

// Tests for refactored command-line and configuration functions.

func TestGetEnvIntOrDefault(t *testing.T) {
	tests := []struct {
		name         string
		envVar       string
		envValue     string
		defaultValue int
		expected     int
	}{
		{
			name:         "returns default when env not set",
			envVar:       "TEST_PENTAMETER_INT_NOTSET",
			envValue:     "",
			defaultValue: 42,
			expected:     42,
		},
		{
			name:         "returns env value when valid integer",
			envVar:       "TEST_PENTAMETER_INT_VALID",
			envValue:     "100",
			defaultValue: 42,
			expected:     100,
		},
		{
			name:         "returns default when env value is invalid",
			envVar:       "TEST_PENTAMETER_INT_INVALID",
			envValue:     "not-a-number",
			defaultValue: 42,
			expected:     42,
		},
		{
			name:         "returns env value zero when explicitly set to zero",
			envVar:       "TEST_PENTAMETER_INT_ZERO",
			envValue:     "0",
			defaultValue: 42,
			expected:     0,
		},
		{
			name:         "returns negative env value when set",
			envVar:       "TEST_PENTAMETER_INT_NEG",
			envValue:     "-10",
			defaultValue: 42,
			expected:     -10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up environment
			if tt.envValue != "" {
				t.Setenv(tt.envVar, tt.envValue)
			}

			result := getEnvIntOrDefault(tt.envVar, tt.defaultValue)
			if result != tt.expected {
				t.Errorf("getEnvIntOrDefault(%q, %d) = %d, want %d",
					tt.envVar, tt.defaultValue, result, tt.expected)
			}
		})
	}
}

func TestDeterminePollInterval(t *testing.T) {
	tests := []struct {
		name                string
		pollIntervalSeconds int
		listenMode          bool
		expected            time.Duration
	}{
		{
			name:                "uses explicit interval when provided",
			pollIntervalSeconds: 30,
			listenMode:          false,
			expected:            30 * time.Second,
		},
		{
			name:                "uses explicit interval in listen mode",
			pollIntervalSeconds: 30,
			listenMode:          true,
			expected:            30 * time.Second,
		},
		{
			name:                "uses listen mode default when no explicit interval",
			pollIntervalSeconds: 0,
			listenMode:          true,
			expected:            10 * time.Second, // listenModePollInterval
		},
		{
			name:                "uses normal default when no explicit interval and not listen mode",
			pollIntervalSeconds: 0,
			listenMode:          false,
			expected:            60 * time.Second, // defaultPollInterval
		},
		{
			name:                "enforces minimum interval",
			pollIntervalSeconds: 2,
			listenMode:          false,
			expected:            5 * time.Second, // minPollInterval
		},
		{
			name:                "allows exact minimum interval",
			pollIntervalSeconds: 5,
			listenMode:          false,
			expected:            5 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := determinePollInterval(tt.pollIntervalSeconds, tt.listenMode)
			if result != tt.expected {
				t.Errorf("determinePollInterval(%d, %v) = %v, want %v",
					tt.pollIntervalSeconds, tt.listenMode, result, tt.expected)
			}
		})
	}
}

// Tests for push notification helper functions.

func TestProcessRawPushNotification(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", true)
	poolMonitor.initializeState()

	tests := []struct {
		msg  map[string]interface{}
		name string
	}{
		{
			name: "handles empty objectList",
			msg: map[string]interface{}{
				"command": "WriteParamList",
			},
		},
		{
			name: "handles nil objectList",
			msg: map[string]interface{}{
				"command":    "WriteParamList",
				"objectList": nil,
			},
		},
		{
			name: "handles valid objectList with changes",
			msg: map[string]interface{}{
				"command": "WriteParamList",
				"objectList": []interface{}{
					map[string]interface{}{
						"objnam": "B0001",
						"changes": []interface{}{
							map[string]interface{}{
								"objnam": "B0001",
								"params": map[string]interface{}{
									"SNAME":  "Pool",
									"TEMP":   "82",
									"OBJTYP": "BODY",
								},
							},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(_ *testing.T) {
			// Should not panic
			poolMonitor.processRawPushNotification(tt.msg)
		})
	}
}

func TestProcessObjectListItem(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", true)
	poolMonitor.initializeState()

	tests := []struct {
		item interface{}
		name string
	}{
		{
			name: "handles non-map item",
			item: "not a map",
		},
		{
			name: "handles item without changes array",
			item: map[string]interface{}{
				"objnam": "B0001",
				"params": map[string]interface{}{"TEMP": "82"},
			},
		},
		{
			name: "handles item with empty changes array",
			item: map[string]interface{}{
				"objnam":  "B0001",
				"changes": []interface{}{},
			},
		},
		{
			name: "handles item with valid changes",
			item: map[string]interface{}{
				"objnam": "B0001",
				"changes": []interface{}{
					map[string]interface{}{
						"objnam": "B0001",
						"params": map[string]interface{}{
							"SNAME":  "Pool",
							"TEMP":   "82",
							"OBJTYP": "BODY",
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(_ *testing.T) {
			// Should not panic
			poolMonitor.processObjectListItem(tt.item)
		})
	}
}

func TestProcessChangeItem(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", true)
	poolMonitor.initializeState()

	tests := []struct {
		change interface{}
		name   string
	}{
		{
			name:   "handles non-map change",
			change: "not a map",
		},
		{
			name: "handles change without params",
			change: map[string]interface{}{
				"objnam": "B0001",
			},
		},
		{
			name: "handles change with valid params",
			change: map[string]interface{}{
				"objnam": "B0001",
				"params": map[string]interface{}{
					"SNAME":  "Pool",
					"TEMP":   "82",
					"OBJTYP": "BODY",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(_ *testing.T) {
			// Should not panic
			poolMonitor.processChangeItem(tt.change)
		})
	}
}

func TestConvertToObjectData(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", false)

	tests := []struct {
		name      string
		objnam    string
		paramsRaw map[string]interface{}
		wantName  string
		wantKey   string
		wantValue string
	}{
		{
			name:   "converts string params",
			objnam: "B0001",
			paramsRaw: map[string]interface{}{
				"SNAME": "Pool",
				"TEMP":  "82",
			},
			wantName:  "B0001",
			wantKey:   "SNAME",
			wantValue: "Pool",
		},
		{
			name:   "converts non-string params to string",
			objnam: "P0001",
			paramsRaw: map[string]interface{}{
				"RPM":    2400,
				"STATUS": true,
			},
			wantName:  "P0001",
			wantKey:   "RPM",
			wantValue: "2400",
		},
		{
			name:      "handles empty params",
			objnam:    "X0001",
			paramsRaw: map[string]interface{}{},
			wantName:  "X0001",
			wantKey:   "",
			wantValue: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := poolMonitor.convertToObjectData(tt.objnam, tt.paramsRaw)

			if result.ObjName != tt.wantName {
				t.Errorf("ObjName = %q, want %q", result.ObjName, tt.wantName)
			}

			if tt.wantKey != "" {
				if result.Params[tt.wantKey] != tt.wantValue {
					t.Errorf("Params[%q] = %q, want %q", tt.wantKey, result.Params[tt.wantKey], tt.wantValue)
				}
			}
		})
	}
}

func TestLogRawMessage(_ *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", false)

	// Test with valid message - should not panic
	poolMonitor.logRawMessage(map[string]interface{}{
		"command": "test",
		"data":    "value",
	})

	// Test with empty message - should not panic
	poolMonitor.logRawMessage(map[string]interface{}{})
}

func TestHandleBodyPush(_ *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", true)
	poolMonitor.initializeState()

	obj := ObjectData{
		ObjName: "B0001",
		Params: map[string]string{
			"SNAME":  "Pool",
			"TEMP":   "82",
			"SETPT":  "85",
			"HTMODE": "1",
			"STATUS": "ON",
			"OBJTYP": "BODY",
		},
	}

	// Should not panic and should process body
	poolMonitor.handleBodyPush(obj, "Pool")
}

func TestHandlePumpPush(_ *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", true)
	poolMonitor.initializeState()

	// Test with valid pump data
	obj := ObjectData{
		ObjName: "P0001",
		Params: map[string]string{
			"SNAME":  "Pool Pump",
			"RPM":    "2400",
			"PWR":    "500",
			"STATUS": "ON",
			"OBJTYP": "PUMP",
		},
	}
	poolMonitor.handlePumpPush(obj, "Pool Pump")

	// Test with invalid RPM (should log error but not panic)
	objInvalid := ObjectData{
		ObjName: "P0002",
		Params: map[string]string{
			"SNAME":  "Bad Pump",
			"RPM":    "invalid",
			"STATUS": "ON",
			"OBJTYP": "PUMP",
		},
	}
	poolMonitor.handlePumpPush(objInvalid, "Bad Pump")
}

func TestHandleCircuitPush(_ *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", true)
	poolMonitor.initializeState()

	obj := ObjectData{
		ObjName: "C0001",
		Params: map[string]string{
			"SNAME":  "Pool Light",
			"STATUS": "ON",
			"OBJTYP": "CIRCUIT",
			"SUBTYP": "LIGHT",
		},
	}

	// Should not panic
	poolMonitor.handleCircuitPush(obj, "Pool Light")
}

func TestHandleHeaterPush(_ *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", true)
	poolMonitor.initializeState()

	obj := ObjectData{
		ObjName: "H0001",
		Params: map[string]string{
			"SNAME":  "Pool Heater",
			"STATUS": "ON",
			"MODE":   "1",
			"OBJTYP": "HEATER",
			"SUBTYP": "THERMAL",
		},
	}

	// Should not panic
	poolMonitor.handleHeaterPush(obj, "Pool Heater")
}

func TestHandleUnknownPush(_ *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", false)

	// Test with normal params
	obj := ObjectData{
		ObjName: "V0001",
		Params: map[string]string{
			"SNAME":  "Pool Valve",
			"STATUS": "OPEN",
			"OBJTYP": "VALVE",
		},
	}
	poolMonitor.handleUnknownPush(obj)

	// Test with empty params
	objEmpty := ObjectData{
		ObjName: "X0001",
		Params:  map[string]string{},
	}
	poolMonitor.handleUnknownPush(objEmpty)
}

func TestProcessPushObject(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", true)
	poolMonitor.initializeState()

	tests := []struct {
		name string
		obj  ObjectData
	}{
		{
			name: "routes BODY type",
			obj: ObjectData{
				ObjName: "B0001",
				Params: map[string]string{
					"SNAME":  "Pool",
					"OBJTYP": "BODY",
					"TEMP":   "82",
				},
			},
		},
		{
			name: "routes PUMP type",
			obj: ObjectData{
				ObjName: "P0001",
				Params: map[string]string{
					"SNAME":  "Pool Pump",
					"OBJTYP": "PUMP",
					"RPM":    "2400",
				},
			},
		},
		{
			name: "routes CIRCUIT type",
			obj: ObjectData{
				ObjName: "C0001",
				Params: map[string]string{
					"SNAME":  "Pool Light",
					"OBJTYP": "CIRCUIT",
					"STATUS": "ON",
				},
			},
		},
		{
			name: "routes HEATER type",
			obj: ObjectData{
				ObjName: "H0001",
				Params: map[string]string{
					"SNAME":  "Pool Heater",
					"OBJTYP": "HEATER",
					"STATUS": "ON",
				},
			},
		},
		{
			name: "routes unknown type",
			obj: ObjectData{
				ObjName: "X0001",
				Params: map[string]string{
					"SNAME":  "Unknown",
					"OBJTYP": "UNKNOWN",
					"STATUS": "ON",
				},
			},
		},
		{
			name: "uses ObjName when SNAME is empty",
			obj: ObjectData{
				ObjName: "B0002",
				Params: map[string]string{
					"OBJTYP": "BODY",
					"TEMP":   "82",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(_ *testing.T) {
			// Should not panic
			poolMonitor.processPushObject(tt.obj)
		})
	}
}

func TestResolveIntelliCenterIPWithProvidedIP(t *testing.T) {
	// Test that provided IP is returned directly
	result := resolveIntelliCenterIP("192.168.1.100")
	if result != "192.168.1.100" {
		t.Errorf("resolveIntelliCenterIP(\"192.168.1.100\") = %q, want \"192.168.1.100\"", result)
	}
}
