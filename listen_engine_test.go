package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/astrostl/pentameter/intellicenter"
)

// TestListenPollFromEngine drives the engine against a mock IntelliCenter, then
// runs a listen poll over the engine's snapshot + a live client and asserts the
// listen diff-state and metrics reflect the equipment — and that a second,
// unchanged poll detects zero changes.
func TestListenPollFromEngine(t *testing.T) {
	responses := map[string]IntelliCenterResponse{
		"GetParamList:OBJTYP=CIRCUIT": {ObjectList: []ObjectData{
			{ObjName: "C0001", Params: map[string]string{"SNAME": "Pool Light", "STATUS": "ON", "OBJTYP": "CIRCUIT", "SUBTYP": "LIGHT", "FREEZE": "OFF"}},
			{ObjName: "C0002", Params: map[string]string{"SNAME": "Cleaner", "STATUS": "ON", "OBJTYP": "CIRCUIT", "SUBTYP": "GENERIC", "FREEZE": "OFF"}},
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
	waitForCond(t, func() bool { return engine.Snapshot().Circuits["C0001"].Name == "Pool Light" })

	// A second live client stands in for the engine's request client that the
	// real OnRawPoll hook hands to listenPoll.
	req := intellicenter.New(host, port)
	if err := req.ConnectWithRetry(ctx); err != nil {
		t.Fatalf("connect req client: %v", err)
	}
	defer req.Close()

	pm := NewPoolMonitor(host, port, true)
	pm.initializeState()

	// Baseline poll: equipment is "detected" and diff state is seeded.
	pm.listenPoll(engine, req, true)

	if !pm.initialPollDone {
		t.Error("baseline poll should mark initial poll done")
	}
	if got := pm.previousState.WaterTemps["Pool"]; got != 82 {
		t.Errorf("water temp diff-state: got %v, want 82", got)
	}
	if got := pm.previousState.PumpRPMs["Pump"]; got != 2000 {
		t.Errorf("pump rpm diff-state: got %v, want 2000", got)
	}
	if got := pm.previousState.AirTemp; got != 75 {
		t.Errorf("air temp diff-state: got %v, want 75", got)
	}
	if got := pm.previousState.Circuits["Pool Light"]; got != "ON" {
		t.Errorf("circuit diff-state: got %q, want ON", got)
	}
	if got := gaugeVal(t, poolTemperature.WithLabelValues("POOL", "Pool")); got != 82 {
		t.Errorf("water temp gauge: got %v, want 82", got)
	}
	if got := gaugeVal(t, pumpRPM.WithLabelValues("PMP01", "Pump")); got != 2000 {
		t.Errorf("pump rpm gauge: got %v, want 2000", got)
	}

	// A second poll over identical state detects no changes.
	pm.previousState.PollChangeCount = 999 // sentinel; listenPoll must reset it
	pm.listenPoll(engine, req, false)
	if pm.previousState.PollChangeCount != 0 {
		t.Errorf("unchanged poll should detect 0 changes, got %d", pm.previousState.PollChangeCount)
	}
}
