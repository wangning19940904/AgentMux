// Package postgressetup provisions only AgentMux's default local PostgreSQL.
// Explicit external/custom connections are never replaced or installed over.
package postgressetup

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/wangning19940904/AgentMux/internal/procutil"
)

//go:embed darwin.sh
var darwinScript string

//go:embed linux.sh
var linuxScript string

// LinuxScript is shared by SSH installation and local startup. Its optional
// first argument selects the port of an existing remote cluster.
func LinuxScript() string { return linuxScript }

// Serialize installation in a process without retaining failures permanently.
var setupSlot = make(chan struct{}, 1)

// LocalURL recognizes the default database only, including the explicit port
// used by remote installs. Unknown query options, credentials and TCP hosts opt
// out of provisioning. Linux uses the distribution's standard Unix socket.
func LocalURL(raw, platform string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgresql" && u.Scheme != "postgres") || u.Host != "" || u.User != nil || u.Path != "/agentmux" || u.Fragment != "" {
		return raw, false
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return raw, false
	}
	for key, values := range q {
		if len(values) != 1 || (key != "host" && key != "port" && key != "sslmode") {
			return raw, false
		}
	}
	if q.Get("sslmode") != "disable" || (q.Get("port") != "" && q.Get("port") != "5432") {
		return raw, false
	}
	host := q.Get("host")
	if platform == "linux" {
		if host != "/tmp" && host != "/var/run/postgresql" {
			return raw, false
		}
		q.Set("host", "/var/run/postgresql")
	} else if host != "/tmp" {
		return raw, false
	}
	u.RawQuery = q.Encode()
	return u.String(), true
}

func ResolveURL(raw string) string {
	resolved, _ := LocalURL(raw, runtime.GOOS)
	return resolved
}

// Ensure installs missing dependencies, starts PostgreSQL and creates the local
// role/database. Schema migrations remain the store's responsibility. Output is
// streamed to the caller; installers never prompt on an invisible stdin.
func Ensure(ctx context.Context, raw string, output io.Writer) error {
	return ensure(ctx, raw, runtime.GOOS, output, probe, runScript)
}

type probeFunc func(context.Context, string) (bool, error)
type runFunc func(context.Context, string, io.Writer) error

func ensure(ctx context.Context, raw, platform string, output io.Writer, check probeFunc, run runFunc) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	resolved, local := LocalURL(raw, platform)
	if !local {
		return nil
	}
	select {
	case setupSlot <- struct{}{}:
		defer func() { <-setupSlot }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if ready, err := check(ctx, resolved); ready || err != nil {
		return err
	}
	var script string
	switch platform {
	case "darwin":
		script = darwinScript
	case "linux":
		script = linuxScript
	default:
		return fmt.Errorf("automatic PostgreSQL installation is not supported on %s; install PostgreSQL 16+ and configure AGENTMUX_DATABASE_URL", platform)
	}
	if output == nil {
		output = io.Discard
	}
	fmt.Fprintln(output, "Preparing local PostgreSQL (installing missing dependencies if needed)…")
	setupCtx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	if err := run(setupCtx, script, output); err != nil {
		return fmt.Errorf("prepare local PostgreSQL: %w", err)
	}
	ready, err := check(ctx, resolved)
	if err != nil {
		return err
	}
	if !ready {
		return fmt.Errorf("PostgreSQL setup completed but the local agentmux database is not reachable; run amux database setup for details")
	}
	return nil
}

func probe(ctx context.Context, raw string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", raw)
	if err != nil {
		return false, err
	}
	defer db.Close()
	var version int
	err = db.QueryRowContext(ctx, "SHOW server_version_num").Scan(&version)
	if err == nil {
		if version < 160000 {
			return false, fmt.Errorf("local PostgreSQL %d is older than the required version 16; migrate the existing database before upgrading", version/10000)
		}
		return true, nil
	}
	if ctx.Err() != nil {
		return false, nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code != "3D000" && pgErr.Code != "57P03" &&
		!(pgErr.Code == "28000" && strings.Contains(pgErr.Message, "does not exist")) {
		// Authentication/permission failures are not evidence of a missing server.
		return false, fmt.Errorf("local PostgreSQL rejected the connection (%s); check database credentials and permissions", pgErr.Code)
	}
	return false, nil
}

func runScript(ctx context.Context, script string, output io.Writer) error {
	cmd := exec.CommandContext(ctx, "/bin/bash", "-c", script)
	procutil.Prepare(cmd)
	tail := &tailWriter{}
	cmd.Stdout = io.MultiWriter(output, tail)
	cmd.Stderr = cmd.Stdout
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %s", err, tail.String())
	}
	return nil
}

// Keep useful failure context without buffering an entire package download log.
type tailWriter struct {
	mu   sync.Mutex
	data string
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.data += string(p)
	if len(w.data) > 4096 {
		w.data = w.data[len(w.data)-4096:]
	}
	return len(p), nil
}
func (w *tailWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.TrimSpace(w.data)
}
