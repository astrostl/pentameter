package main

// Homebridge mode: pentameter as the sidecar for the
// homebridge-pentair-intellicenter-ai-ng plugin. It speaks a tiny
// newline-delimited JSON protocol to the Node shim over stdio:
//
//	stdout (sidecar -> shim):  {"t":"ready"}
//	                           {"t":"accessories","items":[{"id","name","kind","on"}]}
//	                           {"t":"state","id","on"}
//	stdin  (shim -> sidecar):  {"t":"set","id","on"}
//	stderr (sidecar -> shim):  human logs (Go's log pkg; shim forwards to Homebridge)
//
// stdout is the IPC channel, so nothing else may write to it. All diagnostics go
// through the standard log package, which writes to stderr.

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log"
	"math"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/astrostl/pentameter/intellicenter"
)

const (
	hbCmdQueueSize = 32          // buffered set-command channel
	hbStdinInitBuf = 64 * 1024   // initial stdin scanner buffer
	hbStdinMaxBuf  = 1024 * 1024 // max stdin line length

	hbKindSwitch     = "switch"      // HomeKit accessory kind for a circuit
	hbKindThermostat = "thermostat"  // HomeKit accessory kind for a body+heater
	hbKindLight      = "lightsensor" // read-only LightSensor: a raw metric encoded as lux
	hbKindOccupancy  = "occupancy"   // read-only OccupancySensor: a boolean system state
	hbKindTempSensor = "tempsensor"  // read-only TemperatureSensor (e.g. air temp)
	hbMsgReady       = "ready"
	hbMsgAccess      = "accessories"
	hbMsgState       = "state"
	hbMsgLState      = "lstate" // sidecar -> shim light-sensor (lux) value
	hbMsgSState      = "sstate" // sidecar -> shim temperature-sensor (Celsius) value
	hbMsgSet         = "set"
	hbMsgTSet        = "tset" // shim -> sidecar thermostat command
	hbFieldID        = "id"
	hbFieldOn        = "on"

	hbSubTypeFreeze = "FRZ"               // SUBTYP of the freeze-protection feature circuit (_FEA2)
	hbFreezeName    = "Freeze Protection" // display name for the freeze occupancy sensor

	// Connection-health sensor: a synthetic OccupancySensor, "detected" when the
	// sidecar is connected to the controller. Driven by the engine's scan result
	// (Go) and by the shim on sidecar process death. hbConnID is shared with the
	// shim (CONNECTION_ID) so it can flip the sensor offline when Go can't.
	hbConnID      = "_conn"
	hbConnName    = "Pool Controller Online"
	hbSensorRPM   = "rpm"
	hbSensorWatts = "watts"
	hbSensorGPM   = "gpm"
	hbSensorRun   = "run" // suffix for a pump's "running" occupancy sensor

	hbModeOn = "on" // tset mode: assign the body's heater (vs "off" = no heater)

	hbHeatSrcNone  = "00000" // HTSRC value meaning "no heater assigned" (heat off)
	hbHeatModeCool = 9       // HTMODE: heat-pump actively cooling

	// Thermostat state strings sent to the shim (off/idle reuse statusWord*).
	hbStateHeat = "heat"
	hbStateCool = "cool"

	fToCRound = 10 // round Celsius to 0.1°
)

type hbAccessory struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	On   bool   `json:"on"`

	// Thermostat fields (kind=="thermostat"). Temperatures are Celsius (HomeKit's
	// internal unit); the shim sets the display unit. Pointers so a real 0 isn't
	// confused with "absent".
	CurC    *float64 `json:"curC,omitempty"`  // current water temperature
	HeatC   *float64 `json:"heatC,omitempty"` // heat setpoint (LOTMP)
	CoolC   *float64 `json:"coolC,omitempty"` // cool setpoint (HITMP), if CanCool
	CanCool bool     `json:"canCool,omitempty"`
	State   string   `json:"state,omitempty"` // off | idle | heat | cool

	// Light-sensor field (kind=="lightsensor"). The raw metric value, which the
	// shim encodes as lux (HomeKit has no read-only numeric tile; lux is the
	// least-bad raw-number channel). Pointer so a real 0 isn't confused with absent.
	Lux *float64 `json:"lux,omitempty"`
}

