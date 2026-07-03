// Package ssh syncs remote coding-agent session logs to a local staging
// directory so the standard parsers can run over them. It satisfies the SSH
// token-statistics requirement: connect over SSH, locate each source's data
// path on the remote host, copy files into a per-host staging tree, and return
// a Staging handle that maps source -> local root.
package ssh

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/ssh"
)

// Target describes a remote machine to collect from.
type Target struct {
	Name     string
	Host     string
	Port     int
	User     string
	KeyPath  string
	Password string
	Sources  []string
	Paths    map[string]string // source -> remote base path override
}

// Staging maps a source id to the local directory its remote files were synced
// into.
type Staging struct {
	roots map[string]string
}

// Root returns the local staging root for a source, or "" if not synced.
func (s *Staging) Root(source string) string { return s.roots[source] }

// defaultRemotePath returns the conventional remote data path for a source,
// relative to the remote home directory.
func defaultRemotePath(source string) string {
	switch source {
	case "claude":
		return ".claude"
	case "codex":
		return ".codex"
	case "gemini":
		return ".gemini"
	case "cursor":
		return ".config/Cursor/User/globalStorage"
	default:
		return ""
	}
}

// Sync connects to the target and copies each source's session files into a
// local staging directory under ~/.agentnexus/ssh/<target>/<source>.
func Sync(ctx context.Context, t Target, log *slog.Logger) (*Staging, error) {
	client, err := dial(t)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	home, _ := os.UserHomeDir()
	stagingBase := filepath.Join(home, ".agentnexus", "ssh", t.Name)
	st := &Staging{roots: map[string]string{}}

	sources := t.Sources
	if len(sources) == 0 {
		sources = []string{"claude", "codex", "gemini"}
	}
	for _, src := range sources {
		remoteRel := t.Paths[src]
		if remoteRel == "" {
			remoteRel = defaultRemotePath(src)
		}
		if remoteRel == "" {
			continue
		}
		localRoot := filepath.Join(stagingBase, src)
		if err := os.MkdirAll(localRoot, 0o755); err != nil {
			return nil, err
		}
		n, err := copyRemoteTree(ctx, client, remoteRel, localRoot)
		if err != nil {
			log.Warn("ssh copy failed", "source", src, "err", err)
			continue
		}
		log.Debug("ssh synced", "source", src, "files", n)
		st.roots[src] = localRoot
	}
	return st, nil
}

func dial(t Target) (*ssh.Client, error) {
	port := t.Port
	if port == 0 {
		port = 22
	}
	var auth []ssh.AuthMethod
	if t.KeyPath != "" {
		key, err := os.ReadFile(t.KeyPath)
		if err != nil {
			return nil, fmt.Errorf("read key: %w", err)
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("parse key: %w", err)
		}
		auth = append(auth, ssh.PublicKeys(signer))
	}
	if t.Password != "" {
		auth = append(auth, ssh.Password(t.Password))
	}
	if len(auth) == 0 {
		return nil, fmt.Errorf("no SSH auth method for target %q (set key_path or password)", t.Name)
	}
	cfg := &ssh.ClientConfig{
		User:            t.User,
		Auth:            auth,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // local tool; document risk
		Timeout:         15 * time.Second,
	}
	addr := fmt.Sprintf("%s:%d", t.Host, port)
	return ssh.Dial("tcp", addr, cfg)
}
