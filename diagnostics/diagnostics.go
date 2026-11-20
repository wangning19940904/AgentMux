// Package diagnostics builds a local, redacted support bundle: a zip that
// describes this AgentMux (version, platform, configuration, frameworks,
// channels, agents, hub link, recent turns and errors) without credentials,
// without the home folder's path and, unless asked for, without what anyone
// wrote. It reads; it never uploads. `amux diagnose` and the Console's
// download button both build it here.
package diagnostics

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"runtime"
	"runtime/debug"
	"sort"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

// Fetcher reads one GET endpoint of the running daemon's API and returns its
// JSON body. A nil Fetcher means the daemon is not reachable.
type Fetcher func(ctx context.Context, path string) ([]byte, error)

// Store is the part of the runtime store the bundle reads.
type Store interface {
	ListChannels(ctx context.Context) ([]core.Channel, error)
	ListAgentInstances(ctx context.Context) ([]core.AgentInstance, error)
	ListTriggers(ctx context.Context) ([]core.Trigger, error)
	ListRecentChannelTasks(ctx context.Context, limit int, withPrompt bool) ([]core.ChannelTask, error)
	ListObservationTraces(ctx context.Context, filter store.ObservationTraceFilter) ([]store.ObservationTrace, error)
}

// Options says what to collect and where from. Every source is optional: a
// bundle built with the daemon stopped and the database unreachable still
// holds the version, the platform and the configuration.
type Options struct {
	// Version is the AgentMux version of whoever builds the bundle.
	Version string
	// Config is the effective configuration; ConfigPath where it came from
	// ("" for built-in defaults) and ConfigError why it could not be read.
	Config      *config.Config
	ConfigPath  string
	ConfigError error
	// Home is the home folder, written as ~ everywhere in the bundle.
	Home string
	// Environ is the environment (os.Environ()). Its secret-looking values
	// are removed wherever they appear; nothing else from it is included.
	Environ []string
	// Daemon reads the running daemon's API; nil when it is not running.
	// DaemonError says why it is not reachable.
	Daemon      Fetcher
	DaemonError error
	// Store reads the database; nil when it cannot be opened. StoreError
	// says why.
	Store      Store
	StoreError error
	// LocalFrameworks detects installed frameworks without the daemon. It
	// is used only when the daemon is not reachable.
	LocalFrameworks func(ctx context.Context) (any, error)
	// IncludeContent keeps prompts, titles and other message content.
	IncludeContent bool
	// RecentTurns is how many recent turns to summarize (default 100).
	RecentTurns int
	// Now stamps the bundle (default time.Now).
	Now func() time.Time
	// Origin says who built the bundle ("cli" or "console").
	Origin string
}

// File is one entry of the bundle.
type File struct {
	Name string `json:"name"`
	Size int    `json:"size"`
}

// Bundle is a built diagnostics zip.
type Bundle struct {
	Zip   []byte
	Files []File
	// Notes say what could not be collected and why.
	Notes []string
}

// daemonEndpoints are the daemon API reads the bundle includes, by file.
var daemonEndpoints = []struct{ file, path string }{
	{"daemon/status.json", "/api/v1/status"},
	{"daemon/frameworks-health.json", "/api/v1/frameworks/health"},
	{"daemon/frameworks.json", "/api/v1/frameworks"},
	{"daemon/channels.json", "/api/v1/channels?view=health"},
	{"daemon/agent-instances.json", "/api/v1/agent-instances"},
	{"daemon/hub-link.json", "/api/v1/link"},
	{"daemon/devices.json", "/api/v1/devices"},
	{"daemon/remote-hosts.json", "/api/v1/remote/hosts"},
	{"daemon/hub-replication.json", "/api/v1/hub/replication"},
	{"daemon/system-metrics.json", "/api/v1/system/metrics"},
}

