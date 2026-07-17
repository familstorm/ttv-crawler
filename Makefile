.PHONY: fmt test build db-up db-down migrate seed run status retry-failed

fmt:
	gofmt -w $$(find . -name '*.go' -type f)

test:
	go test ./...

build:
	go build -o bin/ttv-crawler ./cmd/ttv-crawler

db-up:
	docker compose up -d postgres

db-down:
	docker compose down

migrate:
	go run ./cmd/ttv-crawler migrate

seed:
	go run ./cmd/ttv-crawler seed

run:
	go run ./cmd/ttv-crawler run

status:
	go run ./cmd/ttv-crawler status

retry-failed:
	go run ./cmd/ttv-crawler retry-failed
