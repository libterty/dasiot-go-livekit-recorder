# Binary name
BINARY_NAME=livekit-recorder

# Go related variables
GOBASE=$(shell pwd)
GOBIN=$(GOBASE)/bin

# Main package location
MAIN_PACKAGE=cmd/recorder/main.go

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

.PHONY: build run clean deps test dev build-all