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

func TestProjectWorkspacePolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	content := `
[[projects]]
name = "isolated"
agent = "codex"
work_dir = "/tmp/project"
workspace_mode = "worktree"
worktree_base_ref = "origin/main"
session_backend = "tmux"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	project := cfg.Projects[0]
	if project.WorkspaceMode != "worktree" || project.WorktreeBaseRef != "origin/main" || project.SessionBackend != "tmux" {
		t.Fatalf("workspace policy = %+v", project)
	}

	bad := filepath.Join(t.TempDir(), "bad.toml")
	if err := os.WriteFile(bad, []byte(`[[projects]]
name = "bad"
agent = "codex"
workspace_mode = "container"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(bad); err == nil {
		t.Fatal("invalid workspace mode was accepted")
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
	if !cfg.Observability.Enabled || cfg.Observability.ContentRetentionDays != 14 || cfg.Observability.DetailRetentionDays != 30 || cfg.Observability.BackfillDays != 30 {
		t.Fatalf("observability config = %+v", cfg.Observability)
	}
	if cfg.Database.URL != "postgresql:///agentmux?host=/tmp&sslmode=disable" ||
		cfg.Database.MaxOpenConnections != 12 || cfg.Database.MaxIdleConnections != 4 ||
		cfg.Database.ConnectionMaxLifetime != "30m" {
		t.Fatalf("database defaults = %+v", cfg.Database)
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

func TestResolvePathSearchesLocalThenXDG(t *testing.T) {
	cwd := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldwd) }()
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvPath, "")
	xdg := filepath.Join(t.TempDir(), "xdg")
	t.Setenv("XDG_CONFIG_HOME", xdg)

	xdgPath := filepath.Join(xdg, "agentmux", "config.toml")
	if err := os.MkdirAll(filepath.Dir(xdgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(xdgPath, []byte("[server]\naddr = \"127.0.0.1:9001\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, _, err := ResolvePath("")
	if err != nil {
		t.Fatal(err)
	}
	if got != xdgPath {
		t.Fatalf("path = %q, want xdg path %q", got, xdgPath)
	}

	localPath := filepath.Join(cwd, "config.toml")
	if err := os.WriteFile(localPath, []byte("[server]\naddr = \"127.0.0.1:9002\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, _, err = ResolvePath("")
	if err != nil {
		t.Fatal(err)
	}
	gotInfo, gotErr := os.Stat(got)
	wantInfo, wantErr := os.Stat(localPath)
	if gotErr != nil || wantErr != nil || !os.SameFile(gotInfo, wantInfo) {
		t.Fatalf("path = %q, want local path %q", got, localPath)
	}
}

func TestResolvePathHonorsExplicitPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custom.toml")
	if err := os.WriteFile(path, []byte("[server]\naddr = \"127.0.0.1:9003\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, candidates, err := ResolvePath(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Fatalf("path = %q, want %q", got, path)
	}
	if len(candidates) != 1 || candidates[0] != path {
		t.Fatalf("candidates = %+v, want only explicit path", candidates)
	}
}

func TestResolvePathReportsMissingExplicitPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.toml")
	_, candidates, err := ResolvePath(missing)
	if err == nil {
		t.Fatal("expected missing config error")
	}
	if !IsNotFound(err) {
		t.Fatalf("err = %v, want NotFoundError", err)
	}
	if len(candidates) != 1 || candidates[0] != missing {
		t.Fatalf("candidates = %+v, want missing explicit path", candidates)
	}
}
