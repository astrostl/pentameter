package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/astrostl/pentameter/intellicenter"
)

// TRMNL (https://docs.trmnl.com/) is an e-ink display. Pentameter pushes pool
// data to a TRMNL "Private Plugin" using its Webhook strategy: we POST the
// current state to the plugin's webhook URL, TRMNL stores the latest payload and
// renders it with the plugin's Liquid markup. Push (rather than TRMNL polling a
// URL) keeps pentameter on its local network with no inbound exposure. See
// TRMNL.md for setup and the sample template.
//
// This pusher is intentionally self-contained: it owns its own intellicenter
// engine and reads a structured snapshot, so it works the same regardless of the
// active mode (metrics or homebridge) without touching any mode-specific code or
// the shared Prometheus gauges.

const (
	trmnlHTTPTimeout = 15 * time.Second
	// trmnlInitialDelay lets the engine complete its baseline scan before the
	// first push, so the screen isn't blank on startup.
	trmnlInitialDelay = 10 * time.Second
	// trmnlMaxPayloadBytes is TRMNL's webhook size limit for standard accounts
	// (TRMNL+ allows 5KB). We don't reject oversized payloads — TRMNL will — but
	// we warn so the cause of a rejected push is obvious. See TRMNL.md.
	trmnlMaxPayloadBytes = 2048
)

// trmnlPayload is the body POSTed to the TRMNL webhook. TRMNL expects a
// top-level "merge_variables" object whose fields become Liquid variables.
type trmnlPayload struct {
	MergeVariables trmnlVars `json:"merge_variables"`
}

// trmnlVars is the full pool snapshot exposed to the Liquid template. It carries
// "everything" so the template (on TRMNL's side) decides what to show — no
// pentameter redeploy needed to change the layout.
type trmnlVars struct {
	Updated      string         `json:"updated"`       // RFC3339 timestamp of this push
	UpdatedEpoch int64          `json:"updated_epoch"` // unix seconds (for "x ago" math in Liquid)
	FreezeActive bool           `json:"freeze_active"`
	Bodies       []trmnlBody    `json:"bodies"`
	Air          []trmnlSensor  `json:"air"`
	Pumps        []trmnlPump    `json:"pumps"`
	Heaters      []trmnlHeater  `json:"heaters"`
	Circuits     []trmnlCircuit `json:"circuits"`
	Features     []trmnlCircuit `json:"features"`
}

type trmnlBody struct {
	Name string  `json:"name"`
	Temp float64 `json:"temp"`
	On   bool    `json:"on"`
	Heat string  `json:"heat"` // off/heating/idle/cooling (from the body's heat mode)
}

type trmnlSensor struct {
	Name string  `json:"name"`
	Temp float64 `json:"temp"`
}

type trmnlPump struct {
	Name string  `json:"name"`
	RPM  float64 `json:"rpm"`
	On   bool    `json:"on"`
}

type trmnlHeater struct {
	Name        string  `json:"name"`
	SubType     string  `json:"subtype"` // e.g. ULTRA (heat pump), GENERIC (gas), SOLAR
	Status      string  `json:"status"`  // off/heating/idle/cooling
	SetpointLow float64 `json:"setpoint_low,omitempty"`
}

type trmnlCircuit struct {
	Name    string `json:"name"`
	SubType string `json:"subtype"`
	On      bool   `json:"on"`
	Freeze  bool   `json:"freeze"` // freeze protection enabled on this circuit
}

// runTRMNLPusher owns an engine and pushes a snapshot to the webhook on a fixed
// cadence until the process exits. It logs to stderr only (never the webhook URL,
// which is a write credential) so it stays clear of homebridge's stdout IPC.
func runTRMNLPusher(cfg *appConfig) {
	engine := intellicenter.NewEngine(cfg.intelliCenterIP, cfg.intelliCenterPort, cfg.pollInterval)
	engine.Logf = log.Printf
	engine.Resolve = newDiscoveryResolver(cfg)
	go func() { _ = engine.Run(context.Background()) }()

	client := &http.Client{Timeout: trmnlHTTPTimeout}
	log.Printf("[trmnl] enabled: pushing pool data to TRMNL every %v", cfg.trmnlInterval)

	ctx := context.Background()
	time.Sleep(trmnlInitialDelay)
	for {
		if err := pushTRMNL(ctx, client, cfg.trmnlWebhook, engine.Snapshot(), engine.Config(), time.Now()); err != nil {
			log.Printf("[trmnl] push failed: %v", err)
		}
		time.Sleep(cfg.trmnlInterval)
	}
}

