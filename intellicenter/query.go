package intellicenter

import "strconv"

// query runs a GetParamList over all objects matching condition (the "INCR"
// iterate-all convention) requesting the given keys.
func (c *Client) query(prefix, condition string, keys []string) ([]ObjectData, error) {
	resp, err := c.roundTrip(prefix, Request{
		Command:    "GetParamList",
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
	objs, err := c.query("circuits", "OBJTYP=CIRCUIT", []string{"SNAME", "STATUS", "OBJTYP", "SUBTYP", "FREEZE"})
	if err != nil {
		return nil, err
	}
	out := make([]Circuit, 0, len(objs))
	for _, o := range objs {
		if o.Params["SNAME"] == "" || o.Params["STATUS"] == "" {
			continue
		}
		out = append(out, Circuit{
			ID:      o.ObjName,
			Name:    o.Params["SNAME"],
			ObjType: o.Params["OBJTYP"],
			SubType: o.Params["SUBTYP"],
			On:      o.Params["STATUS"] == statusOn,
			Freeze:  o.Params["FREEZE"] == statusOn,
		})
	}
	return out, nil
}

// Bodies lists pool/spa bodies with temp + heat mode + setpoints.
func (c *Client) Bodies() ([]Body, error) {
	objs, err := c.query("bodies", "OBJTYP=BODY", []string{"SNAME", "STATUS", "TEMP", "SUBTYP", "HTMODE", "HTSRC", "LOTMP", "HITMP"})
	if err != nil {
		return nil, err
	}
	out := make([]Body, 0, len(objs))
	for _, o := range objs {
		if o.Params["SNAME"] == "" {
			continue
		}
		out = append(out, Body{
			ID:        o.ObjName,
			Name:      o.Params["SNAME"],
			On:        o.Params["STATUS"] == statusOn,
			Temp:      parseFloat(o.Params["TEMP"]),
			HeatMode:  parseInt(o.Params["HTMODE"]),
			HeaterID:  o.Params["HTSRC"],
			LoSetTemp: parseFloat(o.Params["LOTMP"]),
			HiSetTemp: parseFloat(o.Params["HITMP"]),
		})
	}
	return out, nil
}

// Pumps lists pumps with RPM/WATTS/GPM (poll-only values).
func (c *Client) Pumps() ([]Pump, error) {
	objs, err := c.query("pumps", "OBJTYP=PUMP", []string{"SNAME", "STATUS", "RPM", "WATTS", "GPM"})
	if err != nil {
		return nil, err
	}
	out := make([]Pump, 0, len(objs))
	for _, o := range objs {
		if o.Params["SNAME"] == "" {
			continue
		}
		out = append(out, Pump{
			ID:    o.ObjName,
			Name:  o.Params["SNAME"],
			On:    o.Params["STATUS"] == statusOn,
			RPM:   parseFloat(o.Params["RPM"]),
			Watts: parseFloat(o.Params["WATTS"]),
			GPM:   parseFloat(o.Params["GPM"]),
		})
	}
	return out, nil
}

// Heaters lists heaters.
func (c *Client) Heaters() ([]Heater, error) {
	objs, err := c.query("heaters", "OBJTYP=HEATER", []string{"SNAME", "STATUS", "SUBTYP", "OBJTYP"})
	if err != nil {
		return nil, err
	}
	out := make([]Heater, 0, len(objs))
	for _, o := range objs {
		if o.Params["SNAME"] == "" {
			continue
		}
		out = append(out, Heater{
			ID:      o.ObjName,
			Name:    o.Params["SNAME"],
			On:      o.Params["STATUS"] == statusOn,
			SubType: o.Params["SUBTYP"],
		})
	}
	return out, nil
}

// Sensor reads a single object's temperature PROBE (e.g. air "_A135").
func (c *Client) Sensor(objnam string) (Sensor, error) {
	resp, err := c.roundTrip("sensor", Request{
		Command:    "GetParamList",
		Condition:  "OBJTYP=SENSE",
		ObjectList: []Object{{ObjName: objnam, Keys: []string{"SNAME", "PROBE"}}},
	})
	if err != nil {
		return Sensor{}, err
	}
	for _, o := range resp.ObjectList {
		if o.ObjName == objnam {
			probe := o.Params["PROBE"]
			return Sensor{ID: objnam, Name: o.Params["SNAME"], Temp: parseFloat(probe), Valid: probe != ""}, nil
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
