package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/astrostl/pentameter/intellicenter"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Version information set at build time.
var version = "dev"

// Constants.
const (
	nanosecondMod       = 1000000
	handshakeTimeout    = 10 * time.Second
	maxRetries          = 5
	defaultPollInterval = 60
	minPollInterval     = 5
	complexityThreshold = 15
	httpReadTimeout     = 15 * time.Second
	httpWriteTimeout    = 15 * time.Second
	httpIdleTimeout     = 60 * time.Second

	// Listen mode polling interval (catches equipment that doesn't push).
	listenModePollInterval = 10

	// Metric key parts count (objnam|name|subtype).
	metricKeyPartsCount = 3

	// Circuit status constants.
	statusOn = "ON"

	// Circuit/feature status metric values.
	circuitStatusOff             = 0.0
	circuitStatusOn              = 1.0
	circuitStatusFreezeProtected = 2.0

	// Status description strings.
	statusDescOff    = "OFF"
	statusDescOn     = "ON"
	statusDescFreeze = keyFREEZE

	// Boolean string constants.
	trueString = "true"

	// Object type constants.
	objTypeBody    = "BODY"
	objTypeCircuit = "CIRCUIT"
	objTypePump    = "PUMP"
	objTypeHeater  = "HEATER"
	objTypeCircGrp = "CIRCGRP"

	// Thermal status constants.
	thermalStatusOff      = 0
	thermalStatusHeating  = 1
	thermalStatusIdle     = 2
	thermalStatusCooling  = 3
	htModeOff             = 0
	htModeHeating         = 1
	htModeHeatPumpHeating = 4
	htModeHeatPumpCooling = 9

	// Protocol command names.
	cmdGetParamList = "GetParamList"

	// Param keys / JSON field names.
	fieldObjnam = "objnam"
	fieldParams = "params"
	keyOBJTYP   = "OBJTYP"
	keyHTMODE   = "HTMODE"
	keyPROBE    = "PROBE"
	keyACT      = "ACT"

	// Special object names.
	objnamIncr       = "INCR"
	objnamFreezeFeat = "_FEA2"

	// Subtype / body-name values.
	subtypGeneric = "GENERIC"
	bodyNamePool  = "pool"
	bodyNameSpa   = "spa"

	// Thermal status description words.
	statusWordOff     = "off"
	statusWordHeating = "heating"
	statusWordIdle    = "idle"
	statusWordCooling = "cooling"
	statusWordUnknown = "unknown"

	// Structured log field names.
	logFieldBody    = "body"
	logFieldCircuit = "circuit"
	logFieldHeater  = "heater"
	fieldName       = "name"
	fieldSubtyp     = "subtyp"

	// Additional param keys.
	keyHTSRC   = "HTSRC"
	keyDLY     = "DLY"
	keyRPM     = "RPM"
	keySNAME   = "SNAME"
	keySTATUS  = "STATUS"
	keyTEMP    = "TEMP"
	keySUBTYP  = "SUBTYP"
	keyLOTMP   = "LOTMP"
	keyHITMP   = "HITMP"
	keyPWR     = "PWR" // pump real power draw (watts)
	keyPARENT  = "PARENT"
	keyUSE     = "USE"
	keyLISTORD = "LISTORD"
	keySTATIC  = "STATIC"
	keyFREEZE  = "FREEZE"
)

// IntelliCenter API structures are aliased to the intellicenter package, which
// now owns the protocol types + transport. Aliases keep the existing parsing,
// metrics, and listen code (and its tests) compiling unchanged.
type (
	IntelliCenterRequest  = intellicenter.Request
	ObjectQuery           = intellicenter.Object
	IntelliCenterResponse = intellicenter.Response
	ObjectData            = intellicenter.ObjectData
)

// Prometheus metrics.
var (
	poolTemperature = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "water_temperature_fahrenheit",
			Help: "Current water temperature in Fahrenheit",
		},
		[]string{logFieldBody, fieldName},
	)

	airTemperature = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "air_temperature_fahrenheit",
			Help: "Current outdoor air temperature in Fahrenheit",
		},
		[]string{"sensor", fieldName},
	)

	connectionFailure = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "intellicenter_connection_failure",
			Help: "1 if there was a connection failure in the last refresh, 0 if successful",
		},
	)

	lastRefreshTimestamp = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "intellicenter_last_refresh_timestamp_seconds",
			Help: "Unix timestamp of the last successful data refresh",
		},
	)

	pumpRPM = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "pump_rpm",
			Help: "Current pump speed in revolutions per minute",
		},
		[]string{"pump", fieldName},
	)

	circuitStatus = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "circuit_status",
			Help: "Circuit status (0=off, 1=on, 2=freeze protection active)",
		},
		[]string{logFieldCircuit, fieldName, fieldSubtyp},
	)

	thermalStatus = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "thermal_status",
			Help: "Thermal equipment operational status derived from IntelliCenter HTMODE+HTSRC " +
				"(0=off, 1=heating, 2=idle, 3=cooling). Note: 'idle' is pentameter's interpretation " +
				"of HTMODE=0+assigned heater, not an IntelliCenter native status.",
		},
		[]string{logFieldHeater, fieldName, fieldSubtyp},
	)

	thermalLowSetpoint = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "thermal_low_setpoint_fahrenheit",
			Help: "Heating target temperature in Fahrenheit (turn on heating when temp drops below this)",
		},
		[]string{logFieldHeater, fieldName, fieldSubtyp},
	)

	thermalHighSetpoint = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "thermal_high_setpoint_fahrenheit",
			Help: "Cooling target temperature in Fahrenheit (turn on cooling when temp rises above this)",
		},
		[]string{logFieldHeater, fieldName, fieldSubtyp},
	)

	featureStatus = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "feature_status",
			Help: "Feature status (0=off, 1=on, 2=freeze protection active)",
		},
		[]string{"feature", fieldName, fieldSubtyp},
	)
)

type PoolMonitor struct {
	lastRefresh            time.Time
	ic                     *intellicenter.Client     // IntelliCenter transport + protocol
	bodyHeatingStatus      map[string]bool           // Track which bodies are actively heating
	referencedHeaters      map[string]BodyHeaterInfo // Track body-to-heater assignments
	featureConfig          map[string]string         // Track feature objnam -> SHOMNU for visibility
	circuitFreezeConfig    map[string]bool           // Track circuit objnam -> freeze protection enabled
	circuitNames           map[string]string         // Track circuit/group objnam -> SNAME for display
	activeCircuitKeys      map[string]bool           // Track active circuit metric keys for stale cleanup
	activeFeatureKeys      map[string]bool           // Track active feature metric keys for stale cleanup
	previousState          *EquipmentState           // Previous state for change detection
	mu                     sync.Mutex                // Protects concurrent access in listen mode
	quiet                  bool                      // Suppress per-object "Updated ..." logs (engine push-driven recomputes)
	listenMode             bool                      // Enable live event logging mode (includes raw JSON output)
	initialPollDone        bool                      // Track if initial poll completed (suppresses "detected" logs after first poll)
	freezeProtectionActive bool                      // Track if freeze protection is currently active
}

