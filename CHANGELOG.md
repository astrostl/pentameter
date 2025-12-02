# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.4.1] - 2025-12-01

### Added
- **Circuit Group (Light Show) monitoring in listen mode** - Tracks CIRCGRP objects for synchronized light shows and circuit groups
- Circuit group state tracking including parent group, circuit reference, active state, and color/mode (USE parameter)
- Push notification handling for circuit group changes with `PUSH: CircGrp` prefix
- Poll-based discovery of circuit group members during listen mode

### Documentation
- Added comprehensive Circuit Groups section to API.md with query examples and response formats
- Documented CIRCGRP object type parameters (PARENT, CIRCUIT, ACT, USE, DLY, LISTORD, STATIC)
- Added local build guidelines to CLAUDE.md

### Code Quality
- Added CircGrpState struct for proper circuit group state management
- Added comprehensive tests for circuit group tracking functionality
- Extended equipment state tracking to include CircGrps map

## [0.4.0] - 2025-11-28

### Fixed
- **MAJOR: Push notifications no longer trigger spurious reconnects** - IntelliCenter sends unsolicited push notifications when equipment changes. Previously, these were misinterpreted as invalid responses, causing unnecessary disconnect/reconnect cycles. Now push notifications are properly recognized and processed, resulting in stable long-running connections.

### Changed
- **Listen mode now uses hybrid push + poll architecture** - Real-time push notifications for instant updates, plus periodic polling (default 60s) to catch equipment that doesn't push (like pumps)
- Listen mode output now distinguishes event sources with `PUSH:` and `POLL:` prefixes
- Reports `POLL: [no changes]` when a poll cycle finds no equipment changes
- Initial state fetch shows all equipment with `POLL:` prefix on startup
- Subsequent polls only report changes, not full equipment state
- State resets on reconnection to ensure complete equipment discovery
- Pump RPM changes now detected via polling since IntelliCenter doesn't push pump updates
- Added 5-second minimum poll interval to prevent overloading IntelliCenter

### Removed
- Removed `--debug` flag and `PENTAMETER_DEBUG` environment variable (was not useful)

### Code Quality
- Reduced cyclomatic complexity in push notification processing through function extraction
- Added constants for object types and reconnect delays
- Refactored command-line flag parsing into smaller, testable functions
- Improved struct field alignment for better memory layout
- Fixed unused parameter warnings with underscore convention
- Added comprehensive test coverage for new helper functions (100+ tests passing)

### Documentation
- Updated API.md with push notification structure and hybrid approach recommendation
- Updated CLAUDE.md with hybrid listen mode architecture details
- Updated README.md listen mode section with PUSH/POLL output examples

## [0.3.5] - 2025-11-28

### Added
- **Freeze protection status monitoring** - Circuits and features now expose freeze protection active status (STATUS=2) as a distinct metric value
- Grafana dashboard updated to display freeze protection status with dedicated color (purple) for visual distinction

### Documentation
- Documented freeze protection API parameters (FREEZE field in circuit/feature objects)
- Documented freeze protection active indicator behavior (STATUS=2 when freeze protection activates equipment)

## [0.3.4] - 2025-11-01

### Added
- **Automatic re-discovery of IntelliCenter when IP address changes** - Pentameter now automatically detects when the IntelliCenter's IP changes (DHCP renewal, router reboot) and re-discovers it via mDNS
- Re-discovery mode activates after 3 consecutive connection failures
- Persistent re-discovery attempts on each poll interval until IntelliCenter is found at new IP
- Comprehensive test coverage for re-discovery logic (74.5% overall coverage)

### Fixed
- **Critical bug where pentameter couldn't recover from IntelliCenter IP address changes** - Previously, if IntelliCenter's IP changed, pentameter would be stuck retrying the old stale IP forever
- Connection resilience significantly improved for long-running deployments

### Changed
- Enhanced connection failure tracking with consecutive failure counter
- Polling logic now distinguishes between temporary connection issues and IP address changes
- Re-discovery can be disabled for testing via `disableAutoRediscovery` flag

