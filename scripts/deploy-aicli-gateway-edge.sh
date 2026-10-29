#!/usr/bin/env bash
# Deploy the aicli-gateway (MCP mount) and/or gateway-edge binaries on the
# production host, following docs/ops/gateway-edge/deployment-and-dev-tunnel-
# connection-runbook.md conventions: stage as *.next, verify SHA-256, configtest,
# back up, atomic mv, restart, verify, rollback on failure.
#
# Run ON THE SERVER (as root), after uploading the staged artifacts:
#   scp aicli-gateway aicli-gateway.sha256 root@host:/root/stage/
#   ssh root@host 'bash -s' < deploy-aicli-gateway-edge.sh -- --target aicli
#
# Nothing is replaced unless the checksum matches and the service comes back up.
set -euo pipefail

TARGET=""           # aicli | edge | both
STAGE_DIR=""        # directory holding <binary> and <binary>.sha256
DRY_RUN=0
ROLLBACK=0

EDGE_ROOT="/opt/ai-gateway-edge"
EDGE_SERVICE="ai-gateway-edge.service"
EDGE_HEALTH="https://127.0.0.1:18443/health/ready"
AICLI_ROOT="/opt/aicli-gateway"
AICLI_SERVICE="aicli-gateway.service"
AICLI_HEALTH="http://127.0.0.1:8090/api/health"

usage() {
  cat <<'USAGE'
usage: deploy-aicli-gateway-edge.sh [--target aicli|edge|both] --stage-dir DIR [--dry-run] [--rollback]

  --target      what to deploy (default: both)
  --stage-dir   server-side directory containing <binary> + <binary>.sha256
  --dry-run     verify and report only; never move or restart anything
  --rollback    restore the newest *.before-* backup and restart
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --target)    TARGET="$2"; shift 2 ;;
    --stage-dir) STAGE_DIR="$2"; shift 2 ;;
    --dry-run)   DRY_RUN=1; shift ;;
    --rollback)  ROLLBACK=1; shift ;;
    -h|--help)   usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage; exit 2 ;;
  esac
done
TARGET="${TARGET:-both}"

log() { printf '[deploy] %s\n' "$*"; }
die() { printf '[deploy] ERROR: %s\n' "$*" >&2; exit 1; }

# ---------------------------------------------------------------- helpers ----

verify_sha() { # $1 = staged file
  local file="$1" expected actual
  [[ -f "$file" ]] || die "missing staged artifact: $file"
  [[ -f "$file.sha256" ]] || die "missing checksum file: $file.sha256"
  expected="$(awk '{print $1}' "$file.sha256" | tr 'A-Z' 'a-z')"
  actual="$(sha256sum "$file" | awk '{print $1}')"
  [[ "$expected" == "$actual" ]] || die "sha256 mismatch for $file (expected $expected, got $actual)"
  log "sha256 OK: $(basename "$file") $actual"
}

service_ready() { # $1 = systemd unit, $2 = health url
  local unit="$1" url="$2" attempt code
  systemctl is-active --quiet "$unit" || return 1
  for attempt in 1 2 3 4 5 6 7 8 9 10; do
    code="$(curl -sS -k -o /dev/null -w '%{http_code}' --max-time 5 "$url" 2>/dev/null || echo 000)"
    if [[ "$code" == "200" ]]; then
      log "health OK: $unit -> $url (200)"
      return 0
    fi
    sleep 2
  done
  log "health FAILED: $unit -> $url (last code ${code})"
  return 1
}

deploy_one() { # $1 = staged binary, $2 = install path, $3 = unit, $4 = health url, $5 = configtest (may be empty)
  local staged="$1" install_path="$2" unit="$3" url="$4" configtest="$5"
  local stamp backup next
  [[ -x "$(dirname "$install_path")" ]] || die "bin directory missing: $(dirname "$install_path") (is this service installed at all?)"
  [[ -f "$install_path" ]] || die "current binary missing: $install_path"

  verify_sha "$staged"
  next="${install_path}.next"
  cp "$staged" "$next"
  chmod 0755 "$next"

  if [[ -n "$configtest" ]]; then
    log "configtest: $configtest"
    # shellcheck disable=SC2086
    if ! eval "$configtest"; then
      rm -f "$next"
      die "configtest failed; candidate discarded, current binary untouched"
    fi
  fi

  if [[ "$DRY_RUN" == "1" ]]; then
    log "dry-run: would move $next -> $install_path and restart $unit"
    rm -f "$next"
    return 0
  fi

  stamp="$(date -u +%Y%m%dT%H%M%SZ)"
  backup="$(dirname "$install_path")/$(basename "$install_path").before-$stamp"
  cp -p "$install_path" "$backup"
  log "backup: $backup"

  mv "$next" "$install_path"
  systemctl restart "$unit"
  if ! service_ready "$unit" "$url"; then
    log "rolling back to $backup"
    cp -p "$backup" "$install_path"
    chmod 0755 "$install_path"
    systemctl restart "$unit"
    service_ready "$unit" "$url" || die "rollback did not restore health; manual intervention required"
    die "deployment failed and was rolled back"
  fi
  log "deployed: $install_path ($(sha256sum "$install_path" | awk '{print $1}'))"
}

rollback_one() { # $1 = install path, $2 = unit, $3 = health url
  local install_path="$1" unit="$2" url="$3" backup
  backup="$(ls -1t "${install_path}".before-* 2>/dev/null | head -1 || true)"
  [[ -n "$backup" ]] || die "no backup found next to $install_path"
  log "restoring $backup"
  cp -p "$backup" "$install_path"
  chmod 0755 "$install_path"
  systemctl restart "$unit"
  service_ready "$unit" "$url" || die "restored binary is not healthy"
}

# ------------------------------------------------------------------ main ----

case "$TARGET" in
  aicli|edge|both) ;;
  *) die "--target must be aicli | edge | both" ;;
esac

if [[ "$ROLLBACK" == "1" ]]; then
  [[ -n "$STAGE_DIR" ]] || true
  case "$TARGET" in
    edge|both)  rollback_one "$EDGE_ROOT/bin/gateway-edge" "$EDGE_SERVICE" "$EDGE_HEALTH" ;;
  esac
  case "$TARGET" in
    aicli|both) rollback_one "$AICLI_ROOT/bin/aicli-gateway" "$AICLI_SERVICE" "$AICLI_HEALTH" ;;
  esac
  log "rollback complete"
  exit 0
fi

[[ -n "$STAGE_DIR" ]] || die "--stage-dir is required"
[[ -d "$STAGE_DIR" ]] || die "stage dir does not exist: $STAGE_DIR"

if [[ "$TARGET" == "edge" || "$TARGET" == "both" ]]; then
  deploy_one "$STAGE_DIR/gateway-edge" "$EDGE_ROOT/bin/gateway-edge" "$EDGE_SERVICE" "$EDGE_HEALTH" \
    "set -a; . $EDGE_ROOT/configs/.env; set +a; $STAGE_DIR/gateway-edge configtest -c $EDGE_ROOT/configs/config.yaml"
fi

if [[ "$TARGET" == "aicli" || "$TARGET" == "both" ]]; then
  # The inner gateway runs behind the edge: loopback HTTP only, public URLs come
  # from --public-base-url, and TLS/WebSocket termination is the edge's job.
  deploy_one "$STAGE_DIR/aicli-gateway" "$AICLI_ROOT/bin/aicli-gateway" "$AICLI_SERVICE" "$AICLI_HEALTH" \
    ""
fi

log "done"
