// Package remote manages SSH profiles used to control another AgentMux
// instance through its loopback management API.
package remote

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const defaultRemoteAddr = "127.0.0.1:8765"

// Host is a persisted SSH target. APIToken and HostKeyFingerprint are never
// returned directly by the management API.
type Host struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Host               string `json:"host"`
	Port               int    `json:"port"`
	User               string `json:"user"`
	KeyPath            string `json:"key_path,omitempty"`
	SSHAlias           string `json:"ssh_alias,omitempty"`
	RemoteAddr         string `json:"remote_addr"`
	APIToken           string `json:"api_token,omitempty"`
	HostKeyFingerprint string `json:"host_key_fingerprint,omitempty"`
}

// HostView is the non-secret representation returned to the Console.
type HostView struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Host               string `json:"host"`
	Port               int    `json:"port"`
	User               string `json:"user"`
	KeyPath            string `json:"key_path,omitempty"`
	SSHAlias           string `json:"ssh_alias,omitempty"`
	RemoteAddr         string `json:"remote_addr"`
	APITokenSet        bool   `json:"api_token_set"`
	HostKeyFingerprint string `json:"host_key_fingerprint,omitempty"`
	Trusted            bool   `json:"trusted"`
}

func (h Host) View() HostView {
	return HostView{
		ID: h.ID, Name: h.Name, Host: h.Host, Port: h.Port, User: h.User,
		KeyPath: h.KeyPath, SSHAlias: h.SSHAlias, RemoteAddr: h.RemoteAddr, APITokenSet: h.APIToken != "",
		HostKeyFingerprint: h.HostKeyFingerprint, Trusted: h.HostKeyFingerprint != "",
	}
}

// Store persists remote profiles in a user-only JSON file.
type Store struct {
	mu    sync.RWMutex
	path  string
	hosts map[string]Host
}

func NewStore(path string) (*Store, error) {
	resolved, err := resolveHostsPath(path)
	if err != nil {
		return nil, err
	}
	s := &Store{path: resolved, hosts: map[string]Host{}}
	raw, err := os.ReadFile(resolved)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read remote hosts: %w", err)
	}
	if err := os.Chmod(resolved, 0o600); err != nil {
		return nil, fmt.Errorf("secure remote hosts: %w", err)
	}
	var hosts []Host
	if err := json.Unmarshal(raw, &hosts); err != nil {
		return nil, fmt.Errorf("parse remote hosts: %w", err)
	}
	for _, host := range hosts {
		normalized, err := normalizeHost(host)
		if err != nil {
			return nil, fmt.Errorf("remote host %q: %w", host.Name, err)
		}
		s.hosts[normalized.ID] = normalized
	}
	return s, nil
}

func resolveHostsPath(raw string) (string, error) {
	path := strings.TrimSpace(raw)
	if path == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(dir, "agentmux", "remote-hosts.json")
	}
	path = os.ExpandEnv(path)
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	return filepath.Abs(path)
}

func (s *Store) List() []Host {
	s.mu.RLock()
	defer s.mu.RUnlock()
	hosts := make([]Host, 0, len(s.hosts))
	for _, host := range s.hosts {
		hosts = append(hosts, host)
	}
	sort.Slice(hosts, func(i, j int) bool {
		return strings.ToLower(hosts[i].Name) < strings.ToLower(hosts[j].Name)
	})
	return hosts
}

func (s *Store) Get(id string) (Host, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	host, ok := s.hosts[id]
	return host, ok
}

// Upsert stores a profile. An omitted API token preserves the existing token
// unless clearAPIToken is explicitly requested.
func (s *Store) Upsert(host Host, clearAPIToken bool) (Host, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if host.ID == "" {
		id, err := newID()
		if err != nil {
			return Host{}, err
		}
		host.ID = id
	}
	current, existed := s.hosts[host.ID]
	if existed {
		if clearAPIToken {
			host.APIToken = ""
		} else if host.APIToken == "" {
			host.APIToken = current.APIToken
		}
		if host.HostKeyFingerprint == "" &&
			current.Host == host.Host && current.Port == host.Port {
			host.HostKeyFingerprint = current.HostKeyFingerprint
		}
		if host.SSHAlias == "" && current.Host == host.Host &&
			current.Port == host.Port && current.User == host.User {
			host.SSHAlias = current.SSHAlias
		}
	}
	normalized, err := normalizeHost(host)
	if err != nil {
		return Host{}, err
	}
	s.hosts[normalized.ID] = normalized
	if err := s.saveLocked(); err != nil {
		if existed {
			s.hosts[normalized.ID] = current
		} else {
			delete(s.hosts, normalized.ID)
		}
		return Host{}, err
	}
	return normalized, nil
}

