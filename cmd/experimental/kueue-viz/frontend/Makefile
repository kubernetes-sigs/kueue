# Makefile for Frontend App

# Default target: build the app
.PHONY: all
all: install build

# Build target: runs the production build
.PHONY: build
build:
	npm run build

# Install target: retrieves npm dependencies
.PHONY: install
install:
	npm install

# Run target: serves the built app
.PHONY: run
run:
	npx react-inject-env set && npx http-server build

# Debug target: runs the app in development mode
.PHONY: debug
debug:
	npm start

# Clean target: cleans the build directory
.PHONY: clean
clean:
	rm -rf build


