package intellicenter

import "fmt"

// Control / writes. pentameter is read-only for metrics and listen modes; these
// SetParamList writes are exercised only by homebridge mode (HomeKit turning
// equipment on/off and changing setpoints). Treat with care: these change
// physical pool equipment state.

// SetParams writes arbitrary params to an object via SetParamList.
func (c *Client) SetParams(objnam string, params map[string]string) error {
	if objnam == "" || len(params) == 0 {
		return fmt.Errorf("SetParams: objnam and params required")
	}
	_, err := c.roundTrip("set", Request{
		Command:    "SetParamList",
		ObjectList: []Object{{ObjName: objnam, Params: params}},
	})
	return err
}

// SetCircuit turns a circuit/feature/body on or off (STATUS ON/OFF).
func (c *Client) SetCircuit(objnam string, on bool) error {
	status := "OFF"
	if on {
		status = statusOn
	}
	return c.SetParams(objnam, map[string]string{"STATUS": status})
}

// SetHeatSetpoint sets a body's heat (LOTMP) setpoint, in IntelliCenter's native
// units (Fahrenheit). Pass the value as an integer-valued string upstream.
func (c *Client) SetHeatSetpoint(bodyObjnam string, lowTempF int) error {
	return c.SetParams(bodyObjnam, map[string]string{"LOTMP": fmt.Sprintf("%d", lowTempF)})
}

// SetCoolSetpoint sets a body's cool (HITMP) setpoint for heat-pump bodies.
func (c *Client) SetCoolSetpoint(bodyObjnam string, highTempF int) error {
	return c.SetParams(bodyObjnam, map[string]string{"HITMP": fmt.Sprintf("%d", highTempF)})
}
