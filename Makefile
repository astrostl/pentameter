# Include .env file if it exists
-include .env

# Variables
BINARY_NAME=pentameter
DOCKER_IMAGE=pentameter
DOCKER_TAG=latest
VERSION ?= $(shell git describe --tags --always --dirty)
LATEST_TAG ?= $(shell git describe --tags --abbrev=0 2>/dev/null || echo "v0.1.0")

# Auto-detect docker command - can be overridden with: make DOCKER_CMD=docker <target>
DOCKER_CMD ?= $(shell command -v nerdctl >/dev/null 2>&1 && echo nerdctl || echo docker)

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
GOMOD=$(GOCMD) mod

.PHONY: all build build-static clean deps test test-race bench docker-build docker-build-stack docker-flush lint lint-enhanced fmt check-fmt gofumpt check-gofumpt cyclo staticcheck vet ineffassign misspell govulncheck modcheck gocritic gosec betteralign fieldalignment goleak go-licenses modverify depcount depoutdated dev help quality quality-strict quality-enhanced quality-comprehensive compose-up compose-down compose-logs compose-logs-once docker-tag docker-push docker-release release

# Default target
all: build

# Build the binary
build:
	$(GOBUILD) -o $(BINARY_NAME) -v .

# Build static binary for containers
build-static:
	CGO_ENABLED=0 GOOS=linux $(GOBUILD) -a -installsuffix cgo -o $(BINARY_NAME) .

# Clean build artifacts
clean:
	$(GOCLEAN)
	rm -f $(BINARY_NAME)

# Run tests
test:
	$(GOTEST) -v ./...

# Run tests with race detection
test-race:
	$(GOTEST) -race -v ./...

# Run benchmarks
bench:
	$(GOTEST) -bench=. -v ./...

# Format code
fmt:
	$(GOCMD) fmt ./...

# Check if code is formatted
check-fmt:
	@test -z "$(shell gofmt -l .)" || (echo "Code is not formatted. Run 'make fmt' to fix." && exit 1)

# Format code with gofumpt (stricter than gofmt)
gofumpt:
	@command -v gofumpt >/dev/null 2>&1 || { \
		echo "Installing gofumpt..."; \
		go install mvdan.cc/gofumpt@latest; \
	}
	export PATH=$$PATH:$$(go env GOPATH)/bin && gofumpt -l -w .

# Check if code is formatted with gofumpt
check-gofumpt:
	@command -v gofumpt >/dev/null 2>&1 || { \
		echo "Installing gofumpt..."; \
		go install mvdan.cc/gofumpt@latest; \
	}
	@export PATH=$$PATH:$$(go env GOPATH)/bin && test -z "$$(gofumpt -l .)" || (echo "Code is not formatted with gofumpt. Run 'make gofumpt' to fix." && exit 1)

# Run linter
lint:
	@command -v golangci-lint >/dev/null 2>&1 || { \
		echo "Installing golangci-lint..."; \
		go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.54.2; \
	}
	export PATH=$$PATH:$$(go env GOPATH)/bin && golangci-lint run

# Run enhanced linter with comprehensive analysis
lint-enhanced:
	@command -v golangci-lint >/dev/null 2>&1 || { \
		echo "Installing golangci-lint..."; \
		go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.54.2; \
	}
	export PATH=$$PATH:$$(go env GOPATH)/bin && golangci-lint run \
		--enable=asasalint,asciicheck,bidichk,bodyclose,containedctx,contextcheck,cyclop,decorder,dogsled,dupl,durationcheck,errcheck,errchkjson,errname,errorlint,exhaustive,copyloopvar,forbidigo,forcetypeassert,funlen,ginkgolinter,gocheckcompilerdirectives,gochecknoinits,gocognit,goconst,gocritic,gocyclo,godot,godox,gofmt,gofumpt,goheader,goimports,mnd,gomoddirectives,gomodguard,goprintffuncname,gosec,gosimple,gosmopolitan,govet,grouper,ineffassign,interfacebloat,lll,loggercheck,maintidx,makezero,misspell,nakedret,nestif,nilerr,nilnil,noctx,nolintlint,nonamedreturns,nosprintfhostport,prealloc,predeclared,promlinter,reassign,revive,rowserrcheck,sqlclosecheck,staticcheck,stylecheck,tagalign,usetesting,testableexamples,testpackage,thelper,tparallel,typecheck,unconvert,unparam,unused,usestdlibvars,varnamelen,wastedassign,whitespace,wrapcheck,zerologlint

