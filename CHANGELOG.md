# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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