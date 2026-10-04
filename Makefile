.PHONY: fmt run-api run-worker run-scheduler migrate-up web-dev

fmt:
	gofmt -w cmd internal

run-api:
	go run ./cmd/server

run-worker:
	go run ./cmd/worker

run-scheduler:
	go run ./cmd/scheduler

migrate-up:
	@echo "Use: psql \"$$POSTGRES_DSN\" -f migrations/001_init.sql && psql \"$$POSTGRES_DSN\" -f migrations/002_seed.sql"

web-dev:
	cd web && npm run dev
