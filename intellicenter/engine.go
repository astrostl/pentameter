package intellicenter

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	engineSubBuffer = 64
	airSensorObjnam = "_A135"
	engineReconnect = 2 * time.Second
)

// Snapshot is the engine's current view of all known equipment, keyed by objnam.
type Snapshot struct {
	Circuits map[string]Circuit
	Bodies   map[string]Body
	Pumps    map[string]Pump
	Heaters  map[string]Heater
	Sensors  map[string]Sensor
}

func newSnapshot() Snapshot {
	return Snapshot{
		Circuits: map[string]Circuit{},
		Bodies:   map[string]Body{},
		Pumps:    map[string]Pump{},
		Heaters:  map[string]Heater{},
		Sensors:  map[string]Sensor{},
	}
}

func (s Snapshot) clone() Snapshot {
	out := newSnapshot()
	for k, v := range s.Circuits {
		out.Circuits[k] = v
	}
	for k, v := range s.Bodies {
		out.Bodies[k] = v
	}
	for k, v := range s.Pumps {
		out.Pumps[k] = v
	}
	for k, v := range s.Heaters {
		out.Heaters[k] = v
	}
	for k, v := range s.Sensors {
		out.Sensors[k] = v
	}
	return out
}

// Change is a single equipment delta emitted on a push or a poll-diff. Exactly
// one field is non-nil; its Kind method reports which.
type Change struct {
	Circuit *Circuit
	Body    *Body
	Pump    *Pump
	Heater  *Heater
	Sensor  *Sensor
}

// Engine maintains live IntelliCenter state from an unsolicited push stream plus
// periodic polling, and broadcasts changes to subscribers. It is metrics- and
// output-agnostic: consumers (metrics/listen/homebridge) subscribe and adapt.
//
// It holds two connections: a push connection (read-only, receives IntelliCenter's
// WriteParamList broadcasts) and a request/response connection (periodic polls +
// control writes). They cannot share a socket — a blocking stream read cannot
// interleave with request/response.
type Engine struct {
	host, port string
	pollEvery  time.Duration

	// Logf, if set, receives human-readable diagnostics (reconnects, errors).
	// nil = silent, so the package stays output-agnostic.
	Logf func(format string, args ...any)

	// OnScan, if set, is called after each full scan attempt (baseline + every
	// poll) and on connect/session failures, with the error (nil = success). It
	// lets consumers track liveness gauges (e.g. connection-failure / last-refresh)
	// without the engine knowing anything about metrics.
	OnScan func(err error)

	// OnRawPush, if set, receives every unsolicited push message verbatim before
	// the engine applies it to typed state. It exists for the listen/troubleshooting
	// consumer, which dumps raw protocol traffic the typed Change stream discards.
	OnRawPush func(msg map[string]any)

	// OnRawPoll, if set, is called after each successful scan (baseline + every
	// poll) with the live request client and whether this scan is a fresh baseline
	// (post-connect/reconnect). It lets the listen consumer run supplementary raw
	// queries (circuit groups, all-object discovery) over the engine's connection
	// instead of maintaining its own.
	OnRawPoll func(req *Client, baseline bool)

	// Resolve, if set, is called before every (re)connect to obtain the current
	// host. It lets the engine follow an IntelliCenter whose IP changes across
	// reconnects (mDNS rediscovery). nil = always dial the host given to NewEngine.
	// A Resolve error is treated like a connect failure: backoff, then retry.
	Resolve func() (string, error)

	mu     sync.RWMutex
	kind   map[string]Kind
	params map[string]map[string]string
	snap   Snapshot
	config map[string]string // FTR objnam -> SHOMNU (feature visibility), loaded at baseline

	subsMu sync.Mutex
	subs   []chan Change

	clientMu  sync.Mutex
	reqClient *Client
}

