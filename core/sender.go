package core

import "context"

// Sender lets external callers (the bridge HTTP API) push a message into a
// project as if it came from a platform.
type Sender interface {
	// SendToProject delivers text to all platforms bound to project.
	SendToProject(ctx context.Context, project, text string) error
}

// SendToProject implements Sender on the Engine: it sends an unsolicited
// message to every platform of the named project.
func (e *Engine) SendToProject(ctx context.Context, project, text string) error {
	e.mu.RLock()
	pr := e.projects[project]
	e.mu.RUnlock()
	if pr == nil {
		return ErrNoProject
	}
	for _, p := range pr.platforms {
		// Broadcasting needs a chat id; bridge callers typically target a
		// known chat, so we surface the first configured chat via Send with an
		// empty id which adapters may reject. This is a thin hook; richer
		// targeting is added with per-platform default chats.
		if err := p.Send(ctx, "", text); err != nil {
			e.log.Warn("bridge send", "platform", p.Name(), "err", err)
		}
	}
	return nil
}

// ErrNoProject is returned when a project name is unknown.
var ErrNoProject = errProject("unknown project")

type errProject string

func (e errProject) Error() string { return string(e) }
