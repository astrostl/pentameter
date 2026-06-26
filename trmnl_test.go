package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/astrostl/pentameter/intellicenter"
)

// sampleSnapshot is a small but representative pool: one heated pool body, an air
// sensor, a running pump, a real heat-pump heater referenced by the body, a
// regular circuit, a generic "AUX" placeholder (must be excluded), a visible
// feature, a hidden feature, and the freeze-protection circuit (active).
func sampleSnapshot() intellicenter.Snapshot {
	return intellicenter.Snapshot{
		Bodies: map[string]intellicenter.Body{
			"B1101": {ID: "B1101", Name: "Pool", On: true, Temp: 82, HeatMode: 1, HeaterID: "H0001", LoSetTemp: 85, HiSetTemp: 104},
		},
		Sensors: map[string]intellicenter.Sensor{
			"_A135": {ID: "_A135", Name: "Air", SubType: "AIR", Temp: 75, Valid: true},
			"_A999": {ID: "_A999", Name: "Bad", Temp: 0, Valid: false}, // excluded: invalid
		},
		Pumps: map[string]intellicenter.Pump{
			"PMP01": {ID: "PMP01", Name: "Pump", On: true, RPM: 2000},
		},
		Heaters: map[string]intellicenter.Heater{
			"H0001": {ID: "H0001", Name: "UltraTemp", SubType: "ULTRA", Real: true},
			"HXULT": {ID: "HXULT", Name: "Preferred", Real: false}, // excluded: pseudo-heater
		},
		Circuits: map[string]intellicenter.Circuit{
			"C0001": {ID: "C0001", Name: "Pool Light", SubType: "LIGHT", On: true, Freeze: false},
			"C0009": {ID: "C0009", Name: "AUX 5", SubType: "GENERIC", On: false}, // excluded: generic AUX
			"FTR01": {ID: "FTR01", Name: "Waterfall", SubType: "GENERIC", On: true, Feature: true},
			"FTR02": {ID: "FTR02", Name: "Hidden", SubType: "GENERIC", On: false, Feature: true},
			"_FEA2": {ID: "_FEA2", Name: "Freeze", SubType: "FRZ", On: true},
		},
	}
}

func TestBuildTRMNLVars(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	// FTR02 hidden (SHOMNU without trailing "w"); FTR01 absent from config -> visible.
	config := map[string]string{"FTR02": "hide"}

	vars := buildTRMNLVars(sampleSnapshot(), config, now)

	if vars.UpdatedEpoch != now.Unix() {
		t.Errorf("UpdatedEpoch = %d, want %d", vars.UpdatedEpoch, now.Unix())
	}
	if !vars.FreezeActive {
		t.Error("FreezeActive = false, want true (_FEA2 is ON)")
	}

	if len(vars.Bodies) != 1 || vars.Bodies[0].Name != "Pool" || vars.Bodies[0].Temp != 82 {
		t.Fatalf("Bodies = %+v, want one Pool@82", vars.Bodies)
	}
	if vars.Bodies[0].Heat != "heating" { // HTMODE=1
		t.Errorf("Bodies[0].Heat = %q, want heating", vars.Bodies[0].Heat)
	}

	if len(vars.Air) != 1 || vars.Air[0].Name != "Air" {
		t.Errorf("Air = %+v, want only the valid Air sensor", vars.Air)
	}

	if len(vars.Pumps) != 1 || vars.Pumps[0].RPM != 2000 || !vars.Pumps[0].On {
		t.Errorf("Pumps = %+v, want one running Pump@2000", vars.Pumps)
	}

	if len(vars.Heaters) != 1 {
		t.Fatalf("Heaters = %+v, want only the real heater", vars.Heaters)
	}
	if h := vars.Heaters[0]; h.Name != "UltraTemp" || h.SubType != "ULTRA" || h.Status != "heating" || h.SetpointLow != 85 {
		t.Errorf("Heaters[0] = %+v, want UltraTemp/ULTRA/heating/85", h)
	}

	if len(vars.Circuits) != 1 || vars.Circuits[0].Name != "Pool Light" {
		t.Errorf("Circuits = %+v, want only Pool Light (AUX excluded)", vars.Circuits)
	}

	if len(vars.Features) != 1 || vars.Features[0].Name != "Waterfall" {
		t.Errorf("Features = %+v, want only the visible Waterfall (Hidden excluded)", vars.Features)
	}
}

func TestPushTRMNLPostsMergeVariables(t *testing.T) {
	var gotBody []byte
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := pushTRMNL(context.Background(), srv.Client(), srv.URL, sampleSnapshot(), nil, time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatalf("pushTRMNL returned error: %v", err)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}

	var payload trmnlPayload
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("payload is not valid JSON: %v\nbody: %s", err, gotBody)
	}
	if len(payload.MergeVariables.Bodies) != 1 {
		t.Errorf("posted Bodies = %d, want 1", len(payload.MergeVariables.Bodies))
	}
	if !payload.MergeVariables.FreezeActive {
		t.Error("posted FreezeActive = false, want true")
	}
}

func TestPushTRMNLNon2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	err := pushTRMNL(context.Background(), srv.Client(), srv.URL, sampleSnapshot(), nil, time.Unix(1_700_000_000, 0))
	if err == nil {
		t.Fatal("pushTRMNL returned nil error on 401, want error")
	}
}

func TestDetermineTRMNLInterval(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want time.Duration
	}{
		{"default when zero", 0, defaultTRMNLInterval * time.Second},
		{"floor when too small", 5, minTRMNLInterval * time.Second},
		{"explicit value honored", 120, 120 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := determineTRMNLInterval(tc.in); got != tc.want {
				t.Errorf("determineTRMNLInterval(%d) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