type hbSet struct {
	T  string `json:"t"`
	ID string `json:"id"`
	On bool   `json:"on"`

	// Thermostat command fields (t=="tset"). Pointers so an absent field is
	// distinguishable from a zero value; each is applied independently as the
	// matching HomeKit characteristic changes.
	HeatC *float64 `json:"heatC,omitempty"` // heat setpoint (Celsius) -> LOTMP
	CoolC *float64 `json:"coolC,omitempty"` // cool setpoint (Celsius) -> HITMP
	Mode  string   `json:"mode,omitempty"`  // off | on  -> HTSRC (none / heater)
}

// hbEmitter serializes newline-JSON writes to stdout.
type hbEmitter struct {
	mu sync.Mutex
	w  *bufio.Writer
}

func newHBEmitter(w io.Writer) *hbEmitter { return &hbEmitter{w: bufio.NewWriter(w)} }

func (e *hbEmitter) send(v any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	_, _ = e.w.Write(b)
	_ = e.w.WriteByte('\n')
	_ = e.w.Flush()
}

func (e *hbEmitter) ready() { e.send(map[string]string{"t": hbMsgReady}) }
func (e *hbEmitter) accessories(items []hbAccessory) {
	e.send(map[string]any{"t": hbMsgAccess, "items": items})
}

func (e *hbEmitter) state(id string, on bool) {
	e.send(map[string]any{"t": hbMsgState, hbFieldID: id, hbFieldOn: on})
}

// lstate pushes a light-sensor's raw value (the shim encodes it as lux).
func (e *hbEmitter) lstate(id string, lux float64) {
	e.send(map[string]any{"t": hbMsgLState, hbFieldID: id, "lux": lux})
}

// sstate pushes a temperature-sensor's current value in Celsius (HomeKit's unit).
func (e *hbEmitter) sstate(id string, c float64) {
	e.send(map[string]any{"t": hbMsgSState, hbFieldID: id, "c": c})
}

// tstate pushes a thermostat's live values (Celsius) on a body change.
func (e *hbEmitter) tstate(t *hbAccessory) {
	e.send(map[string]any{
		"t": "tstate", hbFieldID: t.ID,
		"curC": t.CurC, "heatC": t.HeatC, "coolC": t.CoolC, "state": t.State,
	})
}

// runHomebridge is the entry point for `pentameter -homebridge`. It drives the
// shim from the intellicenter.Engine: the engine owns connection, baseline,
// reconnect, polling and mDNS rediscovery (via Resolve), while this adapter maps
// engine events to the stdio IPC protocol. Circuit state reaches HomeKit in real
// time off the engine's push stream instead of waiting for the next poll.
func runHomebridge(cfg *appConfig) {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	out := newHBEmitter(os.Stdout)
	cmds := make(chan hbSet, hbCmdQueueSize)
	go hbReadStdin(ctx, cmds)

	engine := intellicenter.NewEngine(cfg.intelliCenterIP, cfg.intelliCenterPort, cfg.pollInterval)
	engine.Logf = log.Printf
	engine.Resolve = newDiscoveryResolver(cfg)

	log.Printf("[homebridge] starting (poll=%v, configured ip=%q)", cfg.pollInterval, cfg.intelliCenterIP)
	hbRun(ctx, engine, out, cmds, cfg.httpPort)
	log.Printf("[homebridge] shutting down")
}

