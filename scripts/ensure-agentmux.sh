#!/usr/bin/env bash
# ensure-agentmux.sh — install, upgrade and start a local AgentMux daemon.
#
# This is the shell equivalent of `python -m agentmux_sdk.bootstrap` for
# consumers without a Python toolchain. It is published as a release asset:
#
#   curl -fsSL https://github.com/wangning19940904/AgentMux/releases/latest/download/ensure-agentmux.sh | bash
#
# Behavior (merged from the historical homebook/rookie-trade scripts):
#   1. Probe GET /api/v1/capabilities (fallback: /api/v1/status).
#      Healthy + version >= pin  -> keep it, exit 0.
#   2. macOS: reuse /Applications/AgentMux.app when present.
#   3. Refuse to touch a port occupied by an unknown/unhealthy process.
#   4. Download the pinned (or latest) GitHub release, verify with the
#      GoReleaser checksums.txt (no separate release manifest).
#   5. Extract to <root>/releases/<version>, switch <root>/current atomically,
#      roll back on a failed health check.
#   6. local mode: write config.toml, `amux database setup`, spawn
#      `amux client --web`. production mode: refresh /etc/agentmux and
#      restart the agentmux.service systemd unit (requires root).
#
# Configuration (flags override environment):
#   --mode local|production        AGENTMUX_MODE           (default local)
#   --version vX.Y.Z               AGENTMUX_TARGET_VERSION (default: latest)
#   --base-url URL                 AGENTMUX_BASE_URL       (default http://127.0.0.1:8765)
#   --install-root PATH            AGENTMUX_INSTALL_ROOT   (default ./.agentmux | /opt/agentmux)
#   --repository owner/repo        AGENTMUX_REPOSITORY
#                                  AGENTMUX_BRIDGE_TOKEN, AGENTMUX_DATABASE_URL,
#                                  AGENTMUX_AUTO_INSTALL=0, AGENTMUX_SERVICE_USER
set -euo pipefail

MODE="${AGENTMUX_MODE:-local}"
TARGET_VERSION="${AGENTMUX_TARGET_VERSION:-}"
BASE_URL="${AGENTMUX_BASE_URL:-http://127.0.0.1:8765}"
REPOSITORY="${AGENTMUX_REPOSITORY:-wangning19940904/AgentMux}"
INSTALL_ROOT="${AGENTMUX_INSTALL_ROOT:-}"
BRIDGE_TOKEN="${AGENTMUX_BRIDGE_TOKEN:-}"
DATABASE_URL="${AGENTMUX_DATABASE_URL:-postgresql:///agentmux?host=/tmp&sslmode=disable}"
AUTO_INSTALL="${AGENTMUX_AUTO_INSTALL:-1}"
SERVICE_USER="${AGENTMUX_SERVICE_USER:-}"
WAIT_SECONDS="${AGENTMUX_WAIT_SECONDS:-30}"
SYSTEMD_UNIT="agentmux.service"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --mode) MODE="$2"; shift 2 ;;
    --version) TARGET_VERSION="$2"; shift 2 ;;
    --base-url) BASE_URL="$2"; shift 2 ;;
    --install-root) INSTALL_ROOT="$2"; shift 2 ;;
    --repository) REPOSITORY="$2"; shift 2 ;;
    -h|--help) grep '^#' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "ensure-agentmux: unknown flag $1" >&2; exit 2 ;;
  esac
done

BASE_URL="${BASE_URL%/}"
if [[ -z "$INSTALL_ROOT" ]]; then
  if [[ "$MODE" == "production" ]]; then INSTALL_ROOT="/opt/agentmux"; else INSTALL_ROOT="$PWD/.agentmux"; fi
fi

log() { echo "ensure-agentmux: $*"; }
fail() { echo "ensure-agentmux: error: $*" >&2; exit 1; }

auth_args=()
[[ -n "$BRIDGE_TOKEN" ]] && auth_args=(-H "Authorization: Bearer $BRIDGE_TOKEN")

# probe -> sets PROBE_STATE (ready|unauthorized|down) and PROBE_VERSION
probe() {
  PROBE_STATE="down"; PROBE_VERSION=""
  local http_code body
  body="$(mktemp)"
  http_code=$(curl -sS -o "$body" -w '%{http_code}' --max-time 4 "${auth_args[@]}" \
    "$BASE_URL/api/v1/capabilities" 2>/dev/null || echo 000)
  if [[ "$http_code" == 404 ]]; then
    http_code=$(curl -sS -o "$body" -w '%{http_code}' --max-time 4 "${auth_args[@]}" \
      "$BASE_URL/api/v1/status" 2>/dev/null || echo 000)
  fi
  case "$http_code" in
    200)
      if grep -q '"ok"[[:space:]]*:[[:space:]]*true' "$body"; then
        PROBE_STATE="ready"
        PROBE_VERSION=$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$body" | head -1)
      fi ;;
    401) PROBE_STATE="unauthorized" ;;
  esac
  rm -f "$body"
}

