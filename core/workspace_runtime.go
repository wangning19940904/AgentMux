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