func (s *Store) SetHostKeyFingerprint(id, fingerprint string) (Host, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	host, ok := s.hosts[id]
	if !ok {
		return Host{}, os.ErrNotExist
	}
	previous := host
	host.HostKeyFingerprint = strings.TrimSpace(fingerprint)
	s.hosts[id] = host
	if err := s.saveLocked(); err != nil {
		s.hosts[id] = previous
		return Host{}, err
	}
	return host, nil
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	host, ok := s.hosts[id]
	if !ok {
		return os.ErrNotExist
	}
	delete(s.hosts, id)
	if err := s.saveLocked(); err != nil {
		s.hosts[id] = host
		return err
	}
	return nil
}

func (s *Store) saveLocked() error {
	hosts := make([]Host, 0, len(s.hosts))
	for _, host := range s.hosts {
		hosts = append(hosts, host)
	}
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].ID < hosts[j].ID })
	raw, err := json.MarshalIndent(hosts, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".remote-hosts-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return err
	}
	return os.Chmod(s.path, 0o600)
}

func normalizeHost(host Host) (Host, error) {
	host.ID = strings.TrimSpace(host.ID)
	host.Name = strings.TrimSpace(host.Name)
	host.Host = strings.TrimSpace(host.Host)
	host.User = strings.TrimSpace(host.User)
	host.KeyPath = strings.TrimSpace(host.KeyPath)
	host.SSHAlias = strings.TrimSpace(host.SSHAlias)
	host.RemoteAddr = strings.TrimSpace(host.RemoteAddr)
	host.APIToken = strings.TrimSpace(host.APIToken)
	host.HostKeyFingerprint = strings.TrimSpace(host.HostKeyFingerprint)
	if host.ID == "" {
		return Host{}, fmt.Errorf("id is required")
	}
	if host.Name == "" {
		return Host{}, fmt.Errorf("name is required")
	}
	if strings.ContainsAny(host.Name, "\r\n\x00") {
		return Host{}, fmt.Errorf("name contains unsupported control characters")
	}
	if strings.ContainsAny(host.APIToken, "\r\n\x00") {
		return Host{}, fmt.Errorf("api_token contains unsupported control characters")
	}
	if host.SSHAlias != "" && !isConcreteSSHAlias(host.SSHAlias) {
		return Host{}, fmt.Errorf("ssh_alias must be a concrete SSH Config host alias")
	}
	if host.Host == "" || strings.ContainsAny(host.Host, "/ \t\r\n") ||
		strings.Contains(host.Host, "://") {
		return Host{}, fmt.Errorf("host must be a hostname or IP address")
	}
	if host.Port == 0 {
		host.Port = 22
	}
	if host.Port < 1 || host.Port > 65535 {
		return Host{}, fmt.Errorf("port must be between 1 and 65535")
	}
	if host.User == "" {
		if current, err := user.Current(); err == nil {
			host.User = current.Username
		}
	}
	if host.User == "" {
		return Host{}, fmt.Errorf("user is required")
	}
	if host.RemoteAddr == "" {
		host.RemoteAddr = defaultRemoteAddr
	}
	remoteHost, remotePort, err := net.SplitHostPort(host.RemoteAddr)
	if err != nil {
		return Host{}, fmt.Errorf("remote_addr must be host:port: %w", err)
	}
	ip := net.ParseIP(strings.Trim(remoteHost, "[]"))
	if !strings.EqualFold(remoteHost, "localhost") && (ip == nil || !ip.IsLoopback()) {
		return Host{}, fmt.Errorf("remote_addr must point to loopback on the SSH host")
	}
	if port, err := net.LookupPort("tcp", remotePort); err != nil || port < 1 {
		return Host{}, fmt.Errorf("remote_addr has an invalid port")
	}
	if host.KeyPath != "" {
		path, err := expandPath(host.KeyPath)
		if err != nil {
			return Host{}, fmt.Errorf("key_path: %w", err)
		}
		host.KeyPath = path
	}
	return host, nil
}

func expandPath(path string) (string, error) {
	path = os.ExpandEnv(strings.TrimSpace(path))
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	return filepath.Abs(path)
}

func newID() (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}