# Check cyclomatic complexity
cyclo:
	@command -v gocyclo >/dev/null 2>&1 || { \
		echo "Installing gocyclo..."; \
		go install github.com/fzipp/gocyclo/cmd/gocyclo@latest; \
	}
	export PATH=$$PATH:$$(go env GOPATH)/bin && gocyclo -over 15 .

# Run standalone staticcheck
staticcheck:
	@command -v staticcheck >/dev/null 2>&1 || { \
		echo "Installing staticcheck..."; \
		go install honnef.co/go/tools/cmd/staticcheck@latest; \
	}
	export PATH=$$PATH:$$(go env GOPATH)/bin && staticcheck ./...

# Run go vet
vet:
	$(GOCMD) vet ./...

# Check for ineffectual assignments
ineffassign:
	@command -v ineffassign >/dev/null 2>&1 || { \
		echo "Installing ineffassign..."; \
		go install github.com/gordonklaus/ineffassign@latest; \
	}
	export PATH=$$PATH:$$(go env GOPATH)/bin && ineffassign ./...

# Check for misspellings
misspell:
	@command -v misspell >/dev/null 2>&1 || { \
		echo "Installing misspell..."; \
		go install github.com/client9/misspell/cmd/misspell@latest; \
	}
	export PATH=$$PATH:$$(go env GOPATH)/bin && misspell -error .

# Check for security vulnerabilities
govulncheck:
	@command -v govulncheck >/dev/null 2>&1 || { \
		echo "Installing govulncheck..."; \
		go install golang.org/x/vuln/cmd/govulncheck@latest; \
	}
	export PATH=$$PATH:$$(go env GOPATH)/bin && govulncheck ./...

# Check module dependencies are tidy
modcheck:
	@echo "Checking if go.mod is tidy..."
	@$(GOCMD) mod tidy
	@git diff --exit-code go.mod go.sum || { \
		echo "⚠️  go.mod or go.sum needs to be updated. Run 'go mod tidy' and commit the changes."; \
		exit 1; \
	}

# Verify module dependencies
modverify:
	@echo "Verifying module dependencies..."
	@$(GOCMD) mod verify

# Check dependency count
depcount:
	@echo "Checking dependency count..."
	@DIRECT_COUNT=$$($(GOCMD) list -m -f '{{if not .Indirect}}{{.Path}}{{end}}' all | grep -v "^$$($(GOCMD) list -m)$$" | wc -l | tr -d ' '); \
	TOTAL_COUNT=$$($(GOCMD) mod graph | grep -v "^$$($(GOCMD) list -m)" | wc -l | tr -d ' '); \
	echo "Direct dependencies: $$DIRECT_COUNT"; \
	echo "Total dependencies (including transitive): $$TOTAL_COUNT"; \
	if [ $$DIRECT_COUNT -gt 10 ]; then \
		echo "⚠️  High direct dependency count ($$DIRECT_COUNT > 10). Consider dependency cleanup."; \
	else \
		echo "✓ Direct dependency count is reasonable ($$DIRECT_COUNT ≤ 10)"; \
	fi

# Check for outdated dependencies
depoutdated:
	@command -v go-mod-outdated >/dev/null 2>&1 || { \
		echo "Installing go-mod-outdated..."; \
		go install github.com/psampaz/go-mod-outdated@latest; \
	}
	@echo "Checking for outdated dependencies..."
	@export PATH=$$PATH:$$(go env GOPATH)/bin && $(GOCMD) list -u -m -json all | go-mod-outdated -update -direct || { \
		echo "⚠️  Some dependencies have updates available. Run 'go get -u' to update."; \
	}

