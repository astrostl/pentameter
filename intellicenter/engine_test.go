package intellicenter //nolint:testpackage // white-box: exercises unexported engine internals + mock

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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
	var sawScanOK, sawBaselinePoll atomic.Bool
	var sawRawPush atomic.Bool
	e.OnScan = func(err error) {
		if err == nil {
			sawScanOK.Store(true)
		}
	}
	e.OnRawPoll = func(req *Client, baseline bool) {
		if req != nil && baseline {
			sawBaselinePoll.Store(true)
		}
	}
	e.OnRawPush = func(msg map[string]any) {
		if msg["command"] == "WriteParamList" {
			sawRawPush.Store(true)
		}
	}
	ch := e.Subscribe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = e.Run(ctx) }()

	// Baseline should populate the snapshot and fire OnScan(nil) + OnRawPoll(baseline).
	waitFor(t, func() bool { return e.Snapshot().Circuits["C0001"].Name == "Pool Light" })
	waitFor(t, sawScanOK.Load)
	waitFor(t, sawBaselinePoll.Load)
	snap := e.Snapshot()
	if snap.Bodies["B1101"].Temp != 82 || !snap.Sensors[airSensorObjnam].Valid {
		t.Fatalf("baseline snapshot incomplete: %+v", snap)
	}
	if snap.Sensors[airSensorObjnam].SubType != "AIR" {
		t.Errorf("sensor subtype not captured: %+v", snap.Sensors[airSensorObjnam])
	}

	// GetConfiguration ran at baseline → feature visibility loaded.
	cfg := e.Config()
	if cfg["FTR01"] != "hide w" || cfg["FTR02"] != "hide" {
		t.Errorf("config not loaded: %+v", cfg)
	}
	if !ShouldShowFeature(cfg["FTR01"]) || ShouldShowFeature(cfg["FTR02"]) {
		t.Errorf("visibility wrong: FTR01=%v FTR02=%v", cfg["FTR01"], cfg["FTR02"])
	}

	// RawObjects exposes merged params + kind for full-fidelity sweeps.
	raw := map[string]RawObject{}
	for _, o := range e.RawObjects() {
		raw[o.ObjName] = o
	}
	if c := raw["C0001"]; c.Kind != KindCircuit || c.Params["SUBTYP"] != "LIGHT" || c.Params["SNAME"] != "Pool Light" {
		t.Errorf("raw circuit wrong: %+v", c)
	}
	if b := raw["B1101"]; b.Kind != KindBody || b.Params["HTSRC"] != "H0001" || b.Params["LOTMP"] != "85" {
		t.Errorf("raw body wrong: %+v", b)
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
	// The raw push hook saw the unsolicited message verbatim.
	waitFor(t, sawRawPush.Load)
}

// TestEnginePMPCircBaselineAndRefresh verifies the circuit⇄pump graph is fetched
// once at baseline (surfaced via RawObjects) and that static config (PMPCIRC +
// GetConfiguration) is re-pulled on the periodic poll cadence, not every poll.
func TestEnginePMPCircBaselineAndRefresh(t *testing.T) {
	mock := newEngineMock(t)
	defer mock.close()
	host, port, _ := strings.Cut(strings.TrimPrefix(mock.srv.URL, "http://"), ":")

	e := NewEngine(host, port, time.Millisecond) // fast poll so 60-poll refresh fires quickly

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = e.Run(ctx) }()

	// Baseline: PMPCIRC fetched once and exposed as a raw object with its kind.
	waitFor(t, func() bool { return e.Snapshot().Circuits["C0001"].Name == "Pool Light" })
	waitFor(t, func() bool { return mock.pmpcQueries.Load() >= 1 && mock.cfgQueries.Load() >= 1 })
	var pc RawObject
	for _, o := range e.RawObjects() {
		if o.ObjName == "p0101" {
			pc = o
		}
	}
	if pc.Kind != KindPMPCirc || pc.Params["CIRCUIT"] != "C0001" || pc.Params["PARENT"] != "PMP01" {
		t.Fatalf("PMPCIRC not surfaced via RawObjects at baseline: %+v", pc)
	}

	// After configRefreshPolls successful polls, both static-config fetches run again.
	waitFor(t, func() bool { return mock.pmpcQueries.Load() >= 2 && mock.cfgQueries.Load() >= 2 })
}

// TestEngineResolveDrivesDial verifies the engine dials the host returned by the
// Resolve hook (not the placeholder passed to NewEngine), and calls it before
// connecting.
func TestEngineResolveDrivesDial(t *testing.T) {
	mock := newEngineMock(t)
	defer mock.close()
	host, port, _ := strings.Cut(strings.TrimPrefix(mock.srv.URL, "http://"), ":")

	var resolveCalls atomic.Int32
	e := NewEngine("placeholder.invalid", port, time.Hour) // host overridden by Resolve
	e.Resolve = func() (string, error) {
		resolveCalls.Add(1)
		return host, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = e.Run(ctx) }()

	// Baseline only succeeds if the engine dialed the resolved host.
	waitFor(t, func() bool { return e.Snapshot().Circuits["C0001"].Name == "Pool Light" })
	if resolveCalls.Load() < 1 {
		t.Error("Resolve should be called before connecting")
	}
}

