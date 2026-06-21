package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/astrostl/pentameter/intellicenter"
	"github.com/gorilla/websocket"
)

// syncBuffer is a goroutine-safe bytes.Buffer for capturing emitter output.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, _ := b.buf.Write(p) // bytes.Buffer.Write never returns an error
	return n, nil
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestHBCircuitItems verifies the snapshot→accessory mapping is sorted by ID and
// carries name/kind/on.
func TestHBCircuitItems(t *testing.T) {
	snap := intellicenter.Snapshot{Circuits: map[string]intellicenter.Circuit{
		"C0002": {ID: "C0002", Name: "Cleaner", On: false, Feature: true},
		"C0001": {ID: "C0001", Name: "Pool Light", On: true, Feature: true},
		"C0003": {ID: "C0003", Name: "Internal Relay", On: true, Feature: false}, // not a Feature → hidden
	}}
	items := hbCircuitItems(snap)
	if len(items) != 2 {
		t.Fatalf("want 2 items (Features only), got %d", len(items))
	}
	if items[0].ID != "C0001" || items[1].ID != "C0002" {
		t.Errorf("items not sorted by ID or non-Feature leaked in: %+v", items)
	}
	if items[0].Kind != "switch" || items[0].Name != "Pool Light" || !items[0].On {
		t.Errorf("first item wrong: %+v", items[0])
	}
}

// TestThermostatAssembly checks per-body thermostat discovery: pseudo heaters are
// filtered, cool capability comes from the real heater, and heat state maps right.
func TestThermostatAssembly(t *testing.T) {
	snap := intellicenter.Snapshot{
		Bodies: map[string]intellicenter.Body{
			"B1101": {ID: "B1101", Name: "Pool", Temp: 84, LoSetTemp: 85, HiSetTemp: 92, HeatMode: 4, HeaterID: "H0001"},
			"B1202": {ID: "B1202", Name: "Spa", Temp: 84, LoSetTemp: 98, HiSetTemp: 104, HeatMode: 0, HeaterID: "00000"},
			"B9999": {ID: "B9999", Name: "NoHeat", Temp: 70, HeaterID: "00000"},
		},
		Heaters: map[string]intellicenter.Heater{
			"H0001": {ID: "H0001", Name: "Pool Heat Pump", SubType: "ULTRA", Body: "B1101", Cool: true, Real: true},
			"HXULT": {ID: "HXULT", Name: "UltraTemp Pref", SubType: "HEATER", Body: "B1101", Real: false}, // pseudo
			"H0002": {ID: "H0002", Name: "Spa Heater", SubType: "GENERIC", Body: "B1202", Cool: false, Real: true},
		},
	}

	// Pseudo HXULT is filtered; pool maps to the real heat pump.
	byBody := realHeatersByBody(snap)
	if byBody["B1101"].ID != "H0001" {
		t.Errorf("pool should map to H0001 (real), got %q", byBody["B1101"].ID)
	}
	if _, ok := byBody["B9999"]; ok {
		t.Error("heater-less body should have no heater")
	}

	items := hbThermostatItems(snap)
	if len(items) != 2 {
		t.Fatalf("want 2 thermostats (pool, spa), got %d", len(items))
	}
	byID := map[string]hbAccessory{}
	for _, it := range items {
		byID[it.ID] = it
	}
	pool, spa := byID["B1101"], byID["B1202"]
	if !pool.CanCool || pool.CoolC == nil {
		t.Errorf("pool should be cool-capable with a cool setpoint: %+v", pool)
	}
	if spa.CanCool || spa.CoolC != nil {
		t.Errorf("spa is heat-only, should have no cool setpoint: %+v", spa)
	}
	if pool.State != hbStateHeat { // HeatMode 4 = heating
		t.Errorf("pool state: got %q want heat", pool.State)
	}
	// B9999 has no heater → no thermostat.
	if _, ok := byID["B9999"]; ok {
		t.Error("heater-less body should not get a thermostat")
	}

	// State mapping spot-checks.
	cases := []struct {
		mode int
		src  string
		want string
	}{
		{4, "H0001", hbStateHeat},
		{9, "H0001", hbStateCool},
		{0, "H0001", statusWordIdle},
		{0, "00000", statusWordOff},
	}
	for _, c := range cases {
		b := intellicenter.Body{HeatMode: c.mode, HeaterID: c.src}
		if got := bodyHeatState(&b); got != c.want {
			t.Errorf("bodyHeatState(mode=%d src=%s): got %q want %q", c.mode, c.src, got, c.want)
		}
	}
}

