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

### Release Process - CRITICAL

**⚠️ COMPLETE RELEASE WORKFLOW: DOCKERHUB + HOMEBREW + GITHUB ⚠️**

This project has a multi-platform release workflow requiring GitHub, DockerHub, and Homebrew coordination. ALWAYS follow the complete workflow below.

**CRITICAL: Clean Working Directory**
- Ensure `git status` shows no uncommitted changes before starting
- The version will show as `-dirty` if there are uncommitted changes, breaking the release
- Use `git stash` to temporarily store any uncommitted changes

**Step 1: Pre-Release Preparation**
```bash
# 1. Ensure clean working directory
git status  # Must be clean!

# 2. Update documentation (if needed)
# - CHANGELOG.md: Add new version section with changes
# - README.md: Update if new features require documentation
# - CLAUDE.md: Update process documentation if needed

# 3. Commit documentation updates
git add CHANGELOG.md README.md CLAUDE.md
git commit -m "Update documentation for v0.X.X release"
git push

# 4. Run quality checks to ensure release readiness
make quality-strict
```

**Step 2: Create Release Tag**
```bash
# Create and push version tag - ONLY when ready for release
git tag v0.X.X
git push origin v0.X.X
```

**Step 3: Multi-Platform Release Build**
The automated `make release` target has dependencies that may fail. Use manual approach:

```bash
# 3a. Quality checks (must pass)
make quality-strict

# 3b. Build and push Docker images manually (more reliable)
# Build multi-platform images
docker build --platform linux/amd64 -t astrostl/pentameter:v0.X.X-amd64 .
docker build --platform linux/arm64 -t astrostl/pentameter:v0.X.X-arm64 .

# Tag for latest
docker tag astrostl/pentameter:v0.X.X-amd64 astrostl/pentameter:latest-amd64
docker tag astrostl/pentameter:v0.X.X-arm64 astrostl/pentameter:latest-arm64

# Push all images
docker push astrostl/pentameter:v0.X.X-amd64
docker push astrostl/pentameter:v0.X.X-arm64
docker push astrostl/pentameter:latest-amd64
docker push astrostl/pentameter:latest-arm64

# Create multi-platform manifests (requires manifest-tool)
export PATH=$PATH:$(go env GOPATH)/bin
manifest-tool push from-args --platforms linux/amd64,linux/arm64 --template astrostl/pentameter:latest-ARCH --target astrostl/pentameter:latest
manifest-tool push from-args --platforms linux/amd64,linux/arm64 --template astrostl/pentameter:v0.X.X-ARCH --target astrostl/pentameter:v0.X.X

# 3c. Build Homebrew assets
make build-macos-binaries package-macos-binaries generate-macos-checksums update-homebrew-formula
```

**Step 4: Create GitHub Release**
```bash
# Create GitHub release with generated assets
gh release create v0.X.X \
  --title "v0.X.X - Release Title" \
  --notes "Release notes from CHANGELOG.md..." \
  dist/pentameter-v0.X.X-darwin-amd64.tar.gz \
  dist/pentameter-v0.X.X-darwin-arm64.tar.gz \
  dist/checksums.txt
```

**Step 5: Final Steps**
```bash
# Push updated Homebrew formula with correct checksums
git add Formula/pentameter.rb
git commit -m "Update Homebrew formula for v0.X.X with real SHA256 checksums"
git push origin master

# Verify Homebrew formula works
brew upgrade  # Should upgrade pentameter without checksum errors
```

**⚠️ CRITICAL CHECKSUM VERIFICATION**
- The Homebrew formula MUST have correct SHA256 checksums matching the GitHub release assets
- If `brew upgrade` fails with "SHA256 mismatch", the checksums in Formula/pentameter.rb are wrong
- Use the checksums from `dist/checksums.txt` to fix Formula/pentameter.rb
- Always test the Homebrew formula after release

**Why this workflow is critical:** 
- Docker users expect images to be available immediately after GitHub releases
- Homebrew users expect tap formulas to work with published GitHub releases  
- The docker-compose.yml references `astrostl/pentameter:latest`
- The Homebrew formula references specific release assets and checksums
- Checksum mismatches break Homebrew installations for all users

**Individual targets available:**
- `make docker-tag` - Tag images for publishing
- `make docker-push` - Build and push multi-platform images (linux/amd64,linux/arm64) - DEFAULT
- `make docker-push-single` - Push single-platform images only (for testing)
- `make docker-manifest` - Create multi-platform manifests using manifest-tool
- `make docker-release` - Build + push multi-platform images (without quality checks)
- `make build-macos-binaries` - Build macOS binaries for Homebrew (Intel + Apple Silicon)
- `make package-macos-binaries` - Package macOS binaries into tar.gz archives
- `make generate-macos-checksums` - Generate SHA256 checksums for macOS packages
- `make update-homebrew-formula` - Update Formula/pentameter.rb with new version and checksums
- `make release` - **Full workflow (quality + Docker + Homebrew + GitHub assets)**

