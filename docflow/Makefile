.PHONY: build run test vet tidy docker

build:
	go build -trimpath -o bin/docflow ./cmd/server

run:
	go run ./cmd/server

test:
	go test ./...

vet:
	go vet ./...

tidy:
	go mod tidy

docker:
	docker compose up -d --build
