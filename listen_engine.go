package main

import (
	"context"
	"log"

	"github.com/astrostl/pentameter/intellicenter"
)

// runListenEngine serves listen/troubleshooting mode driven by the
// intellicenter.Engine: raw protocol traffic is dumped as the engine receives
// it, reusing the engine's connection/baseline/reconnect/poll machinery instead
// of a second hand-rolled connection loop.
//
// The PoolMonitor here is the interpretation + diff-state holder (never connected
// itself); the engine owns the connection. Two hooks reproduce the legacy output:
//
//   - OnRawPush: each unsolicited message is processed for a human summary and
//     echoed verbatim (PUSH: <json>), exactly as the old listenLoop did.
//   - OnRawPoll: after each scan, typed equipment is recomputed from the engine
//     snapshot (emitting POLL change lines) and the listen-only discovery queries
//     (circuit groups, all objects) run over the engine's request client.
func runListenEngine(cfg *appConfig) {
	pm := NewPoolMonitor(cfg.intelliCenterIP, cfg.intelliCenterPort, true)
	pm.initializeState()

	engine := intellicenter.NewEngine(cfg.intelliCenterIP, cfg.intelliCenterPort, cfg.pollInterval)
	engine.Logf = log.Printf
	engine.Resolve = newDiscoveryResolver(cfg)

	engine.OnRawPush = func(msg map[string]any) {
		pm.mu.Lock()
		defer pm.mu.Unlock()
		pm.processRawPushNotification(msg)
		pm.outputRawJSON("PUSH", msg)
	}

	engine.OnRawPoll = func(req *intellicenter.Client, baseline bool) {
		pm.mu.Lock()
		defer pm.mu.Unlock()
		pm.listenPoll(engine, req, baseline)
	}

	log.Println("Listening for real-time changes (Ctrl+C to stop)...")
	_ = engine.Run(context.Background())
}

// listenPoll reproduces a legacy listen poll over the engine's connection: it
// recomputes typed equipment from the engine snapshot (which emits the POLL
// change/detected lines via the listen track* helpers), then runs the
// listen-only discovery queries over the engine's request client. On a fresh
// baseline (post-connect/reconnect) it resets the diff state so a full
// "detected" report is produced, matching the legacy listenLoop reconnect path.
// Caller holds pm.mu.
func (pm *PoolMonitor) listenPoll(engine *intellicenter.Engine, req *intellicenter.Client, baseline bool) {
	pm.ic = req // route discovery queries through the engine's live connection
	if baseline {
		pm.previousState = nil
		pm.initializeState()
		pm.initialPollDone = false
	}
	wasInitial := !pm.initialPollDone
	pm.previousState.PollChangeCount = 0

	// Typed equipment: bodies → air → pumps → freeze → circuits → thermal,
	// same order as a legacy full poll.
	pm.refreshFromEngine(engine)

	// Listen-only discovery the typed scan doesn't cover.
	if err := pm.getCircuitGroups(); err != nil {
		log.Printf("Warning: failed to get circuit groups: %v", err)
	}
	if err := pm.getAllObjects(); err != nil {
		log.Printf("Warning: failed to get all objects: %v", err)
	}

	changes := pm.previousState.PollChangeCount
	pm.initialPollDone = true
	if !wasInitial && changes == 0 {
		log.Println("POLL: [no changes]")
	}
}
