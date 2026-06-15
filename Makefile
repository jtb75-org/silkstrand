.PHONY: dev dev-deps build test lint docker run migrate-up migrate-down clean seed seed-mssql seed-mongo jwt hash-password bundle bundle-all bundle-sign publish-bundles collector-mssql collector-mssql-all

# Start local dependencies (Postgres + Redis)
dev-deps:
	docker compose up -d

# Run the API server locally (requires dev-deps)
# Loads api/.env.local if present for Clerk and other config
run:
	cd api && if [ -f .env.local ]; then set -a && . ./.env.local && set +a; fi && go run ./cmd/silkstrand-api/

# Start everything for local development
dev: dev-deps
	@echo "Waiting for Postgres..."
	@until docker compose exec -T postgres pg_isready -U silkstrand > /dev/null 2>&1; do sleep 1; done
	@echo "Dependencies ready. Starting API..."
	cd api && if [ -f .env.local ]; then set -a && . ./.env.local && set +a; fi && go run ./cmd/silkstrand-api/

# Build the API binary
build:
	cd api && CGO_ENABLED=0 go build -ldflags="-s -w" -o ../bin/silkstrand-api ./cmd/silkstrand-api/

# Run tests
test:
	cd api && go test ./... -v -race

# Run linter
lint:
	cd api && golangci-lint run ./...

# Build Docker image
docker:
	docker build -t silkstrand-api:local -f api/Dockerfile api/

# --- Backoffice ---

run-backoffice:
	cd backoffice && go run ./cmd/backoffice-api/

build-backoffice:
	cd backoffice && CGO_ENABLED=0 go build -ldflags="-s -w" -o ../bin/backoffice-api ./cmd/backoffice-api/

test-backoffice:
	cd backoffice && go test ./... -v -race

lint-backoffice:
	cd backoffice && golangci-lint run ./...

# --- Seed & Dev Helpers ---

# Seed local databases with test data (requires docker compose up)
seed:
	bash scripts/seed.sh

# Seed the local SQL Server 2022 container with a read-only scan user.
seed-mssql:
	bash scripts/seed-mssql.sh

# Seed the local MongoDB 8 container with a read-only scan user.
seed-mongo:
	bash scripts/seed-mongo.sh

# Generate a dev JWT token for the default test tenant
jwt:
	python3 scripts/gen-jwt.py

# Generate a bcrypt hash (usage: make hash-password PASS=mysecret)
hash-password:
	cd backoffice && go run ../scripts/hash-password.go $(or $(PASS),admin123)

# --- Infrastructure ---

# Stop local dependencies
down:
	docker compose down

# --- Bundles ---

bundle:
	@scripts/build-bundle.sh $(BUNDLE)

bundle-all:
	@scripts/build-bundle.sh cis-postgresql-16
	@scripts/build-bundle.sh cis-mssql-2022
	@scripts/build-bundle.sh cis-mongodb-8

bundle-sign:
	@scripts/build-bundle.sh $(BUNDLE) --sign $(SIGN_KEY)

# Build, upload (mc → public MinIO), and register the CIS bundles so their
# bundles.gcs_path is set and off-cluster agents can fetch them. Requires
# MINIO_ACCESS_KEY/MINIO_SECRET_KEY + API_URL + JWT (see scripts/publish-bundles.sh).
publish-bundles:
	@scripts/publish-bundles.sh $(BUNDLES)

# --- Collectors ---

GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)

collector-mssql:
	cd collectors/mssql && CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -o ../../dist/mssql-collector-$(GOOS)-$(GOARCH) .

collector-mssql-all:
	cd collectors/mssql && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o ../../dist/mssql-collector-linux-amd64 .
	cd collectors/mssql && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o ../../dist/mssql-collector-linux-arm64 .
	cd collectors/mssql && CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -o ../../dist/mssql-collector-darwin-amd64 .
	cd collectors/mssql && CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o ../../dist/mssql-collector-darwin-arm64 .

# Clean up
clean:
	docker compose down -v
	rm -rf bin/ dist/