// CircGrpState tracks the state of a circuit group member.
type CircGrpState struct {
	Active  string // ACT: ON/OFF
	Use     string // USE: color/mode (e.g., "White", "Blue")
	Circuit string // CIRCUIT: referenced circuit ID (e.g., "C0003")
	Parent  string // PARENT: parent group ID (e.g., "GRP01")
}

// EquipmentState tracks the current state of all equipment for change detection.
type EquipmentState struct {
	WaterTemps      map[string]float64      // body -> temperature
	PumpRPMs        map[string]float64      // pump -> RPM
	Circuits        map[string]string       // circuit -> ON/OFF
	Thermals        map[string]int          // heater -> status (0=off, 1=heating, 2=idle, 3=cooling)
	Features        map[string]string       // feature -> ON/OFF
	CircGrps        map[string]CircGrpState // circgrp objnam -> state
	UnknownEquip    map[string]string       // objnam -> "OBJTYP:STATUS" for equipment not otherwise tracked
	ParseErrors     map[string]bool         // Track parse errors we've already logged
	SkippedFeatures map[string]bool         // Track skipped features we've already logged
	AirTemp         float64
	PollChangeCount int // Count changes detected during current poll
}

type BodyHeaterInfo struct {
	BodyName  string
	BodyObj   string
	HeaterObj string
	HTMode    int
	Temp      float64
	LoTemp    float64
	HiTemp    float64
}

func NewPoolMonitor(intelliCenterIP, intelliCenterPort string, listenMode bool) *PoolMonitor {
	return &PoolMonitor{
		ic:                     intellicenter.New(intelliCenterIP, intelliCenterPort),
		bodyHeatingStatus:      make(map[string]bool),
		referencedHeaters:      make(map[string]BodyHeaterInfo),
		featureConfig:          make(map[string]string),
		circuitFreezeConfig:    make(map[string]bool),
		circuitNames:           make(map[string]string),
		activeCircuitKeys:      make(map[string]bool),
		activeFeatureKeys:      make(map[string]bool),
		previousState:          nil,
		listenMode:             listenMode,
		freezeProtectionActive: false,
	}
}

// Connect establishes the IntelliCenter connection (with retry/backoff). The
// transport now lives in the intellicenter package; PoolMonitor delegates.
// outputRawJSON outputs the raw JSON message to the log with a prefix.
func (pm *PoolMonitor) outputRawJSON(prefix string, msg map[string]interface{}) {
	jsonBytes, err := json.Marshal(msg)
	if err != nil {
		log.Printf("%s: [marshal error: %v]", prefix, err)
		return
	}
	log.Printf("%s: %s", prefix, string(jsonBytes))
}

// outputRawObjectData outputs ObjectData as raw JSON for polling context.
func (pm *PoolMonitor) outputRawObjectData(obj ObjectData) {
	objMap := map[string]interface{}{
		fieldObjnam: obj.ObjName,
		fieldParams: obj.Params,
	}
	pm.outputRawJSON("POLL", objMap)
}

// processRawPushNotification handles raw JSON push notifications.
// Logs everything received, then processes known types.
func (pm *PoolMonitor) processRawPushNotification(msg map[string]interface{}) {
	objectList, ok := msg["objectList"].([]interface{})
	if !ok || len(objectList) == 0 {
		pm.logRawPushMessage(msg)
		return
	}

	for _, item := range objectList {
		pm.processObjectListItem(item)
	}
}

func (pm *PoolMonitor) logRawPushMessage(msg map[string]interface{}) {
	jsonBytes, err := json.Marshal(msg)
	if err != nil {
		log.Printf("PUSH: [marshal error: %v]", err)
		return
	}
	log.Printf("PUSH: %s", string(jsonBytes))
}

func (pm *PoolMonitor) processObjectListItem(item interface{}) {
	itemMap, ok := item.(map[string]interface{})
	if !ok {
		return
	}

	changes, ok := itemMap["changes"].([]interface{})
	if !ok {
		pm.logRawPushMessage(itemMap)
		return
	}

	for _, change := range changes {
		pm.processChangeItem(change)
	}
}

func (pm *PoolMonitor) processChangeItem(change interface{}) {
	changeMap, ok := change.(map[string]interface{})
	if !ok {
		return
	}

	objnam, _ := changeMap[fieldObjnam].(string)
	paramsRaw, ok := changeMap[fieldParams].(map[string]interface{})
	if !ok {
		return
	}

	obj := pm.convertToObjectData(objnam, paramsRaw)
	pm.processPushObject(obj)
}

func (pm *PoolMonitor) convertToObjectData(objnam string, paramsRaw map[string]interface{}) ObjectData {
	params := make(map[string]string)
	for k, v := range paramsRaw {
		if s, ok := v.(string); ok {
			params[k] = s
		} else {
			params[k] = fmt.Sprintf("%v", v)
		}
	}
	return ObjectData{
		ObjName: objnam,
		Params:  params,
	}
}

// processPushObject routes a push notification to the appropriate handler.
// Uses the same processing functions as polling mode, then logs a human-readable summary.
func (pm *PoolMonitor) processPushObject(obj ObjectData) {
	objType := obj.Params[keyOBJTYP]
	name := obj.Params[keySNAME]
	if name == "" {
		name = obj.ObjName
	}

	// Use the same processing functions as polling mode, then log the change.
	switch objType {
	case objTypeBody:
		pm.handleBodyPush(obj, name)
	case objTypePump:
		pm.handlePumpPush(obj, name)
	case objTypeCircuit:
		pm.handleCircuitPush(obj, name)
	case objTypeHeater:
		pm.handleHeaterPush(obj, name)
	case objTypeCircGrp:
		pm.handleCircGrpPush(obj)
	default:
		pm.handleUnknownPush(obj)
	}
}

func (pm *PoolMonitor) handleBodyPush(obj ObjectData, name string) {
	referencedHeaters := make(map[string]BodyHeaterInfo)
	pm.processBodyObject(obj, referencedHeaters)
	for k, v := range referencedHeaters {
		pm.referencedHeaters[k] = v
	}
	log.Printf("PUSH: %s temp=%s°F setpoint=%s°F htmode=%s status=%s",
		name, obj.Params[keyTEMP], obj.Params["SETPT"], obj.Params[keyHTMODE], obj.Params[keySTATUS])
}

func (pm *PoolMonitor) handlePumpPush(obj ObjectData, name string) {
	if err := pm.processPumpObject(obj, 0); err != nil {
		log.Printf("PUSH: %s pump error: %v", name, err)
	} else {
		log.Printf("PUSH: %s rpm=%s watts=%s status=%s",
			name, obj.Params[keyRPM], obj.Params[keyPWR], obj.Params[keySTATUS])
	}
}

func (pm *PoolMonitor) handleCircuitPush(obj ObjectData, name string) {
	pm.processCircuitObject(obj)
	log.Printf("PUSH: %s status=%s", name, obj.Params[keySTATUS])
}

