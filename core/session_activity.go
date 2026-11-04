package core

// SessionActivity counts the live Agent sessions owned by this AgentMux
// process by what they need from an operator. Native sessions started by other
// applications are not included because this process cannot observe or
// control them.
type SessionActivity struct {
	// NeedsInput counts turns blocked on an approval or explicit user input.
	NeedsInput int `json:"needs_input"`
	// Working counts turns that are running or queued.
	Working int `json:"working"`
	// Idle counts live sessions with no turn in progress.
	Idle int `json:"idle"`
}

// SessionActivityReporter exposes the live session counts for monitoring.
type SessionActivityReporter interface {
	SessionActivity() SessionActivity
}

// SessionActivity summarizes every attached channel runtime. Locks are taken
// one at a time (engine, then each runtime's session and control locks
// separately) so monitoring never extends a critical section of the turn path.
func (e *Engine) SessionActivity() SessionActivity {
	e.mu.RLock()
	runtimes := make([]*channelRuntime, 0, len(e.channels))
	for _, rt := range e.channels {
		runtimes = append(runtimes, rt)
	}
	e.mu.RUnlock()

	var total SessionActivity
	for _, rt := range runtimes {
		activity := rt.sessionActivity()
		total.NeedsInput += activity.NeedsInput
		total.Working += activity.Working
		total.Idle += activity.Idle
	}
	return total
}

func (rt *channelRuntime) sessionActivity() SessionActivity {
	if rt == nil {
		return SessionActivity{}
	}
	busySessions, idleSessions := 0, 0
	rt.mu.Lock()
	for _, binding := range rt.sessions {
		if binding == nil || binding.closed {
			continue
		}
		if binding.active > 0 {
			busySessions++
		} else {
			idleSessions++
		}
	}
	// A conversation reset retires its session while a turn may still be
	// draining; that turn is still work in progress.
	for binding := range rt.retiredSessions {
		if binding != nil && !binding.closed && binding.active > 0 {
			busySessions++
		}
	}
	rt.mu.Unlock()

	needsInput, activeTurns := 0, 0
	rt.controlMu.Lock()
	busyKeys := make(map[string]bool, len(rt.controlTasks)+len(rt.directTurns))
	for key, state := range rt.controlTasks {
		if state == nil {
			continue
		}
		switch {
		case state.active != nil && state.active.task.Status == ChannelTaskWaitingInput:
			needsInput++
			busyKeys[key] = true
		case state.active != nil || len(state.queue) > 0:
			activeTurns++
			busyKeys[key] = true
		}
	}
	for key, turn := range rt.directTurns {
		if turn != nil && !busyKeys[key] {
			activeTurns++
		}
	}
	rt.controlMu.Unlock()

	// Session bindings are the general signal: every turn path holds its
	// binding while it runs. Tracked tasks also cover turns that are queued or
	// still starting their session, so the larger count wins.
	working := busySessions - needsInput
	if activeTurns > working {
		working = activeTurns
	}
	if working < 0 {
		working = 0
	}
	return SessionActivity{NeedsInput: needsInput, Working: working, Idle: idleSessions}
}
