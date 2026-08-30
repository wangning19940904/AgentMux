package config

const exampleConfig = `# AgentMux configuration.
# ${ENV_VAR} placeholders are expanded from the environment.

display_mode = "normal"  # quiet | compact | normal | full

[server]
addr = "127.0.0.1:8765"

[database]
url = "postgresql:///agentmux?host=/tmp&sslmode=disable"
max_open_connections = 12
max_idle_connections = 4
connection_max_lifetime = "30m"

[bridge]
enabled = false
# token = "${BRIDGE_TOKEN}"   # REQUIRED when enabled = true

[remote]
connect_timeout_seconds = 10
# hosts_file = "~/.config/agentmux/remote-hosts.json"

# Runtime Agents, Channels, and Triggers are PostgreSQL resources managed by
# the Console/API. Import a legacy project config with:
#   amux database import-config --apply

[provider]
failover = true
# proxy_addr = "127.0.0.1:15733"

[usage]
sources = ["claude", "codex", "cursor", "gemini"]
offline = false
# cache_dir = "~/.cache/agentmux"

[observability]
enabled = true
capture_content = "full" # off | metadata | full
content_retention_days = 30
detail_retention_days = 30
backfill_days = 30
# master_key_env = "AGENTMUX_OBSERVABILITY_KEY"

# [[observability.exporters]]
# name = "local-otel"
# type = "otlp_http"
# protocol = "http/json"
# enabled = true
# endpoint = "http://127.0.0.1:4318"
# include_content = false
# timeout_seconds = 10
# queue_size = 10000

# Optional: collect usage from remote machines over SSH.
# [[usage.ssh]]
# name = "build-box"
# host = "10.0.0.5"
# port = 22
# user = "dev"
# key_path = "~/.ssh/id_ed25519"
# sources = ["claude", "codex"]
#   [usage.ssh.paths]
#   claude = ".claude"
`

// ExampleConfig returns a starter config.toml that can be written by CLI
// installers on machines where the source tree is not present.
func ExampleConfig() string {
	return exampleConfig
}