func (pm *PoolMonitor) handleHeaterPush(obj ObjectData, name string) {
	pm.processHeaterObject(obj)
	log.Printf("PUSH: %s status=%s mode=%s", name, obj.Params[keySTATUS], obj.Params["MODE"])
}

func (pm *PoolMonitor) handleCircGrpPush(obj ObjectData) {
	pm.trackCircGrp(obj)
	// Log with resolved circuit group names
	groupName := pm.resolveCircuitName(obj.Params[keyPARENT])
	circuitName := pm.resolveCircuitName(obj.Params[objTypeCircuit])
	act := obj.Params[keyACT]
	use := obj.Params[keyUSE]
	log.Printf("PUSH: CircGrp %s/%s act=%s use=%s",
		groupName, circuitName, act, use)
}

func (pm *PoolMonitor) handleUnknownPush(obj ObjectData) {
	jsonBytes, err := json.Marshal(obj.Params)
	if err != nil {
		log.Printf("PUSH: unknown %s: [marshal error: %v]", obj.ObjName, err)
		return
	}
	log.Printf("PUSH: unknown %s: %s", obj.ObjName, string(jsonBytes))
}

// applyBodyTemperatures updates body metrics and collects heater assignments from
// a set of body objects (sourced either from a live query or the engine snapshot).
func (pm *PoolMonitor) applyBodyTemperatures(objs []ObjectData) {
	referencedHeaters := make(map[string]BodyHeaterInfo)
	for _, obj := range objs {
		pm.processBodyObject(obj, referencedHeaters)
	}
	// Store referenced heaters for heater status processing
	pm.referencedHeaters = referencedHeaters
}

func (pm *PoolMonitor) processBodyObject(obj ObjectData, referencedHeaters map[string]BodyHeaterInfo) {
	name := obj.Params[keySNAME]
	tempStr := obj.Params[keyTEMP]
	subtype := obj.Params[keySUBTYP]
	status := obj.Params[keySTATUS]
	htmodeStr := obj.Params[keyHTMODE]
	htsrc := obj.Params[keyHTSRC]
	lotmpStr := obj.Params[keyLOTMP]
	hitmpStr := obj.Params[keyHITMP]

	pm.processBodyTemperature(name, tempStr, subtype, status, obj)
	pm.processBodyHeatingStatus(name, htmodeStr, obj.ObjName)
	pm.processHeaterAssignment(name, tempStr, htmodeStr, htsrc, lotmpStr, hitmpStr, obj.ObjName, referencedHeaters)
}

func (pm *PoolMonitor) processBodyTemperature(name, tempStr, subtype, status string, obj ObjectData) {
	if tempStr == "" || name == "" {
		return
	}

	tempFahrenheit, err := strconv.ParseFloat(tempStr, 64)
	if err != nil {
		// Only log parse errors once in listen mode
		errorKey := fmt.Sprintf("temp-parse-%s", name)
		if pm.listenMode && pm.previousState != nil {
			if !pm.previousState.ParseErrors[errorKey] {
				log.Printf("Failed to parse temperature %s for %s: %v", tempStr, name, err)
				pm.previousState.ParseErrors[errorKey] = true
			}
		} else if !pm.listenMode {
			log.Printf("Failed to parse temperature %s for %s: %v", tempStr, name, err)
		}
		return
	}

	// Store temperature in Fahrenheit as per project standard
	poolTemperature.WithLabelValues(subtype, name).Set(tempFahrenheit)
	pm.trackWaterTemp(name, tempFahrenheit, obj)
	pm.logIfNotListeningf("Updated temperature: %s (%s) = %.1f°F (Status: %s)", name, subtype, tempFahrenheit, status)
}

func (pm *PoolMonitor) processBodyHeatingStatus(name, htmodeStr, objName string) {
	if htmodeStr == "" || name == "" {
		return
	}

	htmode, err := strconv.Atoi(htmodeStr)
	if err != nil {
		log.Printf("Failed to parse HTMODE %s for %s: %v", htmodeStr, name, err)
		return
	}

	// HTMODE >= 1 means heater is on (1=actively heating, 2=on but not heating)
	pm.bodyHeatingStatus[strings.ToLower(name)] = htmode >= 1
	pm.logIfNotListeningf("Updated body heating status: %s (%s) HTMODE=%d [%v]", name, objName, htmode, htmode >= 1)
}

func (pm *PoolMonitor) processHeaterAssignment(
	name, tempStr, htmodeStr, htsrc, lotmpStr, hitmpStr, objName string,
	referencedHeaters map[string]BodyHeaterInfo,
) {
	if htsrc == "" || htsrc == "00000" || name == "" {
		return
	}

	// Parse temperature setpoints
	temp, _ := strconv.ParseFloat(tempStr, 64)
	lotmp, _ := strconv.ParseFloat(lotmpStr, 64)
	hitmp, _ := strconv.ParseFloat(hitmpStr, 64)
	htmode, _ := strconv.Atoi(htmodeStr)

	referencedHeaters[htsrc] = BodyHeaterInfo{
		BodyName:  name,
		BodyObj:   objName,
		HeaterObj: htsrc,
		HTMode:    htmode,
		Temp:      temp,
		LoTemp:    lotmp,
		HiTemp:    hitmp,
	}
}

// applyAirTemperature updates the air-temperature metric from a set of sensor objects.
func (pm *PoolMonitor) applyAirTemperature(objs []ObjectData) {
	for _, obj := range objs {
		name := obj.Params[keySNAME]
		tempStr := obj.Params[keyPROBE]
		subtype := obj.Params[keySUBTYP]
		status := obj.Params[keySTATUS]

		if tempStr != "" && name != "" {
			tempFahrenheit, err := strconv.ParseFloat(tempStr, 64)
			if err != nil {
				log.Printf("Failed to parse air temperature %s for %s: %v", tempStr, name, err)
				continue
			}

			// Store temperature in Fahrenheit as per project standard
			airTemperature.WithLabelValues(subtype, name).Set(tempFahrenheit)
			pm.trackAirTemp(tempFahrenheit, obj)
			pm.logIfNotListeningf("Updated air temperature: %s (%s) = %.1f°F (Status: %s)", name, subtype, tempFahrenheit, status)
		}
	}
}

// applyPumpData updates pump metrics from a set of pump objects. responseTime is
// for logging only (0 when sourced from the engine snapshot rather than a query).
func (pm *PoolMonitor) applyPumpData(objs []ObjectData, responseTime time.Duration) {
	for _, obj := range objs {
		if err := pm.processPumpObject(obj, responseTime); err != nil {
			log.Printf("Failed to process pump object %s: %v", obj.ObjName, err)
		}
	}
}

// applyFreezeProtection sets freezeProtectionActive from the _FEA2 feature's status.
// objs may be the dedicated _FEA2 query result or the full circuit set (the engine
// path passes all circuits; only _FEA2 is inspected).
func (pm *PoolMonitor) applyFreezeProtection(objs []ObjectData) {
	pm.freezeProtectionActive = false
	for _, obj := range objs {
		if obj.ObjName == objnamFreezeFeat && obj.Params[keySTATUS] == statusOn {
			pm.freezeProtectionActive = true
			pm.logIfNotListeningf("Freeze protection is ACTIVE")
			break
		}
	}

	if !pm.freezeProtectionActive {
		pm.logIfNotListeningf("Freeze protection is inactive")
	}
}

