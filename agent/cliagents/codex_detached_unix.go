//go:build unix

package cliagents

import (
	"errors"
	"os"
	"syscall"
)

// codexDetachSupported reports whether a Codex app-server can run apart from
// AgentMux here: in its own session, found again through a unix socket.
const codexDetachSupported = true

// detachedProcAttr starts the app-server in a session of its own, so the
// signals that stop AgentMux, its process group or its launchd job do not
// reach it.
func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// lockFile takes an exclusive lock on path, shared with other AgentMux
// processes on this machine. Without wait it reports false when another
// holder has it.
func lockFile(path string, wait bool) (func(), bool, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	how := syscall.LOCK_EX
	if !wait {
		how |= syscall.LOCK_NB
	}
	if err := syscall.Flock(int(file.Fd()), how); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, true, nil
}

func processAlive(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}

// signalProcessGroup signals the app-server and the processes it started; as
// a session leader its process group id is its pid.
func signalProcessGroup(pid int, signal syscall.Signal) {
	if pid <= 0 {
		return
	}
	if syscall.Kill(-pid, signal) != nil {
		_ = syscall.Kill(pid, signal)
	}
}

const (
	sigTerm = syscall.SIGTERM
	sigKill = syscall.SIGKILL
)
