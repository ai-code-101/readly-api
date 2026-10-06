include $(wildcard .env)
export

.PHONY: run seed test db-up fmt vet

run:            ## run the API against DATABASE_URL
	go run ./cmd/api

seed:           ## create the starter categories
	go run ./cmd/seed

db-up:          ## start only Postgres via docker compose
	docker compose up -d db

test:           ## unit tests + integration tests (needs TEST_DATABASE_URL)
	go test ./...

fmt:
	gofmt -w .

vet:
	go vet ./...
