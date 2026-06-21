package intellicenter //nolint:testpackage // white-box: tests exercise unexported protocol types and the mock server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeIC is a mock IntelliCenter speaking the request/response protocol over a
// WebSocket, so the client can be validated without hardware.
type fakeIC struct {
	srv     *httptest.Server
	t       *testing.T
	lastSet Request
}

func newFakeIC(t *testing.T) *fakeIC {
	t.Helper()
	f := &fakeIC{t: t}
	up := websocket.Upgrader{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			var req Request
			if err := c.ReadJSON(&req); err != nil {
				return
			}
			f.handle(c, req)
		}
	}))
	return f
}

func (f *fakeIC) handle(c *websocket.Conn, req Request) {
	switch req.Command {
	case "GetParamList":
		// One unsolicited push first, to exercise push-skipping.
		_ = c.WriteJSON(Response{Command: "NotifyList", MessageID: "push-1", Response: "200"})
		_ = c.WriteJSON(Response{Command: "GetParamList", MessageID: req.MessageID, Response: "200", ObjectList: f.objectsFor(req.Condition)})
	case "SetParamList":
		f.lastSet = req
		_ = c.WriteJSON(Response{Command: "SetParamList", MessageID: req.MessageID, Response: "200"})
	default:
		_ = c.WriteJSON(Response{Command: req.Command, MessageID: req.MessageID, Response: "400"})
	}
}

func (f *fakeIC) objectsFor(condition string) []ObjectData {
	switch condition {
	case "OBJTYP=CIRCUIT":
		return []ObjectData{
			{ObjName: "C0001", Params: map[string]string{"SNAME": "Pool Light", "STATUS": "ON", "OBJTYP": "CIRCUIT", "SUBTYP": "LIGHT", "FREEZE": "OFF"}},
			{ObjName: "FTR01", Params: map[string]string{"SNAME": "Waterfall", "STATUS": "OFF", "OBJTYP": "CIRCUIT", "SUBTYP": "GENERIC"}},
			{ObjName: "BAD", Params: map[string]string{"SNAME": "", "STATUS": ""}},
		}
	case "OBJTYP=BODY":
		return []ObjectData{
			{ObjName: "B1101", Params: map[string]string{"SNAME": "Pool", "STATUS": "ON", "TEMP": "82", "HTMODE": "1", "HTSRC": "H0001", "LOTMP": "85", "HITMP": "104"}},
		}
	case "":
		// Air sensor is queried by objnam with no condition.
		return []ObjectData{{ObjName: "_A135", Params: map[string]string{"SNAME": "Air", "PROBE": "75", "SUBTYP": "AIR"}}}
	}
	return nil
}

func (f *fakeIC) close() { f.srv.Close() }

func dial(t *testing.T, f *fakeIC) *Client {
	t.Helper()
	addr := strings.TrimPrefix(f.srv.URL, "http://")
	host, port, _ := strings.Cut(addr, ":")
	c := New(host, port)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	return c
}

func TestCircuitsParsesAndSkipsPush(t *testing.T) {
	f := newFakeIC(t)
	defer f.close()
	c := dial(t, f)
	defer c.Close()

	circuits, err := c.Circuits()
	if err != nil {
		t.Fatalf("Circuits: %v", err)
	}
	if len(circuits) != 2 {
		t.Fatalf("want 2 circuits (empty skipped), got %d: %+v", len(circuits), circuits)
	}
	if circuits[0].ID != "C0001" || !circuits[0].On || circuits[0].Name != "Pool Light" {
		t.Errorf("circuit[0] wrong: %+v", circuits[0])
	}
	if circuits[1].ID != "FTR01" || circuits[1].On {
		t.Errorf("circuit[1] wrong: %+v", circuits[1])
	}
}

func TestBodiesAndHeatStatus(t *testing.T) {
	f := newFakeIC(t)
	defer f.close()
	c := dial(t, f)
	defer c.Close()

	bodies, err := c.Bodies()
	if err != nil {
		t.Fatalf("Bodies: %v", err)
	}
	if len(bodies) != 1 {
		t.Fatalf("want 1 body, got %d", len(bodies))
	}
	b := bodies[0]
	if b.Temp != 82 || b.LoSetTemp != 85 || b.HeatMode != 1 {
		t.Errorf("body parse wrong: %+v", b)
	}
	if b.HeatStatus() != HeatStatusHeating {
		t.Errorf("HTMODE=1 should be heating, got %d", b.HeatStatus())
	}
}