// NewEngine builds an engine targeting ws://host:port, polling every pollEvery.
func NewEngine(host, port string, pollEvery time.Duration) *Engine {
	return &Engine{
		host:      host,
		port:      port,
		pollEvery: pollEvery,
		kind:      map[string]Kind{},
		params:    map[string]map[string]string{},
		snap:      newSnapshot(),
		config:    map[string]string{},
	}
}

func (e *Engine) logf(format string, args ...any) {
	if e.Logf != nil {
		e.Logf(format, args...)
	}
}

func (e *Engine) onScan(err error) {
	if e.OnScan != nil {
		e.OnScan(err)
	}
}

func (e *Engine) onRawPush(msg map[string]any) {
	if e.OnRawPush != nil {
		e.OnRawPush(msg)
	}
}

func (e *Engine) onRawPoll(req *Client, baseline bool) {
	if e.OnRawPoll != nil {
		e.OnRawPoll(req, baseline)
	}
}

// Subscribe returns a channel of Change events. Subscribe before calling Run to
// receive the initial baseline as a series of changes. The channel is buffered;
// if a consumer falls behind, changes are dropped rather than blocking the engine.
func (e *Engine) Subscribe() <-chan Change {
	ch := make(chan Change, engineSubBuffer)
	e.subsMu.Lock()
	e.subs = append(e.subs, ch)
	e.subsMu.Unlock()
	return ch
}

func (e *Engine) emit(c Change) {
	e.subsMu.Lock()
	defer e.subsMu.Unlock()
	for _, ch := range e.subs {
		select {
		case ch <- c:
		default: // consumer behind — drop rather than block the engine
		}
	}
}

// Snapshot returns a deep copy of the current state.
func (e *Engine) Snapshot() Snapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.snap.clone()
}

// RawObject is one object's merged raw params plus its inferred kind. Consumers
// that need the full param set (e.g. metrics interpretation) sweep RawObjects
// rather than the typed Snapshot.
type RawObject struct {
	ObjName string
	Kind    Kind
	Params  map[string]string
}

// RawObjects returns a deep copy of every tracked object's merged raw params.
// Use it for full-fidelity recomputes that need params not surfaced on the typed
// Snapshot (e.g. body/circuit SUBTYP, HTSRC, FREEZE).
func (e *Engine) RawObjects() []RawObject {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]RawObject, 0, len(e.params))
	for objnam, params := range e.params {
		cp := make(map[string]string, len(params))
		for k, v := range params {
			cp[k] = v
		}
		out = append(out, RawObject{ObjName: objnam, Kind: e.kind[objnam], Params: cp})
	}
	return out
}

// Config returns a copy of the loaded feature-visibility config (FTR objnam ->
// SHOMNU). Empty until the baseline GetConfiguration completes.
func (e *Engine) Config() map[string]string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make(map[string]string, len(e.config))
	for k, v := range e.config {
		out[k] = v
	}
	return out
}

// --- control (writes) -----------------------------------------------------

// SetCircuit turns a circuit/feature/body on or off.
func (e *Engine) SetCircuit(id string, on bool) error {
	return e.withReqClient(func(c *Client) error { return c.SetCircuit(id, on) })
}

// SetHeatSetpoint sets a body's heat setpoint (°F).
func (e *Engine) SetHeatSetpoint(bodyID string, tempF int) error {
	return e.withReqClient(func(c *Client) error { return c.SetHeatSetpoint(bodyID, tempF) })
}

// SetCoolSetpoint sets a body's cool setpoint (°F) for heat-pump bodies.
func (e *Engine) SetCoolSetpoint(bodyID string, tempF int) error {
	return e.withReqClient(func(c *Client) error { return c.SetCoolSetpoint(bodyID, tempF) })
}

// SetHeatSource assigns a body's heat source (heater objnam, or HeatSourceNone
// to turn heating off).
func (e *Engine) SetHeatSource(bodyID, heaterID string) error {
	return e.withReqClient(func(c *Client) error { return c.SetHeatSource(bodyID, heaterID) })
}

