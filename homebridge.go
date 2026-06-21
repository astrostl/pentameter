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
	"os"
	"os/signal"
	"sort"
	"sync"
	"syscall"

	"github.com/astrostl/pentameter/intellicenter"
)

const (
	hbCmdQueueSize = 32          // buffered set-command channel
	hbStdinInitBuf = 64 * 1024   // initial stdin scanner buffer
	hbStdinMaxBuf  = 1024 * 1024 // max stdin line length

	hbKindSwitch = "switch" // HomeKit accessory kind for a circuit
	hbMsgReady   = "ready"
	hbMsgAccess  = "accessories"
	hbMsgSet     = "set"
)

type hbAccessory struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	On   bool   `json:"on"`
}

type hbSet struct {
	T  string `json:"t"`
	ID string `json:"id"`
	On bool   `json:"on"`
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
	e.send(map[string]any{"t": "state", "id": id, "on": on})
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
	items := hbCircuitItems(engine.Snapshot())
	out.accessories(items)
	out.ready()
	p.mu.Lock()
	p.published = true
	p.mu.Unlock()
	log.Printf("[homebridge] published %d circuits", len(items))
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
		if ch.Circuit != nil && pub.isPublished() {
			out.state(ch.Circuit.ID, ch.Circuit.On)
		}
	}
}

// hbApplySets applies set commands from the shim. Pushes report the resulting
// state, so no explicit re-poll is needed.
func hbApplySets(engine *intellicenter.Engine, cmds <-chan hbSet) {
	for cmd := range cmds {
		if cmd.T != hbMsgSet {
			continue
		}
		if err := engine.SetCircuit(cmd.ID, cmd.On); err != nil {
			log.Printf("[homebridge] set %s=%v failed: %v", cmd.ID, cmd.On, err)
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
