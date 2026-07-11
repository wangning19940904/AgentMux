package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadWithSSHAndEnv(t *testing.T) {
	os.Setenv("TEST_TG_TOKEN", "secret-token")
	defer os.Unsetenv("TEST_TG_TOKEN")

	content := `
[server]
addr = "127.0.0.1:9000"

[[projects]]
name = "demo"
agent = "claudecode"
  [[projects.platforms]]
  type = "telegram"
  token = "${TEST_TG_TOKEN}"

[usage]
sources = ["claude", "codex"]

[[usage.ssh]]
name = "box"
host = "10.0.0.5"
port = 2222
user = "dev"
key_path = "/home/dev/.ssh/id"
sources = ["claude"]
  [usage.ssh.paths]
  claude = ".claude"
`
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Addr != "127.0.0.1:9000" {
		t.Fatalf("addr = %q", cfg.Server.Addr)
	}
	if len(cfg.Projects) != 1 || cfg.Projects[0].Agent != "claudecode" {
		t.Fatalf("projects = %+v", cfg.Projects)
	}
	// env expansion
	if got := cfg.Projects[0].Platforms[0]["token"]; got != "secret-token" {
		t.Fatalf("token = %v, want expanded", got)
	}
	if len(cfg.Usage.SSHTargets) != 1 {
		t.Fatalf("ssh targets = %d, want 1", len(cfg.Usage.SSHTargets))
	}
	ssh := cfg.Usage.SSHTargets[0]
	if ssh.Host != "10.0.0.5" || ssh.Port != 2222 || ssh.Paths["claude"] != ".claude" {
		t.Fatalf("ssh target mismatch: %+v", ssh)
	}
}

func TestBridgeRequiresToken(t *testing.T) {
	content := `
[bridge]
enabled = true
`
	path := filepath.Join(t.TempDir(), "config.toml")
	_ = os.WriteFile(path, []byte(content), 0o644)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error: bridge enabled without token")
	}
}

func TestObservabilityDefaultsAndExporter(t *testing.T) {
	content := `
[observability]
enabled = true
capture_content = "full"
content_retention_days = 14

[[observability.exporters]]
name = "local-collector"
enabled = true
endpoint = "http://127.0.0.1:4318"
`
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Observability.Enabled || cfg.Observability.ContentRetentionDays != 14 || cfg.Observability.DetailRetentionDays != 180 || cfg.Observability.BackfillDays != 180 {
		t.Fatalf("observability config = %+v", cfg.Observability)
	}
	if len(cfg.Observability.Exporters) != 1 {
		t.Fatalf("exporters = %+v", cfg.Observability.Exporters)
	}
	exporter := cfg.Observability.Exporters[0]
	if exporter.Type != "otlp_http" || exporter.Protocol != "http/json" || exporter.IncludeContent || exporter.QueueSize != 10000 {
		t.Fatalf("exporter defaults = %+v", exporter)
	}
}

func TestObservabilityDefaultsEnabledAndAllowsExplicitOptOut(t *testing.T) {
	for name, content := range map[string]string{
		"default":      "[server]\naddr = \"127.0.0.1:9000\"\n",
		"explicit-off": "[observability]\nenabled = false\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			want := name == "default"
			if cfg.Observability.Enabled != want {
				t.Fatalf("enabled = %v, want %v", cfg.Observability.Enabled, want)
			}
		})
	}
}

func TestObservabilityRejectsUnsafeOrInvalidConfig(t *testing.T) {
	for name, content := range map[string]string{
		"capture": `[observability]
capture_content = "everything"`,
		"exporter": `[[observability.exporters]]
name = "missing-endpoint"
enabled = true`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("expected invalid observability config to fail")
			}
		})
	}
}