func (e *Engine) withReqClient(fn func(*Client) error) error {
	e.clientMu.Lock()
	c := e.reqClient
	e.clientMu.Unlock()
	if c == nil {
		return fmt.Errorf("engine not connected")
	}
	return fn(c)
}

func (e *Engine) setReqClient(c *Client) {
	e.clientMu.Lock()
	e.reqClient = c
	e.clientMu.Unlock()
}

// resolveHost refreshes e.host from the Resolve hook (if set) ahead of a
// (re)connect. Called only on the Run goroutine, which is the sole reader of
// e.host, so no lock is needed.
func (e *Engine) resolveHost() error {
	if e.Resolve == nil {
		return nil
	}
	host, err := e.Resolve()
	if err != nil {
		return err
	}
	if host != e.host {
		e.logf("engine: host resolved to %s", host)
	}
	e.host = host
	return nil
}

// --- run loop -------------------------------------------------------------

// Run connects, performs an initial baseline scan, then runs the push stream and
// the poll ticker until ctx is canceled. It reconnects with backoff on failure.
func (e *Engine) Run(ctx context.Context) error {
	delay := engineReconnect
	for ctx.Err() == nil {
		if err := e.resolveHost(); err != nil {
			e.logf("engine: resolve host failed: %v", err)
			e.onScan(err)
			if !sleepCtx(ctx, delay) {
				break
			}
			delay = nextEngineDelay(delay)
			continue
		}

		req := New(e.host, e.port)
		push := New(e.host, e.port)

		if err := req.ConnectWithRetry(ctx); err != nil {
			e.logf("engine: connect (req) failed: %v", err)
			e.onScan(err)
		} else if err := push.ConnectWithRetry(ctx); err != nil {
			e.logf("engine: connect (push) failed: %v", err)
			e.onScan(err)
			req.Close()
		} else if err := e.session(ctx, req, push); err != nil {
			e.logf("engine: session ended: %v", err)
			e.onScan(err)
		}

		req.Close()
		push.Close()
		e.setReqClient(nil)

		// sleepCtx returns false (→ break) if ctx is canceled during backoff;
		// the loop header re-checks ctx.Err() otherwise.
		if !sleepCtx(ctx, delay) {
			break
		}
		delay = nextEngineDelay(delay)
	}
	return nil // exits only on ctx cancellation — a clean shutdown, not an error
}

// session runs one connected lifetime: baseline, then poll ticker + push loop.
func (e *Engine) session(ctx context.Context, req, push *Client) error {
	if err := e.scan(req); err != nil {
		return fmt.Errorf("baseline: %w", err)
	}
	e.loadConfig(req) // best-effort: feature visibility, never fatal to a session
	e.setReqClient(req)
	e.onScan(nil) // baseline succeeded → live
	e.onRawPoll(req, true)
	e.logf("engine: connected to %s:%s (baseline complete)", e.host, e.port)

	pollCtx, cancelPoll := context.WithCancel(ctx)
	defer cancelPoll()
	go e.pollLoop(pollCtx, req)

	// Push loop runs in the foreground; returns when the stream errors or ctx ends.
	return e.pushLoop(ctx, push)
}

func (e *Engine) pollLoop(ctx context.Context, req *Client) {
	ticker := time.NewTicker(e.pollEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := e.scan(req)
			if err != nil {
				e.logf("engine: poll error: %v", err)
			}
			e.onScan(err)
			if err == nil {
				e.onRawPoll(req, false)
			}
		}
	}
}

func (e *Engine) pushLoop(ctx context.Context, push *Client) error {
	for ctx.Err() == nil {
		msg, err := push.ReadMessage()
		if err != nil {
			return fmt.Errorf("push stream: %w", err)
		}
		e.onRawPush(msg)
		e.handlePush(msg)
	}
	return nil // ctx canceled — shutdown, not an error
}

// --- state updates --------------------------------------------------------

type scanGroup struct {
	kind Kind
	cond string
	keys []string
}

