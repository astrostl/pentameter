package intellicenter

import "strconv"

// query runs a GetParamList over all objects matching condition (the "INCR"
// iterate-all convention) requesting the given keys.
func (c *Client) query(prefix, condition string, keys []string) ([]ObjectData, error) {
	resp, err := c.roundTrip(prefix, Request{
		Command:    cmdGetParamList,
		Condition:  condition,
		ObjectList: []Object{{ObjName: "INCR", Keys: keys}},
	})
	if err != nil {
		return nil, err
	}
	return resp.ObjectList, nil
}

// Circuits lists all circuits/features with on/off + freeze state.
func (c *Client) Circuits() ([]Circuit, error) {
	objs, err := c.query("circuits", "OBJTYP=CIRCUIT", []string{keySName, keyStatus, keyObjTyp, keySubTyp, keyFreeze})
	if err != nil {
		return nil, err
	}
	out := make([]Circuit, 0, len(objs))
	for _, o := range objs {
		if o.Params[keySName] == "" || o.Params[keyStatus] == "" {
			continue
		}
		out = append(out, Circuit{
			ID:      o.ObjName,
			Name:    o.Params[keySName],
			ObjType: o.Params[keyObjTyp],
			SubType: o.Params[keySubTyp],
			On:      o.Params[keyStatus] == statusOn,
			Freeze:  o.Params[keyFreeze] == statusOn,
		})
	}
	return out, nil
}

// Bodies lists pool/spa bodies with temp + heat mode + setpoints.
func (c *Client) Bodies() ([]Body, error) {
	objs, err := c.query("bodies", "OBJTYP=BODY", []string{keySName, keyStatus, keyTemp, keySubTyp, keyHTMode, keyHTSrc, keyLoTmp, keyHiTmp})
	if err != nil {
		return nil, err
	}
	out := make([]Body, 0, len(objs))
	for _, o := range objs {
		if o.Params[keySName] == "" {
			continue
		}
		out = append(out, Body{
			ID:        o.ObjName,
			Name:      o.Params[keySName],
			On:        o.Params[keyStatus] == statusOn,
			Temp:      parseFloat(o.Params[keyTemp]),
			HeatMode:  parseInt(o.Params[keyHTMode]),
			HeaterID:  o.Params[keyHTSrc],
			LoSetTemp: parseFloat(o.Params[keyLoTmp]),
			HiSetTemp: parseFloat(o.Params[keyHiTmp]),
		})
	}
	return out, nil
}

// Pumps lists pumps with RPM/WATTS/GPM (poll-only values).
func (c *Client) Pumps() ([]Pump, error) {
	objs, err := c.query("pumps", "OBJTYP=PUMP", []string{keySName, keyStatus, keyRPM, keyWatts, keyGPM})
	if err != nil {
		return nil, err
	}
	out := make([]Pump, 0, len(objs))
	for _, o := range objs {
		if o.Params[keySName] == "" {
			continue
		}
		out = append(out, Pump{
			ID:    o.ObjName,
			Name:  o.Params[keySName],
			On:    o.Params[keyStatus] == statusOn,
			RPM:   parseFloat(o.Params[keyRPM]),
			Watts: parseFloat(o.Params[keyWatts]),
			GPM:   parseFloat(o.Params[keyGPM]),
		})
	}
	return out, nil
}

// Heaters lists heaters.
func (c *Client) Heaters() ([]Heater, error) {
	objs, err := c.query("heaters", "OBJTYP=HEATER", []string{keySName, keyStatus, keySubTyp, keyObjTyp})
	if err != nil {
		return nil, err
	}
	out := make([]Heater, 0, len(objs))
	for _, o := range objs {
		if o.Params[keySName] == "" {
			continue
		}
		out = append(out, Heater{
			ID:      o.ObjName,
			Name:    o.Params[keySName],
			On:      o.Params[keyStatus] == statusOn,
			SubType: o.Params[keySubTyp],
		})
	}
	return out, nil
}

// Sensor reads a single object's temperature PROBE (e.g. air "_A135").
func (c *Client) Sensor(objnam string) (Sensor, error) {
	resp, err := c.roundTrip("sensor", Request{
		Command:    cmdGetParamList,
		Condition:  condSense,
		ObjectList: []Object{{ObjName: objnam, Keys: []string{keySName, keyProbe}}},
	})
	if err != nil {
		return Sensor{}, err
	}
	for _, o := range resp.ObjectList {
		if o.ObjName == objnam {
			probe := o.Params[keyProbe]
			return Sensor{ID: objnam, Name: o.Params[keySName], Temp: parseFloat(probe), Valid: probe != ""}, nil
		}
	}
	return Sensor{ID: objnam}, nil
}

// NOTE: feature-visibility filtering via GetConfiguration/SHOMNU is deferred.
// That request uses queryName/arguments and a different response envelope
// ("answer", not "objectList"); add it during the feature-visibility increment.

func parseFloat(s string) float64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}

func parseInt(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}
