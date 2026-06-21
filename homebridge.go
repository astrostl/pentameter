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
	"strings"
	"sync"
	"syscall"

	"github.com/astrostl/pentameter/intellicenter"
)

const (
	hbCmdQueueSize = 32          // buffered set-command channel
	hbStdinInitBuf = 64 * 1024   // initial stdin scanner buffer
	hbStdinMaxBuf  = 1024 * 1024 // max stdin line length

	hbKindSwitch     = "switch"     // HomeKit accessory kind for a circuit
	hbKindThermostat = "thermostat" // HomeKit accessory kind for a body+heater
	hbMsgReady       = "ready"
	hbMsgAccess      = "accessories"
	hbMsgState       = "state"
	hbMsgSet         = "set"
	hbMsgTSet        = "tset" // shim -> sidecar thermostat command
	hbFieldID        = "id"

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
	e.send(map[string]any{"t": hbMsgState, hbFieldID: id, "on": on})
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
	hbRun(ctx, engine, out, cmds)
	log.Printf("[homebridge] shutting down")
}

// hbPublisher gates state emission: circuit changes are only meaningful to the
// shim once the accessory list has been announced. It flips published at the
// first baseline and stays so across reconnects (which re-announce idempotently).
type hbPublisher struct {
	mu        sync.Mutex
	published bool
}

// announce (re)publishes the accessory list with current state and marks the
// shim ready, mirroring the old per-session announce on each (re)connect.
func (p *hbPublisher) announce(engine *intellicenter.Engine, out *hbEmitter) {
	snap := engine.Snapshot()
	items := hbCircuitItems(snap)
	thermos := hbThermostatItems(snap)
	out.accessories(append(items, thermos...))
	out.ready()
	p.mu.Lock()
	p.published = true
	p.mu.Unlock()
	log.Printf("[homebridge] published %d circuits, %d thermostats", len(items), len(thermos))
}

func (p *hbPublisher) isPublished() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.published
}

// hbRun wires an engine to the shim IPC and blocks on the engine run loop until
// ctx is canceled. Split out from runHomebridge so it can be driven in tests
// with an in-memory emitter.
func hbRun(ctx context.Context, engine *intellicenter.Engine, out *hbEmitter, cmds <-chan hbSet) {
	pub := &hbPublisher{}
	engine.OnRawPoll = func(_ *intellicenter.Client, baseline bool) {
		if baseline {
			pub.announce(engine, out)
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

// hbApplyThermostat applies one thermostat command (any subset of heat setpoint,
// cool setpoint, on/off mode) to a body via the engine.
func hbApplyThermostat(engine *intellicenter.Engine, cmd hbSet) {
	if cmd.HeatC != nil {
		if err := engine.SetHeatSetpoint(cmd.ID, cToF(*cmd.HeatC)); err != nil {
			log.Printf("[homebridge] heat setpoint %s failed: %v", cmd.ID, err)
		}
	}
	if cmd.CoolC != nil {
		if err := engine.SetCoolSetpoint(cmd.ID, cToF(*cmd.CoolC)); err != nil {
			log.Printf("[homebridge] cool setpoint %s failed: %v", cmd.ID, err)
		}
	}
	if cmd.Mode != "" {
		src := intellicenter.HeatSourceNone
		if cmd.Mode == hbModeOn {
			heaters := realHeatersByBody(engine.Snapshot())
			heater, ok := heaters[cmd.ID]
			if !ok {
				log.Printf("[homebridge] mode on for %s: no real heater; ignoring", cmd.ID)
				return
			}
			src = heater.ID
		}
		if err := engine.SetHeatSource(cmd.ID, src); err != nil {
			log.Printf("[homebridge] heat source %s=%s failed: %v", cmd.ID, src, err)
		}
	}
}

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
		heat := fToC(body.LoSetTemp)
		item := hbAccessory{
			ID: body.ID, Name: body.Name, Kind: hbKindThermostat,
			CurC: &cur, HeatC: &heat, CanCool: heater.Cool, State: bodyHeatState(&body),
		}
		if heater.Cool {
			cool := fToC(body.HiSetTemp)
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
	heat := fToC(b.LoSetTemp)
	cool := fToC(b.HiSetTemp)
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
