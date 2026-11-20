package diagnostics

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

// Planted values. None of the secrets, the home folder or (without
// --include-content) the message content may reach the zip.
const (
	home = "/Users/planted-person"

	bridgeToken    = "bridge-0f9e8d7c6b5a4f3e"
	dbPassword     = "pg-pass-71c2a9e4"
	envAPIKey      = "sk-ant-api03-plantedenvvalue0123456789"
	platformSecret = "feishu-app-secret-5a5b5c5d"
	headerValue    = "hdr-value-9c8b7a6f5e"
	sshPassword    = "ssh-pass-3e4f5a6b"
	leaseSecret    = "lease-secret-1a2b3c4d"
	githubToken    = "ghp_PlantedEnvironmentToken0123456789"
	shellSecret    = "plain-env-secret-q8w7e6r5"
	channelSecret  = "channel-bot-token-8d7c6b5a"
	agentEnvValue  = "agent-env-value-4f3e2d1c"
	triggerToken   = "trigger-hook-token-2b3c4d5e"
	daemonCookie   = "session-cookie-6e5d4c3b"
	daemonOnly     = "daemon-api-key-0a9b8c7d"
	pemKey         = "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQ\n-----END OPENSSH PRIVATE KEY-----"
	pemBody        = "b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQ"

	promptText   = "please refactor the billing module"
	titleText    = "Billing refactor thread"
	systemPrompt = "You are the planted system prompt"
	replyPreview = "Sure, here is the refactor"
)

var plantedSecrets = []string{
	bridgeToken, dbPassword, envAPIKey, platformSecret, headerValue, sshPassword,
	leaseSecret, githubToken, shellSecret, channelSecret, agentEnvValue, triggerToken,
	daemonCookie, daemonOnly, pemBody, home,
}

var plantedContent = []string{promptText, titleText, systemPrompt, replyPreview}

func plantedConfig() *config.Config {
	cfg := config.Default()
	cfg.Bridge.Enabled = true
	cfg.Bridge.Token = bridgeToken
	cfg.Database.URL = "postgresql://agentmux:" + dbPassword + "@db.internal:5432/agentmux"
	cfg.Hub.LeaseSecret = leaseSecret
	cfg.Hub.LeaseSecretFile = home + "/Library/Application Support/agentmux/hub-lease-secret"
	cfg.Projects = []config.ProjectConfig{{
		Name:         "billing",
		Agent:        "claudecode",
		WorkDir:      home + "/code/billing",
		SystemPrompt: systemPrompt,
		Env:          map[string]string{"ANTHROPIC_API_KEY": envAPIKey, "HARMLESS_NAME": shellSecret},
		Platforms:    []map[string]any{{"type": "feishu", "app_id": "cli_public", "app_secret": platformSecret}},
	}}
	cfg.Observability.Exporters = []config.ObservabilityExporterConfig{{
		Name: "otel", Endpoint: "https://otel.example/v1/traces",
		Headers: map[string]string{"X-Honeycomb-Team": headerValue},
	}}
	cfg.Usage.SSHTargets = []config.SSHTarget{{Name: "box", Host: "box.local", User: "me", KeyPath: home + "/.ssh/id_ed25519", Password: sshPassword}}
	return cfg
}

type fakeStore struct{}

func (fakeStore) ListChannels(context.Context) ([]core.Channel, error) {
	return []core.Channel{{
		ID: "ch1", Name: "Team chat", Type: "slack", AgentID: "ag1", Enabled: true,
		Config: map[string]string{"bot_token": channelSecret, "channel": "C123"},
	}}, nil
}

func (fakeStore) ListAgentInstances(context.Context) ([]core.AgentInstance, error) {
	return []core.AgentInstance{{
		ID: "ag1", Name: "Billing agent", RuntimeID: "claudecode", Enabled: true,
		WorkDir: home + "/code/billing", SystemPrompt: systemPrompt,
		Env: map[string]string{"OPENAI_BASE_URL_EXTRA": agentEnvValue},
		ChannelBindings: []core.AgentChannelBinding{{
			ID: "ch1", Type: "slack", Config: map[string]string{"signing_secret": channelSecret},
		}},
	}}, nil
}

func (fakeStore) ListTriggers(context.Context) ([]core.Trigger, error) {
	return []core.Trigger{{
		ID: "tr1", Name: "nightly", Kind: "webhook", Token: triggerToken,
		LastStatus: "error", LastError: "POST https://hooks.example/?token=" + triggerToken + " failed",
	}}, nil
}

