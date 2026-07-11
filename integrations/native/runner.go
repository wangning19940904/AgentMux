package native

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Command is passed to Runner so tests can emulate native CLIs without
// touching the real HOME.
type Command struct {
	Name string   `json:"name"`
	Args []string `json:"args"`
	Env  []string `json:"-"`
}

type CommandOutput struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
}

type Runner interface {
	LookPath(name string) (string, error)
	Run(ctx context.Context, command Command) (CommandOutput, error)
}

type ExecRunner struct{}

func (ExecRunner) LookPath(name string) (string, error) { return exec.LookPath(name) }

func (ExecRunner) Run(ctx context.Context, command Command) (CommandOutput, error) {
	cmd := exec.CommandContext(ctx, command.Name, command.Args...)
	cmd.Env = command.Env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := CommandOutput{Stdout: stdout.String(), Stderr: stderr.String()}
	if err != nil {
		message := strings.TrimSpace(result.Stderr)
		if message == "" {
			message = strings.TrimSpace(result.Stdout)
		}
		if message != "" {
			return result, fmt.Errorf("%s %s: %w: %s", command.Name, strings.Join(command.Args, " "), err, message)
		}
		return result, fmt.Errorf("%s %s: %w", command.Name, strings.Join(command.Args, " "), err)
	}
	return result, nil
}

func commandEnv(home string) []string {
	env := append([]string(nil), os.Environ()...)
	env = replaceEnv(env, "HOME", home)
	env = replaceEnv(env, "CODEX_HOME", home+string(os.PathSeparator)+".codex")
	return env
}

func replaceEnv(env []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}
