# Pentair IntelliCenter WebSocket API

This document describes the WebSocket API for communicating with Pentair IntelliCenter pool controllers.

## Quick Start

**WebSocket Endpoint:** `ws://[IP_ADDRESS]:6680`

**Test Connection:**
```bash
echo '{"command":"GetQuery","queryName":"GetConfiguration","arguments":"","messageID":"test-123"}' | websocat ws://[IP_ADDRESS]:6680
```

## Message Protocol

All messages use JSON format with these structures:

### Request
```json
{
  "command": "CommandName",
  "queryName": "QueryType", 
  "arguments": "",
  "messageID": "unique-identifier"
}
```

### Success Response  
```json
{
  "command": "SendQuery",
  "messageID": "unique-identifier",
  "queryName": "QueryType", 
  "response": "200",
  "answer": [...]
}
```

### Error Response
```json
{
  "command": "Error",
  "messageID": "generated-uuid",
  "response": "404", 
  "description": "'CommandName' Unknown command!"
}
```

## Basic Configuration Queries

These commands retrieve static system configuration data.

### GetConfiguration
Returns complete system setup including bodies, circuits, and features.

```json
{
  "command": "GetQuery",
  "queryName": "GetConfiguration",
  "arguments": "",
  "messageID": "config-001"
}
```

