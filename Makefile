.PHONY: doctor up down logs ps db-shell db-reset fmt test run dev migrate migrate-down migrate-status ml-sync embeddings

COMPOSE := ./scripts/compose.sh

doctor:
	@./scripts/doctor.sh

up:
	@$(COMPOSE) up -d db
	@./scripts/wait-db.sh
	@./scripts/ensure-db-password.sh

down:
	@$(COMPOSE) down

logs:
	@$(COMPOSE) logs -f db

ps:
	@$(COMPOSE) ps

db-shell:
	@$(COMPOSE) exec db sh -c 'exec psql -U "$$POSTGRES_USER" -d "$$POSTGRES_DB"'

db-reset:
	@printf 'Delete the SciGraphDB PostgreSQL data volume? [y/N] '; \
	read answer; \
	if [ "$$answer" = y ] || [ "$$answer" = Y ]; then \
		$(COMPOSE) down -v; \
	else \
		printf 'Cancelled.\n'; \
	fi

fmt:
	@go fmt ./...

test:
	@go test ./...

run:
	@env -u DATABASE_URL go run ./cmd/api

dev: up
	@env -u DATABASE_URL go run ./cmd/migrate up
	@env -u DATABASE_URL go run ./cmd/api

migrate:
	@env -u DATABASE_URL go run ./cmd/migrate up

migrate-down:
	@env -u DATABASE_URL go run ./cmd/migrate down

migrate-status:
	@env -u DATABASE_URL go run ./cmd/migrate status

ML_LIMIT ?= 100
ML_BATCH_SIZE ?= 16

ml-sync:
	@uv sync --locked

embeddings: up migrate ml-sync
	@env -u DATABASE_URL uv run --locked python -m ml.embed --limit $(ML_LIMIT) --batch-size $(ML_BATCH_SIZE)