// hbMetrics drives Prometheus metrics off the homebridge engine, reusing the
// metrics-mode interpretation (PoolMonitor.refreshFromEngine). With it, a single
// homebridge sidecar emits BOTH HomeKit (stdio IPC) and Grafana (HTTP /metrics)
// from one controller connection. Read-only and on a separate TCP port, so it
// can't interfere with the IPC channel or with writes.
type hbMetrics struct {
	pm    *PoolMonitor
	adv   *MDNSAdvertiser
	mu    sync.Mutex
	ready bool
}

// startHBMetrics registers the gauges, serves /metrics, and starts a push-driven
// recompute. It returns a handle whose onScan does the full poll-cadence refresh.
func startHBMetrics(engine *intellicenter.Engine, port string) *hbMetrics {
	met := &hbMetrics{pm: NewPoolMonitor("", "", false)}
	registry := createPrometheusRegistry()

	// Push-driven freshness: recompute (quietly) on every change between polls.
	// A second engine subscriber, independent of the shim IPC subscriber.
	changes := engine.Subscribe()
	go func() {
		for range changes {
			met.mu.Lock()
			r := met.ready
			met.mu.Unlock()
			if r {
				met.recompute(engine, true)
			}
		}
	}()

	go setupHTTPEndpoints(registry, met.pm, port) // blocks in its own goroutine

	// Advertise the metrics endpoint over mDNS, matching standalone metrics mode.
	// (Note: ineffective from inside bridge-networked Docker — same limitation
	// that requires a static IP there — but correct when run on the host/LAN.)
	if adv, err := StartMDNSAdvertiser(port, false); err != nil {
		log.Printf("[homebridge] mDNS advertisement disabled: %v", err)
	} else {
		met.adv = adv
	}
	log.Printf("[homebridge] serving Prometheus metrics on :%s/metrics (mDNS-advertised)", port)
	return met
}

// close releases the mDNS advertiser on shutdown.
func (m *hbMetrics) close() {
	if m == nil || m.adv == nil {
		return
	}
	if err := m.adv.Close(); err != nil {
		log.Printf("[homebridge] error closing mDNS advertiser: %v", err)
	}
}

func (m *hbMetrics) recompute(engine *intellicenter.Engine, quiet bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pm.quiet = quiet
	defer func() { m.pm.quiet = false }()
	m.pm.refreshFromEngine(engine)
}

// onScan mirrors runMetricsEngine: a failed scan flags the connection-failure
// gauge; a successful scan does a full logged refresh at the poll cadence.
func (m *hbMetrics) onScan(engine *intellicenter.Engine, err error) {
	if err != nil {
		connectionFailure.Set(1)
		return
	}
	connectionFailure.Set(0)
	m.mu.Lock()
	m.ready = true
	m.mu.Unlock()
	m.recompute(engine, false)
	m.pm.updateRefreshTimestamp()
}

// hbPublisher gates state emission: circuit changes are only meaningful to the
// shim once the accessory list has been announced. It flips published at the
// first baseline and stays so across reconnects (which re-announce idempotently).
type hbPublisher struct {
	mu        sync.Mutex
	published bool
	lastSig   string // membership signature of the last announced accessory list
}

// announce publishes the accessory list with current state and marks the shim
// ready, mirroring the old per-session announce on each (re)connect.
func (p *hbPublisher) announce(engine *intellicenter.Engine, out *hbEmitter) {
	items := hbAllItems(engine.Snapshot())
	out.accessories(items)
	out.ready()
	p.mu.Lock()
	p.published = true
	p.lastSig = accessorySignature(items)
	p.mu.Unlock()
	log.Printf("[homebridge] published %d accessories", len(items))
}

// resync re-announces only when the *set* of accessories changed (a Feature or
// heater appeared/disappeared, a rename, cool-capability flip) since the last
// announce — so an IntelliCenter config change self-heals on the next poll
// without a sidecar restart. State-only changes flow through state/tstate and
// don't trip this, so there's no announce spam.
func (p *hbPublisher) resync(engine *intellicenter.Engine, out *hbEmitter) {
	items := hbAllItems(engine.Snapshot())
	sig := accessorySignature(items)
	p.mu.Lock()
	changed := p.published && sig != p.lastSig
	if changed {
		p.lastSig = sig
	}
	p.mu.Unlock()
	if !changed {
		return
	}
	out.accessories(items)
	log.Printf("[homebridge] accessory set changed; re-announced %d accessories", len(items))
}

