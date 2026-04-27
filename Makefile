.PHONY: run build test test-integration lint generate migrate migrate-down swagger docker-up docker-down jaeger metrics

run:
	go run ./cmd/api

build:
	go build -o bin/blueprint ./cmd/api

test:
	go test ./... -race -count=1

test-integration:
	go test ./... -tags=integration -race -count=1

lint:
	golangci-lint run ./...

generate:
	sqlc generate
	swag init -g cmd/api/main.go -o docs/

migrate:
	goose -dir migrations postgres "$(DATABASE_URL)" up

migrate-down:
	goose -dir migrations postgres "$(DATABASE_URL)" down

swagger:
	swag init -g cmd/api/main.go -o docs/

docker-up:
	docker compose up -d

docker-down:
	docker compose down

jaeger:
	open http://localhost:16686

metrics:
	curl -s http://localhost:9091/metrics | grep go_blueprint
