//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

// Package procutil makes context cancellation cover an entire subprocess
// tree, including grandchildren that create their own process groups.
package procutil

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

// Prepare must be called before Cmd.Start. The default os/exec cancellation
// kills only the direct child; coding agents commonly launch tool shells in a
// separate process group, which otherwise survive as orphaned commands.
func Prepare(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.WaitDelay = 5 * time.Second
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return killTree(cmd.Process.Pid)
	}
}

func killTree(root int) error {
	descendants := processDescendants(root)
	killed := false
	// The direct child is its own group leader. Kill that group first to stop
	// it from creating more tools, then kill the snapshotted descendants from
	// leaves to root even when they moved into independent process groups.
	if err := syscall.Kill(-root, syscall.SIGKILL); err == nil {
		killed = true
	} else if !errors.Is(err, syscall.ESRCH) {
		return err
	}
	for i := len(descendants) - 1; i >= 0; i-- {
		if err := syscall.Kill(descendants[i], syscall.SIGKILL); err == nil {
			killed = true
		} else if !errors.Is(err, syscall.ESRCH) && !errors.Is(err, syscall.EPERM) {
			return err
		}
	}
	if killed {
		return nil
	}
	return os.ErrProcessDone
}

func processDescendants(root int) []int {
	out, err := exec.Command("ps", "-e", "-o", "pid=,ppid=").Output()
	if err != nil {
		return nil
	}
	children := map[int][]int{}
	for _, line := range bytes.Split(out, []byte{'\n'}) {
		fields := bytes.Fields(line)
		if len(fields) != 2 {
			continue
		}
		pid, pidErr := strconv.Atoi(string(fields[0]))
		ppid, ppidErr := strconv.Atoi(string(fields[1]))
		if pidErr == nil && ppidErr == nil && pid > 1 {
			children[ppid] = append(children[ppid], pid)
		}
	}
	var descendants []int
	var visit func(int)
	visit = func(parent int) {
		for _, child := range children[parent] {
			descendants = append(descendants, child)
			visit(child)
		}
	}
	visit(root)
	return descendants
}
