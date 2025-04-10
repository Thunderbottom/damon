.PHONY: build clean test lint run docker docker-run help

# Build variables
BINARY_NAME=damon
VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME=$(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS=-ldflags "-X main.buildString=${VERSION} -s -w"

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOMOD=$(GOCMD) mod
GOLINT=golangci-lint

# Default target
.DEFAULT_GOAL := help

# Help target for documentation
help:
	@echo "Damon - Nomad Event Operator"
	@echo ""
	@echo "Usage:"
	@echo "  make build           Build the binary"
	@echo "  make clean           Clean build artifacts"
	@echo "  make test            Run unit tests"
	@echo "  make lint            Run linter"
	@echo "  make run             Run the application locally"
	@echo "  make docker          Build Docker image"
	@echo "  make docker-run      Run Docker container"
	@echo "  make tidy            Tidy and verify dependencies"
	@echo "  make integration     Run integration tests"
	@echo "  make help            Show this help message"

# Build the application
build:
	$(GOBUILD) $(LDFLAGS) -o $(BINARY_NAME) ./cmd

# Clean build artifacts
clean:
	$(GOCLEAN)
	rm -f $(BINARY_NAME)

# Run tests with coverage
test:
	$(GOTEST) -v -race -coverprofile=coverage.out ./...
	$(GOCMD) tool cover -html=coverage.out -o coverage.html

# Run integration tests
integration:
	DAMON_INTEGRATION_TEST=true $(GOTEST) -v -tags=integration ./test/integration

# Run linter
lint:
	$(GOLINT) run

# Run the application locally
run:
	$(GOBUILD) $(LDFLAGS) -o $(BINARY_NAME) ./cmd
	./$(BINARY_NAME)

# Tidy and verify go modules
tidy:
	$(GOMOD) tidy
	$(GOMOD) verify

# Build Docker image
docker:
	docker build -t $(BINARY_NAME):$(VERSION) -f Dockerfile .

# Run Docker container
docker-run:
	docker run --rm -it \
		-v $(PWD)/config.toml:/app/config.toml \
		-e NOMAD_ADDR=http://host.docker.internal:4646 \
		-e NOMAD_TOKEN \
		$(BINARY_NAME):$(VERSION)