# version_lte A B — true when A <= B (semantic, ignores leading v)
version_lte() {
  [[ "$(printf '%s\n%s\n' "${1#v}" "${2#v}" | sort -V | head -1)" == "${1#v}" ]]
}

port_of_base_url() {
  local hostport="${BASE_URL#*://}"; hostport="${hostport%%/*}"
  if [[ "$hostport" == *:* ]]; then echo "${hostport##*:}"; else echo 8765; fi
}

port_listening() {
  local host="${BASE_URL#*://}"; host="${host%%/*}"; host="${host%%:*}"
  (exec 3<>"/dev/tcp/$host/$(port_of_base_url)") 2>/dev/null && { exec 3>&-; return 0; }
  return 1
}

wait_healthy() {
  local want="$1" i
  for ((i = 0; i < WAIT_SECONDS; i++)); do
    sleep 1
    probe
    if [[ "$PROBE_STATE" == "ready" ]]; then
      [[ -z "$want" || "${PROBE_VERSION#v}" == "${want#v}" ]] && return 0
    fi
  done
  return 1
}

resolve_target_version() {
  if [[ -n "$TARGET_VERSION" ]]; then
    [[ "$TARGET_VERSION" == v* ]] || TARGET_VERSION="v$TARGET_VERSION"
    return
  fi
  TARGET_VERSION=$(curl -fsSL --max-time 8 \
    -H 'Accept: application/vnd.github+json' -H 'User-Agent: ensure-agentmux' \
    "https://api.github.com/repos/$REPOSITORY/releases/latest" |
    sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)
  [[ -n "$TARGET_VERSION" ]] || fail "could not resolve the latest release of $REPOSITORY"
  log "no pinned version given; using latest release $TARGET_VERSION"
}

archive_name() {
  local os arch
  case "$(uname -s)" in
    Darwin) os=darwin ;;
    Linux) os=linux ;;
    *) fail "unsupported OS $(uname -s)" ;;
  esac
  case "$(uname -m)" in
    x86_64|amd64) arch=amd64 ;;
    arm64|aarch64) arch=arm64 ;;
    *) fail "unsupported architecture $(uname -m)" ;;
  esac
  echo "agentmux_${os}_${arch}.tar.gz"
}

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}';
  else shasum -a 256 "$1" | awk '{print $1}'; fi
}

download_and_extract() {
  local asset url tmp checksum expected release_dir
  asset="$(archive_name)"
  url="https://github.com/$REPOSITORY/releases/download/$TARGET_VERSION"
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN
  log "downloading $url/$asset"
  curl -fSL --retry 3 -o "$tmp/$asset" "$url/$asset"
  curl -fsSL --retry 3 -o "$tmp/checksums.txt" "$url/checksums.txt"
  expected=$(awk -v name="$asset" '$2 == name || $2 == "*"name {print $1}' "$tmp/checksums.txt")
  [[ -n "$expected" ]] || fail "checksums.txt has no entry for $asset"
  checksum="$(sha256_of "$tmp/$asset")"
  [[ "$checksum" == "$expected" ]] || fail "checksum mismatch for $asset: got $checksum want $expected"
  release_dir="$INSTALL_ROOT/releases/$TARGET_VERSION"
  mkdir -p "$release_dir"
  tar -xzf "$tmp/$asset" -C "$release_dir"
  [[ -x "$release_dir/amux" ]] || chmod +x "$release_dir/amux" 2>/dev/null || fail "release archive has no amux binary"
}

switch_current() {
  PREVIOUS_TARGET=""
  if [[ -L "$INSTALL_ROOT/current" ]]; then
    PREVIOUS_TARGET="$(readlink "$INSTALL_ROOT/current")"
  fi
  ln -sfn "$INSTALL_ROOT/releases/$TARGET_VERSION" "$INSTALL_ROOT/current.next"
  mv -f "$INSTALL_ROOT/current.next" "$INSTALL_ROOT/current" 2>/dev/null ||
    { rm -f "$INSTALL_ROOT/current"; ln -sfn "$INSTALL_ROOT/releases/$TARGET_VERSION" "$INSTALL_ROOT/current"; }
}