// applyCircuitStatus updates circuit + feature metrics from a set of circuit
// objects, then prunes metric series no longer present (stale cleanup).
func (pm *PoolMonitor) applyCircuitStatus(objs []ObjectData) {
	// Save previous keys for stale metric cleanup
	previousCircuitKeys := pm.activeCircuitKeys
	previousFeatureKeys := pm.activeFeatureKeys
	pm.activeCircuitKeys = make(map[string]bool)
	pm.activeFeatureKeys = make(map[string]bool)

	// Update Prometheus metrics
	for _, obj := range objs {
		pm.processCircuitObject(obj)
	}

	// Cleanup stale circuit metrics
	pm.cleanupStaleMetrics(previousCircuitKeys, pm.activeCircuitKeys, circuitStatus, logFieldCircuit)

	// Cleanup stale feature metrics
	pm.cleanupStaleMetrics(previousFeatureKeys, pm.activeFeatureKeys, featureStatus, "feature")
}

func (pm *PoolMonitor) cleanupStaleMetrics(previous, current map[string]bool, metric *prometheus.GaugeVec, metricType string) {
	for key := range previous {
		if !current[key] {
			// Parse the key back into label values (format: "objnam|name|subtype")
			parts := strings.SplitN(key, "|", metricKeyPartsCount)
			if len(parts) == metricKeyPartsCount {
				deleted := metric.DeleteLabelValues(parts[0], parts[1], parts[2])
				if deleted {
					log.Printf("Cleaned up stale %s metric: %s (%s)", metricType, parts[1], parts[0])
				}
			}
		}
	}
}

func (pm *PoolMonitor) processCircuitObject(obj ObjectData) {
	name := obj.Params[keySNAME]
	status := obj.Params[keySTATUS]
	subtype := obj.Params[keySUBTYP]
	freezeEnabled := obj.Params[keyFREEZE] == statusOn

	if name == "" || status == "" {
		return
	}

	// Cache circuit name for display in circuit group logging
	pm.circuitNames[obj.ObjName] = name

	// Separate features (FTR) from circuits (C)
	if strings.HasPrefix(obj.ObjName, "FTR") {
		pm.processFeatureObject(obj, name, status, subtype, freezeEnabled)
	} else if pm.isValidCircuit(obj.ObjName, name, subtype) {
		statusValue := pm.calculateCircuitStatusValue(name, status, obj.ObjName, freezeEnabled)
		circuitStatus.WithLabelValues(obj.ObjName, name, subtype).Set(statusValue)
		pm.activeCircuitKeys[obj.ObjName+"|"+name+"|"+subtype] = true
		pm.trackCircuit(name, status, obj)
	}
}

func (pm *PoolMonitor) isValidCircuit(objName, name, subtype string) bool {
	// Accept regular circuits (C prefix) and circuit groups (GRP prefix)
	hasValidPrefix := strings.HasPrefix(objName, "C") || strings.HasPrefix(objName, "GRP")
	isGenericAux := strings.HasPrefix(objName, "C") && strings.HasPrefix(name, "AUX ") && subtype == subtypGeneric
	return hasValidPrefix && !isGenericAux
}

func (pm *PoolMonitor) processFeatureObject(obj ObjectData, name, status, subtype string, freezeEnabled bool) {
	// Check if feature should be shown based on IntelliCenter's "Show as Feature" setting
	shomnu, exists := pm.featureConfig[obj.ObjName]
	if !exists || strings.HasSuffix(shomnu, "w") {
		// Feature should be shown - continue to processing
		pm.processVisibleFeature(obj, name, status, subtype, freezeEnabled)
		return
	}

	// Feature hidden - log skip message
	pm.logSkippedFeature(name, obj.ObjName, shomnu)
}

func (pm *PoolMonitor) logSkippedFeature(name, objName, shomnu string) {
	// Only log skipped features once in listen mode
	if pm.listenMode && pm.previousState != nil {
		if !pm.previousState.SkippedFeatures[objName] {
			log.Printf("Skipping feature with 'Show as Feature: NO': %s (%s) SHOMNU=%s", name, objName, shomnu)
			pm.previousState.SkippedFeatures[objName] = true
		}
		return
	}

	if !pm.listenMode {
		log.Printf("Skipping feature with 'Show as Feature: NO': %s (%s) SHOMNU=%s", name, objName, shomnu)
	}
}

func (pm *PoolMonitor) processVisibleFeature(obj ObjectData, name, status, subtype string, freezeEnabled bool) {
	// Calculate feature status value with freeze protection support
	statusValue := circuitStatusOff
	statusDesc := statusDescOff

	if status == statusOn {
		// Check if freeze protection is active and this feature has freeze protection enabled
		if pm.freezeProtectionActive && freezeEnabled {
			statusValue = circuitStatusFreezeProtected
			statusDesc = statusDescFreeze
		} else {
			statusValue = circuitStatusOn
			statusDesc = statusDescOn
		}
	}

	// Update Prometheus metric using IntelliCenter's SUBTYP
	featureStatus.WithLabelValues(obj.ObjName, name, subtype).Set(statusValue)
	pm.activeFeatureKeys[obj.ObjName+"|"+name+"|"+subtype] = true
	pm.trackFeature(name, status)

	pm.logIfNotListeningf("Updated feature status: %s (%s) = %s [%.0f]", name, obj.ObjName, statusDesc, statusValue)
}

func (pm *PoolMonitor) calculateCircuitStatusValue(name, status, objName string, freezeEnabled bool) float64 {
	isHeaterCircuit := strings.Contains(strings.ToLower(name), "heat")

	if isHeaterCircuit {
		return pm.getHeaterCircuitStatus(name, objName, freezeEnabled)
	}

	return pm.getRegularCircuitStatus(name, status, objName, freezeEnabled)
}

func (pm *PoolMonitor) getHeaterCircuitStatus(name, objName string, freezeEnabled bool) float64 {
	bodyName := pm.getBodyNameFromCircuit(name)
	statusValue := circuitStatusOff
	statusDesc := statusDescOff

	if bodyName != "" && pm.bodyHeatingStatus[bodyName] {
		// Check if freeze protection is active and this circuit has freeze protection enabled
		if pm.freezeProtectionActive && freezeEnabled {
			statusValue = circuitStatusFreezeProtected
			statusDesc = statusDescFreeze
		} else {
			statusValue = circuitStatusOn
			statusDesc = statusDescOn
		}
	}

	pm.logIfNotListeningf("Updated heater circuit status: %s (%s) = %s [%.0f] (Body: %s, Heating: %v)",
		name, objName, statusDesc, statusValue, bodyName, pm.bodyHeatingStatus[bodyName])

	return statusValue
}

func (pm *PoolMonitor) getBodyNameFromCircuit(name string) string {
	lowerName := strings.ToLower(name)
	if strings.Contains(lowerName, bodyNameSpa) {
		return bodyNameSpa
	}
	if strings.Contains(lowerName, bodyNamePool) {
		return bodyNamePool
	}
	return ""
}

