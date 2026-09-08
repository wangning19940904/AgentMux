set -euo pipefail
export PATH="${PATH:-}:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
export PGCONNECT_TIMEOUT=3
unset PGHOST PGPORT PGDATABASE PGUSER PGPASSWORD PGSERVICE PGSERVICEFILE PGOPTIONS
socket=/var/run/postgresql
port=${1:-5432}
case "$port" in ''|*[!0-9]*) echo "Invalid PostgreSQL port." >&2; exit 1;; esac
[ "$port" -ge 1 ] && [ "$port" -le 65535 ] || { echo "Invalid PostgreSQL port." >&2; exit 1; }
role=$(id -un)
case "$role" in ''|*[!A-Za-z0-9_.-]*) echo "Unsupported PostgreSQL role name." >&2; exit 1;; esac

root() {
  if [ "$(id -u)" = 0 ]; then "$@";
  elif command -v sudo >/dev/null 2>&1 && sudo -n true 2>/dev/null; then sudo -n "$@";
  else
    echo "PostgreSQL setup needs root or passwordless sudo. Run amux database setup with an account allowed to install packages and provision the database." >&2
    return 1
  fi
}
as_postgres() {
  if [ "$(id -u)" = 0 ]; then runuser -u postgres -- "$@";
  else sudo -n -u postgres "$@"; fi
}

server_bin=""
for candidate in /usr/lib/postgresql/*/bin/postgres /usr/pgsql-*/bin/postgres /usr/bin/postgres; do
  if [ -x "$candidate" ]; then server_bin="$candidate"; break; fi
done
for candidate in /usr/lib/postgresql/*/bin /usr/pgsql-*/bin; do
  if [ -x "$candidate/psql" ]; then export PATH="$candidate:$PATH"; fi
done
if [ -z "$server_bin" ] || ! command -v psql >/dev/null || ! command -v pg_isready >/dev/null; then
  echo "Installing PostgreSQL and client tools…"
  if command -v apt-get >/dev/null 2>&1; then
    root apt-get update -qq
    # Use the distro package when it provides 16; otherwise enable official PGDG.
    if ! apt-cache show postgresql-16 >/dev/null 2>&1; then
      root env DEBIAN_FRONTEND=noninteractive apt-get install -y postgresql-common ca-certificates curl
      root /usr/share/postgresql-common/pgdg/apt.postgresql.org.sh -y
    fi
    root env DEBIAN_FRONTEND=noninteractive apt-get install -y postgresql-16 postgresql-client-16
  elif command -v dnf >/dev/null 2>&1; then
    if dnf -q module info postgresql:16 >/dev/null 2>&1; then
      root dnf -y module install postgresql:16/server
    else
      root dnf install -y postgresql-server postgresql
    fi
  elif command -v yum >/dev/null 2>&1; then
    root yum install -y postgresql-server postgresql
  else
    echo "No supported package manager found (apt/dnf/yum). Install PostgreSQL 16+ or configure AGENTMUX_DATABASE_URL." >&2
    exit 1
  fi
fi
for candidate in /usr/lib/postgresql/*/bin /usr/pgsql-*/bin; do
  if [ -x "$candidate/psql" ]; then export PATH="$candidate:$PATH"; fi
done
if ! command -v psql >/dev/null || ! command -v pg_isready >/dev/null; then
  echo "PostgreSQL client tools are missing from the installed server package." >&2
  exit 1
fi
if ! pg_isready -q -h "$socket" -p "$port"; then
  # RHEL/Fedora packages need initialization; never initialize an existing cluster.
  if command -v postgresql-setup >/dev/null && ! root test -f /var/lib/pgsql/data/PG_VERSION; then
    root postgresql-setup --initdb
  fi
  if command -v pg_lsclusters >/dev/null && [ -z "$(pg_lsclusters --no-header)" ]; then
    root pg_createcluster 16 main --port "$port"
  fi
  if command -v systemctl >/dev/null && root systemctl enable --now postgresql.service; then
    :
  elif command -v pg_ctlcluster >/dev/null; then
    # Debian containers may not run systemd. Start only the cluster on our port.
    cluster=$(pg_lsclusters --no-header | awk -v port="$port" '$3 == port {print $1 " " $2; exit}')
    [ -n "$cluster" ] || { echo "No PostgreSQL cluster configured on port $port." >&2; exit 1; }
    read -r cluster_version cluster_name <<< "$cluster"
    root pg_ctlcluster "$cluster_version" "$cluster_name" start
  else
    root service postgresql start
  fi
fi
for ((attempt=0; attempt<60; attempt++)); do
  if pg_isready -q -h "$socket" -p "$port"; then break; fi
  if [ "$attempt" -eq 59 ]; then echo "PostgreSQL did not become ready on $socket:$port." >&2; exit 1; fi
  sleep 0.5
done
# If the current Unix role already owns a usable database, no sudo is necessary.
if ! psql -X -w -h "$socket" -p "$port" -d agentmux -Atqc 'SELECT 1' >/dev/null 2>&1; then
  root true
  version=$(as_postgres psql -X -w -h "$socket" -p "$port" -d postgres -Atqc 'SHOW server_version_num')
  if [ "$version" -lt 160000 ]; then echo "Existing PostgreSQL is older than 16; migrate it before upgrading." >&2; exit 1; fi
  if [ "$(as_postgres psql -X -w -h "$socket" -p "$port" -d postgres -Atqc "SELECT 1 FROM pg_roles WHERE rolname='$role'")" != 1 ]; then
    as_postgres createuser -w -h "$socket" -p "$port" --login "$role" || \
      [ "$(as_postgres psql -X -w -h "$socket" -p "$port" -d postgres -Atqc "SELECT 1 FROM pg_roles WHERE rolname='$role'")" = 1 ]
  fi
  if [ "$(as_postgres psql -X -w -h "$socket" -p "$port" -d postgres -Atqc "SELECT 1 FROM pg_database WHERE datname='agentmux'")" != 1 ]; then
    as_postgres createdb -w -h "$socket" -p "$port" --owner="$role" agentmux || \
      [ "$(as_postgres psql -X -w -h "$socket" -p "$port" -d postgres -Atqc "SELECT 1 FROM pg_database WHERE datname='agentmux'")" = 1 ]
  fi
fi
version=$(psql -X -w -h "$socket" -p "$port" -d agentmux -Atqc 'SHOW server_version_num')
if [ "$version" -lt 160000 ]; then echo "Existing PostgreSQL is older than 16; migrate it before upgrading." >&2; exit 1; fi
echo "Local PostgreSQL is ready."