// TestHBPumpRunningItems verifies each pump yields a read-only "running"
// OccupancySensor (suffixed id) tracking RPM > 0, sorted by ID.
func TestHBPumpRunningItems(t *testing.T) {
	snap := intellicenter.Snapshot{Pumps: map[string]intellicenter.Pump{
		"PMP02": {ID: "PMP02", Name: "pool", On: true, RPM: 2450},
		"PMP01": {ID: "PMP01", Name: "VS", On: false, RPM: 0},
	}}
	items := hbPumpRunningItems(snap)
	if len(items) != 2 {
		t.Fatalf("want 2 running sensors, got %d", len(items))
	}
	if items[0].ID != "PMP01.run" || items[1].ID != "PMP02.run" {
		t.Errorf("not sorted / wrong suffix: %+v", items)
	}
	if items[0].Kind != hbKindOccupancy || items[0].Name != "VS Running" || items[0].On {
		t.Errorf("stopped pump sensor wrong: %+v", items[0])
	}
	if !items[1].On || items[1].Name != "pool Running" {
		t.Errorf("running pump sensor wrong: %+v", items[1])
	}
}

// TestHBPumpSensorItems verifies pumps yield read-only LightSensors carrying raw
// metrics as lux, with suffixed IDs, and GPM suppressed when the pump has no flow
// capability (MaxFlow == 0, i.e. the controller's GPM is an estimate).
func TestHBPumpSensorItems(t *testing.T) {
	snap := intellicenter.Snapshot{Pumps: map[string]intellicenter.Pump{
		"PMP01": {ID: "PMP01", Name: "VS", RPM: 1800, Watts: 215, GPM: 55, MaxFlow: 0},     // SPEED pump: no real flow
		"PMP02": {ID: "PMP02", Name: "pool", RPM: 2450, Watts: 760, GPM: 45, MaxFlow: 140}, // VSF: real flow
	}}
	items := hbPumpSensorItems(snap)
	byID := map[string]hbAccessory{}
	for _, it := range items {
		if it.Kind != hbKindLight {
			t.Errorf("expected lightsensor kind: %+v", it)
		}
		byID[it.ID] = it
	}
	// VS pump: RPM + Watts, but NO GPM (MaxFlow 0).
	if _, ok := byID["PMP01.gpm"]; ok {
		t.Error("VS pump (MaxFlow=0) should not emit a GPM sensor")
	}
	if s := byID["PMP01.rpm"]; s.Lux == nil || *s.Lux != 1800 || s.Name != "VS RPM" {
		t.Errorf("VS RPM sensor wrong: %+v", s)
	}
	if s := byID["PMP01.watts"]; s.Lux == nil || *s.Lux != 215 {
		t.Errorf("VS Watts sensor wrong: %+v", s)
	}
	// VSF pump: GPM present.
	if s := byID["PMP02.gpm"]; s.Lux == nil || *s.Lux != 45 || s.Name != "pool GPM" {
		t.Errorf("VSF pump should emit GPM=45: %+v", s)
	}
}

// TestHBSensorItems verifies temperature sensors become TemperatureSensors with
// the °F PROBE value converted to Celsius, and invalid/nameless sensors skipped.
func TestHBSensorItems(t *testing.T) {
	snap := intellicenter.Snapshot{Sensors: map[string]intellicenter.Sensor{
		"_A135": {ID: "_A135", Name: "Air", Temp: 77, Valid: true}, // 77°F -> 25°C
		"_BAD":  {ID: "_BAD", Name: "Broken", Valid: false},        // invalid → skipped
	}}
	items := hbSensorItems(snap)
	if len(items) != 1 {
		t.Fatalf("want 1 temp sensor (invalid skipped), got %d", len(items))
	}
	s := items[0]
	if s.ID != "_A135" || s.Kind != hbKindTempSensor || s.Name != "Air" {
		t.Errorf("air sensor wrong: %+v", s)
	}
	if s.CurC == nil || *s.CurC != fToC(77) {
		t.Errorf("temp should be 77°F converted to Celsius: %v", s.CurC)
	}
}

