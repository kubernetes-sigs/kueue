# Makefile for kueue-viz-go

APP_NAME := kueue-viz
MODULE_NAME := kueue-viz

.PHONY: all build run test tidy clean help

# Default target
all: build

## Build the Go application
build:
	@echo "Building the application..."
	go build -o bin/$(APP_NAME)

## Run the application
run: build
	@echo "Running the application..."
	GIN_MODE=release ./bin/$(APP_NAME)

## Run debug
debug: build
	@echo "Running in debug mode with hot code replacement enabled..."
	CompileDaemon --build="make" --command=./bin/$(APP_NAME)

## Run tests
test:
	@echo "Running tests..."
	go test ./... -v

## Tidy up dependencies
tidy:
	@echo "Tidying up module dependencies..."
	go mod tidy

## Clean up build artifacts
clean:
	@echo "Cleaning up..."
	rm -f $(APP_NAME)

## Display help
help:
	@echo "Usage: make [target]"
	@echo
	@echo "Targets:"
	@awk '/^[a-zA-Z\-]+:/ {print $$1}' $(MAKEFILE_LIST) | sed 's/:$$//' | xargs -n1 -I{} echo "  {}"



