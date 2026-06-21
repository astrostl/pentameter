package main

import (
	"context"
	"log"
	"sync"

	"github.com/astrostl/pentameter/intellicenter"
	"github.com/prometheus/client_golang/prometheus"
)

// runMetricsEngine serves Prometheus metrics driven by the intellicenter.Engine:
// gauges refresh on every IntelliCenter push (real-time) with the engine's poll
// as a safety net. Opt-in via -engine / PENTAMETER_ENGINE; the legacy poll path
// (StartTemperaturePolling) is left untouched as the default.
//
// The PoolMonitor here is used purely as the interpretation + metric-state holder
// (listenMode=false, never connected); the engine owns the connection, push
// stream, polling and reconnect. Full-fidelity gauge values come from recomputing
// the entire equipment set out of the engine's raw snapshot — identical to a
// legacy poll — so cross-object logic (freeze protection, thermal interpretation,
// feature visibility, stale cleanup) stays exactly as published.
func runMetricsEngine(cfg *appConfig, registry *prometheus.Registry) {
	pm := NewPoolMonitor(cfg.intelliCenterIP, cfg.intelliCenterPort, false)
	engine := intellicenter.NewEngine(cfg.intelliCenterIP, cfg.intelliCenterPort, cfg.pollInterval)
	engine.Logf = log.Printf

	// Serialize recomputes: the push subscriber and the OnScan callback both
	// drive refreshFromEngine, which mutates shared PoolMonitor metric state.
	var mu sync.Mutex
	ready := false

	recompute := func(quiet bool) {
		mu.Lock()
		defer mu.Unlock()
		pm.quiet = quiet
		defer func() { pm.quiet = false }()
		pm.refreshFromEngine(engine)
	}

	engine.OnScan = func(err error) {
		if err != nil {
			connectionFailure.Set(1)
			return
		}
		connectionFailure.Set(0)
		mu.Lock()
		ready = true
		mu.Unlock()
		recompute(false) // full, logged refresh at the engine's poll cadence
		pm.updateRefreshTimestamp()
	}

	// Push-driven freshness: every change recomputes (quietly) between polls.
	changes := engine.Subscribe()
	go func() {
		for range changes {
			mu.Lock()
			r := ready
			mu.Unlock()
			if r {
				recompute(true)
			}
		}
	}()

	go func() { _ = engine.Run(context.Background()) }()

	// Advertise over mDNS so this exporter is discoverable, matching the legacy path.
	if adv, err := StartMDNSAdvertiser(cfg.httpPort, false); err != nil {
		log.Printf("Warning: mDNS advertisement disabled: %v", err)
	} else {
		defer func() {
			if cerr := adv.Close(); cerr != nil {
				log.Printf("Error closing mDNS advertiser: %v", cerr)
			}
		}()
	}

	setupHTTPEndpoints(registry, pm, cfg.httpPort)
}

// refreshFromEngine recomputes every metric from the engine's current raw snapshot,
// reproducing a legacy full poll. Object groups are applied in the same order as
// GetAllEquipmentStatus (bodies → air → pumps → freeze → circuits → thermal) so
// dependent state (referenced heaters, freeze-protection active) is set first.
func (pm *PoolMonitor) refreshFromEngine(e *intellicenter.Engine) {
	pm.featureConfig = e.Config()

	var bodies, circuits, pumps, heaters, sensors []ObjectData
	for _, o := range e.RawObjects() {
		od := ObjectData{ObjName: o.ObjName, Params: o.Params}
		switch o.Kind {
		case intellicenter.KindBody:
			bodies = append(bodies, od)
		case intellicenter.KindCircuit:
			circuits = append(circuits, od)
		case intellicenter.KindPump:
			pumps = append(pumps, od)
		case intellicenter.KindHeater:
			heaters = append(heaters, od)
		case intellicenter.KindSensor:
			sensors = append(sensors, od)
		}
	}

	pm.applyBodyTemperatures(bodies)
	pm.applyAirTemperature(sensors)
	pm.applyPumpData(pumps, 0)
	pm.applyFreezeProtection(circuits) // _FEA2 lives among the circuit objects
	pm.applyCircuitStatus(circuits)
	pm.applyThermalStatus(heaters)
}
