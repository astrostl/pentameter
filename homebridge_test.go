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