**Multi-Platform Publishing (Automatic):**

All DockerHub releases are now multi-platform by default. The standard workflow automatically builds for both AMD64 and ARM64:

1. Builds separate architecture-specific images (`:latest-amd64`, `:latest-arm64`) 
2. Pushes both images to DockerHub
3. Uses `manifest-tool` to create multi-platform manifests that automatically select the correct image per platform
4. Works with both `docker` and `nerdctl` (unlike Docker buildx)

**Final DockerHub Layout:**
- `astrostl/pentameter:latest` - Multi-platform manifest (users should use this)
- `astrostl/pentameter:latest-amd64` - Intel/AMD64 specific image
- `astrostl/pentameter:latest-arm64` - Apple Silicon/ARM64 specific image
- Version-specific tags for both architectures and manifests

**Troubleshooting Release Issues:**

1. **Docker Build Issues:**
   - If `make docker-build` fails with "no such service: pentameter", the docker-compose.yml uses published images, not local builds
   - Use manual Docker commands instead: `docker build --platform linux/amd64 -t astrostl/pentameter:v0.X.X-amd64 .`

2. **Version Shows "-dirty":**
   - Uncommitted changes make the version "v0.X.X-dirty" which breaks the release process
   - Use `git stash` to temporarily store changes, complete release, then `git stash pop`

3. **Manifest-tool Issues:**
   - If `manifest-tool` fails, run `make docker-manifest` separately to create multi-platform manifests
   - Ensure `manifest-tool` is installed: `go install github.com/estesp/manifest-tool/v2/cmd/manifest-tool@latest`
   - Add to PATH: `export PATH=$PATH:$(go env GOPATH)/bin`

4. **Homebrew Checksum Mismatches:**
   - "SHA256 mismatch" errors mean Formula/pentameter.rb has wrong checksums
   - Use actual checksums from `dist/checksums.txt` to fix the formula
   - Always verify with `brew upgrade` after fixing

5. **make release Target Issues:**
   - The automated `make release` may fail due to docker-compose dependencies
   - Use the manual step-by-step process documented above for more reliable releases

**Why manifest-tool:** Docker buildx requires Docker specifically and doesn't work with nerdctl. The manifest-tool approach builds individual platform images and creates manifests, working with any container runtime.

## Homebrew Tap Distribution

The project includes a consolidated Homebrew tap in the main repository for easy macOS installation.

### Repository Structure

```
pentameter/
├── Formula/
│   └── pentameter.rb      # Homebrew formula for macOS binaries
├── dist/                  # Generated during release (macOS binaries + checksums)
├── docker-compose.yml     # Full monitoring stack
├── Makefile              # Build automation including Homebrew targets
└── ...
```

### Homebrew Formula Management

The `Formula/pentameter.rb` file is automatically maintained by the Makefile:

**During Development:**
- Formula contains placeholder SHA256 values (`PLACEHOLDER_AMD64_SHA256`, `PLACEHOLDER_ARM64_SHA256`)
- Points to the current version tag for GitHub release assets

**During Release (`make release`):**
1. Builds clean macOS binaries for current git tag
2. Generates real SHA256 checksums 
3. Updates formula with actual version and checksums
4. Creates release-ready assets in `dist/` directory

### User Installation

**Tap Installation:**
```bash
# Add the tap (one time setup)
brew tap astrostl/pentameter https://github.com/astrostl/pentameter

# Install pentameter
brew install pentameter
```

**Direct Installation:**
```bash
# One-line install (automatically adds tap)
brew install astrostl/pentameter/pentameter
```

### Post-Release Steps

After running `make release`, you must manually create the GitHub release with the generated assets:

```bash
# Assets are ready in dist/ directory
gh release create v1.0.0 \
  --title "v1.0.0 - Release Title" \
  --notes "Release notes..." \
  dist/pentameter-v1.0.0-darwin-amd64.tar.gz \
  dist/pentameter-v1.0.0-darwin-arm64.tar.gz \
  dist/checksums.txt
```

**Critical:** The Homebrew formula references these exact GitHub release assets, so the release must be created with the generated binaries for the formula to work.

### Platform Support