**Response contains:**
- **Bodies** (B#### format): Pool/Spa with temperature ranges and heater assignments
- **Circuits** (C#### format): Individual equipment (Pool, Spa, Lights, Cleaner, etc.)
- **Features** (FTR## format): Special functions (heating, jets, fountains)

### GetHardwareDefinition
Returns panel and module information with MAC addresses and firmware versions.

```json
{
  "command": "GetQuery", 
  "queryName": "GetHardwareDefinition",
  "arguments": "",
  "messageID": "hardware-001"
}
```

### GetPumpConfiguration
Returns pump setup with speed settings for different circuits.

```json
{
  "command": "GetQuery",
  "queryName": "GetPumpConfiguration", 
  "arguments": "",
  "messageID": "pump-001"
}
```

### GetHeaterConfiguration
Returns heater setup and communication settings.

```json
{
  "command": "GetQuery",
  "queryName": "GetHeaterConfiguration",
  "arguments": "",
  "messageID": "heater-001"
}
```

## Real-Time Monitoring

The `GetParamList` command retrieves current operational data for monitoring.

### Request Format
```json
{
  "messageID": "unique-id",
  "command": "GetParamList",
  "condition": "OBJTYP=OBJECTTYPE", 
  "objectList": [{"objnam": "INCR", "keys": ["param1", "param2"]}]
}
```

### Temperature Monitoring

**Water Temperatures (Pool/Spa):**
```json
{
  "messageID": "water-temp-001",
  "command": "GetParamList",
  "condition": "OBJTYP=BODY",
  "objectList": [{"objnam": "INCR", "keys": ["SNAME", "TEMP", "STATUS"]}]
}
```

**Air Temperature:**
```json
{
  "messageID": "air-temp-001", 
  "command": "GetParamList",
  "condition": "",
  "objectList": [{"objnam": "_A135", "keys": ["SNAME", "PROBE", "STATUS"]}]
}
```

**All Temperature Sensors:**
```json
{
  "messageID": "all-sensors-001",
  "command": "GetParamList", 
  "condition": "OBJTYP=SENSE",
  "objectList": [{"objnam": "INCR", "keys": ["SNAME", "PROBE", "SUBTYP"]}]
}
```

### Pump Monitoring

**Current Pump Data:**
```json
{
  "messageID": "pump-001",
  "command": "GetParamList", 
  "condition": "OBJTYP=PUMP",
  "objectList": [{"objnam": "INCR", "keys": ["SNAME", "STATUS", "RPM", "GPM", "WATTS"]}]
}
```

**Response includes:**
- **STATUS**: "10" = running, other values = stopped
- **RPM**: Current revolutions per minute  
- **GPM**: Flow rate in gallons per minute
- **WATTS**: Power consumption (may have formatting issues)

### Circuit and Feature Status

**Equipment On/Off Status:**
```json
{
  "messageID": "circuit-001",
  "command": "GetParamList",
  "condition": "OBJTYP=CIRCUIT", 
  "objectList": [{"objnam": "INCR", "keys": ["SNAME", "STATUS", "SUBTYP"]}]
}
```

**Response includes:**
- **STATUS**: "ON" or "OFF" for equipment state
- **SUBTYP**: Equipment type (POOL, SPA, LIGHT, GENERIC)
- **Note**: Returns many circuits; filter to C#### and FTR## objects for actual equipment

## System Object Reference

### Object Naming Conventions

| Object Type | Format | Description | Example |
|-------------|--------|-------------|----------|
| Bodies | B#### | Pool/Spa with temperature ranges | B1101 (Pool), B1202 (Spa) |
| Circuits | C#### | Individual equipment controls | C0001 (Spa), C0003 (Pool Light) |
| Features | FTR## | Special pool features | FTR01 (Spa Heat), FTR03 (Spa Jets) |
| Heaters | H#### | Heating equipment | H0001 (UltraTemp), H0002 (Gas Heater) |
| Pumps | PMP## | Variable speed pumps | PMP01 (VS), PMP02 (pool) |
| Sensors | Various | Temperature sensors | _A135 (Air), SSS11 (Solar) |

### Key Parameters by Object Type

**Bodies (OBJTYP=BODY):**
- **SNAME**: Display name (Pool, Spa)
- **TEMP**: Current water temperature (°F)
- **HTMODE**: Heating status (0=off, 1=heating)
- **HTSRC**: Assigned heater object ID

**Circuits (OBJTYP=CIRCUIT):**
- **SNAME**: Display name 
- **STATUS**: Equipment state ("ON"/"OFF")
- **SUBTYP**: Equipment type (POOL, SPA, LIGHT, GENERIC)

**Pumps (OBJTYP=PUMP):**
- **SNAME**: Display name
- **STATUS**: Running status ("10"=running)
- **RPM**: Current speed
- **GPM**: Current flow rate

**Sensors (OBJTYP=SENSE):**
- **SNAME**: Display name
- **PROBE**: Temperature reading (°F)
- **SUBTYP**: Sensor type (AIR, POOL, SOLAR)

## Response Codes

- **200**: Success
- **400**: Bad Request (missing/invalid parameters) 
- **404**: Unknown command

## Important Notes

- Each request must have a unique `messageID`
- Controller generates UUIDs for error responses  
- Some queries require specific parameters (return 400 if missing)
- Raw TCP also available on port 6681 (same JSON format, no WebSocket framing)
- Many virtual controls and unused circuits are returned; filter to meaningful equipment

---

## Advanced Topics

### Temperature Sensor Discovery

**Discover All Sensors:**
```json
{
  "messageID": "discover-001",
  "command": "GetParamList",
  "condition": "",
  "objectList": [{"objnam": "INCR", "keys": ["OBJTYP", "SNAME", "TEMP", "PROBE", "SUBTYP"]}]
}
```

**Known Temperature Sensors:**

| Object ID | Name | Type | Temperature Key | Notes |
|-----------|------|------|-----------------|-------|
| B1101 | Pool | BODY | TEMP | Pool water temperature |
| B1202 | Spa | BODY | TEMP | Spa water temperature |
| _A135 | Air Sensor | SENSE | PROBE | Outdoor air temperature |
| SSS11/SSS12 | Solar Sensors | SENSE | PROBE | May need different parameters |

### Circuit Filtering for Monitoring

**Include (Meaningful Equipment):**
- **C0001-C0011**: Core equipment (Pool, Spa, Lights, Cleaner)  
- **FTR01-FTR03**: Features (Spa Heat, Fountain, Spa Jets)

**Exclude (Virtual Controls):**
- **X-prefixed**: Virtual buttons (Pump Speed +/-, Heat Enable)
- **_A-prefixed**: Action buttons (All Lights On/Off)
- **AUX circuits**: Often unused placeholder circuits

This filtering reduces ~35 total circuits to ~9 actual equipment items.

### Heater Status Monitoring

IntelliCenter tracks heating at multiple levels. For accurate monitoring, use body-level `HTMODE` rather than circuit features.

**Heater Status Query:**
```json
{
  "messageID": "heating-001",
  "command": "GetParamList", 
  "condition": "OBJTYP=BODY",
  "objectList": [{"objnam": "INCR", "keys": ["SNAME", "HTMODE", "HTSRC", "TEMP"]}]
}
```

**HTMODE Values:**
- **0**: No heating demand (off or setpoint reached)
- **1**: Actively calling for heat (traditional heater firing)
- **4**: Heat pump heating mode (UltraTemp operation)
- **9**: Heat pump cooling mode (UltraTemp operation)

**Key Parameters:**
- **HTMODE**: Actual heating demand (most important)
- **HTSRC**: Assigned heater object ID (H0001, H0002, etc.)
- **Circuit features** (FTR01="Spa Heat"): Enable/disable only, not active status

**Best Practice:** Use `HTMODE >= 1` to detect active heating rather than relying on circuit feature STATUS.

**Heat Pump Detection:** 
- Use `HTMODE == 4` to detect heat pump heating operations
- Use `HTMODE == 9` to detect heat pump cooling operations

## Heat Pump Detection and Monitoring

IntelliCenter systems with UltraTemp heat pumps support both heating and cooling operations with multiple operational modes. Heat pump monitoring requires tracking multiple object types and parameters.

### Heat Pump Equipment Discovery

**Heater Configuration Query:**
```json
{
  "command": "GetQuery",
  "queryName": "GetHeaterConfiguration",
  "arguments": "",
  "messageID": "heater-config-001"
}
```

**Expected Response Structure:**
```json
{
  "objnam": "H0001",
  "params": {
    "SUBTYP": "ULTRA",
    "SNAME": "UltraTemp",
    "DLY": "5",
    "COMUART": "1",
    "LISTORD": "1",
    "ACT": "ACT"
  }
}
```

**Heat Pump Identification:**
- **H0001**: Primary heat pump (SUBTYP="ULTRA")
- **H0002**: Backup gas heater (SUBTYP="GENERIC")
- **HXULT**: Heat pump preference setting

### Operational Modes

Heat pumps operate in two primary modes affecting heating priority:

**UltraTemp Only:**
- Heat pump exclusive for heating
- No gas heater backup used
- Cooling operations available when enabled

**UltraTemp Preferred:**
- Heat pump priority for heating
- Gas heater as backup/supplement
- Heat pump used first, gas heater supplements as needed

### Heat Pump Status Monitoring

**Real-time Heater Status:**
```json
{
  "messageID": "heater-status-001",
  "command": "GetParamList",
  "condition": "OBJTYP=HEATER",
  "objectList": [{"objnam": "INCR", "keys": ["SNAME", "STATUS", "SUBTYP"]}]
}
```

**Heat Pump Response:**
```json
{
  "objnam": "H0001",
  "params": {
    "SNAME": "UltraTemp",
    "STATUS": "ON",
    "SUBTYP": "ULTRA"
  }
}
```

### Cooling Detection

Heat pumps with cooling capability require monitoring both heating and cooling setpoints:

**Body Temperature Monitoring:**
```json
{
  "messageID": "body-temps-001",
  "command": "GetParamList",
  "condition": "OBJTYP=BODY",
  "objectList": [{"objnam": "INCR", "keys": ["SNAME", "TEMP", "LOTMP", "HITMP", "HTMODE"]}]
}
```

**Cooling Logic Parameters:**
- **TEMP**: Current water temperature
- **LOTMP**: Low temperature setpoint (heating threshold)
- **HITMP**: High temperature setpoint (cooling threshold)
- **HTMODE**: Heating demand (0=off, 1=heating active)

**Cooling Operation Detection:**
- **Heating**: Triggered when TEMP < LOTMP
- **Cooling**: Triggered when TEMP > HITMP
- **Idle**: When LOTMP <= TEMP <= HITMP

### Heat Pump Control Circuits

Heat pump operations may involve multiple virtual circuits:

**Circuit Discovery:**
```json
{
  "messageID": "heatpump-circuits-001",
  "command": "GetParamList",
  "condition": "OBJTYP=CIRCUIT",
  "objectList": [{"objnam": "INCR", "keys": ["SNAME", "STATUS", "SUBTYP"]}]
}
```

**Key Heat Pump Circuits:**
- **X0034**: "Heat Pump" circuit
- **X0035**: "UltraTemp" circuit  
- **X0044**: "Pool Heater" circuit
- **X0051**: "Heater" circuit

### Monitoring Complexity

Heat pump detection requires monitoring multiple object types:

1. **Body Level** (OBJTYP=BODY):
   - Temperature comparisons (TEMP vs LOTMP/HITMP)
   - Heating demand status (HTMODE)

2. **Heater Level** (OBJTYP=HEATER):
   - Equipment operational status
   - Heat pump vs gas heater identification

3. **Circuit Level** (OBJTYP=CIRCUIT):
   - Virtual control circuit status
   - Heat pump specific controls

**Important Notes:**
- Cooling operations may not be reflected in traditional HTMODE parameters
- Heat pump operational status during cooling requires different parameter monitoring
- Multiple heater objects may be active simultaneously in "Preferred" mode
- Circuit status may lag behind actual equipment operation

### Test Results - UltraTemp Only Heating Mode

**Baseline State:**
```json
{
  "Pool": {"TEMP": "92", "HTMODE": "0", "LOTMP": "86", "HITMP": "92"},
  "Circuits": {"X0034": "OFF", "X0035": "OFF", "X0044": "OFF"}
}
```

**UltraTemp Only Heat Activated:**
```json
{
  "Pool": {"TEMP": "92", "HTMODE": "4", "LOTMP": "101", "HITMP": "104"},
  "Circuits": {"X0034": "OFF", "X0035": "OFF", "X0044": "OFF"}
}
```

**Key Findings:**
- **HTMODE=4**: Indicates heat pump heating in "Only" mode
- **Temperature Setpoints**: Both LOTMP and HITMP increase when heat pump activates
- **Circuit Status**: Heat pump circuits (X0034, X0035, X0044) remain OFF during operation
- **Heater Objects**: H0001 status unchanged during activation

**Detection Logic:**
- Monitor body-level `HTMODE == 4` for heat pump heating detection
- Traditional gas heater uses `HTMODE == 1`
- Circuit status unreliable for heat pump operational detection

### Heat Pump Mode Detection

**UltraTemp Only vs Preferred Mode Detection:**
```json
{
  "messageID": "mode-detection-001",
  "command": "GetParamList", 
  "condition": "OBJTYP=BODY",
  "objectList": [{"objnam": "INCR", "keys": ["SNAME", "HTSRC", "MODE", "HEATER", "HTMODE"]}]
}
```

**UltraTemp Only Mode:**
```json
{
  "Pool": {
    "HTSRC": "H0001",
    "MODE": "5", 
    "HEATER": "H0001",
    "HTMODE": "4"
  }
}
```

**UltraTemp Preferred Mode:**
```json
{
  "Pool": {
    "HTSRC": "HXULT",
    "MODE": "6",
    "HEATER": "HXULT", 
    "HTMODE": "4"
  }
}
```

**Mode Detection Logic:**
- **HTSRC="H0001"** + **MODE="5"**: UltraTemp Only (direct heat pump)
- **HTSRC="HXULT"** + **MODE="6"**: UltraTemp Preferred (preference controller)
- **HTMODE="4"**: Heat pump heating active (both modes)
- **HTMODE="9"**: Heat pump cooling active (both modes)
- **HEATER** parameter mirrors HTSRC value for confirmation

### Test Results - UltraTemp Only Cooling Mode

**UltraTemp Only Cooling Activated:**
```json
{
  "Pool": {
    "TEMP": "92", 
    "HTMODE": "9", 
    "LOTMP": "75", 
    "HITMP": "82",
    "HTSRC": "H0001",
    "MODE": "5",
    "HEATER": "H0001"
  }
}
```

**Cooling Detection Logic:**
- **HTMODE="9"**: Heat pump cooling mode active
- **TEMP > HITMP**: Cooling demand (92°F > 82°F setpoint)
- **HTSRC="H0001"**: Confirms UltraTemp Only mode during cooling
- **MODE="5"**: Consistent with Only mode operation

### Test Results - UltraTemp Preferred Cooling Mode

**UltraTemp Preferred Cooling Activated:**
```json
{
  "Pool": {
    "TEMP": "92", 
    "HTMODE": "9", 
    "LOTMP": "75", 
    "HITMP": "82",
    "HTSRC": "HXULT",
    "MODE": "6",
    "HEATER": "HXULT"
  }
}
```

**Preferred Cooling Confirmation:**
- **HTMODE="9"**: Heat pump cooling mode (same as Only)
- **HTSRC="HXULT"**: Confirms UltraTemp Preferred mode during cooling
- **MODE="6"**: Consistent with Preferred mode operation
- **Temperature setpoints**: Identical to Only mode (75°F heat, 82°F cool)

### Complete Heat Pump Detection Matrix

**All Operational Combinations:**

| Mode | Operation | HTSRC | MODE | HTMODE | Detection Logic |
|------|-----------|-------|------|--------|-----------------|
| Only | Heating | H0001 | 5 | 4 | Direct heat pump heating |
| Only | Cooling | H0001 | 5 | 9 | Direct heat pump cooling |
| Preferred | Heating | HXULT | 6 | 4 | Preference controller heating |
| Preferred | Cooling | HXULT | 6 | 9 | Preference controller cooling |
| Any | Idle/Off | * | * | 0 | No heat pump operation |

### Test Results - Idle/Neutral Zone Behavior

**UltraTemp Preferred - Idle (Neutral Zone):**
```json
{
  "Pool": {
    "TEMP": "92", 
    "HTMODE": "0", 
    "LOTMP": "75", 
    "HITMP": "94",
    "HTSRC": "HXULT",
    "MODE": "6",
    "HEATER": "HXULT"
  }
}
```

**UltraTemp Only - Idle (Neutral Zone):**
```json
{
  "Pool": {
    "TEMP": "92", 
    "HTMODE": "0", 
    "LOTMP": "75", 
    "HITMP": "94",
    "HTSRC": "H0001",
    "MODE": "5",
    "HEATER": "H0001"
  }
}
```

**Neutral Zone Findings:**
- **HTMODE="0"**: System idle when LOTMP < TEMP < HITMP (75°F < 92°F < 94°F)
- **Mode Detection Persistent**: HTSRC/MODE values show current mode setting even when idle
- **Real-time Mode Switching**: HTSRC/MODE change immediately when switching between Only/Preferred
- **Temperature Setpoints**: Persist across mode changes
- **Independent Operation**: Mode configuration independent of operational status

**Summary Detection Logic:**
- **HTMODE=0**: Heat pump idle/off (neutral zone: LOTMP < TEMP < HITMP)
- **HTMODE=4**: Heat pump heating (TEMP < LOTMP)
- **HTMODE=9**: Heat pump cooling (TEMP > HITMP)
- **Mode Detection**: HTSRC + MODE values distinguish Only vs Preferred in all states
- **Real-time Updates**: Mode changes reflected immediately regardless of operational status

### Connection Staleness Detection

**Problem:** WebSocket connections can become "stale" - appearing connected but delivering cached data instead of real-time updates.

**Solution:** Use unique messageID correlation to detect stale connections.

**Implementation:**
```go
messageID := fmt.Sprintf("request-%d-%d", time.Now().Unix(), time.Now().Nanosecond()%1000000)

// Send request with unique messageID
// Validate response messageID matches exactly
if resp.MessageID != messageID {
    // Connection is stale - force reconnection
    conn.Close()
    reconnect()
}
```

**Why This Works:**
- Unique messageIDs prevent cached responses
- Immediate detection of stale connections
- No false positives from network conditions
- Works regardless of equipment stability

**Alternative methods** (response timing, data variance tracking) are less reliable due to false positives.

### Feature Display Control (SHOMNU Parameter)

IntelliCenter features have a "Show as Feature" setting in the web interface that controls whether they appear as user-accessible features. This setting is encoded in the `SHOMNU` parameter.

**Discovery Method:**
```json
{
  "command": "GetQuery",
  "queryName": "GetConfiguration",
  "arguments": "",
  "messageID": "config-001"
}
```

**SHOMNU Encoding:**
- **Show as Feature: YES** → SHOMNU ends with 'w' (e.g., `"fcsrepvhzmtow"`)
- **Show as Feature: NO** → SHOMNU without 'w' (e.g., `"fcsrepvhzmto"`)

**Real-time Verification:**
```json
{
  "messageID": "feature-display-check",
  "command": "GetParamList",
  "condition": "OBJTYP=CIRCUIT",
  "objectList": [{"objnam": "FTR01", "keys": ["SNAME", "SHOMNU"]}]
}
```

**Response Examples:**

Feature enabled:
```json
{"objnam": "FTR01", "params": {"SNAME": "Spa Heat", "SHOMNU": "fcsrepvhzmtow"}}
```

Feature disabled:
```json
{"objnam": "FTR01", "params": {"SNAME": "Spa Heat", "SHOMNU": "fcsrepvhzmto"}}
```

**Implementation Pattern:**
```go
// Respect IntelliCenter's "Show as Feature" setting
if !strings.HasSuffix(shomnu, "w") {
    // User has disabled "Show as Feature" - skip this feature
    return
}
```

**Use Cases:**
- **Eliminate duplicate controls** (e.g., "Spa Heat" feature vs "Spa Heater" thermal equipment)
- **Respect user configuration** (users control what appears as features)
- **Universal compatibility** (works with any IntelliCenter setup)

**Benefits:**
- No hardcoded equipment names or logic
- User-controlled through IntelliCenter interface
- Automatically handles equipment/feature relationships
- Works across different pool configurations