.PHONY: run migrate invite test vet build

run:
	go run ./cmd/api

migrate:
	go run ./cmd/migrate

invite:
	go run ./cmd/admin invite create -uses 20 -ttl 720h

test:
	go test ./...

vet:
	go vet ./...

build:
	go build ./...
