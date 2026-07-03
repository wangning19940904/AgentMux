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
