.PHONY: run migrate cleanup test vet build

run:
	go run ./cmd/api

migrate:
	go run ./cmd/migrate

cleanup:
	go run ./cmd/admin cleanup

test:
	go test ./...

vet:
	go vet ./...

build:
	go build ./...