# Run go-critic for additional static analysis
gocritic:
	@command -v gocritic >/dev/null 2>&1 || { \
		echo "Installing go-critic..."; \
		go install github.com/go-critic/go-critic/cmd/gocritic@latest; \
	}
	export PATH=$$PATH:$$(go env GOPATH)/bin && gocritic check ./...

# Run gosec for security analysis
gosec:
	@command -v gosec >/dev/null 2>&1 || { \
		echo "Installing gosec..."; \
		go install github.com/securego/gosec/v2/cmd/gosec@latest; \
	}
	export PATH=$$PATH:$$(go env GOPATH)/bin && gosec ./...

# Run betteralign for struct field alignment optimization
betteralign:
	@command -v betteralign >/dev/null 2>&1 || { \
		echo "Installing betteralign..."; \
		go install github.com/dkorunic/betteralign/cmd/betteralign@latest; \
	}
	export PATH=$$PATH:$$(go env GOPATH)/bin && betteralign -apply ./...

# Run fieldalignment for memory layout optimization
fieldalignment:
	@command -v fieldalignment >/dev/null 2>&1 || { \
		echo "Installing fieldalignment..."; \
		go install golang.org/x/tools/go/analysis/passes/fieldalignment/cmd/fieldalignment@latest; \
	}
	export PATH=$$PATH:$$(go env GOPATH)/bin && fieldalignment -fix ./...

# Check for goroutine leaks
goleak:
	@echo "Running goleak check..."
	@echo "Note: goleak is integrated into tests via go.uber.org/goleak import"
	@$(GOTEST) -v ./... -run="Test.*" || echo "⚠️  Tests with potential goroutine leaks detected (see above)"

# Check license compliance
go-licenses:
	@command -v go-licenses >/dev/null 2>&1 || { \
		echo "Installing go-licenses..."; \
		go install github.com/google/go-licenses@latest; \
	}
	@echo "Checking license compliance..."
	@export PATH=$$PATH:$$(go env GOPATH)/bin && go-licenses check . || echo "⚠️  License compliance issues detected (see above)"

# Development workflow (build + quality checks)
dev: build quality

# Run all quality checks
quality: check-fmt test
	@echo "Running go vet..."
	@$(MAKE) vet || echo "⚠️  Go vet found issues (see above)"
	@echo "Running linter..."
	@$(MAKE) lint || echo "⚠️  Linter found issues (see above)"
	@echo "Running complexity check..."
	@$(MAKE) cyclo || echo "⚠️  High complexity functions found (see above)"
	@echo "Running ineffectual assignment check..."
	@$(MAKE) ineffassign || echo "⚠️  Ineffectual assignments found (see above)"
	@echo "Running misspelling check..."
	@$(MAKE) misspell || echo "⚠️  Misspellings found (see above)"
	@echo "Running vulnerability check..."
	@$(MAKE) govulncheck || echo "⚠️  Security vulnerabilities found (see above)"
	@echo "Running go-critic check..."
	@$(MAKE) gocritic || echo "⚠️  Go-critic found issues (see above)"
	@echo "Running security analysis..."
	@$(MAKE) gosec || echo "⚠️  Security issues found (see above)"
	@echo "Running module dependency check..."
	@$(MAKE) modcheck || echo "⚠️  Module dependencies need updating (see above)"
	@echo "Running module verification..."
	@$(MAKE) modverify || echo "⚠️  Module verification failed (see above)"
	@echo "Running dependency count check..."
	@$(MAKE) depcount || echo "⚠️  Dependency count check failed (see above)"
	@echo "Running outdated dependency check..."
	@$(MAKE) depoutdated || echo "⚠️  Outdated dependency check failed (see above)"
	@echo "Running goroutine leak check..."
	@$(MAKE) goleak || echo "⚠️  Goroutine leak check failed (see above)"
	@echo "Running license compliance check..."
	@$(MAKE) go-licenses || echo "⚠️  License compliance check failed (see above)"
	@echo "✓ Core quality checks completed!"

