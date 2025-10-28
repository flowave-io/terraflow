# Makefile for Terraflow CLI
test:
	go test ./...
build:
	go build -o bin/terraflow ./cmd/terraflow
run:
	go run ./cmd/terraflow
lint:
	golangci-lint run
fmt:
	go fmt ./...
vet:
	go vet ./...