- **macOS (Intel + Apple Silicon)**: Pre-built binaries via Homebrew
- **Linux**: Docker deployment (docker-compose.yml) or build from source
- **Windows**: Docker deployment or build from source

The formula provides helpful error messages for non-macOS platforms directing users to Docker or source builds.

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

## Release Process

### Version Management

This project uses semantic versioning with git tags. The Makefile automatically determines versions using:
- `VERSION ?= $(shell git describe --tags --always --dirty)`
- `LATEST_TAG ?= $(shell git describe --tags --abbrev=0 2>/dev/null || echo "v0.1.0")`

### Release Workflow

**⚠️ CRITICAL: NEVER CREATE RELEASES WITHOUT EXPLICIT USER APPROVAL ⚠️**

All release creation (git tags, version bumps, DockerHub publishing) requires explicit user direction. Never create releases proactively or as troubleshooting attempts.

#### 1. Prepare Release
```bash
# Update documentation
# - CHANGELOG.md: Add new version section with changes
# - README.md: Update installation instructions and configuration
# - CLAUDE.md: Update any process documentation

# Commit documentation updates
git add CHANGELOG.md README.md CLAUDE.md
git commit -m "Update documentation for v0.X.X release"
git push
```

#### 2. Create Git Tag (ONLY WITH USER APPROVAL)
```bash
# Create and push version tag - ONLY when user explicitly requests it
git tag v0.X.X
git push origin v0.X.X
```

#### 3. Build and Publish to DockerHub
```bash
# Full release workflow (quality checks + multi-platform Docker publishing)
make release

# This automatically:
# - Runs quality-strict (all quality checks must pass)
# - Builds multi-platform images (AMD64 + ARM64)
# - Pushes to DockerHub with version tag and latest
# - Creates multi-platform manifests using manifest-tool
```

### Multi-Platform Docker Publishing

The project uses manifest-tool for reliable multi-platform image creation:

#### Automatic Multi-Platform (Default)
```bash
make docker-push    # Builds AMD64 + ARM64, creates manifests
make release        # Full workflow including quality checks
```

#### Manual Steps (if needed)
```bash
# 1. Build and push architecture-specific images
make docker-tag
docker build --platform linux/amd64 -t astrostl/pentameter:v0.X.X-amd64 .
docker build --platform linux/arm64 -t astrostl/pentameter:v0.X.X-arm64 .
docker push astrostl/pentameter:v0.X.X-amd64
docker push astrostl/pentameter:v0.X.X-arm64

# 2. Create multi-platform manifests
make docker-manifest
```

#### Single Platform (Testing Only)
```bash
make docker-push-single    # Push current platform only
```

### Release Verification

After releasing:
```bash
# Verify multi-platform manifest exists
docker manifest inspect astrostl/pentameter:v0.X.X

# Test published image works
docker run --rm astrostl/pentameter:v0.X.X --help

# Verify Go Report Card updates (may take time)
curl -s https://goreportcard.com/report/github.com/astrostl/pentameter
```

### Container Names

The stack uses namespaced container names to prevent conflicts:
- `pentameter-app` - Main pentameter application (port 8080)
- `pentameter-prometheus` - Prometheus metrics storage (port 9090)
- `pentameter-grafana` - Grafana dashboard interface (port 3000)

This prevents conflicts with existing monitoring containers and clearly identifies pentameter stack components.

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

## Listen Mode - Live Equipment Monitoring

### Overview

Listen mode (`--listen` flag) provides real-time event monitoring for pool equipment changes. It's designed for debugging, equipment discovery, and understanding IntelliCenter behavior.

### Design Principles

**State-Based Change Detection:**
- Maintains previous state snapshots for all equipment types
- Compares current values against previous snapshots to detect changes
- Only logs when values actually change (not on every poll)
- Detects initial state on first poll and logs all equipment discovery

**Universal Equipment Discovery:**
- Automatically discovers and logs ANY equipment type returned by IntelliCenter
- Works with equipment types not specifically implemented (valves, sensors, remotes, etc.)
- Uses IntelliCenter's OBJTYP field to classify unknown equipment
- No hardcoded equipment assumptions - works with any configuration

**Clean Event Logging:**
- Suppresses verbose operational messages in listen mode
- Shows only meaningful events: initial state, changes, and errors
- Event messages use consistent "EVENT:" prefix for easy filtering
- Temperature changes show both old and new values for context

### Key Features

1. **Rapid Polling**: Defaults to 2-second intervals for quick change detection (vs 60-second default for normal mode)
2. **Multi-Equipment Tracking**: Monitors circuits, pumps, temperatures, thermal equipment, and features simultaneously
3. **Unknown Equipment Handling**: Logs equipment types not specifically implemented, using OBJTYP and basic status fields
4. **Initial State Discovery**: Shows all equipment and current state on startup for complete context
5. **Operational State Visibility**: For thermal equipment, shows heating/cooling/off states, not just on/off

