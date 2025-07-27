# Pentameter - Pentair IntelliCenter Metrics & Dashboarding

[![Go Report Card](https://goreportcard.com/badge/github.com/astrostl/pentameter)](https://goreportcard.com/report/github.com/astrostl/pentameter)

> **⚠️ AI-Generated Code Warning**: This project was "vibe coded" using generative AI and should be thoroughly reviewed before use. It comes with no warranty or guarantee of functionality, security, or reliability.

![Pentameter Grafana Dashboard](pentameter.png?v=1)

Pentameter is a Prometheus exporter for Pentair IntelliCenter pool controllers that connects via WebSocket and exposes pool equipment data as Prometheus metrics with pre-configured Grafana dashboards for visualization.

## Features

### Real-Time Data Collection
- **Temperature Monitoring**: Pool, spa, and outdoor air sensors
- **Pump Monitoring**: Variable speed RPM and flow rates  
- **Equipment Status**: Circuit and feature on/off states
- **Connection Health**: Automatic failure detection and recovery

### Data Export and Visualization
- **Prometheus Metrics**: Standard format compatible with any monitoring tool
- **Pre-configured Dashboards**: Grafana dashboards with automatic provisioning
- **Kiosk Mode**: Clean displays for dedicated monitoring screens
- **Historical Trends**: Long-term data visualization and analysis

### Smart Features
- **Intelligent Filtering**: Focus on meaningful equipment vs virtual controls
- **User-Controlled Feature Visibility**: Respects IntelliCenter's "Show as Feature" settings
- **Graceful Degradation**: Handle missing sensors without errors
- **Browser Compatibility**: Standard metrics endpoint works everywhere

### Technical Features
- WebSocket connection to IntelliCenter controllers
- Robust connection management with exponential backoff retry logic
- Health checks with automatic reconnection on failures
- Configurable via command line flags or environment variables
- Health check endpoint

## Architecture Overview

Pentameter connects to IntelliCenter controllers via WebSocket and transforms pool data into standard Prometheus metrics for monitoring and alerting.

### System Components

1. **Go Service** - Core monitoring service
   - WebSocket client for IntelliCenter communication
   - Prometheus metrics endpoint (`/metrics`)
   - Health checks and connection management

2. **IntelliCenter Interface**
   - WebSocket connection (default port 6680)
   - JSON message protocol using GetParamList API
   - Exponential backoff retry with health checks

3. **Metrics Export**
   - HTTP server (default port 8080)
   - Standard Prometheus format
   - Compatible with any monitoring tool

4. **Docker Deployment**
   - Multi-stage builds with scratch base images (~12MB)
   - Docker Compose orchestration
   - Prometheus and Grafana included

### Technology Stack

- **Language:** Go with standard library HTTP server
- **WebSocket:** `github.com/gorilla/websocket`
- **Metrics:** `github.com/prometheus/client_golang`
- **Build:** Makefile with Docker integration
- **Deployment:** Docker Compose with restart policies

## Quick Start

```bash
# Clone the repository (includes all config files)
git clone https://github.com/astrostl/pentameter.git
cd pentameter

# Configure your IntelliCenter IP
cp .env.example .env
# Edit .env and set your PENTAMETER_IC_IP

# Start the complete monitoring stack
docker compose up -d
```

This uses the published Docker images from DockerHub, so no build time is required.

Then visit:
- **Grafana Dashboard**: http://HOSTNAME:3000/d/intellicenter/ 
- **Metrics Endpoint**: http://HOSTNAME:8080/metrics
- **Prometheus**: http://HOSTNAME:9090

## Configuration

All configuration options can be set via command line flags or environment variables:

| Flag | Environment Variable | Default | Description |
|------|---------------------|---------|-------------|
| `--ic-ip` | `PENTAMETER_IC_IP` | (required) | IntelliCenter IP address |
| `--ic-port` | `PENTAMETER_IC_PORT` | `6680` | IntelliCenter WebSocket port |
| `--http-port` | `PENTAMETER_HTTP_PORT` | `8080` | HTTP server port for metrics |
| `--interval` | `PENTAMETER_INTERVAL` | `60` | Temperature polling interval (seconds) |

## Usage

### Command Line Flags
```bash
go run main.go --ic-ip 192.168.192.168 --ic-port 6680 --http-port 8080 --interval 60
```

### Environment Variables
```bash
export PENTAMETER_IC_IP=192.168.192.168
export PENTAMETER_IC_PORT=6680
export PENTAMETER_HTTP_PORT=8080
export PENTAMETER_INTERVAL=60
go run main.go
```

### Mixed Configuration
```bash
# Use environment for IP, override HTTP port via flag
export PENTAMETER_IC_IP=192.168.192.168
go run main.go --http-port 9090
```

## Endpoints

- **Metrics**: `http://HOSTNAME:8080/metrics` - Prometheus metrics
- **Health**: `http://HOSTNAME:8080/health` - Health check
- **Prometheus**: `http://HOSTNAME:9090` - Prometheus web interface
- **Grafana**: `http://HOSTNAME:3000/d/intellicenter/` - Grafana dashboards (no login required)
- **Kiosk Mode**: `http://HOSTNAME:3000/d/intellicenter/intellicenter?kiosk` - Clean dashboard display

## Metrics Reference

### Temperature Metrics
```prometheus
# Water temperatures
water_temperature_fahrenheit{body="POOL",name="Pool"} 87
water_temperature_fahrenheit{body="SPA",name="Spa"} 84

# Air temperature (optional)
air_temperature_fahrenheit{sensor="AIR",name="Air Sensor"} 73
```

### Equipment Metrics
```prometheus
# Pump speeds and flow
pump_rpm{pump="PMP01",name="VS"} 3000
pump_rpm{pump="PMP02",name="pool"} 2450

# Circuit status (1=on, 0=off)
circuit_status{circuit="C0001",name="Spa",type="SPA"} 1
circuit_status{circuit="C0003",name="Pool Light",type="LIGHT"} 0
circuit_status{circuit="FTR01",name="Spa Heat",type="GENERIC"} 0
```

### Thermal Equipment Metrics

**thermal_status Values - Pentameter's Interpretation Layer:**

**Derived from IntelliCenter Data:**
- **0 (off)**: Based on HTSRC="00000" (no heater assigned)
- **1 (heating)**: Based on HTMODE=1 or HTMODE=4 (active heating demand)

**Pentameter's Logical Inference:**
- **2 (idle)**: Pentameter's interpretation of HTMODE=0 + HTSRC≠"00000" (heater assigned but not demanded)
- **3 (cooling)**: Pentameter's interpretation of HTMODE=9 (heat pump cooling mode)

**IntelliCenter Raw Data:**
- **HTMODE**: 0, 1, 4, 9 (heating/cooling demand states)
- **HTSRC**: "00000" or heater ID (assignment status)

The thermal_status metric translates IntelliCenter's raw operational data into human-friendly states. The "idle" and "cooling" concepts are pentameter's abstractions - IntelliCenter itself only provides demand and assignment status.

```prometheus
# Thermal equipment operational status (see interpretation above)
thermal_status{heater="H0002",name="Spa Heater",subtyp="GENERIC"} 2

# Temperature setpoints (Fahrenheit)
thermal_low_setpoint_fahrenheit{heater="H0002",name="Spa Heater",subtyp="GENERIC"} 95
thermal_high_setpoint_fahrenheit{heater="H0001",name="Pool Heat Pump",subtyp="ULTRA"} 88
```

**Setpoint Display Logic:**
- **Heatpoint (low setpoint)**: Always shown for any assigned heater
- **Coolpoint (high setpoint)**: Only shown when < 100°F and equipment is idle or cooling
- **100°F Threshold**: Filters out impractical cooling setpoints from heating-only equipment

### System Health Metrics
```prometheus
# Connection monitoring
intellicenter_connection_failure 0
intellicenter_last_refresh_timestamp_seconds 1751302319

# Equipment connection status (1=connected, 0=disconnected)
thermal_status{heater="H0001",name="Pool Heat Pump",subtyp="ULTRA"} 0
pump_status{pump="PMP01",name="VS",subtyp="PUMP"} 1
```

**Connection Status Behavior:**
- **Service Level**: `intellicenter_connection_failure` tracks WebSocket connectivity to IntelliCenter
- **Equipment Level**: Individual equipment metrics disappear when equipment is offline/disconnected
- **Graceful Degradation**: Missing equipment doesn't cause service failures
- **Automatic Recovery**: Equipment metrics reappear when equipment comes back online

### Data Sources

| Metric Type | Source | API Query | Parameter |
|-------------|--------|-----------|------------|
| Water Temperature | Pool/Spa bodies | OBJTYP=BODY | TEMP |
| Air Temperature | Outdoor sensor | Object _A135 | PROBE |
| Pump RPM | Variable speed pumps | OBJTYP=PUMP | RPM |
| Circuit Status | Equipment controls | OBJTYP=CIRCUIT | STATUS |
| Thermal Status | Heating equipment | OBJTYP=HEATER | STATUS + HTMODE |
| Thermal Setpoints | Pool/Spa bodies | OBJTYP=BODY | LOTMP, HITMP |
| Connection Health | Internal monitoring | N/A | WebSocket health checks |
| Equipment Health | Individual equipment | API responses | Missing data = offline |
| Refresh Timestamp | Internal tracking | N/A | Unix timestamp |

## Connection Reliability

### Service-Level Connection Management
- **Exponential Backoff**: 1s → 2s → 4s → 8s → 16s → 30s max
- **Health Checks**: WebSocket ping/pong every 30 seconds
- **Retry Limits**: Maximum 5 attempts before giving up
- **Connection Failure Metric**: `intellicenter_connection_failure` (0=connected, 1=failed)

### Equipment-Level Connection Handling
- **Individual Equipment Status**: Each piece of equipment reports its own connection state
- **Missing Data Detection**: Equipment metrics disappear when equipment goes offline
- **No Service Impact**: Individual equipment failures don't affect service or other equipment
- **Automatic Reappearance**: Equipment metrics return when equipment comes back online

### Graceful Degradation Examples
- **Pump Offline**: `pump_rpm` metrics disappear, water temperature monitoring continues
- **Heater Offline**: `thermal_status` metrics disappear, circuit monitoring continues  
- **Sensor Offline**: `air_temperature` metrics disappear, pool/spa monitoring continues
- **Service Recovery**: All equipment metrics reappear when service reconnects to IntelliCenter

### Configuration
```go
RetryConfig{
    MaxRetries:      5,
    BaseDelay:       1 * time.Second,
    MaxDelay:        30 * time.Second,
    BackoffFactor:   2.0,
    HealthCheckRate: 30 * time.Second,
}
```

## Smart Circuit Filtering

Pentameter filters IntelliCenter's ~35 circuits down to ~9 meaningful equipment items:

**Included (Useful Equipment):**
- **C-prefixed**: Core equipment (Pool, Spa, Lights, Cleaner)
- **FTR-prefixed**: Features (Spa Heat, Fountain, Spa Jets)

**Excluded (Virtual Controls):**
- **AUX circuits**: Unused placeholder circuits
- **X-prefixed**: Virtual buttons (Pump Speed +/-)
- **_A-prefixed**: Action commands (All Lights On/Off)
- **Duplicates**: Multiple entries for same equipment

## Building

### Using Makefile (Recommended)
```bash
# Build the binary
make build

# Build Docker image
make docker-build

# Run with Docker Compose
make compose-up

# View all available targets (now organized by category)
make help
```

### Manual Build
```bash
go build -o pentameter main.go
./pentameter --ic-ip 192.168.192.168
```

## Docker Usage

### Quick Start with Docker Compose
```bash
# Using Makefile (recommended)
make compose-up    # Start pentameter, Prometheus, and Grafana
make compose-logs  # View logs
make compose-down  # Stop all services

# Or manually
docker-compose up -d
docker-compose logs -f
docker-compose down
docker-compose restart
```

This starts the complete monitoring stack:
- **Pentameter**: Pool data collection service
- **Prometheus**: Metrics storage and querying
- **Grafana**: Pre-configured dashboard at http://HOSTNAME:3000/d/intellicenter/

### Configuration via Environment Variables
The `docker-compose.yml` uses environment variables that you can override:

```yaml
environment:
  - PENTAMETER_IC_IP=192.168.192.168
  - PENTAMETER_IC_PORT=6680
  - PENTAMETER_HTTP_PORT=8080
  - PENTAMETER_INTERVAL=60
```

### Manual Docker Build and Run
```bash
# Build the image (required during early development)
docker build --no-cache -t pentameter .

# Run the container
docker run -d \
  --name pentameter \
  -p 8080:8080 \
  -e PENTAMETER_IC_IP=192.168.192.168 \
  pentameter
```

### Docker Image Details
- **Base Image**: `scratch` (minimal ~12MB image)
- **Multi-stage build**: Go compilation in `golang:1.24-alpine`, final binary in scratch
- **Health Check**: Built-in health check endpoint at `/health`
- **Restart Policy**: `unless-stopped` for automatic recovery

## Prometheus Integration

### Configuration
- **Scrape Interval**: 60 seconds (matches polling interval)
- **Data Retention**: 30 days for temperature and connection metrics
- **Network**: Docker bridge communication via pentameter-net
- **Format**: Standard Prometheus metrics with label-based time series

Add to your Prometheus `prometheus.yml`:

```yaml
scrape_configs:
  - job_name: 'pentameter'
    static_configs:
      - targets: ['pentameter-app:8080']
    scrape_interval: 60s
```

### Common Queries
```promql
# Specific equipment
water_temperature_fahrenheit{body="POOL"}
pump_rpm{name="VS"}
circuit_status{type="LIGHT"}

# All equipment types
water_temperature_fahrenheit
pump_rpm
circuit_status

# System health
intellicenter_connection_failure
intellicenter_last_refresh_timestamp_seconds
```

## Grafana Integration

### Dashboard Features
- **Pre-configured**: Automatically provisioned "Pool Monitoring" dashboard
- **Anonymous Access**: No authentication required for local use
- **Adaptive Layout**: Handles missing air sensors gracefully
- **Real-time Updates**: 30-second refresh with 6-hour default range

### Display Options
- **Standard View**: Full dashboard at `http://HOSTNAME:3000/d/intellicenter/`
- **Kiosk Mode**: Clean display at `http://HOSTNAME:3000/d/intellicenter/intellicenter?kiosk`
- **Recommended Dashboard**: `SERVER:PORT/d/intellicenter/intellicenter?kiosk=&autofitpanels=true`
- **Mobile Friendly**: Responsive design for all screen sizes

### Visual Elements
- Water temperature trends (Pool & Spa)
- Air temperature trends (if available)
- Connection status indicators
- Human-readable timestamps ("X minutes ago" format)

### Manual Dashboard Creation
Create custom panels using these queries:
```promql
water_temperature_fahrenheit{body="POOL"}
water_temperature_fahrenheit{body="SPA"}
air_temperature_fahrenheit{sensor="AIR"}
intellicenter_connection_failure
intellicenter_last_refresh_timestamp_seconds
```

## Feature Visibility Control

Pentameter respects IntelliCenter's "Show as Feature" settings to avoid duplicate controls and maintain clean dashboards.

### How It Works

IntelliCenter allows users to control whether features appear in the interface through a "Show as Feature" checkbox in the Feature Circuits configuration. Pentameter automatically detects and respects this setting.

**Common Use Case**: Pool heating equipment often appears as both:
- **Feature**: User control (e.g., "Spa Heat" - enable/disable heating)  
- **Thermal Equipment**: Actual equipment status (e.g., "Spa Heater" - heating/idle/cooling)

This creates duplication in monitoring dashboards where both the user control and equipment status are shown.

### User Control

Users can eliminate this duplication through their IntelliCenter interface:

1. **Access IntelliCenter** → Settings → Feature Circuits
2. **Select the feature** (e.g., "Spa Heat")
3. **Uncheck "Show as Feature"** to hide the user control
4. **Keep thermal equipment monitoring** for actual operational status

### Technical Implementation

Pentameter uses IntelliCenter's `SHOMNU` parameter to detect the "Show as Feature" setting:

- **Show as Feature: YES** → Feature appears in `feature_status` metrics
- **Show as Feature: NO** → Feature is automatically hidden from metrics

### Benefits

- **User-Controlled**: No hardcoded logic - users decide what to show
- **Universal**: Works with any equipment configuration and naming
- **Clean Dashboards**: Eliminates duplicate controls when desired
- **Flexible**: Users can show both if they want different information

### Example Scenarios

**Scenario 1: Show Both Controls**
- `feature_status{name="Spa Heat"}` → User enable/disable control
- `thermal_status{name="Spa Heater"}` → Equipment operational status
- **Use Case**: Monitor both user intent and equipment response

**Scenario 2: Show Equipment Only**  
- User disables "Show as Feature" for "Spa Heat"
- Only `thermal_status{name="Spa Heater"}` appears
- **Use Case**: Focus on equipment operation, control via IntelliCenter

This approach ensures Pentameter works universally across different pool configurations while giving users full control over their monitoring interface.