// Build collects everything the options allow and returns the zip.
func Build(ctx context.Context, opts Options) (*Bundle, error) {
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	if opts.RecentTurns <= 0 {
		opts.RecentTurns = 100
	}
	b := &builder{opts: opts, red: newRedactor(opts.Home, opts.IncludeContent), files: map[string][]byte{}}
	b.red.addEnv(opts.Environ)

	// Known secrets first: every file is checked against all of them, so
	// a token from the database cannot reach a file built from the daemon.
	configTree := b.configTree()
	b.red.collectSecrets(configTree)
	var channels []core.Channel
	var agents []core.AgentInstance
	var triggers []core.Trigger
	if opts.Store != nil {
		var err error
		if channels, err = opts.Store.ListChannels(ctx); err != nil {
			b.note("database channels: %v", err)
		}
		if agents, err = opts.Store.ListAgentInstances(ctx); err != nil {
			b.note("database agents: %v", err)
		}
		if triggers, err = opts.Store.ListTriggers(ctx); err != nil {
			b.note("database triggers: %v", err)
		}
		b.red.collectSecretsFrom(channels)
		b.red.collectSecretsFrom(agents)
		for _, t := range triggers {
			b.red.addSecret(t.Token)
		}
	}

	b.add("system.json", b.system(now()))
	b.add("config.json", map[string]any{
		"path":   b.configPath(),
		"error":  errText(opts.ConfigError),
		"config": configTree,
	})
	b.collectDaemon(ctx)
	if opts.Store != nil {
		b.collectStore(ctx, channels, agents, triggers)
	} else {
		b.note("database not read: %s", errOr(opts.StoreError, "no database connection"))
	}

	manifest := map[string]any{
		"generated_at":    now().UTC().Format(time.RFC3339),
		"generated_by":    opts.Origin,
		"agentmux":        opts.Version,
		"include_content": opts.IncludeContent,
		"daemon_running":  opts.Daemon != nil,
		"database_read":   opts.Store != nil,
		"notes":           b.notes,
		"redaction": []string{
			"credential files, SSH keys, cookies and API tokens are never read into the bundle",
			"values of secret-named fields, env and header maps are replaced with " + redacted,
			"known secrets (config, database, secret-named environment variables) are replaced wherever they appear",
			"the home folder is written as ~",
			contentNote(opts.IncludeContent),
		},
	}
	b.add("manifest.json", manifest)
	return b.zip()
}

func contentNote(include bool) string {
	if include {
		return "message content (prompts, titles, previews) is included because --include-content was given"
	}
	return "message content (prompts, titles, previews) is replaced with its length"
}

type builder struct {
	opts  Options
	red   *redactor
	files map[string][]byte
	notes []string
}

func (b *builder) note(format string, args ...any) {
	b.notes = append(b.notes, b.red.text(fmt.Sprintf(format, args...)))
}

// add renders one file; a value that cannot be rendered becomes a note.
func (b *builder) add(name string, value any) {
	data, err := b.red.render(value)
	if err != nil {
		b.note("%s: %v", name, err)
		return
	}
	b.files[name] = data
}

func (b *builder) configPath() string {
	if b.opts.ConfigPath == "" {
		return "(built-in defaults)"
	}
	return b.opts.ConfigPath
}

