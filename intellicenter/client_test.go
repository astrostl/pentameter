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
	case "OBJTYP=SENSE":
		return []ObjectData{{ObjName: "_A135", Params: map[string]string{"SNAME": "Air", "PROBE": "75"}}}
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

func TestShouldShowFeature(t *testing.T) {
	if !ShouldShowFeature("ABCw") {
		t.Error("ABCw should be visible")
	}
	if ShouldShowFeature("ABC") {
		t.Error("ABC should be hidden")
	}
}