func (p *hbPublisher) isPublished() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.published
}

// hbAllItems is the full announced accessory list: circuits (Features),
// thermostats, pump light-sensors, pump "running" sensors, then the freeze
// sensor — each group already sorted by ID for stable output.
func hbAllItems(snap intellicenter.Snapshot) []hbAccessory {
	items := append(hbCircuitItems(snap), hbThermostatItems(snap)...)
	items = append(items, hbPumpSensorItems(snap)...)
	items = append(items, hbPumpRunningItems(snap)...)
	items = append(items, hbSensorItems(snap)...)
	items = append(items, hbFreezeItems(snap)...)
	// Connection-health sensor. Announce happens only after a successful baseline,
	// so we're connected at announce time → On (detected) = true. Live changes flow
	// via OnScan -> state(hbConnID, ...).
	return append(items, hbAccessory{ID: hbConnID, Name: hbConnName, Kind: hbKindOccupancy, On: true})
}

// hbSensorItems builds a read-only TemperatureSensor per valid temperature sensor
// (e.g. the air sensor _A135). The controller reports °F (PROBE); HomeKit wants
// Celsius, so values are converted. Water temp is already on each thermostat, so
// only standalone sensors (air, solar) surface here.
func hbSensorItems(snap intellicenter.Snapshot) []hbAccessory {
	ids := make([]string, 0, len(snap.Sensors))
	for id := range snap.Sensors {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	items := make([]hbAccessory, 0, len(ids))
	for _, id := range ids {
		s := snap.Sensors[id]
		if !s.Valid || s.Name == "" {
			continue
		}
		c := fToC(s.Temp)
		items = append(items, hbAccessory{ID: s.ID, Name: s.Name, Kind: hbKindTempSensor, CurC: &c})
	}
	return items
}

// hbPumpRunningItems builds a read-only OccupancySensor per pump reporting
// whether it's running (RPM > 0). Unlike the metric LightSensors (a readout),
// an occupancy sensor can drive HomeKit automations/notifications on pump
// start/stop. IDs are suffixed so they don't collide with the metric sensors.
func hbPumpRunningItems(snap intellicenter.Snapshot) []hbAccessory {
	ids := make([]string, 0, len(snap.Pumps))
	for id := range snap.Pumps {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	items := make([]hbAccessory, 0, len(ids))
	for _, id := range ids {
		pump := snap.Pumps[id]
		items = append(items, hbAccessory{
			ID: pump.ID + "." + hbSensorRun, Name: pump.Name + " Running", Kind: hbKindOccupancy, On: pump.On,
		})
	}
	return items
}

// hbPumpSensorItems builds read-only LightSensors carrying each pump's raw
// metrics (RPM, WATTS, GPM) as lux — the only HomeKit element that shows a true
// number read-only. IDs are suffixed so they don't collide with the pump's Fan.
// GPM is emitted only when the pump is flow-capable (MaxFlow > 0); otherwise the
// controller's GPM is an estimate and we suppress it.
func hbPumpSensorItems(snap intellicenter.Snapshot) []hbAccessory {
	ids := make([]string, 0, len(snap.Pumps))
	for id := range snap.Pumps {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var items []hbAccessory
	for _, id := range ids {
		pump := snap.Pumps[id]
		add := func(metric, label string, val float64) {
			v := val
			items = append(items, hbAccessory{
				ID: pump.ID + "." + metric, Name: pump.Name + " " + label, Kind: hbKindLight, Lux: &v,
			})
		}
		add(hbSensorRPM, "RPM", pump.RPM)
		add(hbSensorWatts, "Watts", pump.Watts)
		if pump.MaxFlow > 0 { // only flow-capable pumps report a real GPM
			add(hbSensorGPM, "GPM", pump.GPM)
		}
	}
	return items
}

// hbFreezeItems builds a read-only OccupancySensor for freeze protection, driven
// by the freeze-protection feature circuit (SUBTYP=FRZ, e.g. _FEA2). STATUS=ON
// means freeze protection is actively running (API.md, verified on hardware).
func hbFreezeItems(snap intellicenter.Snapshot) []hbAccessory {
	ids := make([]string, 0, len(snap.Circuits))
	for id := range snap.Circuits {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		c := snap.Circuits[id]
		if c.SubType == hbSubTypeFreeze {
			return []hbAccessory{{ID: c.ID, Name: hbFreezeName, Kind: hbKindOccupancy, On: c.On}}
		}
	}
	return nil
}

// accessorySignature captures the membership-relevant identity of the accessory
// list (which accessories exist, their name/kind/cool-capability) but not their
// live state — so it changes on add/remove/rename, not on an on/off or temp tick.
func accessorySignature(items []hbAccessory) string {
	var sig strings.Builder
	for i := range items {
		it := &items[i]
		sig.WriteString(it.ID)
		sig.WriteByte('|')
		sig.WriteString(it.Name)
		sig.WriteByte('|')
		sig.WriteString(it.Kind)
		sig.WriteByte('|')
		if it.CanCool {
			sig.WriteByte('c')
		}
		sig.WriteByte('\n')
	}
	return sig.String()
}

// hbRun wires an engine to the shim IPC and blocks on the engine run loop until
// ctx is canceled. Split out from runHomebridge so it can be driven in tests
// with an in-memory emitter.
func hbRun(ctx context.Context, engine *intellicenter.Engine, out *hbEmitter, cmds <-chan hbSet, metricsPort string) {
	pub := &hbPublisher{}
	engine.OnRawPoll = func(_ *intellicenter.Client, baseline bool) {
		if baseline {
			pub.announce(engine, out)
		} else {
			pub.resync(engine, out)
		}
	}
	// Prometheus metrics: one sidecar serves both HomeKit and Grafana. Always on
	// in production (httpPort has a default); tests pass "" to skip binding a port.
	var metrics *hbMetrics
	if metricsPort != "" {
		metrics = startHBMetrics(engine, metricsPort)
		defer metrics.close()
	}
	// Connection health: report connected/disconnected to the shim on change.
	// OnScan fires nil on every successful scan and an error on connect/session
	// failure. Emit only on change (and only once announced) to avoid spam.
	lastConn, firstScan := false, true
	engine.OnScan = func(err error) {
		if metrics != nil {
			metrics.onScan(engine, err) // full metric refresh + liveness gauges
		}
		connected := err == nil
		if !firstScan && connected == lastConn {
			return
		}
		firstScan, lastConn = false, connected
		if pub.isPublished() {
			out.state(hbConnID, connected)
		}
	}
	go hbForwardChanges(engine.Subscribe(), out, pub)
	go hbApplySets(engine, cmds)
	_ = engine.Run(ctx)
}

// hbForwardChanges emits a state msg for every circuit change once accessories
// have been announced. Pushes and poll diffs both arrive here.
func hbForwardChanges(changes <-chan intellicenter.Change, out *hbEmitter, pub *hbPublisher) {
	for ch := range changes {
		if !pub.isPublished() {
			continue
		}
		switch {
		case ch.Circuit != nil:
			out.state(ch.Circuit.ID, ch.Circuit.On)
		case ch.Body != nil:
			// Body temp / setpoint / heat-mode changed → refresh its thermostat.
			// Bodies without a thermostat are simply unknown to the shim, which
			// ignores the update.
			t := thermostatStateFromBody(ch.Body)
			out.tstate(&t)
		case ch.Pump != nil:
			// Pump changed (poll-only) → refresh its metric LightSensors and its
			// "running" occupancy sensor (which rides the circuit state message).
			pump := ch.Pump
			out.lstate(pump.ID+"."+hbSensorRPM, pump.RPM)
			out.lstate(pump.ID+"."+hbSensorWatts, pump.Watts)
			if pump.MaxFlow > 0 {
				out.lstate(pump.ID+"."+hbSensorGPM, pump.GPM)
			}
			out.state(pump.ID+"."+hbSensorRun, pump.On)
		case ch.Sensor != nil:
			// Temperature sensor (e.g. air) changed → push its Celsius value.
			if ch.Sensor.Valid {
				out.sstate(ch.Sensor.ID, fToC(ch.Sensor.Temp))
			}
		}
	}
}

// hbApplySets applies set commands from the shim. Pushes report the resulting
// state, so no explicit re-poll is needed.
func hbApplySets(engine *intellicenter.Engine, cmds <-chan hbSet) {
	for cmd := range cmds {
		switch cmd.T {
		case hbMsgSet:
			if err := engine.SetCircuit(cmd.ID, cmd.On); err != nil {
				log.Printf("[homebridge] set %s=%v failed: %v", cmd.ID, cmd.On, err)
			}
		case hbMsgTSet:
			hbApplyThermostat(engine, cmd)
		}
	}
}

// hbWant records an intended ("ought") body value for one HomeKit command, so we
// can reconcile it against the controller's actual ("is") state after the write.
type hbWant struct {
	label string // human label for logs, e.g. "heat setpoint"
	want  string // intended value, stringified to match the read-side param
	got   func(*intellicenter.Body) string
}

// hbApplyThermostat applies one thermostat command (any subset of heat setpoint,
// cool setpoint, on/off mode) to a body, then verifies the controller actually
// took the change. The verify step (1) corrects HomeKit to the controller's real
// state immediately — even when a write is rejected and nothing changed, which is
// what left a stale "optimistic" value stuck before — and (2) logs an explicit
// is-vs-ought delta when the controller didn't obey.
func hbApplyThermostat(engine *intellicenter.Engine, cmd hbSet) {
	body, ok := engine.Snapshot().Bodies[cmd.ID]
	if !ok {
		log.Printf("[homebridge] thermostat set for unknown body %s; ignoring", cmd.ID)
		return
	}

	// Dedupe against the controller's current value (everything compared in
	// IntelliCenter's native units: whole °F, heater objnam). HomeKit works in a
	// 0.5°C grid that doesn't line up with whole °F, so it re-sends slightly
	// different values that map to the SAME °F; writing those would create a
	// write -> re-read -> push -> re-set feedback loop. Skipping no-op writes
	// breaks the loop and keeps redundant writes off the controller.
	var wants []hbWant
	attempted := false

	// write issues one field write only when want differs from the controller's
	// current value (cur), recording a hbWant for post-write verification.
	write := func(label, want, cur string, apply func() error, got func(*intellicenter.Body) string) {
		if want == cur {
			return
		}
		attempted = true
		if err := apply(); err != nil {
			log.Printf("[homebridge] %s %s failed: %v", label, cmd.ID, err)
		}
		wants = append(wants, hbWant{label: label, want: want, got: got})
	}

	if cmd.HeatC != nil {
		tempF := cToF(*cmd.HeatC)
		write("heat setpoint (LOTMP)", itoa(tempF), itoa(int(body.LoSetTemp)),
			func() error { return engine.SetHeatSetpoint(cmd.ID, tempF) },
			func(b *intellicenter.Body) string { return itoa(int(b.LoSetTemp)) })
	}
	if cmd.CoolC != nil {
		tempF := cToF(*cmd.CoolC)
		write("cool setpoint (HITMP)", itoa(tempF), itoa(int(body.HiSetTemp)),
			func() error { return engine.SetCoolSetpoint(cmd.ID, tempF) },
			func(b *intellicenter.Body) string { return itoa(int(b.HiSetTemp)) })
	}
	if cmd.Mode != "" {
		if src, ok := hbHeatSourceFor(engine, cmd); ok {
			write("heat source (HTSRC)", src, body.HeaterID,
				func() error { return engine.SetHeatSource(cmd.ID, src) },
				func(b *intellicenter.Body) string { return b.HeaterID })
		}
	}

	// Only re-read/re-emit when we actually wrote something; echoing state on a
	// no-op is exactly what would re-feed the loop.
	if attempted {
		hbVerifyBody(engine, cmd.ID, wants)
	}
}

// hbHeatSourceFor resolves the HTSRC value a mode command wants: the body's real
// heater objnam for "on", or HeatSourceNone for "off". ok is false when "on" is
// asked for a body with no real heater (nothing to assign).
func hbHeatSourceFor(engine *intellicenter.Engine, cmd hbSet) (string, bool) {
	if cmd.Mode != hbModeOn {
		return intellicenter.HeatSourceNone, true
	}
	heater, found := realHeatersByBody(engine.Snapshot())[cmd.ID]
	if !found {
		log.Printf("[homebridge] mode on for %s: no real heater; ignoring", cmd.ID)
		return "", false
	}
	return heater.ID, true
}

// hbVerifyBody re-reads the body from the controller (which force-emits its true
// state to HomeKit, undoing any optimistic display) and logs any field where the
// controller's actual value doesn't match what HomeKit asked for.
func hbVerifyBody(engine *intellicenter.Engine, bodyID string, wants []hbWant) {
	if err := engine.RefreshBody(bodyID); err != nil {
		log.Printf("[homebridge] verify %s: re-read failed: %v", bodyID, err)
		return
	}
	body, ok := engine.Snapshot().Bodies[bodyID]
	if !ok {
		return
	}
	for _, w := range wants {
		if got := w.got(&body); got != w.want {
			log.Printf("[homebridge] STATE DELTA on %s: HomeKit asked %s=%s but controller reports %s "+
				"(write not applied; HomeKit corrected to controller state)", bodyID, w.label, w.want, got)
		}
	}
}

// itoa is strconv.Itoa, aliased locally to keep call sites terse.
func itoa(n int) string { return strconv.Itoa(n) }

// hbCircuitItems builds the accessory list from an engine snapshot, sorted by ID
// for stable output across (re)announces.
func hbCircuitItems(snap intellicenter.Snapshot) []hbAccessory {
	ids := make([]string, 0, len(snap.Circuits))
	for id := range snap.Circuits {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	items := make([]hbAccessory, 0, len(ids))
	for _, id := range ids {
		c := snap.Circuits[id]
		if !c.Feature { // only circuits flagged as Features in IntelliCenter
			continue
		}
		items = append(items, hbAccessory{ID: c.ID, Name: c.Name, Kind: hbKindSwitch, On: c.On})
	}
	return items
}

// hbThermostatItems builds one Thermostat per body that has a real (configured,
// non-pseudo) heater. The heater's COOL flag decides heat-only vs heat+cool.
// Bodies with no real heater get no thermostat.
func hbThermostatItems(snap intellicenter.Snapshot) []hbAccessory {
	heaters := realHeatersByBody(snap)

	ids := make([]string, 0, len(snap.Bodies))
	for id := range snap.Bodies {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var items []hbAccessory
	for _, id := range ids {
		heater, ok := heaters[id]
		if !ok {
			continue
		}
		body := snap.Bodies[id]
		cur := fToC(body.Temp)
		heat := fToCStep(body.LoSetTemp)
		item := hbAccessory{
			ID: body.ID, Name: body.Name, Kind: hbKindThermostat,
			CurC: &cur, HeatC: &heat, CanCool: heater.Cool, State: bodyHeatState(&body),
		}
		if heater.Cool {
			cool := fToCStep(body.HiSetTemp)
			item.CoolC = &cool
		}
		items = append(items, item)
	}
	return items
}

// realHeatersByBody maps each body ID to the heater that serves it, skipping
// pseudo "Preferred" objects. If several real heaters serve one body (a true
// hybrid), the lowest heater ID wins — deterministic, and good enough until we
// model multi-source bodies.
func realHeatersByBody(snap intellicenter.Snapshot) map[string]intellicenter.Heater {
	ids := make([]string, 0, len(snap.Heaters))
	for id := range snap.Heaters {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	out := map[string]intellicenter.Heater{}
	for _, id := range ids {
		heater := snap.Heaters[id]
		if !heater.Real {
			continue // pseudo "Preferred"/combo object
		}
		for _, bodyID := range strings.Fields(heater.Body) {
			if _, taken := out[bodyID]; !taken {
				out[bodyID] = heater
			}
		}
	}
	return out
}

// thermostatStateFromBody builds a thermostat state update (Celsius) from a body
// alone — used for live pushes, where CanCool was already fixed at announce.
func thermostatStateFromBody(b *intellicenter.Body) hbAccessory {
	cur := fToC(b.Temp)
	heat := fToCStep(b.LoSetTemp)
	cool := fToCStep(b.HiSetTemp)
	return hbAccessory{ID: b.ID, CurC: &cur, HeatC: &heat, CoolC: &cool, State: bodyHeatState(b)}
}

// bodyHeatState reports the body's current climate state for HomeKit:
// off (no heater assigned), heat, cool, or idle (assigned but setpoint satisfied).
func bodyHeatState(body *intellicenter.Body) string {
	if body.HeaterID == "" || body.HeaterID == hbHeatSrcNone {
		return statusWordOff
	}
	switch {
	case body.HeatMode == hbHeatModeCool:
		return hbStateCool
	case body.HeatMode >= 1: // heating (1 = heater, 4 = heat pump)
		return hbStateHeat
	default:
		return statusWordIdle // heater assigned, setpoint satisfied
	}
}

// fToC converts Fahrenheit (IntelliCenter's unit) to Celsius (HomeKit's unit),
// rounded to 0.1° to avoid noisy float tails.
func fToC(f float64) float64 {
	const (
		freezeF = 32.0
		ratio   = 1.8
	)
	c := (f - freezeF) / ratio
	return math.Round(c*fToCRound) / fToCRound
}

// fToCStep converts a setpoint to Celsius snapped to HomeKit's 0.5° grid (its
// thermostat minStep). Pushing grid-aligned values stops HomeKit from coercing
// an off-grid value and re-sending it — the source of the setpoint feedback loop.
func fToCStep(tempF float64) float64 {
	const (
		freezeF  = 32.0
		ratio    = 1.8
		halfStep = 2.0 // round to nearest 0.5 == round(x*2)/2
	)
	c := (tempF - freezeF) / ratio
	return math.Round(c*halfStep) / halfStep
}

// cToF converts a HomeKit Celsius setpoint to the nearest whole Fahrenheit
// degree, IntelliCenter's native setpoint unit.
func cToF(c float64) int {
	const (
		freezeF = 32.0
		ratio   = 1.8
	)
	return int(math.Round(c*ratio + freezeF))
}

// hbReadStdin parses newline-JSON commands from the shim. If stdin closes the
// shim (our parent) is gone, so exit rather than run orphaned.
func hbReadStdin(ctx context.Context, cmds chan<- hbSet) {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, hbStdinInitBuf), hbStdinMaxBuf)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var cmd hbSet
		if err := json.Unmarshal(line, &cmd); err != nil {
			log.Printf("[homebridge] bad command: %v", err)
			continue
		}
		select {
		case cmds <- cmd:
		case <-ctx.Done():
			return
		}
	}
	if ctx.Err() == nil {
		log.Printf("[homebridge] stdin closed (shim gone); exiting")
		os.Exit(0)
	}
}