# Run quality checks with strict enforcement
quality-strict: check-fmt vet lint cyclo ineffassign misspell govulncheck gocritic gosec betteralign fieldalignment goleak go-licenses modcheck modverify depcount depoutdated test
	@echo "✓ All quality checks passed with strict enforcement!"

# Run enhanced quality checks (includes race detection, benchmarks, and standalone staticcheck)
quality-enhanced: check-gofumpt vet lint cyclo ineffassign misspell govulncheck gocritic gosec betteralign fieldalignment goleak go-licenses modcheck modverify depcount depoutdated staticcheck test test-race bench
	@echo "✓ All enhanced quality checks passed!"

# Run comprehensive quality checks with maximum linter coverage
quality-comprehensive: check-gofumpt vet lint-enhanced cyclo ineffassign misspell govulncheck gocritic gosec betteralign fieldalignment goleak go-licenses modcheck modverify depcount depoutdated staticcheck test test-race bench
	@echo "✓ All comprehensive quality checks passed!"

# Download dependencies
deps:
	$(GOMOD) download
	$(GOMOD) tidy

# Build Docker image (always nuclear - no cache)
docker-build:
	@echo "🚀 Building pentameter with aggressive cache clearing..."
	@$(DOCKER_CMD) compose stop pentameter 2>/dev/null || true
	@$(DOCKER_CMD) system prune -f
	@$(DOCKER_CMD) compose build --no-cache pentameter
	@$(DOCKER_CMD) compose start pentameter
	@echo "✓ Pentameter built and started"
	@echo "Verifying changes took effect:"
	@sleep 2
	@curl -s http://localhost:8080/metrics | head -5 || echo "⚠️  Metrics endpoint not ready yet"

# Full stack rebuild (if needed)
docker-build-stack:
	@echo "🚀 Full stack build with complete cache clearing..."
	@$(DOCKER_CMD) compose down
	@$(DOCKER_CMD) system prune -f
	@$(DOCKER_CMD) compose build --no-cache
	@$(DOCKER_CMD) compose up -d
	@echo "✓ Full stack built and started"

# Flush Prometheus and Grafana databases
docker-flush:
	@echo "🗑️  Flushing Prometheus and Grafana databases..."
	@$(DOCKER_CMD) compose down
	@$(DOCKER_CMD) volume rm pentameter_prometheus-data pentameter_grafana-data 2>/dev/null || true
	@$(DOCKER_CMD) compose up -d
	@echo "✓ Databases flushed and stack restarted"

# Docker Compose shortcuts (fallback to direct docker commands if compose fails)
compose-up:
	@$(DOCKER_CMD) compose up -d 2>/dev/null || { \
		$(DOCKER_CMD) stop $(BINARY_NAME) || true; \
		$(DOCKER_CMD) rm $(BINARY_NAME) || true; \
		$(DOCKER_CMD) run -d \
			--name $(BINARY_NAME) \
			-p 8080:8080 \
			-e PENTAMETER_IC_IP=$(PENTAMETER_IC_IP) \
			$(DOCKER_IMAGE):$(DOCKER_TAG); \
	}

compose-down:
	@$(DOCKER_CMD) compose down 2>/dev/null || { \
		$(DOCKER_CMD) stop $(BINARY_NAME) || true; \
		$(DOCKER_CMD) rm $(BINARY_NAME) || true; \
	}

compose-logs:
	@$(DOCKER_CMD) compose logs -f 2>/dev/null || $(DOCKER_CMD) logs -f $(BINARY_NAME)

compose-logs-once:
	@$(DOCKER_CMD) compose logs || $(DOCKER_CMD) logs $(BINARY_NAME)

# Docker publishing targets
docker-tag:
	docker tag pentameter:latest astrostl/pentameter:latest
	docker tag pentameter:latest astrostl/pentameter:$(VERSION)

docker-push: docker-tag
	@echo "🚀 Setting up multi-platform builder..."
	docker buildx create --use --name multiarch 2>/dev/null || docker buildx use multiarch
	@echo "🏗️  Building and pushing multi-platform images..."
	docker buildx build --platform linux/amd64,linux/arm64 -t astrostl/pentameter:latest -t astrostl/pentameter:$(VERSION) --push .