func (pm *PoolMonitor) getRegularCircuitStatus(name, status, objName string, freezeEnabled bool) float64 {
	statusValue := circuitStatusOff
	statusDesc := statusDescOff

	if status == statusOn {
		// Check if freeze protection is active and this circuit has freeze protection enabled
		if pm.freezeProtectionActive && freezeEnabled {
			statusValue = circuitStatusFreezeProtected
			statusDesc = statusDescFreeze
		} else {
			statusValue = circuitStatusOn
			statusDesc = statusDescOn
		}
	}

	pm.logIfNotListeningf("Updated circuit status: %s (%s) = %s [%.0f]", name, objName, statusDesc, statusValue)
	return statusValue
}

// applyThermalStatus updates thermal (heater) metrics from a set of heater objects.
func (pm *PoolMonitor) applyThermalStatus(objs []ObjectData) {
	for _, obj := range objs {
		pm.processHeaterObject(obj)
	}
}

func (pm *PoolMonitor) getCircuitGroups() error {
	req := IntelliCenterRequest{
		Command:   cmdGetParamList,
		Condition: "OBJTYP=CIRCGRP",
		ObjectList: []ObjectQuery{
			{
				ObjName: objnamIncr,
				Keys:    []string{keyOBJTYP, keyPARENT, objTypeCircuit, keyACT, keyUSE, keyDLY, keyLISTORD, keySTATIC},
			},
		},
	}

	resp, err := pm.ic.Do(req)
	if err != nil {
		return fmt.Errorf("circuit group request: %w", err)
	}

	// Process circuit group data
	for _, obj := range resp.ObjectList {
		pm.trackCircGrp(obj)
	}

	return nil
}

func (pm *PoolMonitor) processHeaterObject(obj ObjectData) {
	name := obj.Params[keySNAME]
	subtype := obj.Params[keySUBTYP]
	status := obj.Params[keySTATUS]

	if name == "" || subtype == "" {
		return
	}

	var heaterStatusValue int
	var statusDescription string

	// Check if this heater is referenced by a body
	bodyInfo, isReferenced := pm.referencedHeaters[obj.ObjName]
	if isReferenced {
		// Use body operational data for referenced heaters
		heaterStatusValue = pm.calculateHeaterStatus(&bodyInfo, subtype)
		statusDescription = fmt.Sprintf("%s (Body: %s, HTMODE: %d)",
			pm.getStatusDescription(heaterStatusValue), bodyInfo.BodyName, bodyInfo.HTMode)
	} else {
		// For non-referenced heaters, determine status by name matching with body heating status
		heaterStatusValue = pm.calculateHeaterStatusFromName(name, status)
		statusDescription = fmt.Sprintf("%s (Non-referenced, inferred from body status)",
			pm.getStatusDescription(heaterStatusValue))
	}

	// Update Prometheus metric
	thermalStatus.WithLabelValues(obj.ObjName, name, subtype).Set(float64(heaterStatusValue))
	pm.trackThermal(name, heaterStatusValue, obj)

	// Handle temperature setpoints
	pm.updateThermalSetpoints(obj.ObjName, name, subtype, isReferenced, &bodyInfo, heaterStatusValue)

	pm.logIfNotListeningf("Updated thermal status: %s (%s) = %d [%s]",
		name, obj.ObjName, heaterStatusValue, statusDescription)
}

func (pm *PoolMonitor) updateThermalSetpoints(objName, name, subtype string, isReferenced bool, bodyInfo *BodyHeaterInfo, heaterStatusValue int) {
	// Always show heatpoint for referenced heaters
	if isReferenced {
		thermalLowSetpoint.WithLabelValues(objName, name, subtype).Set(bodyInfo.LoTemp)
	} else {
		// Remove low setpoint metric when not referenced
		thermalLowSetpoint.DeleteLabelValues(objName, name, subtype)
	}

	// Only show coolpoint if realistic temperature (< 100°F) and relevant state
	if isReferenced && bodyInfo.HiTemp < 100 && (heaterStatusValue == 3 || heaterStatusValue == 2) { // Cooling or Idle with realistic setpoint
		thermalHighSetpoint.WithLabelValues(objName, name, subtype).Set(bodyInfo.HiTemp)
	} else {
		// Remove high setpoint metric when >= 100°F, not cooling/idle, or not referenced
		thermalHighSetpoint.DeleteLabelValues(objName, name, subtype)
	}
}

func (pm *PoolMonitor) calculateHeaterStatus(bodyInfo *BodyHeaterInfo, _ string) int {
	switch bodyInfo.HTMode {
	case htModeOff:
		// When heater is off, determine if it's idle (within setpoints) or off (outside setpoints)
		if bodyInfo.Temp >= bodyInfo.LoTemp && bodyInfo.Temp <= bodyInfo.HiTemp {
			return thermalStatusIdle // Idle (heater assigned, temperature within setpoints)
		}
		return thermalStatusOff // Off (temperature outside setpoints, heater not needed)
	case htModeHeating:
		return thermalStatusHeating // Heating (traditional gas heater)
	case htModeHeatPumpHeating:
		return thermalStatusHeating // Heating (heat pump heating mode)
	case htModeHeatPumpCooling:
		return thermalStatusCooling // Cooling (heat pump cooling mode)
	default:
		return thermalStatusOff // Unknown mode, treat as off
	}
}

func (pm *PoolMonitor) calculateHeaterStatusFromName(heaterName, status string) int {
	// For non-referenced heaters, try to match with body heating status
	// Look for body names that might be associated with this heater
	heaterNameLower := strings.ToLower(heaterName)

	// Check if any body is currently heating and matches this heater's name
	for bodyName, isHeating := range pm.bodyHeatingStatus {
		if strings.Contains(heaterNameLower, bodyName) || strings.Contains(bodyName, heaterNameLower) {
			if isHeating {
				return thermalStatusHeating // Heating
			}
			return thermalStatusOff // Off
		}
	}

	// If no body match found, use the heater's own status if available
	if status == statusOn {
		return thermalStatusHeating // Heating
	}

	return thermalStatusOff // Off
}

func (pm *PoolMonitor) getStatusDescription(status int) string {
	switch status {
	case 0:
		return statusWordOff
	case 1:
		return statusWordHeating
	case thermalStatusIdle:
		return statusWordIdle
	case thermalStatusCooling:
		return statusWordCooling
	default:
		return statusWordUnknown
	}
}

func (pm *PoolMonitor) processPumpObject(obj ObjectData, responseTime time.Duration) error {
	name := obj.Params[keySNAME]
	rpmStr := obj.Params[keyRPM]
	status := obj.Params[keySTATUS]

	if rpmStr == "" || name == "" {
		return nil
	}

	rpm, err := strconv.ParseFloat(rpmStr, 64)
	if err != nil {
		log.Printf("Failed to parse RPM %s for pump %s: %v", rpmStr, name, err)
		return fmt.Errorf("failed to parse RPM %s for pump %s: %w", rpmStr, name, err)
	}

	pumpRPM.WithLabelValues(obj.ObjName, name).Set(rpm)
	pm.trackPumpRPM(name, rpm, obj)
	pm.logPumpUpdate(name, obj.ObjName, rpm, status, responseTime)
	return nil
}