rollback_current() {
  [[ -n "${PREVIOUS_TARGET:-}" ]] || return 0
  log "rolling back to $PREVIOUS_TARGET"
  ln -sfn "$PREVIOUS_TARGET" "$INSTALL_ROOT/current"
  [[ "$MODE" == "production" ]] && systemctl restart "$SYSTEMD_UNIT" || true
}

write_config() {
  local config_path="$1" hostport addr
  hostport="${BASE_URL#*://}"; addr="${hostport%%/*}"
  [[ "$addr" == *:* ]] || addr="$addr:8765"
  {
    echo '[server]'
    echo "addr = \"$addr\""
    echo
    echo '[database]'
    echo "url = \"$DATABASE_URL\""
    echo
    echo '[bridge]'
    if [[ -n "$BRIDGE_TOKEN" ]]; then
      echo 'enabled = true'
      echo "token = \"$BRIDGE_TOKEN\""
    else
      echo 'enabled = false'
    fi
  } >"$config_path"
}

start_local() {
  local binary="$INSTALL_ROOT/current/amux" config="$INSTALL_ROOT/config/config.toml"
  mkdir -p "$INSTALL_ROOT/config" "$INSTALL_ROOT/data"
  write_config "$config"
  "$binary" --config "$config" database setup
  nohup "$binary" --config "$config" client --web >>"$INSTALL_ROOT/agentmux.log" 2>&1 &
  echo $! >"$INSTALL_ROOT/agentmux.pid"
  log "spawned amux client --web (pid $(cat "$INSTALL_ROOT/agentmux.pid"))"
}

start_production() {
  [[ "$(id -u)" == 0 ]] || fail "production mode must run as root"
  mkdir -p /etc/agentmux
  [[ -f /etc/agentmux/config.toml ]] || write_config /etc/agentmux/config.toml
  {
    echo "AGENTMUX_DATABASE_URL=$DATABASE_URL"
    echo "AGENTMUX_BRIDGE_TOKEN=$BRIDGE_TOKEN"
  } >/etc/agentmux/agentmux.env
  chmod 640 /etc/agentmux/agentmux.env
  [[ -n "$SERVICE_USER" ]] && chown -R "$SERVICE_USER:$SERVICE_USER" "$INSTALL_ROOT"
  systemctl daemon-reload
  systemctl enable --now "$SYSTEMD_UNIT"
  systemctl restart "$SYSTEMD_UNIT"
}

main() {
  resolve_target_version
  probe
  case "$PROBE_STATE" in
    unauthorized)
      fail "AgentMux answered but rejected the bridge token; fix AGENTMUX_BRIDGE_TOKEN" ;;
    ready)
      if version_lte "$TARGET_VERSION" "${PROBE_VERSION:-v0.0.0}"; then
        log "AgentMux ${PROBE_VERSION:-unknown} is already healthy at $BASE_URL"
        exit 0
      fi
      if [[ "$MODE" != "production" ]]; then
        log "preserving healthy AgentMux $PROBE_VERSION; pinned baseline is $TARGET_VERSION"
        exit 0
      fi
      log "upgrading AgentMux $PROBE_VERSION -> $TARGET_VERSION" ;;
    down)
      if [[ "$(uname -s)" == "Darwin" && -d "/Applications/AgentMux.app" ]]; then
        log "starting existing /Applications/AgentMux.app"
        open -a AgentMux
        wait_healthy "" && { log "reused the AgentMux desktop installation"; exit 0; }
        fail "AgentMux.app exists but did not become healthy; refusing a second installation"
      fi
      if port_listening; then
        fail "port $(port_of_base_url) is occupied by an unknown or unhealthy process"
      fi
      if [[ "$AUTO_INSTALL" != 1 && "$AUTO_INSTALL" != true ]]; then
        log "AgentMux is missing and auto-install is disabled"
        [[ "$MODE" == "production" ]] && exit 1 || exit 0
      fi ;;
  esac

  mkdir -p "$INSTALL_ROOT"
  download_and_extract
  switch_current
  if ! {
    if [[ "$MODE" == "production" ]]; then start_production; else start_local; fi &&
      wait_healthy "$TARGET_VERSION"
  }; then
    rollback_current
    fail "AgentMux installation completed but the service is not healthy"
  fi
  log "AgentMux $TARGET_VERSION installed and healthy at $BASE_URL"
}

main
