#!/bin/sh

set -eu

repository="wangning19940904/AgentMux"
requested_version=${1:-${AMUX_VERSION:-latest}}
install_dir=${AMUX_INSTALL_DIR:-}
download_root=${AMUX_DOWNLOAD_ROOT:-"https://github.com/${repository}/releases"}
temp_dir=""

log() {
  printf '%s\n' "agentmux-installer: $*"
}

fail() {
  printf '%s\n' "agentmux-installer: error: $*" >&2
  exit 1
}

cleanup() {
  if [ -n "$temp_dir" ] && [ -d "$temp_dir" ]; then
    rm -rf -- "$temp_dir"
  fi
}

trap cleanup EXIT HUP INT TERM

command -v curl >/dev/null 2>&1 || fail "curl is required"
command -v tar >/dev/null 2>&1 || fail "tar is required"
command -v install >/dev/null 2>&1 || fail "the install command is required"

case "$requested_version" in
  latest)
    release_base="${download_root}/latest/download"
    ;;
  *[!0-9A-Za-z._+-]*)
    fail "invalid version: $requested_version"
    ;;
  v[0-9]*)
    release_base="${download_root}/download/${requested_version}"
    ;;
  [0-9]*)
    requested_version="v${requested_version}"
    release_base="${download_root}/download/${requested_version}"
    ;;
  *)
    fail "version must be 'latest' or a semantic version such as v0.1.0"
    ;;
esac

case "$(uname -s)" in
  Darwin) operating_system="darwin" ;;
  Linux) operating_system="linux" ;;
  *) fail "unsupported operating system: $(uname -s)" ;;
esac

case "$(uname -m)" in
  x86_64 | amd64) architecture="amd64" ;;
  arm64 | aarch64) architecture="arm64" ;;
  *) fail "unsupported architecture: $(uname -m)" ;;
esac

if [ -z "$install_dir" ]; then
  [ -n "${HOME:-}" ] || fail "HOME is not set; set AMUX_INSTALL_DIR explicitly"
  install_dir="${HOME}/.local/bin"
fi

case "$install_dir" in
  "" | /) fail "refusing unsafe install directory: $install_dir" ;;
esac

archive_name="agentmux_${operating_system}_${architecture}.tar.gz"
temp_dir=$(mktemp -d "${TMPDIR:-/tmp}/agentmux-install.XXXXXX")
archive_path="${temp_dir}/${archive_name}"
checksums_path="${temp_dir}/checksums.txt"
unpack_dir="${temp_dir}/unpack"

log "downloading ${archive_name} (${requested_version})"
curl -fL --retry 3 --retry-delay 1 -o "$archive_path" "${release_base}/${archive_name}"
curl -fL --retry 3 --retry-delay 1 -o "$checksums_path" "${release_base}/checksums.txt"

expected_checksum=$(awk -v name="$archive_name" '$2 == name { print $1; exit }' "$checksums_path")
[ -n "$expected_checksum" ] || fail "${archive_name} is missing from checksums.txt"

if command -v sha256sum >/dev/null 2>&1; then
  actual_checksum=$(sha256sum "$archive_path" | awk '{ print $1 }')
elif command -v shasum >/dev/null 2>&1; then
  actual_checksum=$(shasum -a 256 "$archive_path" | awk '{ print $1 }')
else
  fail "sha256sum or shasum is required to verify the download"
fi

[ "$actual_checksum" = "$expected_checksum" ] || fail "checksum verification failed"

mkdir -p "$unpack_dir"
tar -xzf "$archive_path" -C "$unpack_dir"
[ -f "${unpack_dir}/amux" ] || fail "release archive does not contain amux"
[ -f "${unpack_dir}/agentmux-hook" ] || fail "release archive does not contain agentmux-hook"

install -d "$install_dir"
install -m 0755 "${unpack_dir}/amux" "${install_dir}/amux"
install -m 0755 "${unpack_dir}/agentmux-hook" "${install_dir}/agentmux-hook"

alias_path="${install_dir}/agentmux"
if [ ! -e "$alias_path" ] && [ ! -L "$alias_path" ]; then
  ln -s amux "$alias_path"
elif [ -L "$alias_path" ] && [ "$(readlink "$alias_path")" = "amux" ]; then
  :
else
  log "preserving existing ${alias_path}; use 'amux' to run AgentMux"
fi

installed_version=$("${install_dir}/amux" version 2>&1 || true)
if [ -n "$installed_version" ]; then
  log "installed ${installed_version} in ${install_dir}"
else
  log "installed AgentMux in ${install_dir}"
fi

case ":${PATH}:" in
  *:"${install_dir}":*) ;;
  *)
    log "add ${install_dir} to PATH before running amux"
    ;;
esac
