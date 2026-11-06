.PHONY: build test run fmt tidy docker-up docker-down

build:
	go build ./...

test:
	go test ./...

run:
	go run ./cmd/server

fmt:
	gofmt -w ./cmd ./internal

tidy:
	go mod tidy

docker-up:
	docker compose -f deploy/docker-compose.yml up -d

docker-down:
	docker compose -f deploy/docker-compose.yml down