// TestHBFreezeItems verifies the freeze-protection occupancy sensor is built from
// the SUBTYP=FRZ feature circuit and tracks its STATUS.
func TestHBFreezeItems(t *testing.T) {
	snap := intellicenter.Snapshot{Circuits: map[string]intellicenter.Circuit{
		"C0001": {ID: "C0001", Name: "Spa", SubType: "SPA", On: true},
		"_FEA2": {ID: "_FEA2", Name: "Freeze", SubType: "FRZ", On: true},
	}}
	items := hbFreezeItems(snap)
	if len(items) != 1 {
		t.Fatalf("want 1 freeze sensor, got %d", len(items))
	}
	f := items[0]
	if f.ID != "_FEA2" || f.Kind != hbKindOccupancy || f.Name != "Freeze Protection" || !f.On {
		t.Errorf("freeze sensor wrong: %+v", f)
	}
	// No FRZ circuit → no sensor.
	none := hbFreezeItems(intellicenter.Snapshot{Circuits: map[string]intellicenter.Circuit{
		"C0001": {ID: "C0001", SubType: "SPA"},
	}})
	if none != nil {
		t.Errorf("no FRZ circuit should yield no freeze sensor, got %+v", none)
	}
}

// TestCToF checks the HomeKit-Celsius -> IntelliCenter-Fahrenheit conversion
// rounds to whole degrees and round-trips the common pool setpoints.
func TestCToF(t *testing.T) {
	cases := []struct {
		c    float64
		want int
	}{
		{0, 32},
		{29.4, 85}, // pool heat setpoint
		{33.3, 92}, // pool cool setpoint
		{40, 104},  // spa max
		{36.7, 98}, // spa heat setpoint
	}
	for _, tc := range cases {
		if got := cToF(tc.c); got != tc.want {
			t.Errorf("cToF(%v): got %d want %d", tc.c, got, tc.want)
		}
	}
	// fToC then cToF round-trips whole Fahrenheit degrees.
	for f := 50; f <= 104; f++ {
		if got := cToF(fToC(float64(f))); got != f {
			t.Errorf("round-trip %d°F -> %d°F", f, got)
		}
	}
}

// TestAccessorySignature confirms the signature tracks membership (id/name/kind/
// cool) but ignores live state (on/temp/setpoint), so resync re-announces on
// add/remove/rename but not on an on-off or temperature tick.
func TestAccessorySignature(t *testing.T) {
	on := []hbAccessory{{ID: "C1", Name: "Pool", Kind: hbKindSwitch, On: true}}
	off := []hbAccessory{{ID: "C1", Name: "Pool", Kind: hbKindSwitch, On: false}}
	if accessorySignature(on) != accessorySignature(off) {
		t.Error("on/off should not change the signature")
	}
	renamed := []hbAccessory{{ID: "C1", Name: "Spa", Kind: hbKindSwitch}}
	if accessorySignature(on) == accessorySignature(renamed) {
		t.Error("rename should change the signature")
	}
	added := append([]hbAccessory{}, on...)
	added = append(added, hbAccessory{ID: "C2", Name: "Light", Kind: hbKindSwitch})
	if accessorySignature(on) == accessorySignature(added) {
		t.Error("adding an accessory should change the signature")
	}
	heat := []hbAccessory{{ID: "B1", Name: "Pool", Kind: hbKindThermostat, CanCool: false}}
	cool := []hbAccessory{{ID: "B1", Name: "Pool", Kind: hbKindThermostat, CanCool: true}}
	if accessorySignature(heat) == accessorySignature(cool) {
		t.Error("cool-capability flip should change the signature")
	}
}

// closableMock is a mock IntelliCenter that exposes its live WebSocket
// connections so a test can sever them mid-session (httptest.Server.Close leaves
// hijacked WebSockets open, so it can't simulate a controller dropping).
type closableMock struct {
	srv   *httptest.Server
	mu    sync.Mutex
	conns []*websocket.Conn
}

func newClosableMock(responses map[string]IntelliCenterResponse) *closableMock {
	m := &closableMock{}
	up := websocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		m.mu.Lock()
		m.conns = append(m.conns, conn)
		m.mu.Unlock()
		defer conn.Close()
		for {
			var req IntelliCenterRequest
			if err := conn.ReadJSON(&req); err != nil {
				return
			}
			resp, ok := responses[req.Command+":"+req.Condition]
			if !ok {
				resp = IntelliCenterResponse{Command: req.Command}
			}
			resp.MessageID = req.MessageID
			if resp.Response == "" {
				resp.Response = "200"
			}
			if err := conn.WriteJSON(resp); err != nil {
				return
			}
		}
	}))
	return m
}

