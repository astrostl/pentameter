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
// as a safety net.
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
	engine.Resolve = newDiscoveryResolver(cfg)

	// Serialize recomputes: the push subscriber and the OnScan callback both
	// drive refreshFromEngine, which mutates shared PoolMonitor metric state.
	var mu sync.Mutex
	ready := false

	// Logging is change-gated in refreshFromEngine (logChangedf), so push- and
	// poll-driven recomputes both log only real transitions; no quiet toggle.
	recompute := func() {
		mu.Lock()
		defer mu.Unlock()
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
		recompute() // refresh at the engine's poll cadence (logs only changes)
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
				recompute()
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

	ln, err := bindMetricsServer(registry, pm, cfg.httpPort)
	if err != nil {
		log.Fatalf("HTTP server failed: %v", err)
	}
	log.Printf("Starting Prometheus metrics server on :%s", cfg.httpPort)
	log.Printf("Metrics available at http://localhost:%s/metrics", cfg.httpPort)
	if err := serveMetrics(ln); err != nil {
		log.Fatalf("HTTP server failed: %v", err)
	}
}

// refreshFromEngine recomputes every metric from the engine's current raw snapshot,
// reproducing a full poll. Object groups are applied in a fixed order
// (bodies → air → pumps → freeze → circuits → thermal) so dependent state
// (referenced heaters, freeze-protection active) is set first.
func (pm *PoolMonitor) refreshFromEngine(e *intellicenter.Engine) {
	pm.featureConfig = e.Config()

	var bodies, circuits, pumps, heaters, sensors, pmpCircs []ObjectData
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
		case intellicenter.KindPMPCirc:
			pmpCircs = append(pmpCircs, od)
		}
	}

	pm.applyBodyTemperatures(bodies)
	pm.applyAirTemperature(sensors)
	pm.applyPumpData(pumps, 0)         // sets pm.pumpRunning (RPM>0 per pump)
	pm.applyPumpAssociations(pmpCircs) // sets pm.circuitToPumps (circuit→pumps)
	pm.applyFreezeProtection(circuits) // _FEA2 lives among the circuit objects
	pm.applyCircuitStatus(circuits)    // gates circuit/feature ON on pump delivery
	pm.applyThermalStatus(heaters)
}