func TestSensor(t *testing.T) {
	f := newFakeIC(t)
	defer f.close()
	c := dial(t, f)
	defer c.Close()

	s, err := c.Sensor("_A135")
	if err != nil {
		t.Fatalf("Sensor: %v", err)
	}
	if !s.Valid || s.Temp != 75 {
		t.Errorf("sensor wrong: %+v", s)
	}
}

func TestSetCircuit(t *testing.T) {
	f := newFakeIC(t)
	defer f.close()
	c := dial(t, f)
	defer c.Close()

	if err := c.SetCircuit("C0001", true); err != nil {
		t.Fatalf("SetCircuit: %v", err)
	}
	obj := f.lastSet.ObjectList[0]
	if f.lastSet.Command != "SetParamList" || obj.ObjName != "C0001" || obj.Params["STATUS"] != "ON" {
		t.Errorf("set wrong: %+v", f.lastSet)
	}
	if err := c.SetCircuit("C0001", false); err != nil {
		t.Fatalf("SetCircuit off: %v", err)
	}
	if f.lastSet.ObjectList[0].Params["STATUS"] != "OFF" {
		t.Errorf("want STATUS=OFF, got %s", f.lastSet.ObjectList[0].Params["STATUS"])
	}
}

func TestSetHeatControls(t *testing.T) {
	f := newFakeIC(t)
	defer f.close()
	c := dial(t, f)
	defer c.Close()

	if err := c.SetHeatSetpoint("B1101", 85); err != nil {
		t.Fatalf("SetHeatSetpoint: %v", err)
	}
	if obj := f.lastSet.ObjectList[0]; obj.ObjName != "B1101" || obj.Params["LOTMP"] != "85" {
		t.Errorf("heat setpoint wrong: %+v", f.lastSet)
	}
	if err := c.SetCoolSetpoint("B1101", 92); err != nil {
		t.Fatalf("SetCoolSetpoint: %v", err)
	}
	if f.lastSet.ObjectList[0].Params["HITMP"] != "92" {
		t.Errorf("cool setpoint wrong: %+v", f.lastSet)
	}
	// Heat source writes the writable HEATER param, NOT read-only HTSRC.
	if err := c.SetHeatSource("B1101", "H0001"); err != nil {
		t.Fatalf("SetHeatSource: %v", err)
	}
	if obj := f.lastSet.ObjectList[0]; obj.Params["HEATER"] != "H0001" || obj.Params["HTSRC"] != "" {
		t.Errorf("heat source should write HEATER not HTSRC: %+v", f.lastSet)
	}
	if err := c.SetHeatSource("B1101", HeatSourceNone); err != nil {
		t.Fatalf("SetHeatSource off: %v", err)
	}
	if f.lastSet.ObjectList[0].Params["HEATER"] != "00000" {
		t.Errorf("heat source off wrong: %+v", f.lastSet)
	}
}

func TestPumpFrom(t *testing.T) {
	// Real-shape params: STATUS is a numeric code (not "ON"), power is under PWR
	// (WATTS is a garbage echo), MAX is the configured top speed.
	running := pumpFrom("PMP01", map[string]string{
		keySName: "VS", keyStatus: "10", keyRPM: "1800", keyMax: "3450",
		keyPwr: "215", keyWatts: "WATTS", keyGPM: "55",
	})
	if !running.On {
		t.Error("pump at 1800 RPM should be On (STATUS is a code, not \"ON\")")
	}
	if running.RPM != 1800 || running.MaxRPM != 3450 {
		t.Errorf("rpm/max wrong: %+v", running)
	}
	if running.Watts != 215 {
		t.Errorf("power should come from PWR (215), not the WATTS echo: got %v", running.Watts)
	}

	// Stopped pump: RPM 0 → not running.
	stopped := pumpFrom("PMP03", map[string]string{keySName: "Idle", keyRPM: "0", keyMax: "3450"})
	if stopped.On {
		t.Error("pump at 0 RPM should be Off")
	}

	// Fallback: a firmware that populates WATTS instead of PWR.
	legacy := pumpFrom("PMP09", map[string]string{keyRPM: "2000", keyWatts: "900"})
	if legacy.Watts != 900 {
		t.Errorf("should fall back to WATTS when PWR absent: got %v", legacy.Watts)
	}
}

func TestShouldShowFeature(t *testing.T) {
	if !ShouldShowFeature("ABCw") {
		t.Error("ABCw should be visible")
	}
	if ShouldShowFeature("ABC") {
		t.Error("ABC should be hidden")
	}
}