// pushTRMNL builds the payload from a snapshot and POSTs it to the webhook.
func pushTRMNL(ctx context.Context, client *http.Client, webhook string, snap intellicenter.Snapshot, config map[string]string, now time.Time) error {
	vars := buildTRMNLVars(snap, config, now)
	body, err := json.Marshal(trmnlPayload{MergeVariables: vars})
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	if len(body) > trmnlMaxPayloadBytes {
		log.Printf("[trmnl] warning: payload is %d bytes, over TRMNL's %d-byte standard limit "+
			"(TRMNL+ allows 5KB); the push may be rejected", len(body), trmnlMaxPayloadBytes)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhook, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("post to TRMNL: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body) // drain so the connection can be reused

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("TRMNL returned %s", resp.Status)
	}

	log.Printf("[trmnl] pushed: %d bodies, %d sensors, %d pumps, %d heaters, %d circuits, %d features",
		len(vars.Bodies), len(vars.Air), len(vars.Pumps), len(vars.Heaters), len(vars.Circuits), len(vars.Features))
	return nil
}

// buildTRMNLVars converts an engine snapshot into the TRMNL payload, mirroring
// the metrics-mode interpretation (heat status, feature visibility, freeze) so
// the screen and Grafana never disagree. Output is sorted by objnam for stable,
// testable ordering.
func buildTRMNLVars(snap intellicenter.Snapshot, config map[string]string, now time.Time) trmnlVars {
	vars := trmnlVars{
		Updated:      now.UTC().Format(time.RFC3339),
		UpdatedEpoch: now.Unix(),
		Bodies:       buildTRMNLBodies(snap),
		Air:          buildTRMNLAir(snap),
		Pumps:        buildTRMNLPumps(snap),
		Heaters:      buildTRMNLHeaters(snap),
	}
	split := buildTRMNLCircuits(snap, config)
	vars.Circuits, vars.Features, vars.FreezeActive = split.circuits, split.features, split.freezeActive
	return vars
}

// trmnlCircuitSplit is the result of classifying circuit objects.
type trmnlCircuitSplit struct {
	circuits     []trmnlCircuit
	features     []trmnlCircuit
	freezeActive bool
}

func buildTRMNLBodies(snap intellicenter.Snapshot) []trmnlBody {
	var out []trmnlBody
	for _, id := range sortedKeys(snap.Bodies) {
		body := snap.Bodies[id]
		if body.Name == "" {
			continue
		}
		out = append(out, trmnlBody{
			Name: body.Name, Temp: body.Temp, On: body.On,
			Heat: intellicenter.HeatStatusString(body.HeatStatus()),
		})
	}
	return out
}

func buildTRMNLAir(snap intellicenter.Snapshot) []trmnlSensor {
	var out []trmnlSensor
	for _, id := range sortedKeys(snap.Sensors) {
		sensor := snap.Sensors[id]
		if !sensor.Valid || sensor.Name == "" {
			continue
		}
		out = append(out, trmnlSensor{Name: sensor.Name, Temp: sensor.Temp})
	}
	return out
}

func buildTRMNLPumps(snap intellicenter.Snapshot) []trmnlPump {
	var out []trmnlPump
	for _, id := range sortedKeys(snap.Pumps) {
		pump := snap.Pumps[id]
		if pump.Name == "" {
			continue
		}
		out = append(out, trmnlPump{Name: pump.Name, RPM: pump.RPM, On: pump.On})
	}
	return out
}

// buildTRMNLHeaters interprets each real heater from the body that references it
// (HTSRC), exactly as the thermal metric does.
func buildTRMNLHeaters(snap intellicenter.Snapshot) []trmnlHeater {
	type heaterRef struct {
		status int
		lo     float64
	}
	refs := map[string]heaterRef{}
	for _, body := range snap.Bodies {
		if body.HeaterID != "" && body.HeaterID != intellicenter.HeatSourceNone {
			refs[body.HeaterID] = heaterRef{status: body.HeatStatus(), lo: body.LoSetTemp}
		}
	}

	var out []trmnlHeater
	for _, id := range sortedKeys(snap.Heaters) {
		heater := snap.Heaters[id]
		if !heater.Real || heater.Name == "" {
			continue
		}
		status := intellicenter.HeatStatusOff
		var lo float64
		if ref, referenced := refs[heater.ID]; referenced {
			status, lo = ref.status, ref.lo
		}
		out = append(out, trmnlHeater{
			Name: heater.Name, SubType: heater.SubType,
			Status: intellicenter.HeatStatusString(status), SetpointLow: lo,
		})
	}
	return out
}

// buildTRMNLCircuits splits circuit objects into displayable circuits and
// features (respecting visibility) and reports whether freeze protection is
// active. Returns (circuits, features, freezeActive).
func buildTRMNLCircuits(snap intellicenter.Snapshot, config map[string]string) trmnlCircuitSplit {
	var split trmnlCircuitSplit
	for _, id := range sortedKeys(snap.Circuits) {
		circ := snap.Circuits[id]
		if circ.Name == "" {
			continue
		}
		if circ.SubType == hbSubTypeFreeze && circ.On {
			split.freezeActive = true
		}
		item := trmnlCircuit{Name: circ.Name, SubType: circ.SubType, On: circ.On, Freeze: circ.Freeze}
		switch {
		case isTRMNLFeature(circ.ID):
			if trmnlFeatureVisible(config, circ.ID) {
				split.features = append(split.features, item)
			}
		case isTRMNLCircuit(circ.ID, circ.Name, circ.SubType):
			split.circuits = append(split.circuits, item)
		}
	}
	return split
}

// isTRMNLFeature reports whether an objnam is a feature circuit (FTR##).
func isTRMNLFeature(id string) bool {
	return strings.HasPrefix(id, "FTR")
}

// trmnlFeatureVisible mirrors the metrics path: a feature is shown unless its
// SHOMNU config flag explicitly hides it (visible when absent, or when SHOMNU
// ends in "w").
func trmnlFeatureVisible(config map[string]string, id string) bool {
	shomnu, ok := config[id]
	if !ok {
		return true
	}
	return intellicenter.ShouldShowFeature(shomnu)
}

// isTRMNLCircuit mirrors PoolMonitor.isValidCircuit: regular circuits (C) and
// circuit groups (GRP), excluding the generic "AUX n" placeholders.
func isTRMNLCircuit(id, name, subType string) bool {
	hasValidPrefix := strings.HasPrefix(id, "C") || strings.HasPrefix(id, "GRP")
	isGenericAux := strings.HasPrefix(id, "C") && strings.HasPrefix(name, "AUX ") && subType == subtypGeneric
	return hasValidPrefix && !isGenericAux
}

// sortedKeys returns a map's keys sorted, for deterministic payload ordering.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
