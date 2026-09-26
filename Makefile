.PHONY: doctor up down logs ps db-shell db-reset fmt test run migrate migrate-down migrate-status

COMPOSE := ./scripts/compose.sh

doctor:
	@./scripts/doctor.sh

up:
	@$(COMPOSE) up -d db
	@./scripts/wait-db.sh

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
	@go run ./cmd/api

migrate:
	@go run ./cmd/migrate up

migrate-down:
	@go run ./cmd/migrate down

migrate-status:
	@go run ./cmd/migrate status
