# Claude Development Guide

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

Docker frequently fails to detect Go source changes and runs stale binaries. ALWAYS use aggressive rebuild strategies.

### After ANY Code Changes - MANDATORY STEPS:

```bash
# ALWAYS do this after changing main.go or any source:
docker compose stop pentameter
docker compose build --no-cache pentameter  
docker compose start pentameter

# Verify changes took effect IMMEDIATELY:
curl -s http://localhost:8080/metrics | head -5
docker compose logs pentameter --tail 5
```

### Nuclear Option (use when in doubt):

```bash
# When standard rebuild fails or you're unsure (PENTAMETER ONLY):
docker compose stop pentameter
docker system prune -f  
docker compose build --no-cache pentameter
docker compose start pentameter

# Full stack nuclear option (only if pentameter-specific approach fails):
docker compose down
docker system prune -f  
docker compose build --no-cache
docker compose up -d
```

### NEVER Trust These Commands for Code Changes:

```bash
docker compose restart pentameter  # ❌ Does NOT rebuild image
docker compose up -d               # ❌ Uses cached image  
make docker-build                  # ❌ May use cached layers
```

### Red Flags - Force Nuclear Rebuild:

- Metrics showing old values after source changes
- Log output not matching recent code changes  
- "Updated" behavior not reflecting new logic
- Type labels showing old values (HEATPUMP vs THERMAL)
- Any doubt whatsoever about whether changes are live

### Verification Commands:

```bash
# Always verify after rebuild:
curl -s http://localhost:8080/metrics | grep "circuit_status.*THERMAL"
docker compose logs pentameter --tail 10 | grep "Updated.*status"
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
**Docker**: `docker-build`, `compose-up`, `compose-down`, `compose-logs`, `compose-logs-once`

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

Use curl to debug metrics endpoints:
```bash
curl -s http://localhost:8080/metrics    # Check pentameter service metrics
curl -s http://localhost:9090/metrics    # Check Prometheus metrics
curl -s http://localhost:9090/api/v1/targets  # Check scrape target status
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