var scanGroups = []scanGroup{
	{KindCircuit, condCircuit, circuitKeys},
	{KindBody, condBody, bodyKeys},
	{KindPump, condPump, pumpKeys},
	{KindHeater, condHeater, heaterKeys},
}

// scan does a full request/response read of every equipment type plus the air
// sensor, merging results and emitting changes. Used for the initial baseline
// and for each poll tick (idempotent: only differences emit).
func (e *Engine) scan(req *Client) error {
	for _, g := range scanGroups {
		objs, err := req.query(string(g.kind), g.cond, g.keys)
		if err != nil {
			return err
		}
		for _, o := range objs {
			if o.Params[keySName] == "" {
				continue
			}
			e.applyAndEmit(g.kind, o.ObjName, o.Params)
		}
	}
	if params, ok := e.querySensor(req, airSensorObjnam); ok {
		e.applyAndEmit(KindSensor, airSensorObjnam, params)
	}
	return nil
}

func (e *Engine) querySensor(c *Client, objnam string) (map[string]string, bool) {
	resp, err := c.roundTrip("sensor", Request{
		Command: cmdGetParamList,
		// No condition: queried by objnam, matching the hardware-proven air-sensor request.
		ObjectList: []Object{{ObjName: objnam, Keys: sensorKeys}},
	})
	if err != nil {
		return nil, false
	}
	for _, o := range resp.ObjectList {
		if o.ObjName == objnam {
			return o.Params, true
		}
	}
	return nil, false
}

// loadConfig fetches GetConfiguration and records each feature's SHOMNU flag for
// visibility decisions. Best-effort: failures leave the config empty (consumers
// then default to showing all features), never aborting the session.
func (e *Engine) loadConfig(req *Client) {
	resp, err := req.DoRaw(map[string]any{
		fieldCommand:   cmdGetQuery,
		fieldQueryName: queryConfiguration,
		fieldArguments: "",
	})
	if err != nil {
		e.logf("engine: load config failed: %v", err)
		return
	}
	answer, ok := resp[fieldAnswer].([]any)
	if !ok {
		return
	}
	cfg := map[string]string{}
	for _, item := range answer {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		objnam, ok := obj["objnam"].(string)
		if !ok || !strings.HasPrefix(objnam, ftrPrefix) {
			continue
		}
		params, ok := obj["params"].(map[string]any)
		if !ok {
			continue
		}
		if shomnu, ok := params[keyShomnu].(string); ok {
			cfg[objnam] = shomnu
		}
	}
	e.mu.Lock()
	e.config = cfg
	e.mu.Unlock()
}

// handlePush applies an unsolicited push (WriteParamList/NotifyList). Objects not
// seen during baseline are skipped; the next poll will pick them up.
func (e *Engine) handlePush(msg map[string]any) {
	for _, po := range extractPushObjects(msg) {
		kind, known := e.kindOf(po.objnam)
		if !known {
			continue
		}
		e.applyAndEmit(kind, po.objnam, po.params)
	}
}

func (e *Engine) kindOf(objnam string) (Kind, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	k, ok := e.kind[objnam]
	return k, ok
}

// applyAndEmit merges partial params for an object, reparses it, and emits a
// Change if the typed value changed.
func (e *Engine) applyAndEmit(kind Kind, objnam string, partial map[string]string) {
	e.apply(kind, objnam, partial, false)
}

// apply merges partial params, reparses, and emits a Change. When force is true
// it emits even if the typed value is unchanged — used after a write so a
// subscriber (HomeKit) is corrected to the controller's actual state even when a
// rejected/ineffective write left that state unchanged.
func (e *Engine) apply(kind Kind, objnam string, partial map[string]string, force bool) {
	e.mu.Lock()
	cur := e.params[objnam]
	if cur == nil {
		cur = map[string]string{}
		e.params[objnam] = cur
	}
	for k, v := range partial {
		cur[k] = v
	}
	e.kind[objnam] = kind
	change, changed := e.reparseLocked(kind, objnam, cur)
	e.mu.Unlock()

	if changed || force {
		e.emit(change)
	}
}

