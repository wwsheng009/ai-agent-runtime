#!/usr/bin/env bash
set -euo pipefail

VERSION="${1:?ripgrep version is required}"
GOOS="${2:?target OS is required}"
GOARCH="${3:?target architecture is required}"
OUTPUT_DIR="${4:?output directory is required}"

case "${GOOS}/${GOARCH}" in
  linux/amd64)  TARGET="x86_64-unknown-linux-musl" ;;
  linux/arm64)  TARGET="aarch64-unknown-linux-musl" ;;
  darwin/amd64) TARGET="x86_64-apple-darwin" ;;
  darwin/arm64) TARGET="aarch64-apple-darwin" ;;
  windows/amd64) TARGET="x86_64-pc-windows-msvc" ;;
  windows/arm64) TARGET="aarch64-pc-windows-msvc" ;;
  *)
    echo "Unsupported ripgrep target: ${GOOS}/${GOARCH}" >&2
    exit 1
    ;;
esac

EXTENSION="tar.gz"
RG_NAME="rg"
if [[ "${GOOS}" == "windows" ]]; then
  EXTENSION="zip"
  RG_NAME="rg.exe"
fi

ASSET_BASE="ripgrep-${VERSION}-${TARGET}"
ASSET="${ASSET_BASE}.${EXTENSION}"
BASE_URL="https://github.com/BurntSushi/ripgrep/releases/download/${VERSION}"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

curl --fail --silent --show-error --location \
  --output "${TMP_DIR}/${ASSET}" "${BASE_URL}/${ASSET}"
curl --fail --silent --show-error --location \
  --output "${TMP_DIR}/${ASSET}.sha256" "${BASE_URL}/${ASSET}.sha256"

EXPECTED_SHA256="$(grep -Eo '[0-9A-Fa-f]{64}' "${TMP_DIR}/${ASSET}.sha256" | head -n 1 | tr 'A-F' 'a-f')"
ACTUAL_SHA256="$(sha256sum "${TMP_DIR}/${ASSET}" | awk '{print $1}')"
if [[ -z "${EXPECTED_SHA256}" || "${EXPECTED_SHA256}" != "${ACTUAL_SHA256}" ]]; then
  echo "ripgrep checksum mismatch: expected=${EXPECTED_SHA256:-missing} actual=${ACTUAL_SHA256}" >&2
  exit 1
fi

if [[ "${EXTENSION}" == "zip" ]]; then
  if command -v unzip >/dev/null 2>&1; then
    unzip -q "${TMP_DIR}/${ASSET}" -d "${TMP_DIR}"
  elif command -v python3 >/dev/null 2>&1; then
    python3 -m zipfile -e "${TMP_DIR}/${ASSET}" "${TMP_DIR}"
  else
    echo "Extracting Windows ripgrep requires unzip or python3" >&2
    exit 1
  fi
else
  tar -xzf "${TMP_DIR}/${ASSET}" -C "${TMP_DIR}"
fi

SOURCE="${TMP_DIR}/${ASSET_BASE}/${RG_NAME}"
if [[ ! -f "${SOURCE}" ]]; then
  echo "ripgrep archive did not contain ${ASSET_BASE}/${RG_NAME}" >&2
  exit 1
fi

mkdir -p "${OUTPUT_DIR}"
install -m 0755 "${SOURCE}" "${OUTPUT_DIR}/${RG_NAME}"
echo "Bundled ${ASSET} -> ${OUTPUT_DIR}/${RG_NAME}"
