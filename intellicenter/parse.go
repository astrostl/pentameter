package intellicenter

// Key sets requested per object type, shared by the Client query methods and the
// Engine's baseline/poll so the wire requests stay identical.
var (
	circuitKeys = []string{keySName, keyStatus, keyObjTyp, keySubTyp, keyFreeze, keyFeatr}
	bodyKeys    = []string{keySName, keyStatus, keyTemp, keySubTyp, keyHTMode, keyHTSrc, keyLoTmp, keyHiTmp}
	pumpKeys    = []string{keySName, keyStatus, keyRPM, keyMax, keyPwr, keyWatts, keyGPM, keyMaxF}
	heaterKeys  = []string{keySName, keyStatus, keySubTyp, keyObjTyp, keyBody, keyCool}
	sensorKeys  = []string{keySName, keyProbe, keySubTyp, keyStatus}
	pmpCircKeys = []string{keyCircuit, keyParent}
)

// Per-object parsers: build a typed domain value from a (possibly merged) param
// map. Used both by one-shot queries and by incremental push merges.

func circuitFrom(objnam string, params map[string]string) Circuit {
	return Circuit{
		ID:      objnam,
		Name:    params[keySName],
		ObjType: params[keyObjTyp],
		SubType: params[keySubTyp],
		On:      params[keyStatus] == statusOn,
		Freeze:  params[keyFreeze] == statusOn,
		Feature: params[keyFeatr] == statusOn,
	}
}

func bodyFrom(objnam string, params map[string]string) Body {
	return Body{
		ID:        objnam,
		Name:      params[keySName],
		On:        params[keyStatus] == statusOn,
		Temp:      parseFloat(params[keyTemp]),
		HeatMode:  parseInt(params[keyHTMode]),
		HeaterID:  params[keyHTSrc],
		LoSetTemp: parseFloat(params[keyLoTmp]),
		HiSetTemp: parseFloat(params[keyHiTmp]),
	}
}

func pumpFrom(objnam string, params map[string]string) Pump {
	rpm := parseFloat(params[keyRPM])
	// Power lives under PWR; fall back to WATTS for firmwares that populate it.
	watts := parseFloat(params[keyPwr])
	if watts == 0 {
		watts = parseFloat(params[keyWatts])
	}
	return Pump{
		ID:      objnam,
		Name:    params[keySName],
		On:      rpm > 0, // STATUS is a numeric code, not "ON"; RPM > 0 == running
		RPM:     rpm,
		MaxRPM:  parseFloat(params[keyMax]),
		Watts:   watts,
		GPM:     parseFloat(params[keyGPM]),
		MaxFlow: parseFloat(params[keyMaxF]),
	}
}

func heaterFrom(objnam string, params map[string]string) Heater {
	status := params[keyStatus]
	return Heater{
		ID:      objnam,
		Name:    params[keySName],
		On:      status == statusOn,
		SubType: params[keySubTyp],
		Body:    params[keyBody],
		Cool:    params[keyCool] == statusOn,
		// A configured heater reports a concrete STATUS; a pseudo "Preferred"
		// object echoes the key name (STATUS="STATUS").
		Real: status == statusOn || status == valueOff,
	}
}

func sensorFrom(objnam string, params map[string]string) Sensor {
	probe := params[keyProbe]
	return Sensor{
		ID:      objnam,
		Name:    params[keySName],
		SubType: params[keySubTyp],
		Temp:    parseFloat(probe),
		Valid:   probe != "",
	}
}