func (m *closableMock) severConns() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.conns {
		_ = c.Close()
	}
}

// TestHomebridgeConnectionGoesOfflineOnDisconnect drives the engine against a
// mock, waits for the baseline announce (connection sensor online), then severs
// the connection mid-session and asserts the connection sensor is reported
// offline — the controller-unreachable path (engine OnScan error), which can't
// be forced from a live network in the sandbox.
func TestHomebridgeConnectionGoesOfflineOnDisconnect(t *testing.T) {
	responses := map[string]IntelliCenterResponse{
		"GetParamList:OBJTYP=CIRCUIT": {ObjectList: []ObjectData{
			{ObjName: "C0001", Params: map[string]string{"SNAME": "Pool Light", "STATUS": "ON", "OBJTYP": "CIRCUIT", "SUBTYP": "LIGHT", "FEATR": "ON"}},
		}},
	}
	mock := newClosableMock(responses)
	defer mock.srv.Close()
	host, port, _ := strings.Cut(strings.TrimPrefix(mock.srv.URL, "http://"), ":")
	engine := intellicenter.NewEngine(host, port, 200*time.Millisecond)

	var buf syncBuffer
	out := newHBEmitter(&buf)
	cmds := make(chan hbSet, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hbRun(ctx, engine, out, cmds)

	// Baseline announce → the connection sensor exists and is online.
	waitForCond(t, func() bool { return strings.Contains(buf.String(), `"t":"accessories"`) })

	// Sever the live connections: the engine session drops, OnScan(err) fires,
	// and the connection sensor must be reported offline.
	mock.severConns()
	waitForCond(t, func() bool { return strings.Contains(buf.String(), `"id":"_conn","on":false`) })
	cancel()
}

// TestHomebridgeEngineAnnounces drives the engine against a mock and asserts the
// adapter announces the discovered circuits + ready over the IPC.
func TestHomebridgeEngineAnnounces(t *testing.T) {
	responses := map[string]IntelliCenterResponse{
		"GetParamList:OBJTYP=CIRCUIT": {ObjectList: []ObjectData{
			{ObjName: "C0001", Params: map[string]string{"SNAME": "Pool Light", "STATUS": "ON", "OBJTYP": "CIRCUIT", "SUBTYP": "LIGHT", "FEATR": "ON"}},
			{ObjName: "C0002", Params: map[string]string{"SNAME": "Cleaner", "STATUS": "OFF", "OBJTYP": "CIRCUIT", "SUBTYP": "GENERIC", "FEATR": "ON"}},
		}},
	}
	server := createMockWebSocketServer(t, responses)
	defer server.Close()

	host, port, _ := strings.Cut(strings.TrimPrefix(server.URL, "http://"), ":")
	engine := intellicenter.NewEngine(host, port, time.Hour) // baseline only

	var buf syncBuffer
	out := newHBEmitter(&buf)
	cmds := make(chan hbSet, 4)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hbRun(ctx, engine, out, cmds)

	waitForCond(t, func() bool { return strings.Contains(buf.String(), `"t":"accessories"`) })
	cancel()

	// Parse the accessories line and verify content.
	var gotAccessories, gotReady bool
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var msg struct {
			T     string        `json:"t"`
			Items []hbAccessory `json:"items"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		switch msg.T {
		case "ready":
			gotReady = true
		case "accessories":
			gotAccessories = true
			// 2 circuits + the always-present connection-health sensor.
			if len(msg.Items) != 3 || msg.Items[0].ID != "C0001" || !msg.Items[0].On {
				t.Errorf("accessories payload wrong: %+v", msg.Items)
			}
			if msg.Items[1].ID != "C0002" || msg.Items[1].On {
				t.Errorf("second accessory wrong: %+v", msg.Items[1])
			}
			conn := msg.Items[2]
			if conn.ID != hbConnID || conn.Kind != hbKindOccupancy || !conn.On {
				t.Errorf("connection sensor wrong: %+v", conn)
			}
		}
	}
	if !gotAccessories {
		t.Error("never emitted accessories")
	}
	if !gotReady {
		t.Error("never emitted ready")
	}
}
