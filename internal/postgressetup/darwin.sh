set -euo pipefail
export PATH="${PATH:-}:/usr/bin:/bin:/usr/sbin:/sbin"
export PGCONNECT_TIMEOUT=3
# Do not inherit a different database/user/password from the caller's libpq env.
unset PGHOST PGPORT PGDATABASE PGUSER PGPASSWORD PGSERVICE PGSERVICEFILE PGOPTIONS
# The optional first argument selects the port of an existing cluster; a
# Homebrew PostgreSQL is not always on the default port.
port=${1:-5432}
case "$port" in ''|*[!0-9]*) echo "Invalid PostgreSQL port." >&2; exit 1;; esac
[ "$port" -ge 1 ] && [ "$port" -le 65535 ] || { echo "Invalid PostgreSQL port." >&2; exit 1; }

find_brew() {
  command -v brew 2>/dev/null && return
  for candidate in /opt/homebrew/bin/brew /usr/local/bin/brew; do
    if [ -x "$candidate" ]; then printf '%s\n' "$candidate"; return; fi
  done
  return 1
}

if ! brew_bin=$(find_brew); then
  echo "Homebrew is missing; installing it from the official Homebrew installer."
  installer=$(mktemp "${TMPDIR:-/tmp}/agentmux-homebrew.XXXXXX")
  trap 'rm -f "$installer"' EXIT
  curl --fail --location --retry 3 --connect-timeout 15 --max-time 120 \
    https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh -o "$installer"
  if ! NONINTERACTIVE=1 /bin/bash "$installer"; then
    echo "Homebrew installation failed. If administrator access is required, install Homebrew in Terminal, then run amux database setup again." >&2
    exit 1
  fi
  brew_bin=$(find_brew) || { echo "Homebrew was not found after installation." >&2; exit 1; }
fi
export PATH="$(dirname "$brew_bin"):$PATH"
# Prefer the original AgentMux default before considering another installed
# major version, so a stopped PostgreSQL 16 cluster is not silently replaced.
formula=""
for candidate in postgresql@16 postgresql@17 postgresql@18; do
  if "$brew_bin" list --versions "$candidate" >/dev/null 2>&1; then
    formula="$candidate"
    break
  fi
done
if [ -z "$formula" ]; then
  formula=postgresql@16
  echo "Installing PostgreSQL 16…"
  "$brew_bin" install "$formula"
fi
pg_prefix=$("$brew_bin" --prefix "$formula")
export PATH="$pg_prefix/bin:$PATH"

if ! pg_isready -q -h /tmp -p "$port"; then
  echo "Starting PostgreSQL…"
  "$brew_bin" services start "$formula"
fi
for ((attempt=0; attempt<60; attempt++)); do
  if pg_isready -q -h /tmp -p "$port"; then break; fi
  if [ "$attempt" -eq 59 ]; then echo "PostgreSQL did not become ready on /tmp:$port. Check brew services list." >&2; exit 1; fi
  sleep 0.5
done
version=$(psql -X -w -h /tmp -p "$port" -d postgres -Atqc 'SHOW server_version_num')
if [ "$version" -lt 160000 ]; then
  echo "Existing PostgreSQL is older than 16. Migrate it before upgrading; existing data was not modified." >&2
  exit 1
fi
if [ "$(psql -X -w -h /tmp -p "$port" -d postgres -Atqc "SELECT 1 FROM pg_database WHERE datname='agentmux'")" != 1 ]; then
  # Another AgentMux process may finish creating it while this process waits.
  createdb -w -h /tmp -p "$port" agentmux || \
    [ "$(psql -X -w -h /tmp -p "$port" -d postgres -Atqc "SELECT 1 FROM pg_database WHERE datname='agentmux'")" = 1 ]
fi
echo "Local PostgreSQL is ready."