### Documentation
- Added comprehensive testing strategy documentation for re-discovery logic
- Documented why `attemptRediscovery` function is not unit tested (requires real hardware)
- Updated test comments to explain pragmatic testing approach

### Infrastructure
- Added 7 new tests for re-discovery functionality
- Test suite completes in ~3 seconds with 100% pass rate
- All quality checks pass (formatting, linting, complexity, security)

## [0.3.3] - 2025-10-12

### Fixed
- **Listen mode reliability improvements** - Prevents unnecessary reconnections on messageID mismatches during rapid polling
- Listen mode now continues operation when receiving mismatched message IDs instead of forcing reconnection
- Cleaner listen mode behavior with warning messages instead of connection disruptions

### Changed
- Refactored polling loop into smaller helper functions for improved code maintainability
- Reduced cyclomatic complexity in temperature polling logic
- Enhanced error handling separation between listen mode and normal mode

## [0.3.2] - 2025-10-11

### Fixed
- **mDNS auto-discovery now works in Docker containers** - Explicitly selects network interface instead of relying on default interface selection
- Auto-discovery reliability improved across different network configurations (macOS, Linux, Docker)
- Discovery process shows which network interface is selected for better debugging

### Changed
- **Docker networking now uses host mode for pentameter-app** - Enables mDNS multicast traffic for IntelliCenter auto-discovery
- Prometheus and Grafana services remain on bridge network for isolation
- Eliminates need for manual IP configuration when running pentameter in Docker with host networking

### Documentation
- Added comprehensive network configuration explanation in CLAUDE.md
- Updated README.md with Docker auto-discovery examples and troubleshooting
- Enhanced RELEASE.md to prevent Homebrew formula checksum confusion
- Documented mDNS interface selection behavior and Docker host networking rationale

## [0.3.1] - 2025-10-11

### Changed
- Improved auto-discovery error messaging to guide users on manual IP configuration via `--ic-ip` flag
- Increased auto-discovery timeout from 5 seconds to 60 seconds for better reliability on slower networks
- Enhanced discovery logging with periodic retry messages every 2 seconds to show progress
- Removed debug log statements from production code for cleaner output
- **Increased Prometheus data retention from 30 days to 730 days (2 years) for long-term historical analysis**
- Updated dependencies: prometheus/client_golang and related packages to latest versions

### Fixed
- Auto-discovery now provides clearer guidance when mDNS fails, directing users to manual IP configuration
- Discovery process shows visible progress indicators during network scanning

### Documentation
- Updated Homebrew formula caveats to explain auto-discovery and `--ic-ip` flag usage
- Added VERSION build arg documentation for Docker builds
- Enhanced RELEASE.md with comprehensive troubleshooting section
- Fixed Homebrew formula checksums to match GitHub release assets
- Updated roadmap to focus on core development tasks

## [0.3.0] - 2025-10-11

### Added
- IntelliCenter auto-discovery via mDNS (multicast DNS) - automatically finds `pentair.local` on network
- `--discover` flag to test auto-discovery and show IntelliCenter IP address
- Optional `--ic-ip` flag - uses auto-discovery when not provided
- Listen mode (`--listen` flag) for live equipment change monitoring with rapid polling
- Real-time event logging showing initial equipment state and all changes
- Unknown equipment discovery - automatically detects and logs equipment types not specifically implemented
- Change detection for all equipment types (circuits, pumps, temperatures, thermal equipment, features)
- Configurable rapid polling interval (defaults to 2 seconds in listen mode)
- Clean event-only output mode for monitoring and debugging

### Changed
- Dependency updates: prometheus/client_golang 1.22.0 → 1.23.2
- Dependency additions: golang.org/x/net v0.46.0, golang.org/x/sys v0.37.0
- Refactored test code for improved maintainability using test constants and helper functions
- Streamlined RELEASE.md checklist to focus on release mechanics
- IntelliCenter IP address now optional - defaults to auto-discovery if not specified

