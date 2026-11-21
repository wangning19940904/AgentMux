//go:build !unix

package cliagents

import "syscall"

// Codex app-servers stay children of AgentMux where unix sockets and sessions
// are not available.
const codexDetachSupported = false

func detachedProcAttr() *syscall.SysProcAttr { return nil }

func lockFile(string, bool) (func(), bool, error) { return func() {}, true, nil }

func processAlive(int) bool { return false }

func signalProcessGroup(int, syscall.Signal) {}

const (
	sigTerm = syscall.Signal(15)
	sigKill = syscall.Signal(9)
)
