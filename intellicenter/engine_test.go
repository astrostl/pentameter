package intellicenter //nolint:testpackage // white-box: exercises unexported engine internals + mock

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// --- unit tests: state merge / diff / emit (no network) -------------------

func TestEngineMergeDiffEmit(t *testing.T) {
	e := NewEngine("h", "6680", time.Hour)
	ch := e.Subscribe()

	// Baseline: a new circuit emits a change.
	e.applyAndEmit(KindCircuit, "C0001", map[string]string{
		keySName: "Pool Light", keyStatus: "ON", keyObjTyp: "CIRCUIT", keySubTyp: "LIGHT", keyFreeze: "OFF",
	})
	c := recvChange(t, ch)
	if c.Circuit == nil || c.Circuit.ID != "C0001" || !c.Circuit.On || c.Circuit.Name != "Pool Light" {
		t.Fatalf("baseline change wrong: %+v", c.Circuit)
	}

	// Re-applying identical state emits nothing.
	e.applyAndEmit(KindCircuit, "C0001", map[string]string{keyStatus: "ON"})
	assertNoChange(t, ch)

	// A partial push (STATUS only) merges over prior params and emits the delta.
	e.applyAndEmit(KindCircuit, "C0001", map[string]string{keyStatus: "OFF"})
	c = recvChange(t, ch)
	if c.Circuit == nil || c.Circuit.On {
		t.Fatalf("expected circuit off, got %+v", c.Circuit)
	}
	// Name must survive the partial merge.
	if got := e.Snapshot().Circuits["C0001"]; got.Name != "Pool Light" || got.On {
		t.Errorf("snapshot after partial merge wrong: %+v", got)
	}
}

func TestExtractPushObjects(t *testing.T) {
	// Direct shape: objectList[].{objnam,params}
	direct := map[string]any{"objectList": []any{
		map[string]any{"objnam": "C0001", "params": map[string]any{"STATUS": "ON"}},
	}}
	got := extractPushObjects(direct)
	if len(got) != 1 || got[0].objnam != "C0001" || got[0].params["STATUS"] != "ON" {
		t.Fatalf("direct shape parse wrong: %+v", got)
	}

	// Nested shape: objectList[].changes[].{objnam,params}
	nested := map[string]any{"objectList": []any{
		map[string]any{"changes": []any{
			map[string]any{"objnam": "B1101", "params": map[string]any{"TEMP": "82"}},
		}},
	}}
	got = extractPushObjects(nested)
	if len(got) != 1 || got[0].objnam != "B1101" || got[0].params["TEMP"] != "82" {
		t.Fatalf("nested shape parse wrong: %+v", got)
	}

	// Garbage is ignored, not panicked on.
	if r := extractPushObjects(map[string]any{"objectList": "nope"}); r != nil {
		t.Errorf("expected nil for bad objectList, got %+v", r)
	}
}

// --- integration test: Run against a mock IntelliCenter -------------------

func TestEngineRunBaselineControlPush(t *testing.T) {
	mock := newEngineMock(t)
	defer mock.close()

	addr := strings.TrimPrefix(mock.srv.URL, "http://")
	host, port, _ := strings.Cut(addr, ":")
	e := NewEngine(host, port, time.Hour) // long poll so only baseline + push fire
	ch := e.Subscribe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = e.Run(ctx) }()

	// Baseline should populate the snapshot.
	waitFor(t, func() bool { return e.Snapshot().Circuits["C0001"].Name == "Pool Light" })
	snap := e.Snapshot()
	if snap.Bodies["B1101"].Temp != 82 || !snap.Sensors[airSensorObjnam].Valid {
		t.Fatalf("baseline snapshot incomplete: %+v", snap)
	}

	// Control: a write reaches IntelliCenter as a SetParamList.
	if err := e.SetCircuit("C0001", false); err != nil {
		t.Fatalf("SetCircuit: %v", err)
	}
	waitFor(t, func() bool {
		s := mock.lastSet()
		return s.Command == "SetParamList" && len(s.ObjectList) == 1 &&
			s.ObjectList[0].ObjName == "C0001" && s.ObjectList[0].Params["STATUS"] == "OFF"
	})

	// Push: an unsolicited WriteParamList flips C0001 and the engine emits it.
	mock.broadcast(map[string]any{
		"command": "WriteParamList",
		"objectList": []any{
			map[string]any{"objnam": "C0001", "params": map[string]any{"STATUS": "OFF"}},
		},
	})
	if c := waitChange(t, ch, func(c Change) bool { return c.Circuit != nil && c.Circuit.ID == "C0001" && !c.Circuit.On }); c.Circuit == nil {
		t.Fatal("did not receive expected circuit-off change from push")
	}
	if e.Snapshot().Circuits["C0001"].On {
		t.Error("snapshot should reflect pushed OFF state")
	}
}

