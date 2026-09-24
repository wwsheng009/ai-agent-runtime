.PHONY: build test lint tidy clean aicli aicli-console aicli-mesh install-aicli uninstall-aicli package-server contract contract-check

BACKEND_DIR := backend
FRONTEND_DIR := frontend

# ---- aicli build / install ----
BIN_NAME    := aicli
CMD_PATH    := ./cmd/aicli
CONSOLE_BIN_NAME := aicli-console
CONSOLE_CMD_PATH := ./cmd/aicli-console
MESH_BIN_NAME := aicli-mesh
MESH_CMD_PATH := ./cmd/aicli-mesh
VERSION     ?= $(shell cat VERSION 2>/dev/null || echo dev)
# 在不同平台拿到一个 ISO-8601 的构建时间（GNU date / BusyBox / git bash 都支持 -u）
BUILD_TIME  ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     := -s -w -X main.version=$(VERSION) -X main.buildTime=$(BUILD_TIME)

build:
	cd $(BACKEND_DIR) && go build ./...

# 单独构建 aicli 二进制到仓库根目录（开发者本地使用）
aicli:
	cd $(BACKEND_DIR) && go build -trimpath -ldflags "$(LDFLAGS)" -o ../$(BIN_NAME) $(CMD_PATH)

# 构建 Windows 原生 Console 启动器。它在 MobaXterm/mintty 等 pipe/PTY
# 环境中通过 CREATE_NEW_CONSOLE 启动同目录的 aicli.exe。
aicli-console:
	cd $(BACKEND_DIR) && go build -trimpath -ldflags "-s -w" -o ../$(CONSOLE_BIN_NAME) $(CONSOLE_CMD_PATH)

# 构建网格运维 CLI（aicli-mesh：ls/show/url/gc/doctor/version）。
# 只注入版本号（与 scripts/build.ps1 的 main-version 口径一致）。
aicli-mesh:
	cd $(BACKEND_DIR) && go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o ../$(MESH_BIN_NAME) $(MESH_CMD_PATH)

# 安装 aicli 到 $GOBIN（默认 $(go env GOPATH)/bin）
# 该目录通常已在用户 PATH 中，跨平台一致；可通过 GOBIN=/your/dir make install-aicli 覆盖
install-aicli:
	cd $(BACKEND_DIR) && go install -trimpath -ldflags "$(LDFLAGS)" $(CMD_PATH)
	cd $(BACKEND_DIR) && go install -trimpath -ldflags "-s -w" $(CONSOLE_CMD_PATH)
	@echo "Installed $(BIN_NAME) to $$(go env GOBIN 2>/dev/null || echo $$(go env GOPATH)/bin)"

uninstall-aicli:
	@dir=$$(go env GOBIN); [ -z "$$dir" ] && dir=$$(go env GOPATH)/bin; \
	  rm -f "$$dir/$(BIN_NAME)" "$$dir/$(BIN_NAME).exe" \
	    "$$dir/$(CONSOLE_BIN_NAME)" "$$dir/$(CONSOLE_BIN_NAME).exe" && \
	  echo "Removed $(BIN_NAME) from $$dir"

# 打包 runtime-server（含内嵌前端）到 dist/；版本号复用 VERSION 文件（v0.4.x）
package-server:
	pwsh scripts/package-runtime-server.ps1 -Version $(VERSION) -OutputDir dist

test:
	cd $(BACKEND_DIR) && go test ./...

lint:
	cd $(BACKEND_DIR) && go vet ./...

# 由后端事件契约注册表（backend/internal/events/contract.go）生成前端类型
# frontend/src/types/runtime/event-contract.ts。只允许改注册表后重新生成，勿手改生成物。
contract:
	cd $(BACKEND_DIR) && go run ./cmd/contractgen

# 校验生成物与注册表一致（CI 用；不一致时退出码 1）。注意 make test 也会跑到
# cmd/contractgen 的 TestGeneratedFileIsUpToDate，二者等价。
contract-check:
	cd $(BACKEND_DIR) && go run ./cmd/contractgen -check

tidy:
	cd $(BACKEND_DIR) && go mod tidy

clean:
	cd $(BACKEND_DIR) && go clean ./...
	rm -f $(BIN_NAME) $(BIN_NAME).exe $(CONSOLE_BIN_NAME) $(CONSOLE_BIN_NAME).exe

frontend-install:
	cd $(FRONTEND_DIR) && pnpm install

frontend-dev:
	cd $(FRONTEND_DIR) && pnpm dev

frontend-build:
	cd $(FRONTEND_DIR) && pnpm build