docker-release: docker-build docker-push
	@echo "Released astrostl/pentameter:$(VERSION) and astrostl/pentameter:latest"

# Release workflow
release: quality-strict docker-release
	@echo "Release $(VERSION) complete"
	@echo "Docker images:"
	@echo "  astrostl/pentameter:latest"
	@echo "  astrostl/pentameter:$(VERSION)"

# Show help
help:
	@echo "Available targets:"
	@echo ""
	@echo "Build & Clean:"
	@echo "  build        - Build the Go binary"
	@echo "  build-static - Build static binary for containers"
	@echo "  clean        - Clean build artifacts"
	@echo "  deps         - Download and tidy dependencies"
	@echo ""
	@echo "Testing:"
	@echo "  test         - Run tests"
	@echo "  test-race    - Run tests with race detection"
	@echo "  bench        - Run benchmarks"
	@echo ""
	@echo "Quality Suites:"
	@echo "  dev          - Build and run quality checks (build + quality)"
	@echo "  quality      - Core checks with warnings (CI-friendly)"
	@echo "  quality-strict - All checks must pass (release builds)"
	@echo "  quality-enhanced - Includes race detection, benchmarks, and staticcheck"
	@echo "  quality-comprehensive - Maximum linter coverage with enhanced analysis"
	@echo ""
	@echo "Individual Quality Tools:"
	@echo "  fmt          - Format code with gofmt"
	@echo "  check-fmt    - Check if code is properly formatted"
	@echo "  gofumpt      - Format code with gofumpt (stricter than gofmt)"
	@echo "  check-gofumpt - Check if code is formatted with gofumpt"
	@echo "  lint         - Run golangci-lint"
	@echo "  lint-enhanced - Run enhanced linter with comprehensive analysis"
	@echo "  cyclo        - Check cyclomatic complexity with gocyclo"
	@echo "  vet          - Run go vet for suspicious constructs"
	@echo "  ineffassign  - Check for ineffectual assignments"
	@echo "  misspell     - Check for common spelling mistakes"
	@echo "  govulncheck  - Check for security vulnerabilities"
	@echo "  gocritic     - Run go-critic for additional static analysis patterns"
	@echo "  gosec        - Run gosec for security-focused analysis"
	@echo "  betteralign  - Optimize struct field alignment for better memory layout"
	@echo "  fieldalignment - Check and fix struct field memory layout optimization"
	@echo "  goleak       - Check for goroutine leaks in tests"
	@echo "  go-licenses  - Check license compliance of dependencies"
	@echo "  staticcheck  - Run standalone staticcheck for additional static analysis"
	@echo ""
	@echo "Dependency Management:"
	@echo "  modcheck     - Check if module dependencies are tidy"
	@echo "  modverify    - Verify module dependencies haven't been tampered with"
	@echo "  depcount     - Check dependency count and provide recommendations"
	@echo "  depoutdated  - Check for outdated dependencies and suggest updates"
	@echo ""
	@echo "Docker:"
	@echo "  docker-build - Build pentameter with aggressive cache clearing (nuclear by default)"
	@echo "  docker-build-stack - Build full stack with complete cache clearing"
	@echo "  docker-flush - Flush Prometheus and Grafana databases (stop, delete data, start)"
	@echo "  compose-up   - Start with docker compose (fallback to direct docker)"
	@echo "  compose-down - Stop docker compose (fallback to direct docker)"
	@echo "  compose-logs - View docker compose logs with tail (fallback to direct docker)"
	@echo "  compose-logs-once - View docker compose logs once (fallback to direct docker)"
	@echo ""
	@echo "Publishing:"
	@echo "  docker-tag   - Tag images for DockerHub publishing"
	@echo "  docker-push  - Push multi-platform images (linux/amd64,linux/arm64) to DockerHub"
	@echo "  docker-release - Build and push multi-platform images to DockerHub"
	@echo "  release      - Full release workflow (quality-strict + docker-release)"
	@echo ""
	@echo "  help         - Show this help"