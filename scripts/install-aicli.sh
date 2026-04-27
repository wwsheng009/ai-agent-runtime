#!/usr/bin/env bash
# install-aicli.sh - 从 GitHub Release 下载并安装 aicli 到用户可执行目录（Linux / macOS）
#
# 用法:
#   curl -fsSL https://raw.githubusercontent.com/wwsheng009/ai-agent-runtime/main/scripts/install-aicli.sh | bash
#
# 环境变量:
#   AICLI_VERSION     指定版本 tag（如 v0.1.0），默认 latest
#   AICLI_INSTALL_DIR 安装目录，默认 $HOME/.local/bin
#   AICLI_REPO        仓库 owner/name，默认 wwsheng009/ai-agent-runtime

set -euo pipefail

REPO="${AICLI_REPO:-wwsheng009/ai-agent-runtime}"
BIN="aicli"
VERSION="${AICLI_VERSION:-latest}"
INSTALL_DIR="${AICLI_INSTALL_DIR:-$HOME/.local/bin}"

err()  { printf '\033[31m[ERR]\033[0m %s\n' "$*" >&2; exit 1; }
info() { printf '\033[32m[INFO]\033[0m %s\n' "$*"; }
warn() { printf '\033[33m[WARN]\033[0m %s\n' "$*" >&2; }

# ---- 检测 OS / ARCH ----
uname_s="$(uname -s)"
case "$uname_s" in
  Linux)  GOOS=linux  ;;
  Darwin) GOOS=darwin ;;
  *)      err "不支持的操作系统: $uname_s（仅支持 Linux / macOS，Windows 请使用 install-aicli.ps1）" ;;
esac

uname_m="$(uname -m)"
case "$uname_m" in
  x86_64|amd64)   GOARCH=amd64 ;;
  arm64|aarch64)  GOARCH=arm64 ;;
  *) err "不支持的 CPU 架构: $uname_m" ;;
esac

# ---- 必备命令检查 ----
need() { command -v "$1" >/dev/null 2>&1 || err "缺少命令: $1"; }
need curl
need tar

# ---- 解析最新版本号 ----
if [ "$VERSION" = "latest" ]; then
  info "查询最新版本..."
  VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep -oE '"tag_name"[[:space:]]*:[[:space:]]*"[^"]+"' \
    | head -n1 | sed -E 's/.*"([^"]+)"$/\1/')"
  [ -n "$VERSION" ] || err "未能解析最新版本号"
fi
info "目标版本: $VERSION (${GOOS}/${GOARCH})"

ARCHIVE="${BIN}-${VERSION}-${GOOS}-${GOARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/download/${VERSION}/${ARCHIVE}"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

info "下载 $URL"
curl -fsSL -o "$tmp/$ARCHIVE" "$URL" || err "下载失败: $URL"

# ---- 校验 sha256（可选）----
if curl -fsSL -o "$tmp/$ARCHIVE.sha256" "$URL.sha256" 2>/dev/null; then
  if command -v sha256sum >/dev/null 2>&1; then
    (cd "$tmp" && sha256sum -c "$ARCHIVE.sha256") || err "sha256 校验失败"
    info "sha256 校验通过"
  elif command -v shasum >/dev/null 2>&1; then
    expected="$(awk '{print $1}' "$tmp/$ARCHIVE.sha256")"
    actual="$(shasum -a 256 "$tmp/$ARCHIVE" | awk '{print $1}')"
    [ "$expected" = "$actual" ] || err "sha256 校验失败: expect=$expected got=$actual"
    info "sha256 校验通过"
  else
    warn "未找到 sha256sum/shasum，跳过校验"
  fi
else
  warn "未找到 sha256 文件，跳过校验"
fi

# ---- 解压 & 安装 ----
tar -xzf "$tmp/$ARCHIVE" -C "$tmp"
[ -f "$tmp/$BIN" ] || err "归档中未找到 $BIN 二进制"

mkdir -p "$INSTALL_DIR"
install -m 0755 "$tmp/$BIN" "$INSTALL_DIR/$BIN"
info "已安装 $BIN -> $INSTALL_DIR/$BIN"

# ---- PATH 提示 ----
case ":${PATH:-}:" in
  *":$INSTALL_DIR:"*) ;;
  *)
    warn "$INSTALL_DIR 不在 PATH 中。请将以下行加入 ~/.bashrc 或 ~/.zshrc:"
    printf '  export PATH="%s:$PATH"\n' "$INSTALL_DIR"
    ;;
esac

"$INSTALL_DIR/$BIN" version 2>/dev/null || true
