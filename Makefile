# Binary name
BINARY_NAME=livekit-recorder

# Go related variables
GOBASE=$(shell pwd)
GOBIN=$(GOBASE)/bin

# Main package location
MAIN_PACKAGE=cmd/recorder/main.go

# Docker Compose file
DOCKER_COMPOSE_FILE=docker-compose.yml

# Build the project
build:
	@echo "Building..."
	@go build -o $(GOBIN)/$(BINARY_NAME) $(MAIN_PACKAGE)

# Run the project
run:
	@go run $(MAIN_PACKAGE)

# Clean build files
clean:
	@echo "Cleaning..."
	@go clean
	@rm -rf $(GOBIN)

# Install dependencies
deps:
	@echo "Installing dependencies..."
	@go mod tidy

# Run tests
test:
	@echo "Running tests..."
	@go test ./...

# Run the project with live reload
dev:
	@echo "Running in development mode..."
	@which air > /dev/null || go install github.com/cosmtrek/air@latest
	@air

# Build for multiple platforms
build-all:
	@echo "Building for multiple platforms..."
	@GOOS=linux GOARCH=amd64 go build -o $(GOBIN)/$(BINARY_NAME)-linux-amd64 $(MAIN_PACKAGE)
	@GOOS=windows GOARCH=amd64 go build -o $(GOBIN)/$(BINARY_NAME)-windows-amd64.exe $(MAIN_PACKAGE)
	@GOOS=darwin GOARCH=amd64 go build -o $(GOBIN)/$(BINARY_NAME)-darwin-amd64 $(MAIN_PACKAGE)

# Docker Compose commands

# Build Docker image
docker-build:
	@echo "Building Docker image..."
	@docker-compose -f $(DOCKER_COMPOSE_FILE) build

# Start Docker containers
docker-start:
	@echo "Starting Docker containers..."
	@docker-compose -f $(DOCKER_COMPOSE_FILE) up -d

# Stop Docker containers
docker-stop:
	@echo "Stopping Docker containers..."
	@docker-compose -f $(DOCKER_COMPOSE_FILE) down

# Build and start Docker containers
docker-up: docker-build docker-start

# Show Docker container logs
docker-logs:
	@echo "Showing Docker container logs..."
	@docker-compose -f $(DOCKER_COMPOSE_FILE) logs -f

# Remove Docker containers, networks, and volumes
docker-clean:
	@echo "Cleaning up Docker resources..."
	@docker-compose -f $(DOCKER_COMPOSE_FILE) down -v --rmi all --remove-orphans

.PHONY: build run clean deps test dev build-all docker-build docker-start docker-stop docker-up docker-logs docker-clean