//go:build !windows

package remote

import (
	"os/exec"
	"sync"
	"syscall"
)

func prepareSSHProxyProcess(cmd *exec.Cmd) func() {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var once sync.Once
	return func() {
		if cmd.Process != nil {
			// Also stop nested ssh/ProxyCommand children holding the pipes open.
			once.Do(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
		}
	}
}
