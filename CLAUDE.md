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