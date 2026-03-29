.PHONY: build test lint tidy clean

BACKEND_DIR := backend
FRONTEND_DIR := frontend

build:
	cd $(BACKEND_DIR) && go build ./...

test:
	cd $(BACKEND_DIR) && go test ./...

lint:
	cd $(BACKEND_DIR) && go vet ./...
	tidy:
	cd $(BACKEND_DIR) && go mod tidy

clean:
	cd $(BACKEND_DIR) && go clean ./...

frontend-install:
	cd $(FRONTEND_DIR) && pnpm install

frontend-dev:
	cd $(FRONTEND_DIR) && pnpm dev

frontend-build:
	cd $(FRONTEND_DIR) && pnpm build
