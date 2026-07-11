// Package native manages the additive AgentNexus observer plugins for Claude
// Code and Codex. All host configuration changes are delegated to the hosts'
// native plugin CLIs; this package never edits hooks.state or shared hook files.
package native

import (
	"errors"
	"fmt"
	"time"
)

const (
	PluginID        = "agentnexus-observer"
	MarketplaceName = "agentnexus-local"
	PluginVersion   = "0.1.0"
)

// Host is a supported native agent host.
type Host string

const (
	HostClaude Host = "claude"
	HostCodex  Host = "codex"
)

func (h Host) Valid() bool { return h == HostClaude || h == HostCodex }

type Severity string

const (
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

// Finding is a stable, machine-readable doctor/preview result.
type Finding struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
	Owner    string   `json:"owner,omitempty"`
	Path     string   `json:"path,omitempty"`
	Blocking bool     `json:"blocking,omitempty"`
}

// Action describes a native CLI or owned-file operation before it runs.
type Action struct {
	Kind    string   `json:"kind"`
	Target  string   `json:"target"`
	Command []string `json:"command,omitempty"`
	Reason  string   `json:"reason"`
}

type Status string

const (
	StatusNotInstalled Status = "not_installed"
	StatusHealthy      Status = "healthy"
	StatusPendingTrust Status = "pending_trust"
	StatusDrift        Status = "drift"
	StatusConflict     Status = "conflict"
	StatusUnavailable  Status = "unavailable"
)

// Preview is the non-mutating plan returned before install or repair.
type Preview struct {
	Host               Host      `json:"host"`
	Status             Status    `json:"status"`
	InstallID          string    `json:"install_id,omitempty"`
	PluginSHA256       string    `json:"plugin_sha256,omitempty"`
	MarketplaceSHA256  string    `json:"marketplace_sha256,omitempty"`
	HandlerFingerprint string    `json:"handler_fingerprint,omitempty"`
	Actions            []Action  `json:"actions"`
	Findings           []Finding `json:"findings"`
	Blocked            bool      `json:"blocked"`
}

// ResourceOwnership records only resources that AgentNexus created through a
// native CLI or in its own private ~/.agentnexus directory. Shared host files
// are observations, never resources restored from snapshots.
type ResourceOwnership struct {
	Kind               string `json:"kind"`
	TargetPath         string `json:"target_path"`
	BeforeHash         string `json:"before_hash,omitempty"`
	AfterHash          string `json:"after_hash"`
	HandlerFingerprint string `json:"handler_fingerprint,omitempty"`
}

// FileObservation captures a native CLI's effect on shared files for audit.
// It is deliberately not an ownership claim and is never restored wholesale.
type FileObservation struct {
	Path       string `json:"path"`
	BeforeHash string `json:"before_hash,omitempty"`
	AfterHash  string `json:"after_hash,omitempty"`
}

// InstallRecord is the persisted ownership manifest. InstallID remains stable
// across repair/update operations.
type InstallRecord struct {
	SchemaVersion      int                 `json:"schema_version"`
	InstallID          string              `json:"install_id"`
	Host               Host                `json:"host"`
	Scope              string              `json:"scope"`
	PluginID           string              `json:"plugin_id"`
	Marketplace        string              `json:"marketplace"`
	MarketplaceRoot    string              `json:"marketplace_root"`
	Version            string              `json:"version"`
	PluginSHA256       string              `json:"plugin_sha256"`
	MarketplaceSHA256  string              `json:"marketplace_sha256"`
	HandlerFingerprint string              `json:"handler_fingerprint"`
	Status             Status              `json:"status"`
	Resources          []ResourceOwnership `json:"resources"`
	SharedFiles        []FileObservation   `json:"shared_files,omitempty"`
	InstalledAt        time.Time           `json:"installed_at"`
	UpdatedAt          time.Time           `json:"updated_at"`
}

// Result describes a completed mutating operation.
type Result struct {
	Host      Host           `json:"host"`
	Changed   bool           `json:"changed"`
	Record    *InstallRecord `json:"record,omitempty"`
	Actions   []Action       `json:"actions"`
	Findings  []Finding      `json:"findings"`
	Preserved []string       `json:"preserved,omitempty"`
}

// DoctorReport is designed to map directly to the Integrations console.
type DoctorReport struct {
	Host      Host              `json:"host"`
	Status    Status            `json:"status"`
	InstallID string            `json:"install_id,omitempty"`
	Coverage  map[string]string `json:"coverage"`
	Owners    []string          `json:"owners,omitempty"`
	Findings  []Finding         `json:"findings"`
}

var (
	ErrConflict = errors.New("native integration conflict")
	ErrDrift    = errors.New("native integration drift")
	ErrCAS      = errors.New("compare-and-swap failed")
)

// OperationError includes all actionable findings while supporting errors.Is.
type OperationError struct {
	Kind     error
	Host     Host
	Findings []Finding
}

func (e *OperationError) Error() string {
	if len(e.Findings) == 0 {
		return fmt.Sprintf("%s: %v", e.Host, e.Kind)
	}
	return fmt.Sprintf("%s: %v: %s", e.Host, e.Kind, e.Findings[0].Message)
}

func (e *OperationError) Unwrap() error { return e.Kind }