// configTree is the effective configuration as plain values, in the field
// names config.toml uses. Redaction happens when it is rendered.
func (b *builder) configTree() any {
	if b.opts.Config == nil {
		return nil
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(b.opts.Config); err != nil {
		b.note("config: %v", err)
		return nil
	}
	var tree map[string]any
	if _, err := toml.Decode(buf.String(), &tree); err != nil {
		b.note("config: %v", err)
		return nil
	}
	generic, err := toGeneric(tree)
	if err != nil {
		b.note("config: %v", err)
		return nil
	}
	return generic
}

func (b *builder) system(now time.Time) map[string]any {
	info := map[string]any{
		"agentmux_version": b.opts.Version,
		"go_version":       runtime.Version(),
		"os":               runtime.GOOS,
		"arch":             runtime.GOARCH,
		"cpus":             runtime.NumCPU(),
		"utc_offset":       now.Format("-07:00"),
	}
	if build, ok := debug.ReadBuildInfo(); ok {
		settings := map[string]string{}
		for _, s := range build.Settings {
			switch s.Key {
			case "vcs.revision", "vcs.time", "vcs.modified", "-tags", "GOOS", "GOARCH", "CGO_ENABLED":
				settings[s.Key] = s.Value
			}
		}
		info["build"] = settings
		info["module_version"] = build.Main.Version
	}
	return info
}

// collectDaemon reads each endpoint of the running daemon, or detects the
// frameworks locally when there is no daemon.
func (b *builder) collectDaemon(ctx context.Context) {
	if b.opts.Daemon == nil {
		b.note("daemon not reachable: %s", errOr(b.opts.DaemonError, "not running"))
		if b.opts.LocalFrameworks != nil {
			frameworks, err := b.opts.LocalFrameworks(ctx)
			if err != nil {
				b.note("local framework detection: %v", err)
			} else {
				b.add("local/frameworks.json", frameworks)
			}
		}
		return
	}
	for _, e := range daemonEndpoints {
		body, err := b.opts.Daemon(ctx, e.path)
		if err != nil {
			b.note("%s (%s): %v", e.file, e.path, err)
			continue
		}
		b.add(e.file, []byte(body))
	}
}

// collectStore summarizes what the database holds: channels, agents, recent
// turns and recent errors.
func (b *builder) collectStore(ctx context.Context, channels []core.Channel, agents []core.AgentInstance, triggers []core.Trigger) {
	b.add("database/channels.json", channelSummaries(channels))
	b.add("database/agents.json", agentSummaries(agents))

	tasks, err := b.opts.Store.ListRecentChannelTasks(ctx, b.opts.RecentTurns, b.opts.IncludeContent)
	if err != nil {
		b.note("database recent turns: %v", err)
	} else {
		b.add("database/recent-turns.json", turnSummaries(tasks, b.opts.IncludeContent))
	}

	errorsOut := map[string]any{}
	var failedTurns []map[string]any
	for _, t := range tasks {
		if t.Error != "" || t.DeliveryError != "" {
			failedTurns = append(failedTurns, map[string]any{
				"id": t.ID, "channel_id": t.ChannelID, "status": t.Status,
				"error": t.Error, "delivery_error": t.DeliveryError, "at": t.UpdatedAt,
			})
		}
	}
	errorsOut["turns"] = failedTurns
	var failedTriggers []map[string]any
	for _, t := range triggers {
		if t.LastError != "" {
			failedTriggers = append(failedTriggers, map[string]any{
				"id": t.ID, "name": t.Name, "kind": t.Kind, "last_status": t.LastStatus,
				"last_error": t.LastError, "last_run": t.LastRun,
			})
		}
	}
	errorsOut["triggers"] = failedTriggers
	traces, err := b.opts.Store.ListObservationTraces(ctx, store.ObservationTraceFilter{Status: core.ObservationStatusError, Limit: 50})
	if err != nil {
		b.note("database observation errors: %v", err)
	} else {
		var failedTraces []map[string]any
		for _, t := range traces {
			entry := map[string]any{
				"trace_id": t.TraceID, "agent_id": t.AgentID, "agent_name": t.AgentName,
				"runtime_id": t.RuntimeID, "source": t.Source, "status": t.Status,
				"started_at": t.StartedAt, "ended_at": t.EndedAt,
			}
			if t.Error != nil {
				entry["error"] = map[string]any{"code": t.Error.Code, "message": t.Error.Message, "retryable": t.Error.Retryable}
			}
			if t.Model != nil {
				entry["model"] = t.Model
			}
			failedTraces = append(failedTraces, entry)
		}
		errorsOut["observation_traces"] = failedTraces
	}
	b.add("database/recent-errors.json", errorsOut)
}

// channelSummaries lists channels with their setting names but no values.
func channelSummaries(channels []core.Channel) []map[string]any {
	out := make([]map[string]any, 0, len(channels))
	for _, c := range channels {
		out = append(out, map[string]any{
			"id": c.ID, "name": c.Name, "type": c.Type, "agent_id": c.AgentID,
			"enabled": c.Enabled, "config_keys": sortedKeys(c.Config),
			"created_at": c.CreatedAt, "updated_at": c.UpdatedAt,
		})
	}
	return out
}

// agentSummaries lists agents with their environment's names but no values.
func agentSummaries(agents []core.AgentInstance) []map[string]any {
	out := make([]map[string]any, 0, len(agents))
	for _, a := range agents {
		bindings := make([]map[string]any, 0, len(a.ChannelBindings))
		for _, binding := range a.ChannelBindings {
			bindings = append(bindings, map[string]any{
				"id": binding.ID, "type": binding.Type, "name": binding.Name, "status": binding.Status,
				"config_keys": sortedKeys(binding.Config),
			})
		}
		out = append(out, map[string]any{
			"id": a.ID, "name": a.Name, "runtime_id": a.RuntimeID, "enabled": a.Enabled,
			"source": a.Source, "work_dir": a.WorkDir, "workspace_mode": a.WorkspaceMode,
			"session_backend": a.SessionBackend, "default_model": a.DefaultModel,
			"default_reasoning_effort": a.DefaultReasoningEffort, "default_approval_mode": a.DefaultApprovalMode,
			"env_keys": sortedKeys(a.Env), "channel_bindings": bindings,
			"mcp_servers": a.MCPServers, "skills": a.Skills, "clis": a.CLIs,
			"schedules": len(a.Schedules), "system_prompt": a.SystemPrompt,
			"updated_at": a.UpdatedAt,
		})
	}
	return out
}

// turnSummaries describes recent turns: status, timing and errors; the
// prompt only when content was asked for.
func turnSummaries(tasks []core.ChannelTask, includeContent bool) map[string]any {
	byStatus := map[string]int{}
	list := make([]map[string]any, 0, len(tasks))
	for _, t := range tasks {
		byStatus[string(t.Status)]++
		entry := map[string]any{
			"id": t.ID, "channel_id": t.ChannelID, "conversation_id": t.ConversationID,
			"status": t.Status, "error": t.Error,
			"delivery_status": t.DeliveryStatus, "delivery_attempts": t.DeliveryAttempts, "delivery_error": t.DeliveryError,
			"created_at": t.CreatedAt,
		}
		if !t.StartedAt.IsZero() {
			entry["started_at"] = t.StartedAt
			if !t.FinishedAt.IsZero() {
				entry["duration_seconds"] = t.FinishedAt.Sub(t.StartedAt).Round(time.Millisecond).Seconds()
			}
		}
		if !t.FinishedAt.IsZero() {
			entry["finished_at"] = t.FinishedAt
		}
		if includeContent {
			entry["prompt"] = t.Prompt
		}
		list = append(list, entry)
	}
	return map[string]any{"count": len(tasks), "by_status": byStatus, "turns": list}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// zip writes every file, manifest first, and lists them with their sizes.
func (b *builder) zip() (*Bundle, error) {
	names := make([]string, 0, len(b.files))
	for name := range b.files {
		if name != "manifest.json" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	names = append([]string{"manifest.json"}, names...)

	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	bundle := &Bundle{Notes: b.notes}
	for _, name := range names {
		data, ok := b.files[name]
		if !ok {
			continue
		}
		f, err := w.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Now()})
		if err != nil {
			return nil, err
		}
		if _, err := f.Write(data); err != nil {
			return nil, err
		}
		bundle.Files = append(bundle.Files, File{Name: name, Size: len(data)})
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	bundle.Zip = buf.Bytes()
	return bundle, nil
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func errOr(err error, fallback string) string {
	if err == nil {
		return fallback
	}
	return err.Error()
}

// ErrNoDaemon is the reason given when nothing answers at the daemon address.
var ErrNoDaemon = errors.New("the AgentMux daemon is not running")

// FileName is the default name for a bundle built at t.
func FileName(t time.Time) string {
	return "agentmux-diagnostics-" + t.Format("20060102-150405") + ".zip"
}
