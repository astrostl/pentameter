# Include .env file if it exists
-include .env

# Variables
BINARY_NAME=pentameter
DOCKER_IMAGE=pentameter
DOCKER_TAG=latest

# Auto-detect docker command - can be overridden with: make DOCKER_CMD=docker <target>
DOCKER_CMD ?= $(shell command -v nerdctl >/dev/null 2>&1 && echo nerdctl || echo docker)

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
GOMOD=$(GOCMD) mod

.PHONY: all build clean test docker docker-build help

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

# Download dependencies
deps:
	$(GOMOD) download
	$(GOMOD) tidy

# Build Docker image
docker: docker-build

docker-build:
	$(DOCKER_CMD) build --no-cache -t $(DOCKER_IMAGE):$(DOCKER_TAG) .

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

# Show help
help:
	@echo "Available targets:"
	@echo "  build        - Build the Go binary"
	@echo "  build-static - Build static binary for containers"
	@echo "  clean        - Clean build artifacts"
	@echo "  test         - Run tests"
	@echo "  deps         - Download and tidy dependencies"
	@echo "  docker       - Build Docker image"
	@echo "  docker-build - Build Docker image"
	@echo "  compose-up   - Start with docker compose (fallback to direct docker)"
	@echo "  compose-down - Stop docker compose (fallback to direct docker)"
	@echo "  compose-logs - View docker compose logs with tail (fallback to direct docker)"
	@echo "  compose-logs-once - View docker compose logs once (fallback to direct docker)"
	@echo "  help         - Show this help"