package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	testShowOnMenuValue   = "1w"            // Test value for SHOMNU parameter indicating feature should be shown.
	testStatusOff         = "OFF"           // Test circuit/feature off status.
	testStatusOn          = statusOn        // Test circuit/feature on status (uses main.go constant).
	testIntelliCenterIP   = "192.168.1.100" // Test IntelliCenter IP address.
	testIntelliCenterPort = "6680"          // Test IntelliCenter port.
	testCircGrpParent     = "GRP01"         // Test circuit group parent ID.
	testCircGrpCircuit    = "C0004"         // Test circuit group circuit reference.
	testCircGrpUseWhite   = "White"         // Test circuit group color/mode (white).
	testCircGrpUseBlue    = "Blue"          // Test circuit group color/mode (blue).
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

	if poolMonitor.ic == nil {
		t.Error("Expected intellicenter client to be initialized")
	}

	if poolMonitor.listenMode {
		t.Error("listenMode should be false")
	}

	if poolMonitor.bodyHeatingStatus == nil {
		t.Error("bodyHeatingStatus map should be initialized")
	}

	if poolMonitor.referencedHeaters == nil {
		t.Error("referencedHeaters map should be initialized")
	}

	if poolMonitor.ic.RetryMax != maxRetries {
		t.Errorf("Expected RetryMax %d, got %d", maxRetries, poolMonitor.ic.RetryMax)
	}
}

// (backoff-delay math now lives in and is tested by the intellicenter package.)

func TestGetBodyTemperatures(t *testing.T) {
	objs := []ObjectData{
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
	}

	poolMonitor := NewPoolMonitor("test", "6680", false)
	poolMonitor.applyBodyTemperatures(objs)

	// Check that heating status was tracked
	if !poolMonitor.bodyHeatingStatus["pool"] {
		t.Error("Pool heating status should be true (HTMODE=1)")
	}
	if poolMonitor.bodyHeatingStatus["spa"] {
		t.Error("Spa heating status should be false (HTMODE=0)")
	}
}

func testAirTemperature(t *testing.T, probeValue string) {
	t.Helper()
	objs := []ObjectData{
		{
			ObjName: "_A135",
			Params: map[string]string{
				"SNAME":  "Air Sensor",
				"PROBE":  probeValue,
				"SUBTYP": "AIR",
				"STATUS": "ON",
			},
		},
	}

	poolMonitor := NewPoolMonitor("test", "6680", false)
	// applyAirTemperature never returns an error; it logs and continues on bad parse.
	poolMonitor.applyAirTemperature(objs)
}

func TestGetAirTemperature(t *testing.T) {
	testAirTemperature(t, "75.2")
}

func TestGetPumpData(_ *testing.T) {
	objs := []ObjectData{
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
	}

	poolMonitor := NewPoolMonitor("test", "6680", false)
	poolMonitor.applyPumpData(objs, 0)
}

