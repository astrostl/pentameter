package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/astrostl/pentameter/intellicenter"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// TestRefreshFromEngine drives the engine against a mock IntelliCenter, then
// recomputes all metrics from its snapshot and asserts the gauge values match
// the legacy interpretation (temps, pump RPM, circuit on/off, freeze coloring,
// thermal status + setpoint).
func TestRefreshFromEngine(t *testing.T) {
	responses := map[string]IntelliCenterResponse{
		"GetParamList:OBJTYP=CIRCUIT": {ObjectList: []ObjectData{
			{ObjName: "C0001", Params: map[string]string{"SNAME": "Pool Light", "STATUS": "ON", "OBJTYP": "CIRCUIT", "SUBTYP": "LIGHT", "FREEZE": "OFF"}},
			{ObjName: "C0002", Params: map[string]string{"SNAME": "Cleaner", "STATUS": "ON", "OBJTYP": "CIRCUIT", "SUBTYP": "GENERIC", "FREEZE": "ON"}},
			{ObjName: "FTR01", Params: map[string]string{"SNAME": "Waterfall", "STATUS": "ON", "OBJTYP": "CIRCUIT", "SUBTYP": "GENERIC", "FREEZE": "OFF"}},
			{ObjName: "_FEA2", Params: map[string]string{"SNAME": "Freeze", "STATUS": "ON", "OBJTYP": "CIRCUIT", "SUBTYP": "GENERIC"}},
		}},
		"GetParamList:OBJTYP=BODY": {ObjectList: []ObjectData{
			{ObjName: "B1101", Params: map[string]string{
				"SNAME": "Pool", "STATUS": "ON", "TEMP": "82", "SUBTYP": "POOL",
				"HTMODE": "1", "HTSRC": "H0001", "LOTMP": "85", "HITMP": "104",
			}},
		}},
		"GetParamList:OBJTYP=PUMP": {ObjectList: []ObjectData{
			{ObjName: "PMP01", Params: map[string]string{"SNAME": "Pump", "STATUS": "ON", "RPM": "2000", "WATTS": "900", "GPM": "60"}},
		}},
		"GetParamList:OBJTYP=HEATER": {ObjectList: []ObjectData{
			{ObjName: "H0001", Params: map[string]string{"SNAME": "Gas", "STATUS": "ON", "SUBTYP": "GAS", "OBJTYP": "HEATER"}},
		}},
		"GetParamList:": {ObjectList: []ObjectData{
			{ObjName: "_A135", Params: map[string]string{"SNAME": "Air", "PROBE": "75", "SUBTYP": "AIR"}},
		}},
	}
	server := createMockWebSocketServer(t, responses)
	defer server.Close()

	host, port, _ := strings.Cut(strings.TrimPrefix(server.URL, "http://"), ":")
	engine := intellicenter.NewEngine(host, port, time.Hour) // long poll: baseline only

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = engine.Run(ctx) }()

	// Wait for the baseline scan to populate the snapshot.
	waitForCond(t, func() bool { return engine.Snapshot().Circuits["C0001"].Name == "Pool Light" })

	pm := NewPoolMonitor(host, port, false)
	pm.refreshFromEngine(engine)

	checks := []struct {
		name string
		got  float64
		want float64
	}{
		{"circuit Pool Light on", gaugeVal(t, circuitStatus.WithLabelValues("C0001", "Pool Light", "LIGHT")), 1},
		{"circuit Cleaner freeze-protected", gaugeVal(t, circuitStatus.WithLabelValues("C0002", "Cleaner", "GENERIC")), 2},
		{"feature Waterfall on", gaugeVal(t, featureStatus.WithLabelValues("FTR01", "Waterfall", "GENERIC")), 1},
		{"water temp", gaugeVal(t, poolTemperature.WithLabelValues("POOL", "Pool")), 82},
		{"air temp", gaugeVal(t, airTemperature.WithLabelValues("AIR", "Air")), 75},
		{"pump rpm", gaugeVal(t, pumpRPM.WithLabelValues("PMP01", "Pump")), 2000},
		{"thermal heating", gaugeVal(t, thermalStatus.WithLabelValues("H0001", "Gas", "GAS")), float64(thermalStatusHeating)},
		{"thermal low setpoint", gaugeVal(t, thermalLowSetpoint.WithLabelValues("H0001", "Gas", "GAS")), 85},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, c.got, c.want)
		}
	}

	// _FEA2 drove freeze-protection active but is itself not exported as a circuit.
	if !pm.freezeProtectionActive {
		t.Error("freeze protection should be active (_FEA2 ON)")
	}
}

// gaugeVal reads a gauge's current value via the metric model (no extra deps).
func gaugeVal(t *testing.T, g prometheus.Gauge) float64 {
	t.Helper()
	var m dto.Metric
	if err := g.Write(&m); err != nil {
		t.Fatalf("write metric: %v", err)
	}
	return m.GetGauge().GetValue()
}

func waitForCond(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		if cond() {
			return
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for condition")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