func (pm *PoolMonitor) logPumpUpdate(name, objName string, rpm float64, status string, responseTime time.Duration) {
	pm.logIfNotListeningf("Updated pump RPM: %s (%s) = %.0f RPM (Status: %s) [ResponseTime: %v]", name, objName, rpm, status, responseTime)
}

func (pm *PoolMonitor) updateRefreshTimestamp() {
	pm.lastRefresh = time.Now()
	lastRefreshTimestamp.Set(float64(pm.lastRefresh.Unix()))
}

func getEnvOrDefault(envVar, defaultValue string) string {
	if value := os.Getenv(envVar); value != "" {
		return value
	}
	return defaultValue
}

func (pm *PoolMonitor) logIfNotListeningf(format string, v ...interface{}) {
	if !pm.listenMode && !pm.quiet {
		log.Printf(format, v...)
	}
}

func (pm *PoolMonitor) initializeState() {
	pm.previousState = &EquipmentState{
		WaterTemps:      make(map[string]float64),
		PumpRPMs:        make(map[string]float64),
		Circuits:        make(map[string]string),
		Thermals:        make(map[string]int),
		Features:        make(map[string]string),
		CircGrps:        make(map[string]CircGrpState),
		UnknownEquip:    make(map[string]string),
		ParseErrors:     make(map[string]bool),
		SkippedFeatures: make(map[string]bool),
	}
}

// logPollChangef logs a change and increments the change counter.
func (pm *PoolMonitor) logPollChangef(format string, args ...interface{}) {
	log.Printf("POLL: "+format, args...)
	pm.previousState.PollChangeCount++
}

// trackNumericValue is a generic helper for tracking numeric values (temps, RPM).
// It handles the common pattern of detect/change logging with raw JSON output.
func (pm *PoolMonitor) trackNumericValue(
	name string,
	value float64,
	obj ObjectData,
	valueMap map[string]float64,
	detectFmt string,
	changeFmt string,
) {
	prev, exists := valueMap[name]
	if !exists {
		if !pm.initialPollDone {
			log.Printf(detectFmt, name, value)
			pm.outputRawObjectData(obj)
		}
	} else if prev != value {
		pm.logPollChangef(changeFmt, name, prev, value)
		pm.outputRawObjectData(obj)
	}
	valueMap[name] = value
}

func (pm *PoolMonitor) trackWaterTemp(name string, temp float64, obj ObjectData) {
	if !pm.listenMode {
		return
	}
	if pm.previousState == nil {
		pm.initializeState()
	}
	pm.trackNumericValue(name, temp, obj, pm.previousState.WaterTemps,
		"POLL: %s temperature detected: %.1f°F",
		"%s temperature changed: %.1f°F → %.1f°F")
}

func (pm *PoolMonitor) trackAirTemp(temp float64, obj ObjectData) {
	if !pm.listenMode {
		return
	}
	if pm.previousState == nil {
		pm.initializeState()
	}

	if pm.previousState.AirTemp == 0 {
		// First time seeing air temp - only log on initial poll
		if !pm.initialPollDone {
			log.Printf("POLL: Air temperature detected: %.1f°F", temp)
			pm.outputRawObjectData(obj)
		}
	} else if pm.previousState.AirTemp != temp {
		pm.logPollChangef("Air temperature changed: %.1f°F → %.1f°F", pm.previousState.AirTemp, temp)
		pm.outputRawObjectData(obj)
	}
	pm.previousState.AirTemp = temp
}

func (pm *PoolMonitor) trackPumpRPM(name string, rpm float64, obj ObjectData) {
	if !pm.listenMode {
		return
	}
	if pm.previousState == nil {
		pm.initializeState()
	}
	pm.trackNumericValue(name, rpm, obj, pm.previousState.PumpRPMs,
		"POLL: %s detected: %.0f RPM",
		"%s RPM changed: %.0f → %.0f")
}

func (pm *PoolMonitor) trackCircuit(name, status string, obj ObjectData) {
	if !pm.listenMode {
		return
	}
	if pm.previousState == nil {
		pm.initializeState()
	}

	prevStatus, exists := pm.previousState.Circuits[name]
	if !exists {
		// First time seeing this circuit - only log on initial poll
		if !pm.initialPollDone {
			log.Printf("POLL: %s detected: %s", name, status)
			pm.outputRawObjectData(obj)
		}
	} else if prevStatus != status {
		pm.logPollChangef("%s turned %s", name, status)
		pm.outputRawObjectData(obj)
	}
	pm.previousState.Circuits[name] = status
}

func (pm *PoolMonitor) trackThermal(name string, status int, obj ObjectData) {
	if !pm.listenMode {
		return
	}
	if pm.previousState == nil {
		pm.initializeState()
	}

	prevStatus, exists := pm.previousState.Thermals[name]
	if !exists {
		// First time seeing this thermal equipment - only log on initial poll
		if !pm.initialPollDone {
			log.Printf("POLL: %s detected: %s", name, pm.getStatusDescription(status))
			pm.outputRawObjectData(obj)
		}
	} else if prevStatus != status {
		pm.logPollChangef("%s status changed: %s → %s", name,
			pm.getStatusDescription(prevStatus), pm.getStatusDescription(status))
		pm.outputRawObjectData(obj)
	}
	pm.previousState.Thermals[name] = status
}

func (pm *PoolMonitor) trackFeature(name, status string) {
	if !pm.listenMode {
		return
	}
	if pm.previousState == nil {
		pm.initializeState()
	}

	prevStatus, exists := pm.previousState.Features[name]
	if !exists {
		// First time seeing this feature - only log on initial poll
		if !pm.initialPollDone {
			log.Printf("POLL: %s detected: %s", name, status)
		}
	} else if prevStatus != status {
		pm.logPollChangef("%s turned %s", name, status)
	}
	pm.previousState.Features[name] = status
}

func (pm *PoolMonitor) trackCircGrp(obj ObjectData) {
	if !pm.listenMode {
		return
	}
	if pm.previousState == nil {
		pm.initializeState()
	}

	objName := obj.ObjName
	newState := CircGrpState{
		Active:  obj.Params[keyACT],
		Use:     obj.Params[keyUSE],
		Circuit: obj.Params[objTypeCircuit],
		Parent:  obj.Params[keyPARENT],
	}

	prevState, exists := pm.previousState.CircGrps[objName]
	pm.previousState.CircGrps[objName] = newState

	// Resolve parent group and circuit names for display
	groupName := pm.resolveCircuitName(newState.Parent)
	circuitName := pm.resolveCircuitName(newState.Circuit)

	if !exists {
		// First time seeing this circuit group member - only log on initial poll
		if !pm.initialPollDone {
			log.Printf("POLL: CircGrp %s/%s detected: act=%s use=%s",
				groupName, circuitName, newState.Active, newState.Use)
		}
		return
	}

	if prevState == newState {
		return
	}

	// Log what changed
	changes := pm.buildCircGrpChanges(prevState, newState)
	if len(changes) > 0 {
		pm.logPollChangef("CircGrp %s/%s changed: %s",
			groupName, circuitName, strings.Join(changes, " "))
	}
}

