.PHONY: build clean build-arm build-host dist fmt deps lint test

BINARY_NAME=alarm-service
BUILD_DIR=bin
VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
VERSION_FLAGS=-X main.version=$(VERSION)
LDFLAGS=-ldflags "-w -s -extldflags '-static' $(VERSION_FLAGS)"
CMD_DIR=cmd/alarm-service

build:
	mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) ./$(CMD_DIR)

clean:
	rm -rf $(BUILD_DIR)

build-arm: build

lint:
	golangci-lint run

test:
	go test -v ./...

run:
	go run ./$(CMD_DIR)

dev-build:
	mkdir -p $(BUILD_DIR)
	go build -o $(BUILD_DIR)/$(BINARY_NAME) ./$(CMD_DIR)

build-native:
	mkdir -p $(BUILD_DIR)
	go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) ./$(CMD_DIR)

build-host:
	mkdir -p $(BUILD_DIR)
	go build -ldflags "$(VERSION_FLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME) ./$(CMD_DIR)

dist: build

fmt:
	go fmt ./...

deps:
	go mod download && go mod tidy