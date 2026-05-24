.PHONY: run build test lint docker-up docker-down tf-plan tf-apply deploy

run:
	go run ./cmd/api

build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/ledger-query ./cmd/api

test:
	go test ./... -race -count=1 -timeout 60s

lint:
	golangci-lint run ./...

docker-up:
	docker compose up --build -d

docker-down:
	docker compose down -v

TF_ENV    ?= prod
TF_DIR     = terraform/envs/$(TF_ENV)
IMAGE_TAG ?= $(shell git rev-parse --short HEAD)

tf-plan:
	terraform -chdir=$(TF_DIR) plan -var="image_tag=$(IMAGE_TAG)"

tf-apply:
	terraform -chdir=$(TF_DIR) apply -var="image_tag=$(IMAGE_TAG)" -auto-approve

deploy:
	bash scripts/deploy.sh --env $(TF_ENV) --tag $(IMAGE_TAG)

deploy-dry:
	bash scripts/deploy.sh --env $(TF_ENV) --tag $(IMAGE_TAG) --plan-only