// TestEngineResolveErrorRetries verifies a Resolve error is treated as a connect
// failure (reported via OnScan), and the engine retries until Resolve succeeds.
func TestEngineResolveErrorRetries(t *testing.T) {
	mock := newEngineMock(t)
	defer mock.close()
	host, port, _ := strings.Cut(strings.TrimPrefix(mock.srv.URL, "http://"), ":")

	var sawErr atomic.Bool
	var calls atomic.Int32
	e := NewEngine(host, port, time.Hour)
	e.OnScan = func(err error) {
		if err != nil {
			sawErr.Store(true)
		}
	}
	e.Resolve = func() (string, error) {
		if calls.Add(1) == 1 {
			return "", errTestResolve
		}
		return host, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = e.Run(ctx) }()

	waitFor(t, sawErr.Load) // first resolve failed → OnScan(err)
	waitFor(t, func() bool { return e.Snapshot().Circuits["C0001"].Name == "Pool Light" })
}

var errTestResolve = resolveError("resolve boom")

type resolveError string

func (e resolveError) Error() string { return string(e) }

// TestEnginePollFailuresForceReconnect verifies a poll connection that stops
// answering — while the push connection stays healthy — forces a reconnect
// after maxConsecutivePollFailures, instead of retrying forever on the same
// broken connection. Regression test for a field incident (2026-07-11): the
// panel accepted the poll socket but never answered GetParamList for 113
// minutes straight while the push socket idled along; nothing ended the
// session until the panel itself reset the push socket too.
func TestEnginePollFailuresForceReconnect(t *testing.T) {
	mock := newEngineMock(t)
	defer mock.close()
	host, port, _ := strings.Cut(strings.TrimPrefix(mock.srv.URL, "http://"), ":")

	e := NewEngine(host, port, 10*time.Millisecond) // fast poll so failures accumulate quickly

	var sawScanErr, sawScanOKAfterErr atomic.Bool
	e.OnScan = func(err error) {
		if err != nil {
			sawScanErr.Store(true)
		} else if sawScanErr.Load() {
			sawScanOKAfterErr.Store(true)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = e.Run(ctx) }()

	// Baseline (condCircuit call #1) succeeds; exactly one req+push pair so far.
	waitFor(t, func() bool { return e.Snapshot().Circuits["C0001"].Name == "Pool Light" })
	waitFor(t, func() bool { return mock.connCount() == 2 })

	// Fail every poll after baseline (calls #2 through #1+maxConsecutivePollFailures):
	// simulates the poll socket going unresponsive while the push socket is untouched.
	mock.failCircuitLo.Store(2)
	mock.failCircuitHi.Store(1 + maxConsecutivePollFailures)

	// The engine must tear down and reconnect: two fresh connections beyond the
	// original pair. Deadline generous enough to clear Run's reconnect backoff.
	waitForTimeout(t, 6*time.Second, func() bool { return mock.connCount() >= 4 })
	if !sawScanErr.Load() {
		t.Error("expected OnScan to observe the poll failures before reconnect")
	}

	// The reconnected session's baseline (condCircuit call #(hi+1), outside the
	// injected-failure range) succeeds again — real recovery, not just a
	// reconnect loop that keeps failing.
	waitForTimeout(t, 6*time.Second, sawScanOKAfterErr.Load)
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
	waitForTimeout(t, 3*time.Second, cond)
}

// waitForTimeout is waitFor with a caller-supplied deadline, for conditions
// that must clear a fixed delay (e.g. Run's reconnect backoff) before they
// can become true.
func waitForTimeout(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.After(timeout)
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
	srv         *httptest.Server
	mu          sync.Mutex
	conns       []*safeConn
	lastReq     Request
	cfgQueries  atomic.Int32 // GetConfiguration (feature visibility) calls
	pmpcQueries atomic.Int32 // PMPCIRC (circuit⇄pump graph) calls

	// circuitCalls counts condCircuit GetParamList calls (1-indexed); calls
	// numbered within [failCircuitLo, failCircuitHi] (inclusive) get an error
	// response instead of data, simulating a poll connection that stops
	// answering. Zero values disable failure injection.
	circuitCalls                 atomic.Int32
	failCircuitLo, failCircuitHi atomic.Int32
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
		if req.Condition == condPMPCirc {
			m.pmpcQueries.Add(1)
		}
		if req.Condition == condCircuit {
			n := m.circuitCalls.Add(1)
			if lo, hi := m.failCircuitLo.Load(), m.failCircuitHi.Load(); lo > 0 && n >= lo && n <= hi {
				sc.writeJSON(Response{Command: req.Command, MessageID: req.MessageID, Response: "400"})
				return
			}
		}
		sc.writeJSON(Response{Command: req.Command, MessageID: req.MessageID, Response: "200", ObjectList: m.objectsFor(req)})
	case "SetParamList":
		m.mu.Lock()
		m.lastReq = req
		m.mu.Unlock()
		sc.writeJSON(Response{Command: req.Command, MessageID: req.MessageID, Response: "200"})
	case cmdGetQuery:
		m.cfgQueries.Add(1)
		// GetConfiguration → "answer" envelope with FTR SHOMNU visibility flags.
		sc.writeJSON(map[string]any{
			"command":   req.Command,
			"messageID": req.MessageID,
			"answer": []any{
				map[string]any{"objnam": "FTR01", "params": map[string]any{"SHOMNU": "hide w"}},
				map[string]any{"objnam": "FTR02", "params": map[string]any{"SHOMNU": "hide"}},
			},
		})
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
	case condPMPCirc:
		return []ObjectData{{ObjName: "p0101", Params: map[string]string{"CIRCUIT": "C0001", "PARENT": "PMP01"}}}
	}
	// Air sensor is queried by objnam with no condition.
	if len(req.ObjectList) == 1 && req.ObjectList[0].ObjName == airSensorObjnam {
		return []ObjectData{{ObjName: airSensorObjnam, Params: map[string]string{
			"SNAME": "Air", "PROBE": "75", "SUBTYP": "AIR",
		}}}
	}
	return nil // pumps, heaters: none in this fixture
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

func (m *engineMock) connCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.conns)
}
