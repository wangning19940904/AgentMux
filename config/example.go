package config

const exampleConfig = `# AgentMux configuration.
# ${ENV_VAR} placeholders are expanded from the environment.

display_mode = "normal"  # quiet | compact | normal | full

[server]
addr = "127.0.0.1:8765"

[bridge]
enabled = false
# token = "${BRIDGE_TOKEN}"   # REQUIRED when enabled = true

# Define projects here, or add channels and agent instances from the WebUI.
# Keep the starter config runnable without credentials.
#
# [[projects]]
# name = "demo"
# agent = "claudecode"
# work_dir = "."
# system_prompt = "You are a helpful coding assistant."
# default_model = "sonnet"
#
#   [projects.env]
#   ANTHROPIC_API_KEY = "${MY_KEY}"
#
#   [[projects.platforms]]
#   type = "telegram"
#   token = "${TELEGRAM_BOT_TOKEN}"

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
detail_retention_days = 180
backfill_days = 180
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