func (fakeStore) ListRecentChannelTasks(_ context.Context, limit int, withPrompt bool) ([]core.ChannelTask, error) {
	task := core.ChannelTask{
		ID: "task1", ChannelID: "ch1", Status: core.ChannelTaskStatus("failed"),
		Error:      "agent exited: open " + home + "/.claude/settings.json: Authorization: Bearer " + daemonOnly,
		CreatedAt:  time.Unix(1_700_000_000, 0).UTC(),
		StartedAt:  time.Unix(1_700_000_001, 0).UTC(),
		FinishedAt: time.Unix(1_700_000_031, 0).UTC(),
	}
	if withPrompt {
		task.Prompt = promptText
	}
	return []core.ChannelTask{task}, nil
}

func (fakeStore) ListObservationTraces(context.Context, store.ObservationTraceFilter) ([]store.ObservationTrace, error) {
	return []store.ObservationTrace{{
		TraceID: "t1", Status: "error",
		Error: &core.ObservationError{Code: "runtime_error", Message: "upstream said: invalid x-api-key " + envAPIKey + " (" + shellSecret + ")"},
	}}, nil
}

// fakeDaemon answers like a daemon that leaks in every way it could.
func fakeDaemon(_ context.Context, path string) ([]byte, error) {
	switch {
	case path == "/api/v1/status":
		return []byte(`{"ok":true,"version":"9.9.9"}`), nil
	case strings.HasPrefix(path, "/api/v1/channels"):
		return json.Marshal([]map[string]any{{
			"id": "ch1", "name": "Team chat", "status": "error",
			"last_error": "slack auth failed for xoxb-" + "123456789012-abcdefabcdef after sending Cookie: " + daemonCookie + "; retrying",
			"config":     map[string]any{"bot_token": channelSecret, "appSecret": platformSecret},
			"title":      titleText,
		}})
	case path == "/api/v1/agent-instances":
		return json.Marshal([]map[string]any{{
			"id": "ag1", "work_dir": home + "/code/billing", "system_prompt": systemPrompt,
			"env":              map[string]any{"ANYTHING": agentEnvValue},
			"last_message":     replyPreview,
			"nested":           []any{map[string]any{"private_key": pemKey, "note": "key at " + home + "/.ssh/id_rsa"}},
			"api_key":          daemonOnly,
			"database_url":     "postgres://u:" + dbPassword + "@h/db",
			"token_prefix":     "amxd_abc",
			"input_tokens":     1234,
			"has_bridge_token": true,
		}})
	case path == "/api/v1/remote/hosts":
		return json.Marshal([]map[string]any{{
			"name": "box", "user": "me", "error": "ssh: " + pemKey,
			"headers": map[string]any{"Cookie": daemonCookie},
		}})
	case path == "/api/v1/hub/replication":
		return nil, errors.New("404 Not Found")
	default:
		return []byte(`{"path":"` + path + `","env_dump":"GITHUB_TOKEN=` + githubToken + `"}`), nil
	}
}

