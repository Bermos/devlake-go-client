DEVLAKE_VERSION ?= $(shell cat spec/DEVLAKE_VERSION)

.PHONY: all spec normalize generate build test test-integration devlake-up devlake-down lint tidy

all: generate build test

## spec: regenerate spec/swagger.json from DevLake (DEVLAKE_VERSION=<git ref>)
spec:
	scripts/generate-spec.sh $(DEVLAKE_VERSION)

## normalize: rebuild spec/openapi.json from spec/swagger.json + spec/overrides.json
normalize:
	cd tools && go run ./specprep -in ../spec/swagger.json -out ../spec/openapi.json \
		-overrides ../spec/overrides.json -version $(DEVLAKE_VERSION)

## generate: regenerate the low-level client from spec/openapi.json
generate: normalize
	cd tools && go tool oapi-codegen -config ../devlakeapi/oapi-codegen.yaml \
		-o ../devlakeapi/devlakeapi.gen.go ../spec/openapi.json

build:
	go build ./...

test:
	go test -race ./...

## test-integration: run against a live DevLake (DEVLAKE_URL, default http://localhost:8080)
test-integration:
	DEVLAKE_VERSION=$(DEVLAKE_VERSION) go test -race -tags integration -count=1 ./integration/...

## devlake-up: start DevLake + PostgreSQL for the integration tests
devlake-up:
	DEVLAKE_VERSION=$(DEVLAKE_VERSION) docker compose -f integration/docker-compose.yml up -d --wait

## devlake-down: stop the integration stack and delete its data
devlake-down:
	DEVLAKE_VERSION=$(DEVLAKE_VERSION) docker compose -f integration/docker-compose.yml down -v

lint:
	go vet ./...
	golangci-lint run ./...

tidy:
	go mod tidy
	cd tools && go mod tidy