### Infrastructure
- Reduced test code by 42 lines through better code reuse patterns
- Test suite maintains 100% pass rate with 82 tests
- Added mDNS discovery capability with network auto-detection

## [0.2.2] - 2025-07-28

### Added
- Listen mode (`--listen` flag) for live equipment change monitoring with rapid polling
- Real-time event logging showing initial equipment state and all changes
- Unknown equipment discovery - automatically detects and logs equipment types not specifically implemented
- Change detection for all equipment types (circuits, pumps, temperatures, thermal equipment, features)
- Configurable rapid polling interval (defaults to 2 seconds in listen mode)
- Clean event-only output mode for monitoring and debugging

### Changed
- Dependency updates: prometheus/client_golang 1.22.0 → 1.23.2
- Refactored test code for improved maintainability using test constants and helper functions
- Streamlined RELEASE.md checklist to focus on release mechanics

### Infrastructure
- Reduced test code by 42 lines through better code reuse patterns
- Test suite maintains 100% pass rate with 82 tests

## [0.2.0] - 2025-07-27

### Added
- Multi-platform Docker image support (AMD64 + ARM64) using manifest-tool
- DockerHub publishing workflow with automated multi-arch builds
- Container namespace isolation with pentameter- prefix for all services
- Git tag-based versioning system for releases
- Comprehensive release workflow in Makefile (`make release`)
- Enhanced CLAUDE.md documentation with multi-platform publishing guide

### Changed
- **BREAKING**: Service names updated to use pentameter- prefix (pentameter-app, pentameter-prometheus, pentameter-grafana)
- **BREAKING**: Container names updated to match service names consistently
- Updated docker-compose.yml to use published DockerHub image instead of local build
- Updated Prometheus and Grafana configurations for new container names
- Fixed Go module path from "pentameter" to "github.com/astrostl/pentameter" for tooling compatibility
- Enhanced Makefile with multi-platform publishing targets using manifest-tool instead of Docker buildx

### Infrastructure
- Established DockerHub repository (astrostl/pentameter) with automated publishing
- Implemented nuclear Docker rebuild strategy to prevent cache issues during development
- Added manifest-tool integration for reliable multi-platform image creation
- Updated all service configurations to support new container naming convention

## [0.1.1] - 2025-07-08

### Added
- Thermal equipment monitoring with separated feature metrics
- IntelliCenter feature visibility control
- Thermal equipment operational status display
- Build step to docker-compose.yml for pentameter service

### Changed
- Fix thermal monitoring to process all heaters, not just referenced ones
- Use IntelliCenter SUBTYP for all equipment metadata instead of hardcoded names
- Set all services to 1-minute refresh cycles for better responsiveness
- Fix temperature metrics to use Fahrenheit instead of Celsius (pool industry standard)

### Fixed
- Thermal equipment now properly processes all configured heaters
- Equipment metadata now uses IntelliCenter's official SUBTYP classification
- Temperature units corrected to Fahrenheit for US pool industry compatibility

## [0.1.0] - 2025-06-30

### Added
- Initial release of Pentameter - IntelliCenter Pool Controller to Prometheus metrics exporter
- WebSocket-based communication with IntelliCenter API
- Prometheus metrics collection for pool data (temperature, chemical levels, equipment status)
- Docker containerization support with docker-compose configuration
- Comprehensive build system with Makefile including quality checks and testing
- Health check endpoints for monitoring
- Configurable polling intervals and connection parameters
- Robust error handling and connection retry logic
- GitHub community files: issue templates, contributing guidelines, code of conduct
- Security policy and funding information
- Dependabot configuration for automated dependency updates
- Development tooling: EditorConfig, golangci-lint configuration, test files
- AI-generated code warnings and proper attribution in README

### Changed
- Enhanced README with comprehensive project scope and usage instructions
- Improved .gitignore with comprehensive exclusions for Go projects

### Infrastructure
- Added GitHub issue templates for bug reports and feature requests
- Configured GitHub Dependabot for Go module updates
- Added GitHub Sponsors funding configuration
- Established code of conduct and contributing guidelines