// resolveCircuitName returns the SNAME for a circuit/group ID, or the ID itself if not found.
func (pm *PoolMonitor) resolveCircuitName(objID string) string {
	if name, ok := pm.circuitNames[objID]; ok && name != "" {
		return name
	}
	return objID
}

func (pm *PoolMonitor) buildCircGrpChanges(prevState, newState CircGrpState) []string {
	var changes []string
	if prevState.Active != newState.Active {
		changes = append(changes, fmt.Sprintf("act=%s→%s", prevState.Active, newState.Active))
	}
	if prevState.Use != newState.Use {
		changes = append(changes, fmt.Sprintf("use=%s→%s", prevState.Use, newState.Use))
	}
	return changes
}

func (pm *PoolMonitor) getAllObjects() error {
	req := IntelliCenterRequest{
		Command:   cmdGetParamList,
		Condition: "", // No filter - get everything
		ObjectList: []ObjectQuery{
			{
				ObjName: objnamIncr,
				Keys:    []string{keySNAME, keySTATUS, keyOBJTYP, keySUBTYP},
			},
		},
	}

	resp, err := pm.ic.Do(req)
	if err != nil {
		return fmt.Errorf("all objects request: %w", err)
	}

	// Process all objects and track unknown ones
	for _, obj := range resp.ObjectList {
		pm.trackUnknownEquipment(obj)
	}

	return nil
}

func (pm *PoolMonitor) trackUnknownEquipment(obj ObjectData) {
	if !pm.listenMode || pm.previousState == nil {
		return
	}

	objType := obj.Params[keyOBJTYP]
	name := obj.Params[keySNAME]
	status := obj.Params[keySTATUS]
	subtype := obj.Params[keySUBTYP]

	// Skip if already handled by specific equipment types
	switch objType {
	case objTypeBody, objTypePump, objTypeCircuit, objTypeHeater, objTypeCircGrp:
		return // Already tracked by specific handlers
	case "":
		return // No object type, skip
	}

	// Skip internal/system objects
	if strings.HasPrefix(obj.ObjName, "_") || strings.HasPrefix(obj.ObjName, "X") {
		return
	}

	// Build a tracking key with meaningful info
	trackingValue := fmt.Sprintf("%s:%s", objType, status)
	if subtype != "" {
		trackingValue = fmt.Sprintf("%s/%s:%s", objType, subtype, status)
	}

	prevValue, exists := pm.previousState.UnknownEquip[obj.ObjName]

	// Log equipment changes with appropriate format
	if !exists {
		// Only log on initial poll
		if !pm.initialPollDone {
			pm.logUnknownEquipmentDetected(name, obj.ObjName, objType, status)
		}
	} else if prevValue != trackingValue {
		pm.logUnknownEquipmentChanged(name, obj.ObjName, prevValue, trackingValue)
	}

	pm.previousState.UnknownEquip[obj.ObjName] = trackingValue
}

func (pm *PoolMonitor) logUnknownEquipmentDetected(name, objName, objType, status string) {
	if name != "" {
		log.Printf("POLL: Unknown equipment detected - %s (%s) type=%s status=%s", name, objName, objType, status)
		return
	}
	log.Printf("POLL: Unknown equipment detected - %s type=%s status=%s", objName, objType, status)
}

func (pm *PoolMonitor) logUnknownEquipmentChanged(name, objName, prevValue, trackingValue string) {
	if name != "" {
		log.Printf("POLL: Unknown equipment changed - %s (%s) %s → %s", name, objName, prevValue, trackingValue)
		return
	}
	log.Printf("POLL: Unknown equipment changed - %s %s → %s", objName, prevValue, trackingValue)
}

func createMetricsHandler(registry *prometheus.Registry, _ *PoolMonitor) http.Handler {
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
}

type appConfig struct {
	intelliCenterIP   string
	intelliCenterPort string
	httpPort          string // port the HTTP /metrics server binds, in every mode
	listenMode        bool
	homebridge        bool
	autoDiscover      bool // no static IP given → (re)discover via mDNS
	pollInterval      time.Duration
}

type commandLineFlags struct {
	intelliCenterIP   *string
	intelliCenterPort *string
	httpPort          *string
	listenMode        *bool
	homebridge        *bool
	pollInterval      *int
	showVersion       *bool
	discoverOnly      *bool
}

func defineFlags() *commandLineFlags {
	return &commandLineFlags{
		intelliCenterIP: flag.String("ic-ip", getEnvOrDefault("PENTAMETER_IC_IP", ""),
			"IntelliCenter IP address (optional, will auto-discover if not provided, env: PENTAMETER_IC_IP)"),
		intelliCenterPort: flag.String("ic-port", getEnvOrDefault("PENTAMETER_IC_PORT", "6680"),
			"IntelliCenter WebSocket port (env: PENTAMETER_IC_PORT)"),
		httpPort: flag.String("http-port", getEnvOrDefault("PENTAMETER_HTTP_PORT", "8080"),
			"HTTP server port for metrics (env: PENTAMETER_HTTP_PORT)"),
		listenMode: flag.Bool("listen", getEnvOrDefault("PENTAMETER_LISTEN", "false") == trueString,
			"Enable live event logging mode with raw JSON output (env: PENTAMETER_LISTEN)"),
		homebridge: flag.Bool("homebridge", getEnvOrDefault("PENTAMETER_HOMEBRIDGE", "false") == trueString,
			"Run as a Homebridge sidecar (stdio JSON IPC; auto-discovers if no IP; env: PENTAMETER_HOMEBRIDGE)"),
		pollInterval: flag.Int("interval", getEnvIntOrDefault("PENTAMETER_INTERVAL", 0),
			"Polling interval in seconds (default 60; 10 in listen mode; minimum 5; env: PENTAMETER_INTERVAL)"),
		showVersion:  flag.Bool("version", false, "Show version information"),
		discoverOnly: flag.Bool("discover", false, "Discover IntelliCenter IP address and exit"),
	}
}

func getEnvIntOrDefault(envVar string, defaultValue int) int {
	if env := os.Getenv(envVar); env != "" {
		if val, err := strconv.Atoi(env); err == nil {
			return val
		}
	}
	return defaultValue
}

func handleEarlyExitFlags(flags *commandLineFlags) {
	if *flags.showVersion {
		log.Printf("pentameter %s", version)
		os.Exit(0)
	}

	if *flags.discoverOnly {
		log.Println("Discovering IntelliCenter...")
		log.Println("Searching for IntelliCenter on network (up to 60 seconds). Press Ctrl-C to cancel.")
		ip, err := DiscoverIntelliCenter(true)
		if err != nil {
			log.Fatalf("Discovery failed: %v", err)
		}
		log.Printf("IntelliCenter discovered at: %s", ip)
		os.Exit(0)
	}
}

