package remote

import (
	"os/exec"
	"sync"
)

func prepareSSHProxyProcess(cmd *exec.Cmd) func() {
	var once sync.Once
	return func() {
		if cmd.Process != nil {
			once.Do(func() { _ = cmd.Process.Kill() })
		}
	}
}
