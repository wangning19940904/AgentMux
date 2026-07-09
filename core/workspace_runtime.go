package core

import (
	"context"
	"fmt"
	"os"
)

func (e *Engine) initializeWorkspace(ctx context.Context, opts WorkspaceInitOptions, fallbackWorkDir string) (string, error) {
	workDir := opts.WorkDir
	if workDir == "" {
		workDir = fallbackWorkDir
	}
	if workDir == "" {
		var err error
		workDir, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	if e.workspace == nil {
		return workDir, nil
	}
	opts.WorkDir = workDir
	res, err := e.workspace.InitializeWorkspace(ctx, opts)
	if err != nil {
		return "", fmt.Errorf("initialize workspace: %w", err)
	}
	if res != nil && res.WorkDir != "" {
		return res.WorkDir, nil
	}
	return workDir, nil
}

func (rt *channelRuntime) prepareWorkspace(ctx context.Context) (string, error) {
	if rt == nil {
		return "", fmt.Errorf("channel runtime is nil")
	}
	opts := rt.workspace
	if opts.WorkDir == "" {
		opts.WorkDir = rt.workDir
	}
	return rt.engine().initializeWorkspace(ctx, opts, rt.workDir)
}

func (rt *channelRuntime) engine() *Engine {
	return rt.owner
}
