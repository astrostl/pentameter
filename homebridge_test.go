package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/astrostl/pentameter/intellicenter"
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
		"C0002": {ID: "C0002", Name: "Cleaner", On: false},
		"C0001": {ID: "C0001", Name: "Pool Light", On: true},
	}}
	items := hbCircuitItems(snap)
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}
	if items[0].ID != "C0001" || items[1].ID != "C0002" {
		t.Errorf("items not sorted by ID: %+v", items)
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

// TestHomebridgeEngineAnnounces drives the engine against a mock and asserts the
// adapter announces the discovered circuits + ready over the IPC.
func TestHomebridgeEngineAnnounces(t *testing.T) {
	responses := map[string]IntelliCenterResponse{
		"GetParamList:OBJTYP=CIRCUIT": {ObjectList: []ObjectData{
			{ObjName: "C0001", Params: map[string]string{"SNAME": "Pool Light", "STATUS": "ON", "OBJTYP": "CIRCUIT", "SUBTYP": "LIGHT"}},
			{ObjName: "C0002", Params: map[string]string{"SNAME": "Cleaner", "STATUS": "OFF", "OBJTYP": "CIRCUIT", "SUBTYP": "GENERIC"}},
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
			if len(msg.Items) != 2 || msg.Items[0].ID != "C0001" || !msg.Items[0].On {
				t.Errorf("accessories payload wrong: %+v", msg.Items)
			}
			if msg.Items[1].ID != "C0002" || msg.Items[1].On {
				t.Errorf("second accessory wrong: %+v", msg.Items[1])
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