// RefreshBody re-reads a single body from the controller and force-emits its
// current state, so callers can verify a write took effect and subscribers
// reflect the controller's truth immediately (within one round-trip, not one
// poll interval). No-op if the body isn't found in the response.
func (e *Engine) RefreshBody(bodyID string) error {
	return e.withReqClient(func(c *Client) error {
		objs, err := c.query(string(KindBody), condBody, bodyKeys)
		if err != nil {
			return err
		}
		for _, o := range objs {
			if o.ObjName == bodyID && o.Params[keySName] != "" {
				e.apply(KindBody, bodyID, o.Params, true)
				return nil
			}
		}
		return nil
	})
}

// diffStore stores v under id and reports whether it differs from the prior value.
func diffStore[T comparable](m map[string]T, id string, v T) bool {
	if prev, ok := m[id]; ok && prev == v {
		return false
	}
	m[id] = v
	return true
}

// reparseLocked rebuilds the typed value from params, updates the snapshot, and
// reports whether it changed. Caller must hold e.mu.
func (e *Engine) reparseLocked(kind Kind, objnam string, params map[string]string) (Change, bool) {
	switch kind {
	case KindCircuit:
		v := circuitFrom(objnam, params)
		return Change{Circuit: &v}, diffStore(e.snap.Circuits, objnam, v)
	case KindBody:
		v := bodyFrom(objnam, params)
		return Change{Body: &v}, diffStore(e.snap.Bodies, objnam, v)
	case KindPump:
		v := pumpFrom(objnam, params)
		return Change{Pump: &v}, diffStore(e.snap.Pumps, objnam, v)
	case KindHeater:
		v := heaterFrom(objnam, params)
		return Change{Heater: &v}, diffStore(e.snap.Heaters, objnam, v)
	case KindSensor:
		v := sensorFrom(objnam, params)
		return Change{Sensor: &v}, diffStore(e.snap.Sensors, objnam, v)
	default:
		return Change{}, false
	}
}

// --- push message parsing -------------------------------------------------

type pushObject struct {
	objnam string
	params map[string]string
}

// extractPushObjects pulls {objnam, params} pairs out of an IntelliCenter push.
// It tolerates both shapes seen in the wild: objectList[].{objnam,params} and
// objectList[].changes[].{objnam,params}.
func extractPushObjects(msg map[string]any) []pushObject {
	list, ok := msg["objectList"].([]any)
	if !ok {
		return nil
	}
	var out []pushObject
	for _, item := range list {
		if obj, ok := item.(map[string]any); ok {
			out = appendPushObjects(out, obj)
		}
	}
	return out
}

// appendPushObjects extracts the direct object and any nested "changes" entries
// from one objectList item.
func appendPushObjects(out []pushObject, obj map[string]any) []pushObject {
	if po, ok := toPushObject(obj); ok {
		out = append(out, po)
	}
	changes, ok := obj["changes"].([]any)
	if !ok {
		return out
	}
	for _, ch := range changes {
		if cm, ok := ch.(map[string]any); ok {
			if po, ok := toPushObject(cm); ok {
				out = append(out, po)
			}
		}
	}
	return out
}

func toPushObject(obj map[string]any) (pushObject, bool) {
	objnam, ok := obj["objnam"].(string)
	if !ok || objnam == "" {
		return pushObject{}, false
	}
	rawParams, ok := obj["params"].(map[string]any)
	if !ok {
		return pushObject{}, false
	}
	params := make(map[string]string, len(rawParams))
	for k, v := range rawParams {
		if s, ok := v.(string); ok {
			params[k] = s
		}
	}
	return pushObject{objnam: objnam, params: params}, true
}

// --- backoff helpers ------------------------------------------------------

func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func nextEngineDelay(d time.Duration) time.Duration {
	d *= 2
	if d > maxDelay {
		return maxDelay
	}
	return d
}
