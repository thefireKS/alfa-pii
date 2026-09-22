# Convenience commands for building, running, testing and demonstrating
# pii-service in Docker. Uses only programs that exist on the host: docker,
# docker-compose, curl, jq and go.

SHELL := /bin/sh

# Docker context for the local colima VM. Override with DOCKER_CONTEXT=default
# if you use Docker Desktop.
DOCKER_CONTEXT ?= colima-alfa-hackathon

# Host port the service is published on.
PORT ?= 8080
BASE_URL ?= http://127.0.0.1:$(PORT)

.PHONY: docker-context build run stop logs ps health process demo test vet race clean

docker-context:
	docker context use $(DOCKER_CONTEXT)

build: docker-context
	docker-compose build

run: docker-context
	docker-compose up -d

stop: docker-context
	docker-compose down

logs: docker-context
	docker-compose logs -f

ps: docker-context
	docker-compose ps

# Health checks. The runtime image has no shell or curl, so readiness is
# probed from the host.
health: docker-context
	@echo "== /livez =="
	@curl -fsS $(BASE_URL)/livez && echo
	@echo "== /readyz =="
	@curl -fsS $(BASE_URL)/readyz && echo

# Full /process cycle: mask a new ID, repeat the original, restore the mask,
# and confirm a conflicting text is rejected with 409.
process: docker-context
	@echo "== POST /process (new ID -> mask) =="
	@curl -fsS -X POST $(BASE_URL)/process -H 'Content-Type: application/json' \
		-d '{"payload":"ФИО: Иванов Иван Иванович, телефон +7 900 123-45-67, email ivan@example.ru","payload_id":"proc-1"}'
	@echo
	@echo "== POST /process (repeat original -> same mask) =="
	@curl -fsS -X POST $(BASE_URL)/process -H 'Content-Type: application/json' \
		-d '{"payload":"ФИО: Иванов Иван Иванович, телефон +7 900 123-45-67, email ivan@example.ru","payload_id":"proc-1"}'
	@echo
	@echo "== POST /process (mask -> original) =="
	@MASK=$$(curl -fsS -X POST $(BASE_URL)/process -H 'Content-Type: application/json' \
		-d '{"payload":"ФИО: Иванов Иван Иванович, телефон +7 900 123-45-67, email ivan@example.ru","payload_id":"proc-1"}' | jq -r .result); \
	curl -fsS -X POST $(BASE_URL)/process -H 'Content-Type: application/json' \
		-d "{\"payload\":$$(printf '%s' "$$MASK" | jq -Rs .),\"payload_id\":\"proc-1\"}"
	@echo
	@echo "== POST /process (conflicting text -> 409) =="
	@curl -sS -o /dev/null -w 'HTTP %{http_code}\n' -X POST $(BASE_URL)/process -H 'Content-Type: application/json' \
		-d '{"payload":"совершенно другой текст","payload_id":"proc-1"}'

# Run the demo scenario against the running container's /v1/mask and /v1/restore.
# The demo consumer secret is read from .env (see .env.example).
demo: docker-context
	@set -a; . ./.env; set +a; go run ./cmd/demo-remote -base $(BASE_URL) -secret-env PII_CONSUMER_DEMO_SECRET

test:
	go test ./...

vet:
	go vet ./...

race:
	go test -race ./...

clean: docker-context
	docker-compose down -v --rmi local