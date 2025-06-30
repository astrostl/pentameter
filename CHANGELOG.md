# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] - 2024-12-30

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