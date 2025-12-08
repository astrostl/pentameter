# Claude Development Guide

## Design Philosophy - CRITICAL

**⚠️ UNIVERSAL COMPATIBILITY IS MANDATORY ⚠️**

This application must work with ANY IntelliCenter configuration, not just our specific setup. All code must be generic and configuration-agnostic.

### Forbidden Practices:
- **NO hardcoded equipment names** (e.g., "Pool Heat Pump", "Spa Heater", "UltraTemp")
- **NO assumptions about specific circuits** (e.g., C0001=Spa, C0002=Air Blower)
- **NO name-based logic** (e.g., if name contains "heat" then...)
- **NO configuration-specific filtering** (e.g., skip FTR01 because it's heating)
- **NO real IP addresses in documentation** - use `$PENTAMETER_IC_IP` or placeholder IPs like `192.168.1.100`

### Required Practices:
- **Use IntelliCenter's official metadata** (OBJTYP, SUBTYP, object IDs)
- **Pass through API data directly** without interpretation
- **Design for any equipment configuration** (different names, different circuits, different features)
- **Test with the mindset**: "Would this work on someone else's pool?"

### Examples:
❌ `if name == "Spa Heat"` (hardcoded to our config)
✅ `if subtype == "THERMAL"` (uses IntelliCenter's classification)

❌ `skip FTR01` (specific to our spa heating setup)  
✅ `process all FTR objects` (works with any feature configuration)

❌ `"Pool Heat Pump"` in dashboard field overrides
✅ `.*subtyp="ULTRA".*` for heat pump detection

**Remember: Other users have different equipment names, different circuit assignments, and different feature configurations. The code must work universally.**

## Build System

This project uses a well-organized Makefile with pinned tool versions for reproducible builds.

### Quick Commands

```bash
make dev          # Build + quality checks (recommended for development)
make build        # Build binary only
make quality      # Run all quality checks (warnings for lint/cyclo issues)
make quality-strict # Run all quality checks with strict enforcement
```

### Local Builds

**IMPORTANT**: Always build binaries in the project directory, not `/tmp` or other locations.

```bash
# Correct - builds in project directory
go build -o pentameter .
./pentameter --listen --ic-ip 192.168.1.100

# Or use the Makefile
make build
./pentameter --listen
```

Building in the project directory ensures:
- Consistent paths for testing and debugging
- Binary is version-controlled location (though ignored by .gitignore)
- Easy cleanup with `make clean`

### Release Process

**⚠️ See [RELEASE.md](RELEASE.md) for the complete release checklist ⚠️**

The release process involves coordinating GitHub, DockerHub, and Homebrew. The detailed step-by-step workflow with troubleshooting is documented in RELEASE.md.

**Key points:**
- Always ensure a clean working directory before releasing
- Never create releases without explicit user approval
- Follow the RELEASE.md checklist exactly - skipping steps causes failures

## Docker Development - CRITICAL SECTION

**⚠️ DOCKER BUILD CACHING IS SEVERELY PROBLEMATIC ⚠️**

Docker frequently fails to detect Go source changes and runs stale binaries. ALWAYS use Makefile targets which implement nuclear rebuild strategies.

### Network Configuration

**Host Networking is the Default:**

The pentameter-app service uses `network_mode: "host"` for several important reasons:

1. **mDNS Auto-Discovery**: Enables pentameter to discover IntelliCenter via mDNS (`pentair.local`)
   - Docker's default bridge network doesn't forward multicast traffic
   - mDNS requires multicast DNS packets on the local network
   - Host networking gives direct access to the physical network

2. **No Performance Overhead**: Eliminates NAT translation layer for WebSocket connections

3. **Simpler Debugging**: Network traffic appears as if running natively on host

4. **Security is Not a Concern**: Pentameter is a local network monitoring tool, not a public service
   - Already requires local network access to communicate with IntelliCenter
   - Not handling untrusted data or exposed to the internet
   - Security boundary is "can talk to local network" which is inherently required

**Why This Works:**
- Pentameter is single-purpose: monitor pool equipment on local network
- Not a multi-tenant system (no port conflicts expected)
- Not a public-facing service (no external attack surface)
- Deployed on single host monitoring single IntelliCenter

**Prometheus and Grafana**: These services remain on the bridge network (`pentameter-net`) since they don't need mDNS access and benefit from network isolation. They connect to pentameter via localhost on the host network.

**mDNS Discovery Implementation Details:**

The mDNS discovery code explicitly selects a network interface rather than using the default (`nil`) interface. This is critical for Docker containers:

- **Interface Selection**: `getBestMulticastInterface()` finds the best available network interface
- **Selection Criteria**: Prefers non-loopback, up interfaces with IPv4 addresses and multicast support
- **Fallback Strategy**: If no ideal interface found, uses any multicast-capable interface
- **Verbose Logging**: When discovery is enabled, logs which interface is selected for debugging

This explicit interface selection is what makes mDNS work in Docker containers with host networking, where the default interface selection doesn't work reliably.

### After ANY Code Changes - MANDATORY STEPS:

**⚠️ CRITICAL: ALWAYS USE MAKEFILE TARGETS FOR PENTAMETER DEBUGGING ⚠️**

When debugging or making ANY changes to pentameter code, ALWAYS use the Makefile targets. They implement nuclear rebuilds by default.

```bash
# ALWAYS do this when debugging or changing pentameter code:
make docker-build

# This automatically:
# - Stops pentameter-app container
# - Prunes Docker system 
# - Rebuilds with --no-cache
# - Starts pentameter-app container
# - Verifies changes took effect
```

### Full Stack Nuclear Option (if pentameter-specific nuclear fails):

```bash
# Only if the pentameter nuclear option above fails:
make docker-build-stack

# This automatically:
# - Stops entire stack
# - Prunes Docker system
# - Rebuilds everything with --no-cache  
# - Starts entire stack
```

### NEVER Use These Commands for Pentameter Debugging:

```bash
docker compose restart pentameter  # ❌ Does NOT rebuild image
docker compose up -d               # ❌ Uses cached image  
docker compose build              # ❌ May use cached layers
docker build                      # ❌ Manual commands bypass nuclear approach
```


### Red Flags - Force Nuclear Rebuild:

- Metrics showing old values after source changes
- Log output not matching recent code changes  
- "Updated" behavior not reflecting new logic
- Type labels showing old values (HEATPUMP vs THERMAL)
- Any doubt whatsoever about whether changes are live

### Verification Commands:

```bash
# Always verify after rebuild (automatic with make docker-build):
curl -s http://localhost:8080/metrics | grep "circuit_status.*THERMAL"
make compose-logs-once | grep "Updated.*status"

# Check specific container logs:
docker logs pentameter-app
docker logs pentameter-prometheus  
docker logs pentameter-grafana
```

**Remember: When code changes don't appear to work, it's usually Docker cache, not your code!**

### Tool Versions

The following tools are automatically installed with latest versions:
- golangci-lint: @latest
- gocyclo: @latest  
- staticcheck: @latest
- go-mod-outdated: @latest
- ineffassign: @latest
- misspell: @latest
- govulncheck: @latest
- gocritic: @latest
- gosec: @latest

### Target Organization

**Build & Clean**: `build`, `build-static`, `clean`, `deps`
**Testing**: `test`, `test-race`, `bench`
**Quality Suites**: `dev` (build + quality), `quality` (CI-friendly warnings), `quality-strict` (enforced), `quality-enhanced` (includes race + bench), `quality-comprehensive` (maximum linter coverage)
**Individual Quality Tools**: `fmt`, `check-fmt`, `lint`, `lint-enhanced`, `cyclo`, `vet`, `ineffassign`, `misspell`, `govulncheck`, `gocritic`, `gosec`, `staticcheck`
**Dependency Management**: `modcheck`, `modverify`, `depcount`, `depoutdated`
**Docker**: `docker-build` (nuclear by default), `docker-build-stack`, `compose-up`, `compose-down`, `compose-logs`, `compose-logs-once`

### Quality Check Levels

1. **`quality`** - Core checks with warnings (CI-friendly)
2. **`quality-strict`** - All checks must pass (release builds)  
3. **`quality-enhanced`** - Includes race detection, benchmarks, and staticcheck
4. **`quality-comprehensive`** - Maximum linter coverage with enhanced analysis

### Lint and Test Commands

Run quality checks before committing:
```bash
make quality      # Quick check with warnings
make dev         # Build + quality (full development cycle)
```

### Debugging

Use Makefile targets and curl to debug:
```bash
make compose-logs        # View pentameter logs with tail
make compose-logs-once   # View pentameter logs once
curl -s http://localhost:8080/metrics    # Check pentameter service metrics
curl -s http://localhost:9090/metrics    # Check Prometheus metrics
curl -s http://localhost:9090/api/v1/targets  # Check scrape target status
```

## README Quick Start Guidelines

**CRITICAL: README Quick Start Must Include All Required Files**

The Quick Start section should always use `git clone` to ensure users get all necessary configuration files:
- `prometheus.yml` - Prometheus scraping configuration
- `grafana/` directory - Dashboard and datasource configurations  
- `.env.example` - Environment variable template
- `docker-compose.yml` - Service orchestration

**Never use `curl` to download individual files** - this breaks the setup because users miss required config files.

**Correct Quick Start Pattern:**
```bash
git clone https://github.com/astrostl/pentameter.git
cd pentameter
cp .env.example .env
# Edit .env and set your PENTAMETER_IC_IP
docker compose up -d
```

**Key Points:**
- Use `cp .env.example .env` (not `echo`) - the example file exists for this purpose
- Emphasize that published Docker images are used (no build time required)
- Keep it simple - clone, configure, run
- Use `HOSTNAME` instead of `localhost` in all user-facing URLs to work in any deployment environment

### IntelliCenter Connection

The IntelliCenter IP address is stored in `.env` as `PENTAMETER_IC_IP`. The WebSocket port is always 6680. Always source this for commands requiring the IntelliCenter IP:
```bash
source .env
echo '{"command":"GetHardwareDefinition"}' | websocat ws://$PENTAMETER_IC_IP:6680
```

**⚠️ See [API.md](API.md) for complete IntelliCenter WebSocket API documentation ⚠️**

API.md contains the definitive reference for all IntelliCenter commands, object types, push notifications, and protocol details.

### Live Circuit Status Queries

When the user asks for circuit status, query **ALL circuits (regular and feature)** and present results in a table format with freeze protection status.

**Query all regular circuits (C0001-C0011):**
```bash
echo '{"messageID":"circuits","command":"GetParamList","condition":"OBJTYP=CIRCUIT","objectList":[{"objnam":"C0001","keys":["SNAME","STATUS","FREEZE"]},{"objnam":"C0002","keys":["SNAME","STATUS","FREEZE"]},{"objnam":"C0003","keys":["SNAME","STATUS","FREEZE"]},{"objnam":"C0004","keys":["SNAME","STATUS","FREEZE"]},{"objnam":"C0005","keys":["SNAME","STATUS","FREEZE"]},{"objnam":"C0006","keys":["SNAME","STATUS","FREEZE"]},{"objnam":"C0007","keys":["SNAME","STATUS","FREEZE"]},{"objnam":"C0008","keys":["SNAME","STATUS","FREEZE"]},{"objnam":"C0009","keys":["SNAME","STATUS","FREEZE"]},{"objnam":"C0010","keys":["SNAME","STATUS","FREEZE"]},{"objnam":"C0011","keys":["SNAME","STATUS","FREEZE"]}]}' | timeout 5 websocat -n1 ws://$PENTAMETER_IC_IP:6680
```

**Query all feature circuits (FTR01-FTR08):**
```bash
echo '{"messageID":"features","command":"GetParamList","condition":"OBJTYP=CIRCUIT","objectList":[{"objnam":"FTR01","keys":["SNAME","STATUS","FREEZE"]},{"objnam":"FTR02","keys":["SNAME","STATUS","FREEZE"]},{"objnam":"FTR03","keys":["SNAME","STATUS","FREEZE"]},{"objnam":"FTR04","keys":["SNAME","STATUS","FREEZE"]},{"objnam":"FTR05","keys":["SNAME","STATUS","FREEZE"]},{"objnam":"FTR06","keys":["SNAME","STATUS","FREEZE"]},{"objnam":"FTR07","keys":["SNAME","STATUS","FREEZE"]},{"objnam":"FTR08","keys":["SNAME","STATUS","FREEZE"]}]}' | timeout 5 websocat -n1 ws://$PENTAMETER_IC_IP:6680
```

**Present results in two tables:**

| Circuit | Status | Reason |
|---------|--------|--------|
| Name | ON/OFF | Normal / FREEZE / - |

| Feature | Status | Reason |
|---------|--------|--------|
| Name | ON/OFF | Normal / FREEZE / - |

**Response interpretation:**
- `STATUS=ON, FREEZE=OFF` → Running normally (user-activated or scheduled)
- `STATUS=ON, FREEZE=ON` → Running due to freeze protection (show "FREEZE")
- `STATUS=OFF` → Off (show "-" for reason)

### Temperature Units

**IMPORTANT**: This project uses Fahrenheit for all temperature metrics, not Celsius.

- Pool temperature metrics: `water_temperature_fahrenheit`
- Air temperature metrics: `air_temperature_fahrenheit`
- Grafana dashboards expect Fahrenheit values
- Pool industry standard is Fahrenheit in the US

Do not convert to Celsius - store and display temperatures in Fahrenheit as received from the IntelliCenter API.

### Equipment Naming

**IMPORTANT**: Always use configured names from IntelliCenter, never make up names.

- Circuit names come from IntelliCenter's `SNAME` parameter in circuit configuration
- Heater names come from IntelliCenter's `SNAME` parameter in heater configuration  
- Body names come from IntelliCenter's `SNAME` parameter in body configuration
- Never construct artificial names like "Pool Heat Pump" - use actual equipment names like "UltraTemp"

When adding new equipment monitoring, always query the IntelliCenter configuration first to get the real equipment names that users have configured.

### Prometheus and Grafana Integration

**IMPORTANT**: All design decisions should prioritize Prometheus and Grafana compatibility.

- **Metric Design**: Structure metrics to work seamlessly with existing Grafana dashboards
- **Naming Conventions**: Use consistent metric names and label structures
- **Data Types**: Prefer enhancing existing metrics over creating separate ones
- **Dashboard Integration**: New features should "just work" with current panels when possible
- **Circuit Status Pattern**: Use the `circuit_status` metric pattern for equipment monitoring with tri-state values (0=off, 1=heating/on, 2=cooling)
- **Label Consistency**: Keep label names and structures consistent across related metrics
- **Minimal Configuration**: Users should not need to modify existing Grafana dashboards for new features

**CRITICAL - USE TYPES NOT NAMES**: Always use the `type` label for Grafana field overrides and filtering. NEVER use equipment names like "Gas Heater" or "UltraTemp" for dashboard configuration. Use types like "THERMAL", "PUMP", "LIGHT", etc. This ensures the dashboard works with any equipment configuration regardless of user-configured names.

- **Field Overrides**: Use regex patterns like `.*type="THERMAL".*` to match thermal equipment
- **Type Classification**: Classify equipment by function (THERMAL, PUMP, LIGHT, etc.) not by specific names
- **Scalability**: Type-based approach works with any equipment names or future equipment additions

When adding new monitoring capabilities, always ask: "How can this integrate seamlessly into the existing Prometheus/Grafana workflow?" Prefer extending existing metrics over creating new ones.

### Polling and Scraping Intervals

The system uses consistent 1-minute (60-second) intervals across all components. There are four key interval settings:

1. **Pentameter Polling Interval** (`main.go:32`): How often pentameter queries IntelliCenter
   ```go
   defaultPollInterval = 60  // seconds
   ```

2. **Prometheus Scraping Interval** (`prometheus.yml:2,9`): How often Prometheus scrapes pentameter metrics
   ```yaml
   global:
     scrape_interval: 60s
   scrape_configs:
     - job_name: 'pentameter'
       scrape_interval: 60s
   ```

3. **Docker Health Check Interval** (`docker-compose.yml:17`): How often Docker checks pentameter health
   ```yaml
   healthcheck:
     interval: 60s
   ```

4. **Prometheus Staleness Period** (`docker-compose.yml:41`): How long Prometheus retains metrics after they stop being emitted
   ```yaml
   command:
     - '--query.lookback-delta=1m'
   ```

**To change intervals**: Update all four locations to maintain consistency. The docker-compose.yml also sets the default via `PENTAMETER_INTERVAL=${PENTAMETER_INTERVAL:-60}` which should match the main.go default. When changing to different intervals (e.g., 5 minutes), update all four settings proportionally to maintain the 1:1:1:1 ratio for optimal metric freshness and cleanup behavior.

## Auto-Discovery - Finding IntelliCenter on Network

### Overview

Pentameter can automatically discover IntelliCenter controllers via mDNS (multicast DNS). The IntelliCenter broadcasts itself as `pentair.local` on the local network, allowing pentameter to find it without manual IP configuration.

### Design Principles

**Zero-Configuration Operation:**
- No IP address required for basic operation
- Automatic network discovery on startup
- Falls back to manual IP if discovery fails or is disabled
- Discovery timeout prevents indefinite waiting (60-second default with progress indicators every 2 seconds)

**mDNS Discovery Implementation:**
- Uses golang.org/x/net/ipv4 for multicast DNS queries
- Queries for `pentair.local` hostname
- Returns first valid IPv4 address found
- Works on most home networks without configuration

### Key Features

1. **Automatic Discovery**: Finds IntelliCenter without user configuration
2. **Fast Discovery**: Typical discovery time 1-5 seconds, timeout after 60 seconds
3. **Progress Indicators**: Visible retry messages every 2 seconds during discovery
4. **Manual Override**: `--ic-ip` flag bypasses discovery
5. **Discovery Testing**: `--discover` flag shows IP and exits
6. **Graceful Fallback**: Clear error messages with guidance on using `--ic-ip` flag if discovery fails

### Implementation Notes

**Discovery Flow:**
```go
// 1. Check if IP provided via flag or environment
if icIP == "" {
    // 2. Attempt mDNS discovery with 60-second timeout and progress indicators
    discoveredIP, err := discoverIntelliCenter(60 * time.Second)
    if err != nil {
        // 3. Fail with helpful error message guiding users to --ic-ip flag
        log.Fatalf("Could not discover IntelliCenter: %v\nUse --ic-ip flag to specify manually", err)
    }
    icIP = discoveredIP
}
```

**Network Requirements:**
- Multicast DNS (mDNS) must be enabled on network
- UDP port 5353 must not be blocked
- IntelliCenter and pentameter must be on same network segment
- Some corporate/guest networks may block multicast traffic

### Testing Discovery

**Discovery Test Mode:**
```bash
# Test discovery and show IP address
pentameter --discover
# Output: IntelliCenter discovered at: 192.168.1.100

# Test with timeout (future enhancement)
pentameter --discover --timeout 10
```

**Manual IP Override:**
```bash
# Bypass discovery entirely
pentameter --ic-ip 192.168.1.100

# Via environment variable
export PENTAMETER_IC_IP=192.168.1.100
pentameter
```

### Troubleshooting

**Discovery Fails:**
- Check IntelliCenter is on same network
- Verify mDNS/Bonjour is not blocked by firewall
- Try manual IP with `--ic-ip` flag
- Test network connectivity: `ping pentair.local`

**Slow Discovery:**
- Normal discovery: 1-5 seconds
- Slow network: up to 60 seconds with progress indicators every 2 seconds
- Discovery shows visible retry attempts during network scanning

### Future Enhancements

Potential improvements for auto-discovery:
- Configurable discovery timeout
- Multiple IntelliCenter detection with selection
- Discovery caching to avoid repeated lookups
- mDNS service browsing for additional metadata

## Listen Mode - Live Equipment Monitoring

### Overview

Listen mode (`--listen` flag) provides real-time event monitoring for pool equipment changes using a hybrid push + poll architecture. It's designed for debugging, equipment discovery, and understanding IntelliCenter behavior.

### Design Principles

**Hybrid Push + Poll Architecture:**
- Receives real-time push notifications from IntelliCenter for instant updates
- Periodic polling (default 60s, configurable via `--interval`) catches equipment that doesn't push
- Maintains previous state snapshots for all equipment types
- Compares current values against previous snapshots to detect changes
- Only logs when values actually change (after initial discovery)
- Uses `PUSH:` and `POLL:` prefixes to distinguish event sources

**Why Hybrid?**
- IntelliCenter pushes body/setpoint changes instantly
- **Pump RPM changes are NOT pushed** - only discoverable via polling
- Polling provides a safety net for any missed push notifications
- Best of both worlds: real-time + comprehensive

**Universal Equipment Discovery:**
- Automatically discovers and logs ANY equipment type returned by IntelliCenter
- Works with equipment types not specifically implemented (valves, sensors, remotes, etc.)
- Uses IntelliCenter's OBJTYP field to classify unknown equipment
- No hardcoded equipment assumptions - works with any configuration

**Clean Event Logging:**
- Shows only meaningful events: initial state, changes, and errors
- Event messages use `PUSH:` or `POLL:` prefix to indicate source
- Reports `POLL: [no changes]` when a poll cycle finds no changes
- Temperature changes show both old and new values for context

### Key Features

1. **Hybrid Push + Poll**: Real-time push notifications + periodic polling for comprehensive coverage
2. **Source Identification**: `PUSH:` and `POLL:` prefixes distinguish event sources
3. **Multi-Equipment Tracking**: Monitors circuits, pumps, temperatures, thermal equipment, and features simultaneously
4. **Unknown Equipment Handling**: Logs equipment types not specifically implemented, using OBJTYP and basic status fields
5. **Initial State Discovery**: Shows all equipment on startup with `POLL:` prefix
6. **Operational State Visibility**: For thermal equipment, shows heating/cooling/off states, not just on/off

### Implementation Architecture

**Hybrid Architecture:**
```go
func (pm *PoolMonitor) StartEventListener(ctx context.Context, pollInterval time.Duration) {
    // 1. Fetch initial state (logged as POLL:)
    pm.GetTemperatures(ctx)

    // 2. Create separate poller with its own connection
    poller := &PoolMonitor{...}  // Shares state, separate websocket

    // 3. Start parallel goroutines
    go pm.pollLoop(ctx, poller, pollInterval)  // Polls every interval
    pm.listenLoop(ctx)  // Blocks on push notifications
}
```

**State Management:**
```go
type EquipmentState struct {
    Circuits        map[string]CircuitState
    Pumps           map[string]float64
    Temps           map[string]float64
    Thermal         map[string]ThermalState
    Features        map[string]string
    PollChangeCount int  // Track changes per poll cycle
}
```

**Mutex Protection:**
Both listener and poller access shared state - mutex ensures thread safety:
```go
pm.mu.Lock()
pm.previousState.PollChangeCount = 0
err := poller.GetTemperatures(ctx)
changes := pm.previousState.PollChangeCount
pm.mu.Unlock()
```

### Configuration Behavior

**Default Intervals:**
- Listen mode with `--interval`: Uses specified interval for polling (minimum 5s)
- Listen mode without `--interval`: Defaults to 60-second polling interval
- Normal mode: Defaults to 60-second polling interval
- Intervals below 5 seconds are automatically raised to 5s with a warning

**Listen Mode Detection:**
```go
// Check for listen mode from flag or environment variable
listenMode := *listenFlag || os.Getenv("PENTAMETER_LISTEN") == "true"

// In listen mode, use StartEventListener with hybrid push+poll
if listenMode {
    pm.StartEventListener(ctx, pollInterval)  // Push + periodic poll
}
```

### Output Examples

**Initial Discovery (via polling):**
```
2025/11/28 18:51:15 Fetching initial equipment state...
2025/11/28 18:51:15 POLL: Pool temperature detected: 22.0°F
2025/11/28 18:51:15 POLL: Spa temperature detected: 95.0°F
2025/11/28 18:51:15 POLL: Air temperature detected: 36.0°F
2025/11/28 18:51:15 POLL: VS detected: 3000 RPM
2025/11/28 18:51:16 POLL: Pool detected: ON
2025/11/28 18:51:16 POLL: Spa Heater detected: heating
2025/11/28 18:51:35 Listening for real-time changes (Ctrl+C to stop)...
```

**Push Notification (real-time):**
```
2025/11/28 18:52:04 PUSH: Spa temp=97°F setpoint=98°F htmode=1 status=ON
```

**Poll Cycle (periodic):**
```
2025/11/28 18:53:35 POLL: Spa temperature changed: 95.0°F → 96.0°F
2025/11/28 18:53:35 POLL: VS changed: 3000 → 2500 RPM
2025/11/28 18:54:35 POLL: [no changes]
```

### Use Cases

1. **Debugging Equipment Issues**: Watch real-time state changes when troubleshooting
2. **Understanding IntelliCenter Behavior**: Learn how equipment responds to commands
3. **Discovering Equipment Configuration**: Find all equipment in the system, including types not yet implemented
4. **Testing New Features**: Verify equipment changes are detected correctly
5. **Development**: Understand equipment behavior before implementing specific support
6. **Pump Monitoring**: Track pump RPM changes that aren't pushed by IntelliCenter

### Integration with Metrics Mode

Listen mode and normal metrics mode are mutually compatible:
- Both can run simultaneously (listen mode doesn't disable metrics collection)
- Metrics continue to be exposed on HTTP port even in listen mode
- Prometheus can scrape metrics while listen mode logs events
- Use listen mode for debugging, normal mode for production monitoring

### Testing Listen Mode

**Quick Test:**
```bash
# Run with default 60s poll interval
pentameter --ic-ip 192.168.1.100 --listen

# Run with custom 10s poll interval
pentameter --ic-ip 192.168.1.100 --listen --interval 10

# Trigger equipment changes (turn lights on/off, adjust temperature setpoints)
# PUSH: messages appear instantly, POLL: messages appear on poll cycles
```

**Docker Testing:**
```bash
# Override docker-compose.yml to enable listen mode
docker run --rm --network host \
  -e PENTAMETER_IC_IP=192.168.1.100 \
  -e PENTAMETER_LISTEN=true \
  -e PENTAMETER_INTERVAL=30 \
  astrostl/pentameter:latest
```

### Future Enhancements

Potential improvements for listen mode:
- JSON output format for structured logging
- WebSocket endpoint for real-time event streaming to web clients
- Event filtering by equipment type or name
- Configurable event history (show last N changes on startup)
- Integration with external notification systems (webhooks, MQTT, etc.)