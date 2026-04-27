.PHONY: build test lint tidy clean aicli install-aicli uninstall-aicli

BACKEND_DIR := backend
FRONTEND_DIR := frontend

# ---- aicli build / install ----
BIN_NAME    := aicli
CMD_PATH    := ./cmd/aicli
VERSION     ?= dev
# 在不同平台拿到一个 ISO-8601 的构建时间（GNU date / BusyBox / git bash 都支持 -u）
BUILD_TIME  ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     := -s -w -X main.version=$(VERSION) -X main.buildTime=$(BUILD_TIME)

build:
	cd $(BACKEND_DIR) && go build ./...

# 单独构建 aicli 二进制到仓库根目录（开发者本地使用）
aicli:
	cd $(BACKEND_DIR) && go build -trimpath -ldflags "$(LDFLAGS)" -o ../$(BIN_NAME) $(CMD_PATH)

# 安装 aicli 到 $GOBIN（默认 $(go env GOPATH)/bin）
# 该目录通常已在用户 PATH 中，跨平台一致；可通过 GOBIN=/your/dir make install-aicli 覆盖
install-aicli:
	cd $(BACKEND_DIR) && go install -trimpath -ldflags "$(LDFLAGS)" $(CMD_PATH)
	@echo "Installed $(BIN_NAME) to $$(go env GOBIN 2>/dev/null || echo $$(go env GOPATH)/bin)"

uninstall-aicli:
	@dir=$$(go env GOBIN); [ -z "$$dir" ] && dir=$$(go env GOPATH)/bin; \
	  rm -f "$$dir/$(BIN_NAME)" "$$dir/$(BIN_NAME).exe" && \
	  echo "Removed $(BIN_NAME) from $$dir"

test:
	cd $(BACKEND_DIR) && go test ./...

lint:
	cd $(BACKEND_DIR) && go vet ./...

tidy:
	cd $(BACKEND_DIR) && go mod tidy

clean:
	cd $(BACKEND_DIR) && go clean ./...
	rm -f $(BIN_NAME) $(BIN_NAME).exe

frontend-install:
	cd $(FRONTEND_DIR) && pnpm install

frontend-dev:
	cd $(FRONTEND_DIR) && pnpm dev

frontend-build:
	cd $(FRONTEND_DIR) && pnpm build
