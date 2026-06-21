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
		Command:    cmdSetParamList,
		ObjectList: []Object{{ObjName: objnam, Params: params}},
	})
	return err
}

// SetCircuit turns a circuit/feature/body on or off (STATUS ON/OFF).
func (c *Client) SetCircuit(objnam string, on bool) error {
	status := valueOff
	if on {
		status = statusOn
	}
	return c.SetParams(objnam, map[string]string{keyStatus: status})
}

// SetHeatSetpoint sets a body's heat (LOTMP) setpoint, in IntelliCenter's native
// units (Fahrenheit). Pass the value as an integer-valued string upstream.
func (c *Client) SetHeatSetpoint(bodyObjnam string, lowTempF int) error {
	return c.SetParams(bodyObjnam, map[string]string{keyLoTmp: fmt.Sprintf("%d", lowTempF)})
}

// SetCoolSetpoint sets a body's cool (HITMP) setpoint for heat-pump bodies.
func (c *Client) SetCoolSetpoint(bodyObjnam string, highTempF int) error {
	return c.SetParams(bodyObjnam, map[string]string{keyHiTmp: fmt.Sprintf("%d", highTempF)})
}

// SetHeatSource assigns a body's heat source. Pass a heater objnam to enable
// that heater, or HeatSourceNone to turn heating off. The write targets the
// writable HEATER param; the controller reflects it on the read-only HTSRC.
// (Writing HTSRC directly is rejected with a 404 — and a rejected write can make
// the controller drop its client sessions, so the param MUST be correct.)
func (c *Client) SetHeatSource(bodyObjnam, heaterObjnam string) error {
	return c.SetParams(bodyObjnam, map[string]string{keyHeater: heaterObjnam})
}

// HeatSourceNone is the HTSRC value meaning "no heater assigned" (heat off).
const HeatSourceNone = "00000"
