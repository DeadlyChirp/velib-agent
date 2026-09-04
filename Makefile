# Raccourcis du quotidien. `make help` liste tout.

.DEFAULT_GOAL := help
.PHONY: help up down logs test test-live cover fmt vet check reset

help: ## Affiche cette aide
	@grep -E '^[a-z-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

up: ## Démarre la pile complète
	docker compose up --build

down: ## Arrête la pile en gardant les données
	docker compose down

reset: ## Arrête la pile ET efface les conversations
	docker compose down -v

logs: ## Suit les journaux de l'API
	docker compose logs -f api

test: ## Tests unitaires, avec détecteur de compétition
	cd api && go test -race -count=1 ./...

test-live: ## Tests contre la vraie API Vélib (réseau requis)
	cd api && go test -tags=live -count=1 -v ./internal/velib/

cover: ## Couverture des tests
	cd api && go test -coverprofile=cover.out ./internal/... && \
		go tool cover -func=cover.out | tail -1

fmt: ## Formate le code
	cd api && gofmt -w .

vet: ## Analyse statique
	cd api && go vet ./...

check: fmt vet test ## Formatage, analyse statique et tests
