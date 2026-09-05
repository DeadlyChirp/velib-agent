# Raccourcis du quotidien. `make help` liste tout.

.DEFAULT_GOAL := help
.PHONY: help up down logs test test-front test-injections test-charge test-norace test-race-docker test-live test-integration cover fmt vet check reset

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
	# -race exige cgo, donc un compilateur C. Sur une machine sans gcc — Windows
	# le plus souvent — la commande echoue sur un message qui parle de cgo et
	# jamais du compilateur manquant. D'ou la cible test-norace en secours, et
	# test-race-docker qui fait tourner le detecteur dans le conteneur.
	cd api && go test -race -count=1 ./...

test-injections: ## 44 vecteurs d'injection, sans jeton (docker compose up requis)
	python audit/injections.py

test-charge: ## Test de charge, 50 clients simultanes (docker compose up requis)
	python audit/charge.py

test-front: ## Tests du rendu front (Node, sans dependance)
	node web/rendu_test.mjs

test-norace: ## Tests unitaires sans detecteur (machine sans compilateur C)
	cd api && go test -count=1 ./...

test-race-docker: ## Detecteur de competition dans le conteneur, comme la CI
	cd api && docker run --rm -v "/$$(pwd):/src" -w //src golang:1.27-alpine 		sh -c "apk add --no-cache gcc musl-dev >/dev/null && CGO_ENABLED=1 go test -race -count=1 ./..." 

test-live: ## Tests contre la vraie API Vélib (réseau requis)
	cd api && go test -tags=live -count=1 -v ./internal/velib/

test-integration: ## Tests en boîte noire sur la pile (docker compose up requis)
	cd api && go test -tags=integration -count=1 -v ./internal/httpapi/

cover: ## Couverture des tests
	cd api && go test -coverprofile=cover.out ./internal/... && \
		go tool cover -func=cover.out | tail -1

fmt: ## Formate le code
	cd api && gofmt -w .

vet: ## Analyse statique
	cd api && go vet ./...

check: fmt vet test test-front ## Formatage, analyse statique et tests
