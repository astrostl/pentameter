# Claude Development Guide

## Design Philosophy - CRITICAL

**⚠️ UNIVERSAL COMPATIBILITY IS MANDATORY ⚠️**

This application must work with ANY IntelliCenter configuration, not just our specific setup. All code must be generic and configuration-agnostic.

### Forbidden Practices:
- **NO hardcoded equipment names** (e.g., "Pool Heat Pump", "Spa Heater", "UltraTemp")
- **NO assumptions about specific circuits** (e.g., C0001=Spa, C0002=Air Blower)
- **NO name-based logic** (e.g., if name contains "heat" then...)
- **NO configuration-specific filtering** (e.g., skip FTR01 because it's heating)

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

## Docker Development - CRITICAL SECTION

**⚠️ DOCKER BUILD CACHING IS SEVERELY PROBLEMATIC ⚠️** 

Docker frequently fails to detect Go source changes and runs stale binaries. ALWAYS use Makefile targets which implement nuclear rebuild strategies.

### After ANY Code Changes - MANDATORY STEPS:

**⚠️ CRITICAL: ALWAYS USE MAKEFILE TARGETS FOR PENTAMETER DEBUGGING ⚠️**

When debugging or making ANY changes to pentameter code, ALWAYS use the Makefile targets. They implement nuclear rebuilds by default.

```bash
# ALWAYS do this when debugging or changing pentameter code:
make docker-build

# This automatically:
# - Stops pentameter
# - Prunes Docker system 
# - Rebuilds with --no-cache
# - Starts pentameter
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

### IntelliCenter Connection

The IntelliCenter IP address is stored in `.env` as `PENTAMETER_IC_IP`. The WebSocket port is always 6680. Always source this for commands requiring the IntelliCenter IP:
```bash
source .env
echo '{"command":"GetHardwareDefinition"}' | websocat ws://$PENTAMETER_IC_IP:6680
```

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