// Package intellicenter is a reusable client for the Pentair IntelliCenter pool
// controller. It speaks the unauthenticated JSON-over-WebSocket protocol on port
// 6680 and exposes typed queries, control (writes), and value interpretation so
// that pentameter's metrics, listen, and homebridge modes can all share one
// implementation.
//
// The protocol is documented in pentameter's API.md, which is the source of
// truth. Notable facts encoded here:
//   - All param values are strings on the wire (parse defensively).
//   - Requests carry a unique messageID; the matching response echoes it.
//   - IntelliCenter also sends unsolicited WriteParamList/NotifyList pushes; a
//     request's read loop must skip those until its messageID arrives.
package intellicenter

import "time"

// Tunables (ported from pentameter's main.go constants).
const (
	handshakeTimeout    = 10 * time.Second
	pingTimeout         = 5 * time.Second
	responseReadTimeout = 30 * time.Second
	healthCheckInterval = 30 * time.Second

	// Skip at most this many unsolicited pushes while awaiting a response.
	maxUnsolicitedMessages = 10

	// Reconnect backoff.
	maxRetries       = 5
	baseDelay        = 1 * time.Second
	maxDelay         = 30 * time.Second
	backoffFactor    = 2.0
	nanosecondMod    = 1000000
	defaultICPortStr = "6680"
)

// --- wire types (JSON shapes per API.md) ---------------------------------

// Request is an IntelliCenter command. ObjectList items carry Keys for queries
// (GetParamList) or Params for writes (SetParamList).
type Request struct {
	MessageID  string   `json:"messageID"`
	Command    string   `json:"command"`
	Condition  string   `json:"condition,omitempty"`
	ObjectList []Object `json:"objectList,omitempty"`
}

// Object is one entry in a request/response objectList.
type Object struct {
	ObjName string            `json:"objnam"`
	Keys    []string          `json:"keys,omitempty"`
	Params  map[string]string `json:"params,omitempty"`
}

// Response is an IntelliCenter reply (or unsolicited push).
type Response struct {
	Command    string       `json:"command"`
	MessageID  string       `json:"messageID"`
	Response   string       `json:"response"`
	ObjectList []ObjectData `json:"objectList"`
}

// ObjectData is one object's params in a response.
type ObjectData struct {
	ObjName string            `json:"objnam"`
	Params  map[string]string `json:"params"`
}

// --- domain types --------------------------------------------------------

// Circuit is a circuit or feature (objnam C#### / FTR##).
type Circuit struct {
	ID      string
	Name    string // SNAME
	ObjType string // OBJTYP
	SubType string // SUBTYP
	On      bool   // STATUS == "ON"
	Freeze  bool   // FREEZE == "ON"
	Feature bool   // FEATR == "ON" (flagged as a Feature in IntelliCenter)
}

// Body is a pool/spa body (objnam B####).
type Body struct {
	ID        string
	Name      string  // SNAME
	On        bool    // STATUS == "ON"
	Temp      float64 // TEMP (current water temp)
	HeatMode  int     // HTMODE (0 off, 1 heat, 4 HP heat, 9 HP cool)
	HeaterID  string  // HTSRC (assigned heater objnam)
	LoSetTemp float64 // LOTMP (heat setpoint)
	HiSetTemp float64 // HITMP (cool setpoint)
}

// Pump is a pump (objnam PMP##). Watts/GPM are poll-only (never pushed).
type Pump struct {
	ID   string
	Name string // SNAME
	// On means the pump is running. Pump STATUS is a numeric code ("10" when
	// running), not "ON", so on/off is derived from RPM > 0 — the unambiguous
	// "is it spinning" signal.
	On      bool
	RPM     float64 // RPM (current speed)
	MaxRPM  float64 // MAX (configured maximum speed)
	Watts   float64 // PWR (real power draw; WATTS key is a garbage echo on current firmware)
	GPM     float64 // GPM (estimated, not measured, when the pump has no flow capability — MaxFlow==0)
	MaxFlow float64 // MAXF (max flow; 0 == no flow capability, so GPM is a controller estimate)
}

