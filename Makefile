.PHONY: dev test build build-all build-panel lint clean

# Development
dev:                           ## Run in development mode
	go run ./cmd/prizm-cli run --task "hello world" --project dev --agent lumi

# Testing
test:                          ## Run all tests
	go test ./... -count=1 -race

test-coverage:                 ## Run tests with coverage report
	go test ./... -coverprofile=coverage.out
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

test-short:                    ## Run tests without race detector (faster)
	go test ./... -count=1 -short

# Building
build:                         ## Build binary for current platform
	CGO_ENABLED=0 go build -o prizm ./cmd/prizm-cli/

build-all:                     ## Cross-compile for all platforms
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o prizm-linux-amd64 ./cmd/prizm-cli/
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -o prizm-darwin-arm64 ./cmd/prizm-cli/
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o prizm-windows-amd64.exe ./cmd/prizm-cli/
	@echo "Built: prizm-linux-amd64, prizm-darwin-arm64, prizm-windows-amd64.exe"

build-panel:                   ## Build the desktop pet panel (own module; needs cgo + a C compiler)
	cd cmd/prizm-panel && CGO_ENABLED=1 go build -o ../../prizm-panel .
	@echo "Built: prizm-panel (run alongside \`prizm serve\`)"

# Linting
lint:                          ## Run linters
	go vet ./...
	@echo "vet: OK"

# CI: the same gates GitHub Actions runs, for local pre-push checks
ci: lint                       ## Run the full CI gate (vet, build, test)
	go build ./...
	go test ./... -count=1
	@echo "ci: OK"

# Docker
docker-build:                  ## Build Docker image
	docker build -t prizm:latest .

docker-run:                    ## Run in Docker
	docker-compose up -d

docker-stop:                   ## Stop Docker containers
	docker-compose down

# Clean
clean:                         ## Remove build artifacts
	rm -f prizm prizm-linux-amd64 prizm-darwin-arm64 prizm-windows-amd64.exe prizm-panel prizm-panel.exe coverage.out coverage.html
	go clean ./...