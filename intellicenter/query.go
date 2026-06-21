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
	objs, err := c.query("circuits", condCircuit, circuitKeys)
	if err != nil {
		return nil, err
	}
	out := make([]Circuit, 0, len(objs))
	for _, o := range objs {
		if o.Params[keySName] == "" || o.Params[keyStatus] == "" {
			continue
		}
		out = append(out, circuitFrom(o.ObjName, o.Params))
	}
	return out, nil
}

// Bodies lists pool/spa bodies with temp + heat mode + setpoints.
func (c *Client) Bodies() ([]Body, error) {
	objs, err := c.query("bodies", condBody, bodyKeys)
	if err != nil {
		return nil, err
	}
	out := make([]Body, 0, len(objs))
	for _, o := range objs {
		if o.Params[keySName] == "" {
			continue
		}
		out = append(out, bodyFrom(o.ObjName, o.Params))
	}
	return out, nil
}

// Pumps lists pumps with RPM/WATTS/GPM (poll-only values).
func (c *Client) Pumps() ([]Pump, error) {
	objs, err := c.query("pumps", condPump, pumpKeys)
	if err != nil {
		return nil, err
	}
	out := make([]Pump, 0, len(objs))
	for _, o := range objs {
		if o.Params[keySName] == "" {
			continue
		}
		out = append(out, pumpFrom(o.ObjName, o.Params))
	}
	return out, nil
}

// Heaters lists heaters.
func (c *Client) Heaters() ([]Heater, error) {
	objs, err := c.query("heaters", condHeater, heaterKeys)
	if err != nil {
		return nil, err
	}
	out := make([]Heater, 0, len(objs))
	for _, o := range objs {
		if o.Params[keySName] == "" {
			continue
		}
		out = append(out, heaterFrom(o.ObjName, o.Params))
	}
	return out, nil
}

// Sensor reads a single object's temperature PROBE (e.g. air "_A135").
func (c *Client) Sensor(objnam string) (Sensor, error) {
	resp, err := c.roundTrip("sensor", Request{
		Command: cmdGetParamList,
		// No condition: the air sensor (_A135) is queried by objnam directly, matching
		// the hardware-proven request shape from pentameter's getAirTemperature.
		ObjectList: []Object{{ObjName: objnam, Keys: sensorKeys}},
	})
	if err != nil {
		return Sensor{}, err
	}
	for _, o := range resp.ObjectList {
		if o.ObjName == objnam {
			return sensorFrom(o.ObjName, o.Params), nil
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