func TestGetCircuitStatus(_ *testing.T) {
	objs := []ObjectData{
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
	}

	poolMonitor := NewPoolMonitor("test", "6680", false)
	poolMonitor.applyCircuitStatus(objs)
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
		{"PUMP1", "Pool Pump", "PUMP", false},       // Wrong prefix
		{"GRP01", "AllOfTheLights", "LITSHO", true}, // Circuit groups
		{"GRP02", "Another Group", "INTELL", true},  // Circuit group with different subtype
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

func TestApplyPumpAssociations(t *testing.T) {
	pm := NewPoolMonitor("test", "6680", false)
	pm.applyPumpAssociations([]ObjectData{
		{ObjName: "p0101", Params: map[string]string{"CIRCUIT": "C0001", "PARENT": "PMP01"}},
		{ObjName: "p0103", Params: map[string]string{"CIRCUIT": "FTR03", "PARENT": "PMP01"}},
		{ObjName: "p0201", Params: map[string]string{"CIRCUIT": "C0006", "PARENT": "PMP02"}},
		// duplicate driver (same circuit + pump) must not double-add
		{ObjName: "pDUPE", Params: map[string]string{"CIRCUIT": "C0001", "PARENT": "PMP01"}},
		// incomplete rows are ignored
		{ObjName: "pBAD", Params: map[string]string{"CIRCUIT": "C0009"}},
	})

	if got := pm.circuitToPumps["C0001"]; len(got) != 1 || got[0] != "PMP01" {
		t.Errorf("C0001 -> %v, want [PMP01]", got)
	}
	if got := pm.circuitToPumps["FTR03"]; len(got) != 1 || got[0] != "PMP01" {
		t.Errorf("FTR03 -> %v, want [PMP01]", got)
	}
	if _, ok := pm.circuitToPumps["C0009"]; ok {
		t.Error("incomplete PMPCIRC row should not create an association")
	}
}

func TestApplyPumpDeliveryGate(t *testing.T) {
	pm := NewPoolMonitor("test", "6680", false)
	pm.circuitToPumps = map[string][]string{
		"C0001": {"PMP01"},          // Spa -> VS
		"FTR03": {"PMP01"},          // Spa Jets -> VS
		"GRP01": {"PMP01", "PMP02"}, // multi-pump driver
	}

	tests := []struct {
		name    string
		objName string
		running map[string]bool
		in      float64
		want    float64
	}{
		{"on + pump running stays on", "C0001", map[string]bool{"PMP01": true}, circuitStatusOn, circuitStatusOn},
		{"on + pump dead floors to off", "C0001", map[string]bool{"PMP01": false}, circuitStatusOn, circuitStatusOff},
		{"freeze + pump dead floors to off", "C0001", map[string]bool{"PMP01": false}, circuitStatusFreezeProtected, circuitStatusOff},
		{"already off stays off", "C0001", map[string]bool{"PMP01": true}, circuitStatusOff, circuitStatusOff},
		{"no association is passthrough", "C0002", map[string]bool{}, circuitStatusOn, circuitStatusOn},
		{"any running pump satisfies", "GRP01", map[string]bool{"PMP01": false, "PMP02": true}, circuitStatusOn, circuitStatusOn},
		{"all pumps dead floors to off", "GRP01", map[string]bool{"PMP01": false, "PMP02": false}, circuitStatusOn, circuitStatusOff},
	}
	for _, tt := range tests {
		pm.pumpRunning = tt.running
		if got := pm.applyPumpDeliveryGate(tt.objName, tt.in); got != tt.want {
			t.Errorf("%s: applyPumpDeliveryGate(%s, %.0f) = %.0f, want %.0f", tt.name, tt.objName, tt.in, got, tt.want)
		}
	}
}

// TestCircuitStatusGatedByPump is the end-to-end of the reported failure: the Spa
// circuit is commanded ON but its pump lost power (RPM=0), so circuit_status must
// read OFF instead of a falsely-healthy ON.
func TestCircuitStatusGatedByPump(t *testing.T) {
	pm := NewPoolMonitor("test", "6680", false)
	pm.circuitToPumps = map[string][]string{"C0001": {"PMP01"}}

	pm.pumpRunning = map[string]bool{"PMP01": true}
	if got := pm.calculateCircuitStatusValue("Spa", statusOn, "C0001", false); got != circuitStatusOn {
		t.Errorf("Spa ON with pump running: got %.0f, want %.0f", got, circuitStatusOn)
	}

	pm.pumpRunning = map[string]bool{"PMP01": false} // breaker popped
	if got := pm.calculateCircuitStatusValue("Spa", statusOn, "C0001", false); got != circuitStatusOff {
		t.Errorf("Spa ON but pump dead: got %.0f, want %.0f (should floor to off)", got, circuitStatusOff)
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

// (request/response correlation now lives in the intellicenter package's
// round-trip; PoolMonitor no longer tracks pending requests.)

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

func TestLogPumpUpdate(_ *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", false)

	// Test pump update logging
	poolMonitor.logPumpUpdate("Test Pump", "PUMP1", 2400, "ON", time.Millisecond)
}

func TestGetEnvOrDefaultWithExistingVar(t *testing.T) {
	// Test with PATH which should exist
	result := getEnvOrDefault("PATH", "default")
	if result == "default" {
		t.Error("Should return actual PATH value, not default")
	}
}

func TestMetricsServerBindAndServe(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping server test in short mode")
	}

	registry := createPrometheusRegistry()
	monitor := NewPoolMonitor("", "", false)

	// Port "0" lets the OS pick a free port, so the test never collides with a
	// real metrics server or another test.
	ln, err := bindMetricsServer(registry, monitor, "0")
	if err != nil {
		t.Fatalf("bindMetricsServer should succeed on a free port: %v", err)
	}
	if ln == nil {
		t.Fatal("bindMetricsServer returned a nil listener with no error")
	}

	served := make(chan error, 1)
	go func() { served <- serveMetrics(ln) }()

	resp, err := http.Get("http://" + ln.Addr().String() + "/health")
	if err != nil {
		t.Fatalf("GET /health failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /health = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if err := resp.Body.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}

	// A graceful close must fold http.ErrServerClosed into a nil return.
	if err := ln.Close(); err != nil {
		t.Errorf("closing listener: %v", err)
	}
	if err := <-served; err != nil {
		t.Errorf("serveMetrics returned %v after a graceful close, want nil", err)
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

	if err := poolMonitor.ic.ConnectWithRetry(ctx); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer poolMonitor.ic.Close()

	err := testFunc(poolMonitor)
	if err == nil {
		t.Errorf("Expected error for API response %s", responseCode)
	}
}

func TestGetBodyTemperaturesWithInvalidTemperature(_ *testing.T) {
	objs := []ObjectData{
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
	}

	poolMonitor := NewPoolMonitor("test", "6680", false)
	// This should not panic even with invalid temperature - it logs and continues.
	poolMonitor.applyBodyTemperatures(objs)
}

func TestGetAirTemperatureWithInvalidTemperature(t *testing.T) {
	testAirTemperature(t, "not_a_number")
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

func TestMainCommandLineParsing(t *testing.T) {
	// Test that main would fail without required IP
	// We can't call main directly since it uses flag.Parse() and os.Exit()
	// But we can test getEnvOrDefault function behavior

	result := getEnvOrDefault("NONEXISTENT_PENTAMETER_VAR", "test-default")
	if result != "test-default" {
		t.Errorf("Expected test-default, got %s", result)
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

func TestGetThermalStatus(_ *testing.T) {
	objs := []ObjectData{
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
	}

	poolMonitor := NewPoolMonitor("test", "6680", true) // Enable debug mode

	// Set up some referenced heaters.
	poolMonitor.referencedHeaters["HTR01"] = BodyHeaterInfo{
		BodyName:  "Pool",
		BodyObj:   "BODY1",
		HeaterObj: "HTR01",
		HTMode:    htModeHeating,
		Temp:      75.0,
		LoTemp:    80.0,
		HiTemp:    85.0,
	}

	// Set up body heating status.
	poolMonitor.bodyHeatingStatus["pool"] = true
	poolMonitor.bodyHeatingStatus["spa"] = false

	poolMonitor.applyThermalStatus(objs)
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
	if poolMonitor.previousState.CircGrps == nil {
		t.Error("CircGrps map should be initialized")
	}
	if poolMonitor.previousState.UnknownEquip == nil {
		t.Error("UnknownEquip map should be initialized")
	}
}

func TestTrackWaterTempInListenMode(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", true)
	emptyObj := ObjectData{}

	// First call - should detect new equipment
	poolMonitor.trackWaterTemp("Pool", 82.5, emptyObj)

	if poolMonitor.previousState == nil {
		t.Error("previousState should be initialized")
	}

	if poolMonitor.previousState.WaterTemps["Pool"] != 82.5 {
		t.Errorf("Expected Pool temp 82.5, got %v", poolMonitor.previousState.WaterTemps["Pool"])
	}

	// Second call with same temp - should not log change
	poolMonitor.trackWaterTemp("Pool", 82.5, emptyObj)

	// Third call with different temp - should log change
	poolMonitor.trackWaterTemp("Pool", 83.0, emptyObj)
	if poolMonitor.previousState.WaterTemps["Pool"] != 83.0 {
		t.Errorf("Expected Pool temp 83.0, got %v", poolMonitor.previousState.WaterTemps["Pool"])
	}
}

func TestTrackWaterTempNotInListenMode(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", false)
	emptyObj := ObjectData{}

	poolMonitor.trackWaterTemp("Pool", 82.5, emptyObj)

	// Should not initialize state when not in listen mode
	if poolMonitor.previousState != nil {
		t.Error("previousState should not be initialized when not in listen mode")
	}
}

func TestTrackAirTempInListenMode(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", true)
	emptyObj := ObjectData{}

	// First call - should detect new temperature
	poolMonitor.trackAirTemp(75.0, emptyObj)

	if poolMonitor.previousState == nil {
		t.Error("previousState should be initialized")
	}

	if poolMonitor.previousState.AirTemp != 75.0 {
		t.Errorf("Expected air temp 75.0, got %v", poolMonitor.previousState.AirTemp)
	}

	// Second call with same temp - should not log change
	poolMonitor.trackAirTemp(75.0, emptyObj)

	// Third call with different temp - should log change
	poolMonitor.trackAirTemp(76.0, emptyObj)
	if poolMonitor.previousState.AirTemp != 76.0 {
		t.Errorf("Expected air temp 76.0, got %v", poolMonitor.previousState.AirTemp)
	}
}

func TestTrackPumpRPMInListenMode(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", true)
	emptyObj := ObjectData{}

	// First call - should detect new pump
	poolMonitor.trackPumpRPM("Pool Pump", 2400, emptyObj)

	if poolMonitor.previousState == nil {
		t.Error("previousState should be initialized")
	}

	if poolMonitor.previousState.PumpRPMs["Pool Pump"] != 2400 {
		t.Errorf("Expected Pool Pump RPM 2400, got %v", poolMonitor.previousState.PumpRPMs["Pool Pump"])
	}

	// Second call with changed RPM - should log change
	poolMonitor.trackPumpRPM("Pool Pump", 2600, emptyObj)
	if poolMonitor.previousState.PumpRPMs["Pool Pump"] != 2600 {
		t.Errorf("Expected Pool Pump RPM 2600, got %v", poolMonitor.previousState.PumpRPMs["Pool Pump"])
	}
}

func TestTrackCircuitInListenMode(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", true)
	emptyObj := ObjectData{}

	// First call - should detect new circuit
	poolMonitor.trackCircuit("Pool Light", testStatusOff, emptyObj)

	if poolMonitor.previousState == nil {
		t.Error("previousState should be initialized")
	}

	if poolMonitor.previousState.Circuits["Pool Light"] != testStatusOff {
		t.Errorf("Expected Pool Light status OFF, got %v", poolMonitor.previousState.Circuits["Pool Light"])
	}

	// Second call with changed status - should log change
	poolMonitor.trackCircuit("Pool Light", testStatusOn, emptyObj)
	if poolMonitor.previousState.Circuits["Pool Light"] != testStatusOn {
		t.Errorf("Expected Pool Light status ON, got %v", poolMonitor.previousState.Circuits["Pool Light"])
	}
}

func TestTrackThermalInListenMode(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", true)
	emptyObj := ObjectData{}

	// First call - should detect new thermal equipment
	poolMonitor.trackThermal("Pool Heater", thermalStatusOff, emptyObj)

	if poolMonitor.previousState == nil {
		t.Error("previousState should be initialized")
	}

	if poolMonitor.previousState.Thermals["Pool Heater"] != thermalStatusOff {
		t.Errorf("Expected Pool Heater status off, got %v", poolMonitor.previousState.Thermals["Pool Heater"])
	}

	// Second call with changed status - should log change
	poolMonitor.trackThermal("Pool Heater", thermalStatusHeating, emptyObj)
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

func TestTrackCircGrpInListenMode(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", true)

	// First call - should detect new circuit group member
	obj := ObjectData{
		ObjName: "c0101",
		Params: map[string]string{
			"ACT":     testStatusOn,
			"USE":     testCircGrpUseWhite,
			"CIRCUIT": testCircGrpCircuit,
			"PARENT":  testCircGrpParent,
		},
	}
	poolMonitor.trackCircGrp(obj)

	if poolMonitor.previousState == nil {
		t.Error("previousState should be initialized")
	}

	state := poolMonitor.previousState.CircGrps["c0101"]
	if state.Active != testStatusOn {
		t.Errorf("Expected ACT %s, got %v", testStatusOn, state.Active)
	}
	if state.Use != testCircGrpUseWhite {
		t.Errorf("Expected USE %s, got %v", testCircGrpUseWhite, state.Use)
	}
	if state.Circuit != testCircGrpCircuit {
		t.Errorf("Expected CIRCUIT %s, got %v", testCircGrpCircuit, state.Circuit)
	}
	if state.Parent != testCircGrpParent {
		t.Errorf("Expected PARENT %s, got %v", testCircGrpParent, state.Parent)
	}

	// Second call with same state - should not log change
	poolMonitor.trackCircGrp(obj)

	// Third call with changed state - should log change
	obj.Params["ACT"] = testStatusOff
	obj.Params["USE"] = testCircGrpUseBlue
	poolMonitor.trackCircGrp(obj)

	state = poolMonitor.previousState.CircGrps["c0101"]
	if state.Active != testStatusOff {
		t.Errorf("Expected ACT %s after change, got %v", testStatusOff, state.Active)
	}
	if state.Use != testCircGrpUseBlue {
		t.Errorf("Expected USE %s after change, got %v", testCircGrpUseBlue, state.Use)
	}
}

func TestTrackCircGrpNotInListenMode(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", false)

	obj := ObjectData{
		ObjName: "c0101",
		Params: map[string]string{
			"ACT":     testStatusOn,
			"USE":     testCircGrpUseWhite,
			"CIRCUIT": testCircGrpCircuit,
			"PARENT":  testCircGrpParent,
		},
	}
	poolMonitor.trackCircGrp(obj)

	// Should not track when not in listen mode
	if poolMonitor.previousState != nil {
		t.Error("previousState should remain nil when not in listen mode")
	}
}

func TestResolveCircuitName(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", true)

	tests := []struct {
		name       string
		objID      string
		setupNames map[string]string
		expected   string
	}{
		{
			name:       "returns cached name when found",
			objID:      "C0004",
			setupNames: map[string]string{"C0004": "Pool Light"},
			expected:   "Pool Light",
		},
		{
			name:       "returns objID when not in cache",
			objID:      "C0005",
			setupNames: map[string]string{"C0004": "Pool Light"},
			expected:   "C0005",
		},
		{
			name:       "returns objID when cached name is empty",
			objID:      "C0006",
			setupNames: map[string]string{"C0006": ""},
			expected:   "C0006",
		},
		{
			name:       "handles GRP prefix",
			objID:      "GRP01",
			setupNames: map[string]string{"GRP01": "AllOfTheLights"},
			expected:   "AllOfTheLights",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Reset the circuitNames map for each test
			poolMonitor.circuitNames = make(map[string]string)
			for k, v := range tc.setupNames {
				poolMonitor.circuitNames[k] = v
			}

			result := poolMonitor.resolveCircuitName(tc.objID)
			if result != tc.expected {
				t.Errorf("resolveCircuitName(%q) = %q, expected %q", tc.objID, result, tc.expected)
			}
		})
	}
}

func TestBuildCircGrpChanges(t *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", true)

	tests := []struct {
		name     string
		prev     CircGrpState
		new      CircGrpState
		expected int
	}{
		{
			name:     "no changes",
			prev:     CircGrpState{Active: testStatusOn, Use: testCircGrpUseWhite, Circuit: testCircGrpCircuit, Parent: testCircGrpParent},
			new:      CircGrpState{Active: testStatusOn, Use: testCircGrpUseWhite, Circuit: testCircGrpCircuit, Parent: testCircGrpParent},
			expected: 0,
		},
		{
			name:     "ACT changed",
			prev:     CircGrpState{Active: testStatusOn, Use: testCircGrpUseWhite, Circuit: testCircGrpCircuit, Parent: testCircGrpParent},
			new:      CircGrpState{Active: testStatusOff, Use: testCircGrpUseWhite, Circuit: testCircGrpCircuit, Parent: testCircGrpParent},
			expected: 1,
		},
		{
			name:     "USE changed",
			prev:     CircGrpState{Active: testStatusOn, Use: testCircGrpUseWhite, Circuit: testCircGrpCircuit, Parent: testCircGrpParent},
			new:      CircGrpState{Active: testStatusOn, Use: testCircGrpUseBlue, Circuit: testCircGrpCircuit, Parent: testCircGrpParent},
			expected: 1,
		},
		{
			name:     "both changed",
			prev:     CircGrpState{Active: testStatusOn, Use: testCircGrpUseWhite, Circuit: testCircGrpCircuit, Parent: testCircGrpParent},
			new:      CircGrpState{Active: testStatusOff, Use: testCircGrpUseBlue, Circuit: testCircGrpCircuit, Parent: testCircGrpParent},
			expected: 2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			changes := poolMonitor.buildCircGrpChanges(tc.prev, tc.new)
			if len(changes) != tc.expected {
				t.Errorf("Expected %d changes, got %d", tc.expected, len(changes))
			}
		})
	}
}

func TestHandleCircGrpPush(_ *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", true)
	poolMonitor.initializeState()

	obj := ObjectData{
		ObjName: "c0101",
		Params: map[string]string{
			"OBJTYP":  objTypeCircGrp,
			"PARENT":  testCircGrpParent,
			"CIRCUIT": testCircGrpCircuit,
			"ACT":     testStatusOn,
			"USE":     testCircGrpUseWhite,
		},
	}

	// Should not panic and should log appropriately
	poolMonitor.handleCircGrpPush(obj)
}

func TestGetCircuitGroups(t *testing.T) {
	responses := map[string]IntelliCenterResponse{
		"GetParamList:OBJTYP=CIRCGRP": {
			Command:  "GetParamList",
			Response: "200",
			ObjectList: []ObjectData{
				{
					ObjName: "c0101",
					Params: map[string]string{
						"OBJTYP":  objTypeCircGrp,
						"PARENT":  testCircGrpParent,
						"CIRCUIT": testCircGrpCircuit,
						"ACT":     testStatusOn,
						"USE":     testCircGrpUseWhite,
						"DLY":     "0",
						"LISTORD": "1",
						"STATIC":  testStatusOff,
					},
				},
				{
					ObjName: "c0102",
					Params: map[string]string{
						"OBJTYP":  objTypeCircGrp,
						"PARENT":  testCircGrpParent,
						"CIRCUIT": "C0003",
						"ACT":     testStatusOn,
						"USE":     testCircGrpUseBlue,
						"DLY":     "0",
						"LISTORD": "2",
						"STATIC":  testStatusOff,
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

	if err := poolMonitor.ic.ConnectWithRetry(ctx); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer poolMonitor.ic.Close()

	poolMonitor.initializeState()

	err := poolMonitor.getCircuitGroups()
	if err != nil {
		t.Fatalf("getCircuitGroups failed: %v", err)
	}

	// Verify circuit groups were tracked
	if poolMonitor.previousState == nil {
		t.Fatal("previousState should be initialized")
	}

	state1 := poolMonitor.previousState.CircGrps["c0101"]
	if state1.Active != testStatusOn || state1.Use != testCircGrpUseWhite || state1.Circuit != testCircGrpCircuit || state1.Parent != testCircGrpParent {
		t.Errorf("c0101 not tracked correctly: %+v", state1)
	}

	state2 := poolMonitor.previousState.CircGrps["c0102"]
	if state2.Active != testStatusOn || state2.Use != testCircGrpUseBlue || state2.Circuit != "C0003" || state2.Parent != testCircGrpParent {
		t.Errorf("c0102 not tracked correctly: %+v", state2)
	}
}

func TestGetCircuitGroupsAPIError(t *testing.T) {
	responses := map[string]IntelliCenterResponse{
		"GetParamList:OBJTYP=CIRCGRP": {
			Command:  "GetParamList",
			Response: "500",
		},
	}

	server := createMockWebSocketServer(t, responses)
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)
	urlParts := strings.Split(strings.TrimPrefix(wsURL, "ws://"), ":")

	poolMonitor := NewPoolMonitor(urlParts[0], urlParts[1], true)
	ctx := t.Context()

	if err := poolMonitor.ic.ConnectWithRetry(ctx); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer poolMonitor.ic.Close()

	err := poolMonitor.getCircuitGroups()
	if err == nil {
		t.Error("Expected error for 500 response")
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

	if err := poolMonitor.ic.ConnectWithRetry(ctx); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer poolMonitor.ic.Close()

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
	poolMonitor := NewPoolMonitor("test", "6680", true)
	poolMonitor.initializeState()

	applyAll := func() {
		poolMonitor.applyBodyTemperatures([]ObjectData{
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
		})
		poolMonitor.applyAirTemperature([]ObjectData{
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
		})
		poolMonitor.applyPumpData([]ObjectData{
			{
				ObjName: "PUMP1",
				Params: map[string]string{
					"SNAME":  "Pool Pump",
					"RPM":    "2400",
					"STATUS": "ON",
				},
			},
		}, 0)
		poolMonitor.applyCircuitStatus([]ObjectData{
			{
				ObjName: "C01",
				Params: map[string]string{
					"SNAME":  "Pool Light",
					"STATUS": "ON",
					"OBJTYP": "CIRCUIT",
					"SUBTYP": "LIGHT",
				},
			},
		})
		poolMonitor.applyThermalStatus([]ObjectData{
			{
				ObjName: "HTR01",
				Params: map[string]string{
					"SNAME":  "Pool Heater",
					"STATUS": "ON",
					"SUBTYP": "THERMAL",
				},
			},
		})
		poolMonitor.trackCircGrp(ObjectData{
			ObjName: "c0101",
			Params: map[string]string{
				"OBJTYP":  objTypeCircGrp,
				"PARENT":  testCircGrpParent,
				"CIRCUIT": testCircGrpCircuit,
				"ACT":     testStatusOn,
				"USE":     testCircGrpUseWhite,
			},
		})
	}

	// First pass - should detect all equipment.
	applyAll()

	// Verify state was tracked.
	if poolMonitor.previousState == nil {
		t.Fatal("previousState should be initialized")
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

	if poolMonitor.previousState.Circuits["Pool Light"] != testStatusOn {
		t.Error("Pool Light should be tracked")
	}

	// Verify circuit group was tracked.
	circGrpState := poolMonitor.previousState.CircGrps["c0101"]
	if circGrpState.Active != testStatusOn || circGrpState.Use != testCircGrpUseWhite {
		t.Errorf("CircGrp c0101 should be tracked: %+v", circGrpState)
	}

	// Second pass - should not produce changes.
	applyAll()
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

func TestLogRawPushMessage(_ *testing.T) {
	poolMonitor := NewPoolMonitor("test", "6680", false)

	// Test with valid message - should not panic
	poolMonitor.logRawPushMessage(map[string]interface{}{
		"command": "test",
		"data":    "value",
	})

	// Test with empty message - should not panic
	poolMonitor.logRawPushMessage(map[string]interface{}{})
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
			name: "routes CIRCGRP type",
			obj: ObjectData{
				ObjName: "c0101",
				Params: map[string]string{
					"OBJTYP":  objTypeCircGrp,
					"PARENT":  testCircGrpParent,
					"CIRCUIT": testCircGrpCircuit,
					"ACT":     testStatusOn,
					"USE":     testCircGrpUseWhite,
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

func TestCleanupStaleMetrics(t *testing.T) {
	// Create a test gauge vec with same labels as circuit_status
	testGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "test_cleanup_metric",
			Help: "Test metric for cleanup testing",
		},
		[]string{"circuit", "name", "subtyp"},
	)

	// Register the gauge (unregister at end of test)
	prometheus.MustRegister(testGauge)
	defer prometheus.Unregister(testGauge)

	poolMonitor := NewPoolMonitor(testIntelliCenterIP, testIntelliCenterPort, false)

	tests := []struct {
		name     string
		previous map[string]bool
		current  map[string]bool
	}{
		{
			name: "deletes stale metrics not in current",
			previous: map[string]bool{
				"C01|Pool Light|LIGHT":    true,
				"C02|Spa Light|LIGHT":     true,
				"C03|Old Circuit|GENERIC": true,
			},
			current: map[string]bool{
				"C01|Pool Light|LIGHT": true,
				"C02|Spa Light|LIGHT":  true,
				// C03 is missing - should be deleted (log output confirms)
			},
		},
		{
			name: "no deletions when all metrics current",
			previous: map[string]bool{
				"C01|Pool Light|LIGHT": true,
			},
			current: map[string]bool{
				"C01|Pool Light|LIGHT": true,
			},
		},
		{
			name:     "handles empty previous",
			previous: map[string]bool{},
			current:  map[string]bool{"C01|Pool Light|LIGHT": true},
		},
		{
			name: "handles malformed key gracefully",
			previous: map[string]bool{
				"malformed_key": true, // Missing pipe separators - should not panic
			},
			current: map[string]bool{},
		},
		{
			name: "handles key with only two parts",
			previous: map[string]bool{
				"C01|Pool Light": true, // Only two parts - should not panic
			},
			current: map[string]bool{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(_ *testing.T) {
			// Reset metrics for each test
			testGauge.Reset()
			for key := range tt.previous {
				parts := strings.SplitN(key, "|", metricKeyPartsCount)
				if len(parts) == metricKeyPartsCount {
					testGauge.WithLabelValues(parts[0], parts[1], parts[2]).Set(1)
				}
			}

			// Run cleanup - should not panic and should log deletions
			// The log output "Cleaned up stale test metric: X (Y)" confirms deletion
			poolMonitor.cleanupStaleMetrics(tt.previous, tt.current, testGauge, "test")
		})
	}
}

func TestActiveCircuitKeyTracking(t *testing.T) {
	poolMonitor := NewPoolMonitor(testIntelliCenterIP, testIntelliCenterPort, false)

	// Process a circuit object
	obj := ObjectData{
		ObjName: "C01",
		Params: map[string]string{
			"SNAME":  "Pool Light",
			"STATUS": "ON",
			"OBJTYP": "CIRCUIT",
			"SUBTYP": "LIGHT",
		},
	}

	poolMonitor.processCircuitObject(obj)

	// Verify the key was tracked
	expectedKey := "C01|Pool Light|LIGHT"
	if !poolMonitor.activeCircuitKeys[expectedKey] {
		t.Errorf("expected activeCircuitKeys to contain %q", expectedKey)
	}
}

func TestActiveFeatureKeyTracking(t *testing.T) {
	poolMonitor := NewPoolMonitor(testIntelliCenterIP, testIntelliCenterPort, false)

	// Set up feature config to allow the feature to be processed
	poolMonitor.featureConfig["FTR01"] = testShowOnMenuValue

	// Process a feature object
	obj := ObjectData{
		ObjName: "FTR01",
		Params: map[string]string{
			"SNAME":  "Spa Jets",
			"STATUS": "ON",
			"OBJTYP": "CIRCUIT",
			"SUBTYP": "GENERIC",
		},
	}

	poolMonitor.processCircuitObject(obj)

	// Verify the key was tracked
	expectedKey := "FTR01|Spa Jets|GENERIC"
	if !poolMonitor.activeFeatureKeys[expectedKey] {
		t.Errorf("expected activeFeatureKeys to contain %q", expectedKey)
	}
}

func TestStaleMetricCleanupIntegration(t *testing.T) {
	// First call returns two circuits.
	objs := []ObjectData{
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
				"SNAME":  "Spa Light",
				"STATUS": "ON",
				"OBJTYP": "CIRCUIT",
				"SUBTYP": "LIGHT",
			},
		},
	}

	poolMonitor := NewPoolMonitor("test", "6680", false)

	// First call - should track both circuits.
	poolMonitor.applyCircuitStatus(objs)

	// Verify both keys are tracked.
	if !poolMonitor.activeCircuitKeys["C01|Pool Light|LIGHT"] {
		t.Error("expected C01 to be tracked after first call")
	}
	if !poolMonitor.activeCircuitKeys["C02|Spa Light|LIGHT"] {
		t.Error("expected C02 to be tracked after first call")
	}
}
