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
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/astrostl/pentameter/intellicenter"
)

const (
	hbReconnectMinDelay = 2 * time.Second
	hbReconnectMaxDelay = 30 * time.Second
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

func newHBEmitter() *hbEmitter { return &hbEmitter{w: bufio.NewWriter(os.Stdout)} }

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

func (e *hbEmitter) ready() { e.send(map[string]string{"t": "ready"}) }
func (e *hbEmitter) accessories(items []hbAccessory) {
	e.send(map[string]any{"t": "accessories", "items": items})
}
func (e *hbEmitter) state(id string, on bool) {
	e.send(map[string]any{"t": "state", "id": id, "on": on})
}

// runHomebridge is the entry point for `pentameter -homebridge`. ip may be empty
// to auto-discover. It owns its own resilient connect/reconnect loop.
func runHomebridge(ip, port string, pollInterval time.Duration) {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	out := newHBEmitter()
	cmds := make(chan hbSet, 32)
	go hbReadStdin(ctx, cmds)

	log.Printf("[homebridge] starting (poll=%v, configured ip=%q)", pollInterval, ip)

	delay := hbReconnectMinDelay
	for ctx.Err() == nil {
		target := ip
		if target == "" {
			discovered, err := DiscoverIntelliCenter(false)
			if err != nil {
				log.Printf("[homebridge] discovery failed: %v (retry in %s)", err, delay)
				if !hbSleep(ctx, delay) {
					break
				}
				delay = hbNextDelay(delay)
				continue
			}
			log.Printf("[homebridge] discovered IntelliCenter at %s", discovered)
			target = discovered
		}

		client := intellicenter.New(target, port)
		if err := client.Connect(ctx); err != nil {
			log.Printf("[homebridge] connect failed: %v (retry in %s)", err, delay)
			if !hbSleep(ctx, delay) {
				break
			}
			delay = hbNextDelay(delay)
			continue
		}
		log.Printf("[homebridge] connected to %s:%s", target, port)
		delay = hbReconnectMinDelay

		if err := hbServe(ctx, client, out, cmds, pollInterval); err != nil {
			log.Printf("[homebridge] session ended: %v", err)
		}
		client.Close()
		if !hbSleep(ctx, hbReconnectMinDelay) {
			break
		}
	}
	log.Printf("[homebridge] shutting down")
}

// hbServe runs one connected session: discover circuits, emit accessories, then
// poll + handle set commands until an error forces a reconnect.
func hbServe(ctx context.Context, client *intellicenter.Client, out *hbEmitter, cmds <-chan hbSet, pollInterval time.Duration) error {
	circuits, err := client.Circuits()
	if err != nil {
		return err
	}

	state := make(map[string]bool, len(circuits))
	items := make([]hbAccessory, 0, len(circuits))
	for _, c := range circuits {
		state[c.ID] = c.On
		items = append(items, hbAccessory{ID: c.ID, Name: c.Name, Kind: "switch", On: c.On})
	}
	out.accessories(items)
	out.ready()
	log.Printf("[homebridge] discovered %d circuits", len(items))

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case cmd := <-cmds:
			if cmd.T != "set" {
				continue
			}
			if err := client.SetCircuit(cmd.ID, cmd.On); err != nil {
				return err
			}
			if err := hbPoll(client, out, state); err != nil {
				return err
			}
		case <-ticker.C:
			if err := hbPoll(client, out, state); err != nil {
				return err
			}
		}
	}
}

// hbPoll queries current circuit state and emits an update for anything changed.
func hbPoll(client *intellicenter.Client, out *hbEmitter, state map[string]bool) error {
	circuits, err := client.Circuits()
	if err != nil {
		return err
	}
	for _, c := range circuits {
		if prev, ok := state[c.ID]; !ok || prev != c.On {
			state[c.ID] = c.On
			out.state(c.ID, c.On)
		}
	}
	return nil
}

// hbReadStdin parses newline-JSON commands from the shim. If stdin closes the
// shim (our parent) is gone, so exit rather than run orphaned.
func hbReadStdin(ctx context.Context, cmds chan<- hbSet) {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
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

func hbSleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func hbNextDelay(d time.Duration) time.Duration {
	d *= 2
	if d > hbReconnectMaxDelay {
		return hbReconnectMaxDelay
	}
	return d
}
