//go:build linux

package procutil

import (
	"bufio"
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestKillTreeIncludesGrandchildInIndependentProcessGroup(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "sh", "-c", `setsid sh -c 'sleep 60' >/dev/null 2>&1 & child=$!; printf '%s\n' "$child"; wait`)
	Prepare(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatalf("child pid missing: %v", scanner.Err())
	}
	childPID, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
	if err != nil {
		t.Fatal(err)
	}
	if err := killTree(cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		err = syscall.Kill(childPID, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("grandchild process %d survived cancellation", childPID)
}
