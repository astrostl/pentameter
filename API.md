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
- **1**: Actively calling for heat (heater firing)

**Key Parameters:**
- **HTMODE**: Actual heating demand (most important)
- **HTSRC**: Assigned heater object ID (H0001, H0002, etc.)
- **Circuit features** (FTR01="Spa Heat"): Enable/disable only, not active status

**Best Practice:** Use `HTMODE >= 1` to detect active heating rather than relying on circuit feature STATUS.

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