// Heater is a heater (objnam H####).
type Heater struct {
	ID      string
	Name    string // SNAME
	On      bool   // STATUS == "ON"
	SubType string // SUBTYP (ULTRA = heat pump, GENERIC = gas, SOLAR)
	Body    string // BODY: space-separated body IDs this heater serves
	Cool    bool   // COOL == "ON" (heat pump cooling capability)
	// Real distinguishes a configured heater device from a "Preferred"/combo
	// pseudo-object (e.g. HXULT), whose params echo their own key names. A real
	// heater has a concrete STATUS ("ON"/"OFF"); a pseudo one has STATUS="STATUS".
	Real bool
}

// Sensor is a temperature sensor reading (e.g. air _A135).
type Sensor struct {
	ID      string
	Name    string  // SNAME
	SubType string  // SUBTYP (used as a metric label)
	Temp    float64 // PROBE
	Valid   bool
}

// Heat/thermal status values (match pentameter's metric encoding).
const (
	HeatStatusOff     = 0
	HeatStatusHeating = 1
	HeatStatusIdle    = 2
	HeatStatusCooling = 3

	htModeOff             = 0
	htModeHeating         = 1
	htModeHeatPumpHeating = 4
	htModeHeatPumpCooling = 9

	statusOn = "ON"
)

// Protocol command names, param keys, and values used across queries/writes.
const (
	cmdGetParamList = "GetParamList"
	cmdSetParamList = "SetParamList"
	cmdGetQuery     = "GetQuery"

	// GetConfiguration query (feature visibility via SHOMNU).
	queryConfiguration = "GetConfiguration"
	keyShomnu          = "SHOMNU"
	ftrPrefix          = "FTR"

	// Raw-request field names (DoRaw map keys / GetQuery envelope).
	fieldCommand   = "command"
	fieldQueryName = "queryName"
	fieldArguments = "arguments"
	fieldAnswer    = "answer"

	keyStatus = "STATUS"
	keyLoTmp  = "LOTMP"
	keyHiTmp  = "HITMP"
	keyFreeze = "FREEZE"
	keyFeatr  = "FEATR"
	keyProbe  = "PROBE"
	keySName  = "SNAME"
	keyObjTyp = "OBJTYP"
	keySubTyp = "SUBTYP"
	keyTemp   = "TEMP"
	keyHTMode = "HTMODE"
	keyHTSrc  = "HTSRC"  // read-only: the body's currently-assigned heat source
	keyHeater = "HEATER" // writable: assign/clear a body's heat source (HTSRC is NOT writable)
	keyBody   = "BODY"
	keyCool   = "COOL"
	keyRPM    = "RPM"
	keyMax    = "MAX"
	// keyPwr is the pump's real power draw. The intuitive "WATTS" key returns a
	// garbage echo on current IntelliCenter firmware; PWR holds the actual value
	// (verified on hardware: VS@1800rpm=215W, VSF@2450rpm=760W). keyWatts is kept
	// as a fallback for firmwares that may populate it instead.
	keyPwr   = "PWR"
	keyWatts = "WATTS"
	keyGPM   = "GPM"
	keyMaxF  = "MAXF" // max flow; 0 == pump has no flow capability (GPM is estimated)

	// PMPCIRC speed-assignment keys: CIRCUIT is the driven circuit/feature objnam,
	// PARENT is the pump that runs it. Together they form the circuit⇄pump graph.
	keyCircuit = "CIRCUIT"
	keyParent  = "PARENT"

	condCircuit = "OBJTYP=CIRCUIT"
	condBody    = "OBJTYP=BODY"
	condPump    = "OBJTYP=PUMP"
	condHeater  = "OBJTYP=HEATER"
	condPMPCirc = "OBJTYP=PMPCIRC"

	valueOff = "OFF"
)

// Kind identifies an equipment type within the engine's state model.
type Kind string

const (
	KindCircuit Kind = "circuit"
	KindBody    Kind = "body"
	KindPump    Kind = "pump"
	KindHeater  Kind = "heater"
	KindSensor  Kind = "sensor"
	KindPMPCirc Kind = "pmpcirc" // PMPCIRC speed assignment (circuit⇄pump link); raw-only, no typed snapshot
)