### Implementation Architecture

**State Management:**
```go
// Previous state tracking (main.go)
var (
    prevCircuits = make(map[string]CircuitState)
    prevPumps = make(map[string]float64)
    prevTemps = make(map[string]float64)
    prevThermal = make(map[string]ThermalState)
    prevFeatures = make(map[string]string)
)
```

**Change Detection Pattern:**
```go
// Compare current state to previous state
if prevState, exists := prevMap[equipmentID]; !exists {
    // First time seeing this equipment - log initial state
    log.Printf("EVENT: Equipment detected: %s", details)
    prevMap[equipmentID] = currentState
} else if currentState != prevState {
    // State changed - log the change
    log.Printf("EVENT: Equipment changed: %s → %s", prevState, currentState)
    prevMap[equipmentID] = currentState
}
```

**Unknown Equipment Discovery:**
```go
// For equipment types without specific implementation
for _, obj := range response.Body.ObjectList {
    objType := obj.Params["OBJTYP"].(string)
    if !isKnownType(objType) {
        // Log unknown equipment with available metadata
        log.Printf("EVENT: Unknown equipment detected - %s (%s) type=%s status=%s",
            name, objID, objType, status)
    }
}
```

### Configuration Behavior

**Automatic Interval Adjustment:**
- When `--listen` is specified without `--interval`, polling defaults to 2 seconds
- When `--interval` is specified with `--listen`, uses the specified interval
- Without `--listen`, polling defaults to 60 seconds regardless of interval flag

**Listen Mode Detection:**
```go
// Check for listen mode from flag or environment variable
listenMode := *listenFlag || os.Getenv("PENTAMETER_LISTEN") == "true"

// Adjust default polling interval for listen mode
if listenMode && *intervalFlag == defaultPollInterval {
    *intervalFlag = 2  // Rapid polling for change detection
}
```

### Output Examples

**Initial Discovery:**
```
2025/10/11 14:46:29 Starting pool monitor for IntelliCenter at 192.168.50.118:6680
2025/10/11 14:46:29 Listen mode enabled - logging equipment changes only
2025/10/11 14:46:29 Starting live event monitoring...
2025/10/11 14:46:29 EVENT: Spa temperature detected: 72.0°F
2025/10/11 14:46:29 EVENT: Pool detected: ON
2025/10/11 14:46:29 EVENT: Spa Light detected: OFF
2025/10/11 14:46:29 EVENT: Pool Heat Pump detected: off
```

**Change Detection:**
```
2025/10/11 14:47:04 EVENT: Air temperature changed: 75.0°F → 76.0°F
2025/10/11 14:47:15 EVENT: Spa Light turned ON
2025/10/11 14:47:32 EVENT: Unknown equipment detected - Valve Motor (V0001) type=VALVE status=CLOSED
```

### Use Cases

1. **Debugging Equipment Issues**: Watch real-time state changes when troubleshooting
2. **Understanding IntelliCenter Behavior**: Learn how equipment responds to commands
3. **Discovering Equipment Configuration**: Find all equipment in the system, including types not yet implemented
4. **Testing New Features**: Verify equipment changes are detected correctly
5. **Development**: Understand equipment behavior before implementing specific support

### Integration with Metrics Mode

Listen mode and normal metrics mode are mutually compatible:
- Both can run simultaneously (listen mode doesn't disable metrics collection)
- Metrics continue to be exposed on HTTP port even in listen mode
- Prometheus can scrape metrics while listen mode logs events
- Use listen mode for debugging, normal mode for production monitoring

### Testing Listen Mode

**Quick Test:**
```bash
# Run for 30 seconds and capture output
pentameter --ic-ip 192.168.1.100 --listen --interval 1 2>&1 | tee /tmp/listen_test.log
# Trigger equipment changes (turn lights on/off, adjust temperature setpoints)
# Review captured output for expected EVENT messages
```

**Docker Testing:**
```bash
# Override docker-compose.yml to enable listen mode
docker run --rm -e PENTAMETER_IC_IP=192.168.1.100 -e PENTAMETER_LISTEN=true astrostl/pentameter:latest
```

### Future Enhancements

Potential improvements for listen mode:
- JSON output format for structured logging
- WebSocket endpoint for real-time event streaming to web clients
- Event filtering by equipment type or name
- Configurable event history (show last N changes on startup)
- Integration with external notification systems (webhooks, MQTT, etc.)