func buildPlanted(t *testing.T, includeContent bool) (*Bundle, map[string]string) {
	t.Helper()
	bundle, err := Build(context.Background(), Options{
		Version:    "1.2.3",
		Config:     plantedConfig(),
		ConfigPath: home + "/Library/Application Support/agentmux/config.toml",
		Home:       home,
		Environ: []string{
			"PATH=/usr/bin:" + home + "/bin",
			"GITHUB_TOKEN=" + githubToken,
			"MY_SERVICE_SECRET=" + shellSecret,
		},
		Daemon:         fakeDaemon,
		Store:          fakeStore{},
		IncludeContent: includeContent,
		Origin:         "test",
		Now:            func() time.Time { return time.Unix(1_700_000_100, 0) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return bundle, unzip(t, bundle.Zip)
}

func unzip(t *testing.T, data []byte) map[string]string {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, f := range reader.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		files[f.Name] = string(body)
	}
	return files
}

func TestBundleLeaksNoPlantedSecret(t *testing.T) {
	for _, includeContent := range []bool{false, true} {
		t.Run(fmt.Sprintf("include_content=%v", includeContent), func(t *testing.T) {
			bundle, files := buildPlanted(t, includeContent)
			for _, want := range []string{
				"manifest.json", "system.json", "config.json",
				"daemon/status.json", "daemon/channels.json", "daemon/agent-instances.json", "daemon/remote-hosts.json",
				"database/channels.json", "database/agents.json", "database/recent-turns.json", "database/recent-errors.json",
			} {
				if _, ok := files[want]; !ok {
					t.Errorf("bundle has no %s; it has %v", want, bundle.Files)
				}
			}
			if len(bundle.Files) != len(files) {
				t.Errorf("listed %d files, zip holds %d", len(bundle.Files), len(files))
			}
			for name, body := range files {
				for _, secret := range plantedSecrets {
					if strings.Contains(body, secret) {
						t.Errorf("%s contains planted value %q:\n%s", name, secret, body)
					}
				}
				for _, bad := range []string{"xoxb-123456789012", "BEGIN OPENSSH PRIVATE KEY"} {
					if strings.Contains(body, bad) {
						t.Errorf("%s contains %q", name, bad)
					}
				}
				if !includeContent {
					for _, content := range plantedContent {
						if strings.Contains(body, content) {
							t.Errorf("%s contains message content %q without --include-content", name, content)
						}
					}
				}
				if strings.HasSuffix(name, ".json") && !json.Valid([]byte(body)) {
					t.Errorf("%s is not valid JSON", name)
				}
			}
			for _, note := range bundle.Notes {
				for _, secret := range plantedSecrets {
					if strings.Contains(note, secret) {
						t.Errorf("note contains planted value %q: %s", secret, note)
					}
				}
			}

			// What is kept stays useful.
			config := files["config.json"]
			for _, want := range []string{`"~/Library/Application Support/agentmux/config.toml"`, `"ANTHROPIC_API_KEY": "[REDACTED]"`, `"app_id": "cli_public"`, `"~/code/billing"`, "db.internal:5432"} {
				if !strings.Contains(config, want) {
					t.Errorf("config.json lacks %s:\n%s", want, config)
				}
			}
			agents := files["daemon/agent-instances.json"]
			for _, want := range []string{`"token_prefix": "amxd_abc"`, `"input_tokens": 1234`, `"has_bridge_token": true`} {
				if !strings.Contains(agents, want) {
					t.Errorf("daemon/agent-instances.json lacks %s:\n%s", want, agents)
				}
			}
			if !strings.Contains(files["database/recent-errors.json"], "agent exited: open ~/.claude/settings.json") {
				t.Errorf("recent errors lost the readable part of the error:\n%s", files["database/recent-errors.json"])
			}
			if !strings.Contains(files["manifest.json"], "hub/replication") {
				t.Errorf("manifest does not say the replication endpoint failed:\n%s", files["manifest.json"])
			}
			if includeContent && !strings.Contains(files["database/recent-turns.json"], promptText) {
				t.Errorf("--include-content left the prompt out:\n%s", files["database/recent-turns.json"])
			}
		})
	}
}

func TestBundleOffline(t *testing.T) {
	bundle, err := Build(context.Background(), Options{
		Version:     "1.2.3",
		Config:      plantedConfig(),
		Home:        home,
		DaemonError: fmt.Errorf("%w (connection refused)", ErrNoDaemon),
		StoreError:  errors.New("connect postgres \"postgresql://agentmux:" + dbPassword + "@db.internal/agentmux\": refused"),
		LocalFrameworks: func(context.Context) (any, error) {
			return []map[string]any{{"kind": "codex", "installed": true, "detail": home + "/.local/bin/codex"}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	files := unzip(t, bundle.Zip)
	for _, want := range []string{"manifest.json", "system.json", "config.json", "local/frameworks.json"} {
		if _, ok := files[want]; !ok {
			t.Errorf("offline bundle has no %s", want)
		}
	}
	for name := range files {
		if strings.HasPrefix(name, "daemon/") || strings.HasPrefix(name, "database/") {
			t.Errorf("offline bundle has %s", name)
		}
	}
	for name, body := range files {
		for _, secret := range plantedSecrets {
			if strings.Contains(body, secret) {
				t.Errorf("%s contains planted value %q", name, secret)
			}
		}
	}
	manifest := files["manifest.json"]
	for _, want := range []string{`"daemon_running": false`, "not running", "database not read"} {
		if !strings.Contains(manifest, want) {
			t.Errorf("manifest lacks %q:\n%s", want, manifest)
		}
	}
}

func TestReplaceHomeOnlyAtPathBoundary(t *testing.T) {
	got := string(replaceHome([]byte(`"/Users/al/x" "/Users/alice/y" "/Users/al"`), "/Users/al"))
	if want := `"~/x" "/Users/alice/y" "~"`; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}