// --- test helpers ---------------------------------------------------------

func recvChange(t *testing.T, ch <-chan Change) Change {
	t.Helper()
	select {
	case c := <-ch:
		return c
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for change")
		return Change{}
	}
}

func assertNoChange(t *testing.T, ch <-chan Change) {
	t.Helper()
	select {
	case c := <-ch:
		t.Fatalf("unexpected change: %+v", c)
	case <-time.After(100 * time.Millisecond):
	}
}

func waitChange(t *testing.T, ch <-chan Change, pred func(Change) bool) Change {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case c := <-ch:
			if pred(c) {
				return c
			}
		case <-deadline:
			t.Fatal("timed out waiting for matching change")
			return Change{}
		}
	}
}

func waitFor(t *testing.T, cond func() bool) {
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

// engineMock is a write-safe mock IntelliCenter supporting multiple connections
// (the engine opens two) and unsolicited broadcast pushes.
type engineMock struct {
	srv     *httptest.Server
	mu      sync.Mutex
	conns   []*safeConn
	lastReq Request
}

type safeConn struct {
	c   *websocket.Conn
	wmu sync.Mutex
}

func (s *safeConn) writeJSON(v any) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_ = s.c.WriteJSON(v)
}

func newEngineMock(t *testing.T) *engineMock {
	t.Helper()
	m := &engineMock{}
	up := websocket.Upgrader{}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		sc := &safeConn{c: c}
		m.mu.Lock()
		m.conns = append(m.conns, sc)
		m.mu.Unlock()
		for {
			var req Request
			if err := c.ReadJSON(&req); err != nil {
				return
			}
			m.handle(sc, req)
		}
	}))
	return m
}

func (m *engineMock) handle(sc *safeConn, req Request) {
	switch req.Command {
	case "GetParamList":
		sc.writeJSON(Response{Command: req.Command, MessageID: req.MessageID, Response: "200", ObjectList: m.objectsFor(req)})
	case "SetParamList":
		m.mu.Lock()
		m.lastReq = req
		m.mu.Unlock()
		sc.writeJSON(Response{Command: req.Command, MessageID: req.MessageID, Response: "200"})
	default:
		sc.writeJSON(Response{Command: req.Command, MessageID: req.MessageID, Response: "400"})
	}
}

func (m *engineMock) objectsFor(req Request) []ObjectData {
	switch req.Condition {
	case condCircuit:
		return []ObjectData{{ObjName: "C0001", Params: map[string]string{
			"SNAME": "Pool Light", "STATUS": "ON", "OBJTYP": "CIRCUIT", "SUBTYP": "LIGHT", "FREEZE": "OFF",
		}}}
	case condBody:
		return []ObjectData{{ObjName: "B1101", Params: map[string]string{
			"SNAME": "Pool", "STATUS": "ON", "TEMP": "82", "SUBTYP": "POOL", "HTMODE": "1", "HTSRC": "H0001", "LOTMP": "85", "HITMP": "104",
		}}}
	case condSense:
		return []ObjectData{{ObjName: airSensorObjnam, Params: map[string]string{"SNAME": "Air", "PROBE": "75"}}}
	default: // pumps, heaters: none in this fixture
		return nil
	}
}

func (m *engineMock) broadcast(push any) {
	m.mu.Lock()
	conns := append([]*safeConn(nil), m.conns...)
	m.mu.Unlock()
	for _, sc := range conns {
		sc.writeJSON(push)
	}
}

func (m *engineMock) lastSet() Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastReq
}

func (m *engineMock) close() { m.srv.Close() }
