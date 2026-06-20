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
	ID    string
	Name  string  // SNAME
	On    bool    // STATUS == "ON"
	RPM   float64 // RPM
	Watts float64 // WATTS
	GPM   float64 // GPM
}

// Heater is a heater (objnam H####).
type Heater struct {
	ID      string
	Name    string // SNAME
	On      bool   // STATUS == "ON"
	SubType string // SUBTYP
}

// Sensor is a temperature sensor reading (e.g. air _A135).
type Sensor struct {
	ID    string
	Name  string  // SNAME
	Temp  float64 // PROBE
	Valid bool
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

	keyStatus = "STATUS"
	keyLoTmp  = "LOTMP"
	keyHiTmp  = "HITMP"
	keyFreeze = "FREEZE"
	keyProbe  = "PROBE"
	keySName  = "SNAME"
	keyObjTyp = "OBJTYP"
	keySubTyp = "SUBTYP"
	keyTemp   = "TEMP"
	keyHTMode = "HTMODE"
	keyHTSrc  = "HTSRC"
	keyRPM    = "RPM"
	keyWatts  = "WATTS"
	keyGPM    = "GPM"

	condSense = "OBJTYP=SENSE"

	valueOff = "OFF"
)
