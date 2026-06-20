package intellicenter

import "strings"

// HeatStatus maps a body's heat mode + current/target temps to a thermal status
// (off/heating/idle/cooling). Ported from pentameter's calculateHeaterStatus.
func (b *Body) HeatStatus() int {
	switch b.HeatMode {
	case htModeOff:
		// Heater assigned but off: idle if within setpoints, else off.
		if b.Temp >= b.LoSetTemp && b.Temp <= b.HiSetTemp {
			return HeatStatusIdle
		}
		return HeatStatusOff
	case htModeHeating, htModeHeatPumpHeating:
		return HeatStatusHeating
	case htModeHeatPumpCooling:
		return HeatStatusCooling
	default:
		return HeatStatusOff
	}
}

// HeatStatusString renders a thermal status for logs.
func HeatStatusString(status int) string {
	switch status {
	case HeatStatusHeating:
		return "heating"
	case HeatStatusIdle:
		return "idle"
	case HeatStatusCooling:
		return "cooling"
	default:
		return "off"
	}
}

// ShouldShowFeature decides whether a feature is user-visible, based on its
// SHOMNU config flag (visible if it ends with "w"). Ported from pentameter's
// processFeatureObject visibility check.
func ShouldShowFeature(shomnu string) bool {
	return strings.HasSuffix(shomnu, "w")
}