func determinePollInterval(pollIntervalSeconds int, listenMode bool) time.Duration {
	if pollIntervalSeconds > 0 {
		if pollIntervalSeconds < minPollInterval {
			log.Printf("Warning: interval %ds is below minimum (%ds), using %ds",
				pollIntervalSeconds, minPollInterval, minPollInterval)
			return minPollInterval * time.Second
		}
		return time.Duration(pollIntervalSeconds) * time.Second
	}
	if listenMode {
		return listenModePollInterval * time.Second
	}
	return defaultPollInterval * time.Second
}

// newDiscoveryResolver returns an engine Resolve hook that rediscovers the
// IntelliCenter via mDNS before each (re)connect, or nil when a static IP was
// configured (no rediscovery needed). This lets the engine-driven modes follow a
// controller whose IP changes, matching the legacy paths' attemptRediscovery.
func newDiscoveryResolver(cfg *appConfig) func() (string, error) {
	if !cfg.autoDiscover {
		return nil
	}
	return func() (string, error) { return DiscoverIntelliCenter(false) }
}

func resolveIntelliCenterIP(ip string) string {
	if ip != "" {
		return ip
	}
	log.Println("No IP address provided, attempting auto-discovery...")
	log.Println("Tip: Specify with --ic-ip flag or export PENTAMETER_IC_IP environment variable to skip discovery")
	log.Println("Searching for IntelliCenter on network (up to 60 seconds). Press Ctrl-C to cancel.")
	discoveredIP, err := DiscoverIntelliCenter(true)
	if err != nil {
		log.Fatalf("Auto-discovery failed: %v\nPlease provide IP address using --ic-ip flag or PENTAMETER_IC_IP environment variable", err)
	}
	log.Printf("Auto-discovered IntelliCenter at: %s", discoveredIP)
	return discoveredIP
}

// doubleDashUsage prints flags in --flag form, grouped into Functions (run once
// and exit), Modes (which long-running role to run as), and Configuration
// (settings). Go's flag package already accepts -flag and --flag identically;
// this only changes how usage is displayed. Both forms keep working.
func doubleDashUsage() {
	out := flag.CommandLine.Output()
	fmt.Fprintf(out, "Usage of %s:\n", os.Args[0])
	groups := []struct {
		title string
		names []string
	}{
		{"Functions (run once and exit)", []string{"discover", "version"}},
		{"Modes (default: Prometheus metrics exporter)", []string{"homebridge", "listen"}},
		{"Configuration", []string{"ic-ip", "ic-port", "http-port", "interval"}},
	}
	for _, grp := range groups {
		fmt.Fprintf(out, "\n%s:\n", grp.title)
		for _, name := range grp.names {
			if fl := flag.Lookup(name); fl != nil {
				printFlagUsage(out, fl)
			}
		}
	}
}

// printFlagUsage renders one flag in --flag form, mirroring the stdlib's
// PrintDefaults layout (name, arg type, usage, default when non-zero).
func printFlagUsage(out io.Writer, fl *flag.Flag) {
	argName, usage := flag.UnquoteUsage(fl)
	line := "  --" + fl.Name
	if argName != "" {
		line += " " + argName
	}
	fmt.Fprintln(out, line)
	fmt.Fprintf(out, "    \t%s", usage)
	if !isZeroFlagValue(fl.DefValue) {
		fmt.Fprintf(out, " (default %q)", fl.DefValue)
	}
	fmt.Fprintln(out)
}

// isZeroFlagValue reports whether a flag's default is its type's zero value, so
// (like the stdlib's PrintDefaults) we omit "(default ...)" for those.
func isZeroFlagValue(v string) bool {
	return v == "" || v == "false" || v == "0"
}

func parseCommandLineFlags() *appConfig {
	flags := defineFlags()
	flag.Usage = doubleDashUsage
	flag.Parse()

	handleEarlyExitFlags(flags)

	cfg := &appConfig{
		intelliCenterIP:   *flags.intelliCenterIP,
		intelliCenterPort: *flags.intelliCenterPort,
		httpPort:          *flags.httpPort,
		listenMode:        *flags.listenMode,
		homebridge:        *flags.homebridge,
		pollInterval:      determinePollInterval(*flags.pollInterval, *flags.listenMode),
	}
	cfg.autoDiscover = cfg.intelliCenterIP == ""
	// All modes now run an intellicenter.Engine, which rediscovers via its Resolve
	// hook; up-front discovery would only block and Fatal. So resolve here only
	// when a static IP was given (a passthrough/validation, no discovery).
	if !cfg.autoDiscover {
		cfg.intelliCenterIP = resolveIntelliCenterIP(cfg.intelliCenterIP)
	}
	return cfg
}

func logStartupMessage(cfg *appConfig) {
	log.Printf("Starting pool monitor for IntelliCenter at %s:%s", cfg.intelliCenterIP, cfg.intelliCenterPort)
	if cfg.listenMode {
		log.Printf("Listen mode enabled - real-time push + polling every %v", cfg.pollInterval)
	} else {
		log.Printf("HTTP server will run on port %s", cfg.httpPort)
		log.Printf("Polling interval: %v", cfg.pollInterval)
	}
}

func createPrometheusRegistry() *prometheus.Registry {
	registry := prometheus.NewRegistry()
	registry.MustRegister(poolTemperature)
	registry.MustRegister(airTemperature)
	registry.MustRegister(connectionFailure)
	registry.MustRegister(lastRefreshTimestamp)
	registry.MustRegister(pumpRPM)
	registry.MustRegister(circuitStatus)
	registry.MustRegister(thermalStatus)
	registry.MustRegister(thermalLowSetpoint)
	registry.MustRegister(thermalHighSetpoint)
	registry.MustRegister(featureStatus)
	return registry
}

func setupHTTPEndpoints(registry *prometheus.Registry, monitor *PoolMonitor, httpPort string) {
	http.Handle("/metrics", createMetricsHandler(registry, monitor))
	http.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("OK")); err != nil {
			log.Printf("Failed to write health check response: %v", err)
		}
	})

	serverAddr := ":" + httpPort
	log.Printf("Starting Prometheus metrics server on %s", serverAddr)
	log.Printf("Metrics available at http://localhost:%s/metrics", httpPort)
	startServer(serverAddr)
}

func main() {
	cfg := parseCommandLineFlags()

	if cfg.homebridge {
		runHomebridge(cfg)
		return
	}

	logStartupMessage(cfg)

	registry := createPrometheusRegistry()

	// Metrics and listen modes are both driven by the push-based
	// intellicenter.Engine (real-time gauges / events, with the poll as a safety
	// net). The engine owns connection, reconnect, and mDNS rediscovery.
	if cfg.listenMode {
		runListenEngine(cfg)
	} else {
		runMetricsEngine(cfg, registry)
	}
}

func startServer(serverAddr string) {
	server := &http.Server{
		Addr:         serverAddr,
		Handler:      nil,
		ReadTimeout:  httpReadTimeout,
		WriteTimeout: httpWriteTimeout,
		IdleTimeout:  httpIdleTimeout,
	}

	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("HTTP server failed: %v", err)
	}
}
