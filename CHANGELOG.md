# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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